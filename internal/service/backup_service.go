package service

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	settingKeyBackupS3Config = "backup_s3_config"
	settingKeyBackupSchedule = "backup_schedule"
	settingKeyBackupRecords  = "backup_records"

	maxBackupRecords           = 100
	backupObjectCleanupTimeout = 2 * time.Minute
	// 锁时长必须超过定时备份 30 分钟的执行上限，防止备份尚未完成时其它实例接管。
	backupScheduledLeaderLockKey = "backup:scheduled:leader"
	backupScheduledLeaderLockTTL = 35 * time.Minute
)

var (
	ErrBackupS3NotConfigured = infraerrors.BadRequest("BACKUP_S3_NOT_CONFIGURED", "backup S3 storage is not configured")
	ErrBackupNotFound        = infraerrors.NotFound("BACKUP_NOT_FOUND", "backup record not found")
	ErrBackupInProgress      = infraerrors.Conflict("BACKUP_IN_PROGRESS", "a backup is already in progress")
	ErrRestoreInProgress     = infraerrors.Conflict("RESTORE_IN_PROGRESS", "a restore is already in progress")
	ErrBackupRecordsCorrupt  = infraerrors.InternalServer("BACKUP_RECORDS_CORRUPT", "backup records data is corrupted")
	ErrBackupS3ConfigCorrupt = infraerrors.InternalServer("BACKUP_S3_CONFIG_CORRUPT", "backup S3 config data is corrupted")
	// 自动生成的临时密钥每次重启都会变化，不能用它持久化新的 S3 Secret，
	// 否则升级后密文将永久无法解密。固定错误码便于后台给出可操作提示。
	ErrSecretEncryptionKeyNotConfigured = infraerrors.BadRequest(
		"SECRET_ENCRYPTION_KEY_NOT_CONFIGURED",
		"cannot store the S3 secret access key without a fixed TOTP_ENCRYPTION_KEY; configure a persistent encryption key and try again",
	)
)

// ─── 接口定义 ───

// DBDumper abstracts database dump/restore operations
type DBDumper interface {
	Dump(ctx context.Context) (io.ReadCloser, error)
	Restore(ctx context.Context, data io.Reader) error
}

// BackupObjectStore abstracts object storage for backup files
type BackupObjectStore interface {
	Upload(ctx context.Context, key string, body io.Reader, contentType string) (sizeBytes int64, err error)
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	PresignURL(ctx context.Context, key string, expiry time.Duration) (string, error)
	HeadBucket(ctx context.Context) error
}

// BackupObjectStoreFactory creates an object store from S3 config
type BackupObjectStoreFactory func(ctx context.Context, cfg *BackupS3Config) (BackupObjectStore, error)

// ─── 数据模型 ───

// BackupS3Config S3 兼容存储配置（支持 Cloudflare R2）
type BackupS3Config struct {
	Endpoint        string `json:"endpoint"` // e.g. https://<account_id>.r2.cloudflarestorage.com
	Region          string `json:"region"`   // R2 用 "auto"
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key,omitempty"` //nolint:revive // field name follows AWS convention
	Prefix          string `json:"prefix"`                      // S3 key 前缀，如 "backups/"
	ForcePathStyle  bool   `json:"force_path_style"`
}

// BackupS3ConfigView is the only representation returned by management APIs.
// Access-key material is intentionally represented as presence bits so a
// step-up read cannot become a reusable credential export.
type BackupS3ConfigView struct {
	Endpoint                  string `json:"endpoint"`
	Region                    string `json:"region"`
	Bucket                    string `json:"bucket"`
	Prefix                    string `json:"prefix"`
	ForcePathStyle            bool   `json:"force_path_style"`
	AccessKeyIDConfigured     bool   `json:"access_key_id_configured"`
	SecretAccessKeyConfigured bool   `json:"secret_access_key_configured"`
}

// IsConfigured 检查必要字段是否已配置
func (c *BackupS3Config) IsConfigured() bool {
	return c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// BackupScheduleConfig 定时备份配置
type BackupScheduleConfig struct {
	Enabled     bool   `json:"enabled"`
	CronExpr    string `json:"cron_expr"`    // cron 表达式，如 "0 2 * * *" 每天凌晨2点
	RetainDays  int    `json:"retain_days"`  // 备份文件过期天数，默认14，0=不自动清理
	RetainCount int    `json:"retain_count"` // 最多保留份数，0=不限制
}

// BackupRecord 备份记录
type BackupRecord struct {
	ID            string       `json:"id"`
	Status        string       `json:"status"`      // pending, running, completed, failed
	BackupType    string       `json:"backup_type"` // postgres
	FileName      string       `json:"file_name"`
	S3Key         string       `json:"s3_key"`
	Parts         []BackupPart `json:"parts,omitempty"`
	SizeBytes     int64        `json:"size_bytes"`
	TriggeredBy   string       `json:"triggered_by"` // manual, scheduled
	ErrorMsg      string       `json:"error_message,omitempty"`
	StartedAt     string       `json:"started_at"`
	FinishedAt    string       `json:"finished_at,omitempty"`
	ExpiresAt     string       `json:"expires_at,omitempty"`     // 过期时间
	Progress      string       `json:"progress,omitempty"`       // "dumping", "uploading", ""
	RestoreStatus string       `json:"restore_status,omitempty"` // "", "running", "completed", "failed"
	RestoreError  string       `json:"restore_error,omitempty"`
	RestoredAt    string       `json:"restored_at,omitempty"`
}

type BackupDownloadPart struct {
	Index     int    `json:"index"`
	SizeBytes int64  `json:"size_bytes"`
	URL       string `json:"url"`
}

// BackupDownloadResponse 保持旧单文件 url 字段，并为大备份返回有序分卷。
type BackupDownloadResponse struct {
	URL   string               `json:"url,omitempty"`
	Parts []BackupDownloadPart `json:"parts,omitempty"`
}

// BackupService 数据库备份恢复服务
type BackupService struct {
	settingRepo SettingRepository
	dbCfg       *config.DatabaseConfig
	encryptor   SecretEncryptor
	// false 表示当前加密密钥为启动时临时生成，持久化 Secret 必须关闭式拒绝。
	encryptionKeyConfigured bool
	storeFactory            BackupObjectStoreFactory
	dumper                  DBDumper

	opMu      sync.Mutex // 保护 backingUp/restoring 标志
	backingUp bool
	restoring bool

	storeMu sync.Mutex // 保护 store/s3Cfg 缓存
	store   BackupObjectStore
	s3Cfg   *BackupS3Config

	recordsMu sync.Mutex // 保护 records 的 load/save 操作

	cronMu      sync.Mutex
	cronSched   *cron.Cron
	cronEntryID cron.EntryID

	// 只用于多实例定时任务选主；二者都为空时维持单机和单元测试原行为。
	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string

	wg            sync.WaitGroup     // 追踪活跃的备份/恢复 goroutine
	shuttingDown  atomic.Bool        // 阻止新备份启动
	bgCtx         context.Context    // 所有后台操作的 parent context
	bgCancel      context.CancelFunc // 取消所有活跃后台操作
	partSizeBytes int64              // 生产为 4 GiB；专项测试可注入小阈值
}

func NewBackupService(
	settingRepo SettingRepository,
	cfg *config.Config,
	encryptor SecretEncryptor,
	storeFactory BackupObjectStoreFactory,
	dumper DBDumper,
) *BackupService {
	bgCtx, bgCancel := context.WithCancel(context.Background())
	return &BackupService{
		settingRepo:             settingRepo,
		dbCfg:                   &cfg.Database,
		encryptor:               encryptor,
		encryptionKeyConfigured: cfg.Totp.EncryptionKeyConfigured,
		storeFactory:            storeFactory,
		dumper:                  dumper,
		bgCtx:                   bgCtx,
		bgCancel:                bgCancel,
		partSizeBytes:           defaultBackupPartSizeBytes,
		instanceID:              uuid.NewString(),
	}
}

// SetLeaderLock 注入跨实例选主后端。手动备份不加锁，只有 cron（定时任务）受控。
func (s *BackupService) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

// Start 启动定时备份调度器并清理孤立记录
func (s *BackupService) Start() {
	s.cronSched = cron.New()
	s.cronSched.Start()

	// 清理重启后孤立的 running 记录
	s.recoverStaleRecords()

	// 加载已有的定时配置
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	schedule, err := s.GetSchedule(ctx)
	if err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 加载定时备份配置失败: %v", err)
		return
	}
	if schedule.Enabled && schedule.CronExpr != "" {
		if err := s.applyCronSchedule(schedule); err != nil {
			logger.LegacyPrintf("service.backup", "[Backup] 应用定时备份配置失败: %v", err)
		}
	}
}

// recoverStaleRecords 启动时将孤立的 running 记录标记为 failed，并清理上传中断留下的对象。
func (s *BackupService) recoverStaleRecords() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	records, err := s.loadRecords(ctx)
	if err != nil {
		return
	}
	for i := range records {
		if records[i].Status == "running" {
			staleRecord := records[i]
			records[i].Status = "failed"
			records[i].ErrorMsg = "interrupted by server restart"
			records[i].Progress = ""
			records[i].FinishedAt = time.Now().Format(time.RFC3339)
			_ = s.saveRecord(ctx, &records[i])
			if len(backupObjectKeys(&staleRecord)) > 0 {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), backupObjectCleanupTimeout)
				cleanupErr := s.deleteBackupObjects(cleanupCtx, &staleRecord)
				cleanupCancel()
				if cleanupErr != nil {
					records[i].ErrorMsg = fmt.Sprintf("interrupted by server restart; cleanup failed: %v", cleanupErr)
					_ = s.saveRecord(context.Background(), &records[i])
				}
			}
			logger.LegacyPrintf("service.backup", "[Backup] recovered stale running record: %s", records[i].ID)
		}
		if records[i].RestoreStatus == "running" {
			records[i].RestoreStatus = "failed"
			records[i].RestoreError = "interrupted by server restart"
			_ = s.saveRecord(ctx, &records[i])
			logger.LegacyPrintf("service.backup", "[Backup] recovered stale restoring record: %s", records[i].ID)
		}
	}
}

// Stop 停止定时备份并等待活跃操作完成
func (s *BackupService) Stop() {
	s.shuttingDown.Store(true)

	s.cronMu.Lock()
	if s.cronSched != nil {
		s.cronSched.Stop()
	}
	s.cronMu.Unlock()

	// 等待活跃备份/恢复完成（最多 5 分钟）
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		logger.LegacyPrintf("service.backup", "[Backup] all active operations finished")
	case <-time.After(5 * time.Minute):
		logger.LegacyPrintf("service.backup", "[Backup] shutdown timeout after 5min, cancelling active operations")
		if s.bgCancel != nil {
			s.bgCancel() // 取消所有后台操作
		}
		// 给 goroutine 时间响应取消并完成清理
		select {
		case <-done:
			logger.LegacyPrintf("service.backup", "[Backup] active operations cancelled and cleaned up")
		case <-time.After(10 * time.Second):
			logger.LegacyPrintf("service.backup", "[Backup] goroutine cleanup timed out")
		}
	}
}

// ─── S3 配置管理 ───

// EncryptionKeyConfigured 表示当前使用的是管理员固定配置、重启后仍相同的密钥。
func (s *BackupService) EncryptionKeyConfigured() bool {
	return s != nil && s.encryptionKeyConfigured
}

func (s *BackupService) GetS3Config(ctx context.Context) (*BackupS3ConfigView, error) {
	cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &BackupS3ConfigView{}, nil
	}
	return backupS3ConfigView(cfg), nil
}

func (s *BackupService) UpdateS3Config(ctx context.Context, cfg BackupS3Config) (*BackupS3ConfigView, error) {
	// GET 不返回凭据，因此空凭据表示“保留原值”。这里读取原始存储值，
	// 保留 Secret 时直接复用原密文，不能先解密再用临时密钥重新加密。
	newSecretProvided := strings.TrimSpace(cfg.SecretAccessKey) != ""
	if old, _ := s.loadStoredS3Config(ctx); old != nil {
		if strings.TrimSpace(cfg.AccessKeyID) == "" {
			cfg.AccessKeyID = old.AccessKeyID
		}
		if !newSecretProvided {
			cfg.SecretAccessKey = old.SecretAccessKey
		}
	}
	if newSecretProvided {
		if !s.encryptionKeyConfigured {
			return nil, ErrSecretEncryptionKeyNotConfigured
		}
		encrypted, err := s.encryptor.Encrypt(cfg.SecretAccessKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt secret: %w", err)
		}
		cfg.SecretAccessKey = encrypted
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal s3 config: %w", err)
	}
	if err := s.settingRepo.Set(ctx, settingKeyBackupS3Config, string(data)); err != nil {
		return nil, fmt.Errorf("save s3 config: %w", err)
	}

	// 清除缓存的 S3 客户端
	s.storeMu.Lock()
	s.store = nil
	s.s3Cfg = nil
	s.storeMu.Unlock()

	return backupS3ConfigView(&cfg), nil
}

func (s *BackupService) TestS3Connection(ctx context.Context, cfg BackupS3Config) error {
	// 测试未保存的新 secret 与正式保存共用同一个 step-up scope；缺省凭据
	// 可以复用已保存值，但永远不会经响应或错误详情返回客户端。
	if old, _ := s.loadS3Config(ctx); old != nil {
		if strings.TrimSpace(cfg.AccessKeyID) == "" {
			cfg.AccessKeyID = old.AccessKeyID
		}
		if cfg.SecretAccessKey == "" {
			cfg.SecretAccessKey = old.SecretAccessKey
		}
	}

	if cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return fmt.Errorf("incomplete S3 config: bucket, access_key_id, secret_access_key are required")
	}

	store, err := s.storeFactory(ctx, &cfg)
	if err != nil {
		return err
	}
	return store.HeadBucket(ctx)
}

func backupS3ConfigView(cfg *BackupS3Config) *BackupS3ConfigView {
	if cfg == nil {
		return &BackupS3ConfigView{}
	}
	return &BackupS3ConfigView{
		Endpoint:                  cfg.Endpoint,
		Region:                    cfg.Region,
		Bucket:                    cfg.Bucket,
		Prefix:                    cfg.Prefix,
		ForcePathStyle:            cfg.ForcePathStyle,
		AccessKeyIDConfigured:     strings.TrimSpace(cfg.AccessKeyID) != "",
		SecretAccessKeyConfigured: strings.TrimSpace(cfg.SecretAccessKey) != "",
	}
}

// ─── 定时备份管理 ───

func (s *BackupService) GetSchedule(ctx context.Context) (*BackupScheduleConfig, error) {
	raw, err := s.settingRepo.GetValue(ctx, settingKeyBackupSchedule)
	if err != nil || raw == "" {
		return &BackupScheduleConfig{}, nil
	}
	var cfg BackupScheduleConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return &BackupScheduleConfig{}, nil
	}
	return &cfg, nil
}

func (s *BackupService) UpdateSchedule(ctx context.Context, cfg BackupScheduleConfig) (*BackupScheduleConfig, error) {
	if cfg.Enabled && cfg.CronExpr == "" {
		return nil, infraerrors.BadRequest("INVALID_CRON", "cron expression is required when schedule is enabled")
	}
	// 验证 cron 表达式
	if cfg.CronExpr != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(cfg.CronExpr); err != nil {
			return nil, infraerrors.BadRequest("INVALID_CRON", fmt.Sprintf("invalid cron expression: %v", err))
		}
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal schedule config: %w", err)
	}
	if err := s.settingRepo.Set(ctx, settingKeyBackupSchedule, string(data)); err != nil {
		return nil, fmt.Errorf("save schedule config: %w", err)
	}

	// 应用或停止定时任务
	if cfg.Enabled {
		if err := s.applyCronSchedule(&cfg); err != nil {
			return nil, err
		}
	} else {
		s.removeCronSchedule()
	}

	return &cfg, nil
}

func (s *BackupService) applyCronSchedule(cfg *BackupScheduleConfig) error {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()

	if s.cronSched == nil {
		return fmt.Errorf("cron scheduler not initialized")
	}

	// 移除旧任务
	if s.cronEntryID != 0 {
		s.cronSched.Remove(s.cronEntryID)
		s.cronEntryID = 0
	}

	entryID, err := s.cronSched.AddFunc(cfg.CronExpr, func() {
		s.runScheduledBackup()
	})
	if err != nil {
		return infraerrors.BadRequest("INVALID_CRON", fmt.Sprintf("failed to schedule: %v", err))
	}
	s.cronEntryID = entryID
	logger.LegacyPrintf("service.backup", "[Backup] 定时备份已启用: %s", cfg.CronExpr)
	return nil
}

func (s *BackupService) removeCronSchedule() {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	if s.cronSched != nil && s.cronEntryID != 0 {
		s.cronSched.Remove(s.cronEntryID)
		s.cronEntryID = 0
		logger.LegacyPrintf("service.backup", "[Backup] 定时备份已停用")
	}
}

func (s *BackupService) runScheduledBackup() {
	s.wg.Add(1)
	defer s.wg.Done()

	ctx, cancel := context.WithTimeout(s.bgCtx, 30*time.Minute)
	defer cancel()

	// 多副本共享数据库时，每台实例都有自己的 cron；选主避免同一时刻重复 pg_dump、
	// 重复占用内存和上传相同对象。锁服务不可用时 helper 会按既有安全回退处理。
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, backupScheduledLeaderLockKey, s.instanceID, backupScheduledLeaderLockTTL)
	if !ok {
		logger.LegacyPrintf("service.backup", "[Backup] 定时备份跳过: 本实例非 leader")
		return
	}
	defer release()

	// 读取定时备份配置中的过期天数
	schedule, _ := s.GetSchedule(ctx)
	expireDays := 14 // 默认14天过期
	if schedule != nil && schedule.RetainDays > 0 {
		expireDays = schedule.RetainDays
	}

	logger.LegacyPrintf("service.backup", "[Backup] 开始执行定时备份, 过期天数: %d", expireDays)
	record, err := s.CreateBackup(ctx, "scheduled", expireDays)
	if err != nil {
		if errors.Is(err, ErrBackupInProgress) {
			logger.LegacyPrintf("service.backup", "[Backup] 定时备份跳过: 已有备份正在进行中")
		} else {
			logger.LegacyPrintf("service.backup", "[Backup] 定时备份失败: %v", err)
		}
		return
	}
	logger.LegacyPrintf("service.backup", "[Backup] 定时备份完成: id=%s size=%d", record.ID, record.SizeBytes)

	// 清理过期备份（复用已加载的 schedule）
	if schedule == nil {
		return
	}
	if err := s.cleanupOldBackups(ctx, schedule); err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 清理过期备份失败: %v", err)
	}
}

// ─── 备份/恢复核心 ───

// CreateBackup 创建全量数据库备份并上传到 S3（流式处理）
// expireDays: 备份过期天数，0=永不过期，默认14天
func (s *BackupService) CreateBackup(ctx context.Context, triggeredBy string, expireDays int) (*BackupRecord, error) {
	if s.shuttingDown.Load() {
		return nil, infraerrors.ServiceUnavailable("SERVER_SHUTTING_DOWN", "server is shutting down")
	}

	s.opMu.Lock()
	if s.backingUp {
		s.opMu.Unlock()
		return nil, ErrBackupInProgress
	}
	s.backingUp = true
	s.opMu.Unlock()
	defer func() {
		s.opMu.Lock()
		s.backingUp = false
		s.opMu.Unlock()
	}()

	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return nil, err
	}
	if s3Cfg == nil || !s3Cfg.IsConfigured() {
		return nil, ErrBackupS3NotConfigured
	}

	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return nil, fmt.Errorf("init object store: %w", err)
	}

	now := time.Now()
	backupID := uuid.New().String()[:8]
	fileName := fmt.Sprintf("%s_%s.sql.gz", s.dbCfg.DBName, now.Format("20060102_150405"))
	s3Key := s.buildS3Key(s3Cfg, fileName)

	var expiresAt string
	if expireDays > 0 {
		expiresAt = now.AddDate(0, 0, expireDays).Format(time.RFC3339)
	}

	record := &BackupRecord{
		ID:          backupID,
		Status:      "running",
		BackupType:  "postgres",
		FileName:    fileName,
		S3Key:       s3Key,
		TriggeredBy: triggeredBy,
		StartedAt:   now.Format(time.RFC3339),
		ExpiresAt:   expiresAt,
	}

	// 大备份必须先落临时压缩文件，才能可靠切卷并计算逐卷摘要；临时文件不会进入数据库或日志。
	archivePath, sizeBytes, err := s.createCompressedBackupFile(ctx)
	if err != nil {
		record.Status = "failed"
		record.ErrorMsg = err.Error()
		record.FinishedAt = time.Now().Format(time.RFC3339)
		_ = s.saveRecord(ctx, record)
		return record, err
	}
	defer func() { _ = cleanupBackupFiles(archivePath) }()
	record.SizeBytes = sizeBytes
	if err := s.saveRecord(ctx, record); err != nil {
		return nil, fmt.Errorf("save initial record: %w", err)
	}
	if err := s.uploadBackupArchive(ctx, record, objectStore, s3Cfg, archivePath); err != nil {
		record.Status = "failed"
		record.ErrorMsg = err.Error()
		record.FinishedAt = time.Now().Format(time.RFC3339)
		_ = s.saveRecord(ctx, record)
		return record, err
	}

	record.Status = "completed"
	record.FinishedAt = time.Now().Format(time.RFC3339)
	if err := s.saveRecord(ctx, record); err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 保存备份记录失败: %v", err)
	}

	return record, nil
}

// StartBackup 异步创建备份，立即返回 running 状态的记录
func (s *BackupService) StartBackup(ctx context.Context, triggeredBy string, expireDays int) (*BackupRecord, error) {
	if s.shuttingDown.Load() {
		return nil, infraerrors.ServiceUnavailable("SERVER_SHUTTING_DOWN", "server is shutting down")
	}

	s.opMu.Lock()
	if s.backingUp {
		s.opMu.Unlock()
		return nil, ErrBackupInProgress
	}
	s.backingUp = true
	s.opMu.Unlock()

	// 初始化阶段出错时自动重置标志
	launched := false
	defer func() {
		if !launched {
			s.opMu.Lock()
			s.backingUp = false
			s.opMu.Unlock()
		}
	}()

	// 在返回前加载 S3 配置和创建 store，避免 goroutine 中配置被修改
	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return nil, err
	}
	if s3Cfg == nil || !s3Cfg.IsConfigured() {
		return nil, ErrBackupS3NotConfigured
	}

	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return nil, fmt.Errorf("init object store: %w", err)
	}

	now := time.Now()
	backupID := uuid.New().String()[:8]
	fileName := fmt.Sprintf("%s_%s.sql.gz", s.dbCfg.DBName, now.Format("20060102_150405"))
	s3Key := s.buildS3Key(s3Cfg, fileName)

	var expiresAt string
	if expireDays > 0 {
		expiresAt = now.AddDate(0, 0, expireDays).Format(time.RFC3339)
	}

	record := &BackupRecord{
		ID:          backupID,
		Status:      "running",
		BackupType:  "postgres",
		FileName:    fileName,
		S3Key:       s3Key,
		TriggeredBy: triggeredBy,
		StartedAt:   now.Format(time.RFC3339),
		ExpiresAt:   expiresAt,
		Progress:    "pending",
	}

	if err := s.saveRecord(ctx, record); err != nil {
		return nil, fmt.Errorf("save initial record: %w", err)
	}

	launched = true
	// 在启动 goroutine 前完成拷贝，避免数据竞争
	result := *record

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.opMu.Lock()
			s.backingUp = false
			s.opMu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				logger.LegacyPrintf("service.backup", "[Backup] panic recovered: %v", r)
				record.Status = "failed"
				record.ErrorMsg = fmt.Sprintf("internal panic: %v", r)
				record.Progress = ""
				record.FinishedAt = time.Now().Format(time.RFC3339)
				_ = s.saveRecord(context.Background(), record)
			}
		}()
		s.executeBackup(record, objectStore, s3Cfg)
	}()

	return &result, nil
}

// executeBackup 后台执行备份（独立于 HTTP context）
func (s *BackupService) executeBackup(record *BackupRecord, objectStore BackupObjectStore, s3Cfg *BackupS3Config) {
	ctx, cancel := context.WithTimeout(s.bgCtx, 30*time.Minute)
	defer cancel()

	// 阶段1: pg_dump -> gzip 临时文件
	record.Progress = "dumping"
	_ = s.saveRecord(ctx, record)
	archivePath, sizeBytes, err := s.createCompressedBackupFile(ctx)
	if err != nil {
		record.Status = "failed"
		record.ErrorMsg = err.Error()
		record.Progress = ""
		record.FinishedAt = time.Now().Format(time.RFC3339)
		_ = s.saveRecord(context.Background(), record)
		return
	}
	defer func() { _ = cleanupBackupFiles(archivePath) }()
	record.SizeBytes = sizeBytes

	// 阶段2: 单对象或分卷上传
	record.Progress = "uploading"
	_ = s.saveRecord(ctx, record)
	if err := s.uploadBackupArchive(ctx, record, objectStore, s3Cfg, archivePath); err != nil {
		record.Status = "failed"
		record.ErrorMsg = err.Error()
		record.Progress = ""
		record.FinishedAt = time.Now().Format(time.RFC3339)
		_ = s.saveRecord(context.Background(), record)
		return
	}

	record.Status = "completed"
	record.Progress = ""
	record.FinishedAt = time.Now().Format(time.RFC3339)
	if err := s.saveRecord(context.Background(), record); err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 保存备份记录失败: %v", err)
	}
}

func (s *BackupService) createCompressedBackupFile(ctx context.Context) (string, int64, error) {
	dumpReader, err := s.dumper.Dump(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("pg_dump: %w", err)
	}
	archive, err := os.CreateTemp("", "sub2api-backup-*.sql.gz")
	if err != nil {
		_ = dumpReader.Close()
		return "", 0, fmt.Errorf("create backup archive: %w", err)
	}
	archivePath := archive.Name()
	gzWriter := gzip.NewWriter(archive)
	_, copyErr := io.Copy(gzWriter, dumpReader)
	if closeErr := gzWriter.Close(); copyErr == nil && closeErr != nil {
		copyErr = closeErr
	}
	if closeErr := dumpReader.Close(); copyErr == nil && closeErr != nil {
		copyErr = closeErr
	}
	if closeErr := archive.Close(); copyErr == nil && closeErr != nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = cleanupBackupFiles(archivePath)
		return "", 0, fmt.Errorf("gzip/dump failed: %w", copyErr)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		_ = cleanupBackupFiles(archivePath)
		return "", 0, fmt.Errorf("stat backup archive: %w", err)
	}
	return archivePath, info.Size(), nil
}

func uploadBackupFile(ctx context.Context, store BackupObjectStore, key, filePath, contentType string) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	return store.Upload(ctx, key, file, contentType)
}

func (s *BackupService) uploadBackupArchive(ctx context.Context, record *BackupRecord, store BackupObjectStore, cfg *BackupS3Config, archivePath string) error {
	info, err := os.Stat(archivePath)
	if err != nil {
		return fmt.Errorf("stat backup archive: %w", err)
	}
	partSize := s.partSizeBytes
	if partSize <= 0 {
		partSize = defaultBackupPartSizeBytes
	}
	if info.Size() <= partSize {
		if _, err := uploadBackupFile(ctx, store, record.S3Key, archivePath, "application/gzip"); err != nil {
			// PUT 可能已经在对象存储完成，只是响应在网络中丢失；失败也要尝试清理目标 key。
			cleanupCtx, cancel := context.WithTimeout(context.Background(), backupObjectCleanupTimeout)
			cleanupErr := deleteBackupObjectKeys(cleanupCtx, store, record)
			cancel()
			return errors.Join(fmt.Errorf("backup upload: %w", err), cleanupErr)
		}
		record.Parts = nil
		return nil
	}
	if cfg == nil {
		return errors.New("backup S3 config is unavailable for split upload")
	}
	localParts, err := splitBackupFile(archivePath, partSize)
	if err != nil {
		return fmt.Errorf("split backup archive: %w", err)
	}
	defer func() {
		for _, part := range localParts {
			_ = os.Remove(part.Path)
		}
	}()
	record.S3Key = ""
	record.Parts = make([]BackupPart, 0, len(localParts))
	partRoot := strings.TrimRight(s.buildS3Key(cfg, record.ID), "/")
	for _, part := range localParts {
		record.Parts = append(record.Parts, BackupPart{
			Index: part.Index, S3Key: s.buildBackupPartKey(partRoot, part.Index),
			SizeBytes: part.SizeBytes, SHA256: part.SHA256,
		})
	}
	// 先保存完整分卷计划，进程异常退出后仍可清理已经落到对象存储的卷。
	if err := s.saveRecord(ctx, record); err != nil {
		return fmt.Errorf("save split backup plan: %w", err)
	}
	for index, part := range localParts {
		if _, err := uploadBackupFile(ctx, store, record.Parts[index].S3Key, part.Path, "application/octet-stream"); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), backupObjectCleanupTimeout)
			cleanupErr := deleteBackupObjectKeys(cleanupCtx, store, record)
			cancel()
			return errors.Join(fmt.Errorf("upload backup part %d: %w", part.Index, err), cleanupErr)
		}
	}
	return nil
}

func (s *BackupService) buildBackupPartKey(root string, index int) string {
	return fmt.Sprintf("%s/payload.part-%06d", strings.TrimRight(root, "/"), index)
}

// RestoreBackup 从 S3 下载备份并流式恢复到数据库
func (s *BackupService) RestoreBackup(ctx context.Context, backupID string) error {
	s.opMu.Lock()
	if s.restoring {
		s.opMu.Unlock()
		return ErrRestoreInProgress
	}
	s.restoring = true
	s.opMu.Unlock()
	defer func() {
		s.opMu.Lock()
		s.restoring = false
		s.opMu.Unlock()
	}()

	record, err := s.GetBackupRecord(ctx, backupID)
	if err != nil {
		return err
	}
	if record.Status != "completed" {
		return infraerrors.BadRequest("BACKUP_NOT_COMPLETED", "can only restore from a completed backup")
	}

	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return err
	}
	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return fmt.Errorf("init object store: %w", err)
	}
	if len(record.Parts) > 0 {
		archivePath, err := s.downloadBackupParts(ctx, objectStore, record.Parts)
		if err != nil {
			return err
		}
		defer func() { _ = cleanupBackupFiles(archivePath) }()
		return s.restoreArchive(ctx, archivePath)
	}

	// 旧记录继续走单对象流式恢复。
	body, err := objectStore.Download(ctx, record.S3Key)
	if err != nil {
		return fmt.Errorf("S3 download failed: %w", err)
	}
	defer func() { _ = body.Close() }()

	// 流式解压 gzip -> psql（不将全部数据加载到内存）
	gzReader, err := gzip.NewReader(body)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gzReader.Close() }()

	// 流式恢复
	if err := s.dumper.Restore(ctx, gzReader); err != nil {
		return fmt.Errorf("pg restore: %w", err)
	}

	return nil
}

// StartRestore 异步恢复备份，立即返回
func (s *BackupService) StartRestore(ctx context.Context, backupID string) (*BackupRecord, error) {
	if s.shuttingDown.Load() {
		return nil, infraerrors.ServiceUnavailable("SERVER_SHUTTING_DOWN", "server is shutting down")
	}

	s.opMu.Lock()
	if s.restoring {
		s.opMu.Unlock()
		return nil, ErrRestoreInProgress
	}
	s.restoring = true
	s.opMu.Unlock()

	// 初始化阶段出错时自动重置标志
	launched := false
	defer func() {
		if !launched {
			s.opMu.Lock()
			s.restoring = false
			s.opMu.Unlock()
		}
	}()

	record, err := s.GetBackupRecord(ctx, backupID)
	if err != nil {
		return nil, err
	}
	if record.Status != "completed" {
		return nil, infraerrors.BadRequest("BACKUP_NOT_COMPLETED", "can only restore from a completed backup")
	}

	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return nil, err
	}
	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return nil, fmt.Errorf("init object store: %w", err)
	}

	record.RestoreStatus = "running"
	_ = s.saveRecord(ctx, record)

	launched = true
	result := *record

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.opMu.Lock()
			s.restoring = false
			s.opMu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				logger.LegacyPrintf("service.backup", "[Backup] restore panic recovered: %v", r)
				record.RestoreStatus = "failed"
				record.RestoreError = fmt.Sprintf("internal panic: %v", r)
				_ = s.saveRecord(context.Background(), record)
			}
		}()
		s.executeRestore(record, objectStore)
	}()

	return &result, nil
}

// executeRestore 后台执行恢复
func (s *BackupService) executeRestore(record *BackupRecord, objectStore BackupObjectStore) {
	ctx, cancel := context.WithTimeout(s.bgCtx, 30*time.Minute)
	defer cancel()
	if len(record.Parts) > 0 {
		archivePath, err := s.downloadBackupParts(ctx, objectStore, record.Parts)
		if err != nil {
			record.RestoreStatus = "failed"
			record.RestoreError = err.Error()
			_ = s.saveRecord(context.Background(), record)
			return
		}
		defer func() { _ = cleanupBackupFiles(archivePath) }()
		if err := s.restoreArchive(ctx, archivePath); err != nil {
			record.RestoreStatus = "failed"
			record.RestoreError = err.Error()
			_ = s.saveRecord(context.Background(), record)
			return
		}
		record.RestoreStatus = "completed"
		record.RestoredAt = time.Now().Format(time.RFC3339)
		_ = s.saveRecord(context.Background(), record)
		return
	}

	body, err := objectStore.Download(ctx, record.S3Key)
	if err != nil {
		record.RestoreStatus = "failed"
		record.RestoreError = fmt.Sprintf("S3 download failed: %v", err)
		_ = s.saveRecord(context.Background(), record)
		return
	}
	defer func() { _ = body.Close() }()

	gzReader, err := gzip.NewReader(body)
	if err != nil {
		record.RestoreStatus = "failed"
		record.RestoreError = fmt.Sprintf("gzip reader: %v", err)
		_ = s.saveRecord(context.Background(), record)
		return
	}
	defer func() { _ = gzReader.Close() }()

	if err := s.dumper.Restore(ctx, gzReader); err != nil {
		record.RestoreStatus = "failed"
		record.RestoreError = fmt.Sprintf("pg restore: %v", err)
		_ = s.saveRecord(context.Background(), record)
		return
	}

	record.RestoreStatus = "completed"
	record.RestoredAt = time.Now().Format(time.RFC3339)
	if err := s.saveRecord(context.Background(), record); err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 保存恢复记录失败: %v", err)
	}
}

func (s *BackupService) downloadBackupParts(ctx context.Context, store BackupObjectStore, parts []BackupPart) (path string, err error) {
	if len(parts) == 0 {
		return "", errors.New("backup parts are empty")
	}
	ordered := append([]BackupPart(nil), parts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	for i, part := range ordered {
		if part.Index != i+1 || part.S3Key == "" || part.SizeBytes <= 0 {
			return "", fmt.Errorf("invalid backup part metadata at index %d", i+1)
		}
	}
	archive, err := os.CreateTemp("", "sub2api-restore-*.sql.gz")
	if err != nil {
		return "", fmt.Errorf("create restore archive: %w", err)
	}
	path = archive.Name()
	cleanup := func() {
		_ = archive.Close()
		_ = cleanupBackupFiles(path)
	}
	for _, part := range ordered {
		body, err := store.Download(ctx, part.S3Key)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("download backup part %d: %w", part.Index, err)
		}
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(archive, hash), body)
		closeErr := body.Close()
		if copyErr != nil || closeErr != nil {
			cleanup()
			if copyErr != nil {
				return "", fmt.Errorf("read backup part %d: %w", part.Index, copyErr)
			}
			return "", fmt.Errorf("close backup part %d: %w", part.Index, closeErr)
		}
		if written != part.SizeBytes {
			cleanup()
			return "", fmt.Errorf("backup part %d size mismatch: got %d, want %d", part.Index, written, part.SizeBytes)
		}
		if part.SHA256 != "" && !strings.EqualFold(part.SHA256, hex.EncodeToString(hash.Sum(nil))) {
			cleanup()
			return "", fmt.Errorf("backup part %d checksum mismatch", part.Index)
		}
	}
	if err := archive.Close(); err != nil {
		_ = cleanupBackupFiles(path)
		return "", fmt.Errorf("close restore archive: %w", err)
	}
	return path, nil
}

func (s *BackupService) restoreArchive(ctx context.Context, path string) error {
	archive, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open restore archive: %w", err)
	}
	defer func() { _ = archive.Close() }()
	gzReader, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gzReader.Close() }()
	if err := s.dumper.Restore(ctx, gzReader); err != nil {
		return fmt.Errorf("pg restore: %w", err)
	}
	return nil
}

// ─── 备份记录管理 ───

func (s *BackupService) ListBackups(ctx context.Context) ([]BackupRecord, error) {
	records, err := s.loadRecords(ctx)
	if err != nil {
		return nil, err
	}
	// 倒序返回（最新在前）
	sort.Slice(records, func(i, j int) bool {
		return records[i].StartedAt > records[j].StartedAt
	})
	return records, nil
}

func (s *BackupService) GetBackupRecord(ctx context.Context, backupID string) (*BackupRecord, error) {
	records, err := s.loadRecords(ctx)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].ID == backupID {
			return &records[i], nil
		}
	}
	return nil, ErrBackupNotFound
}

func (s *BackupService) DeleteBackup(ctx context.Context, backupID string) error {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()

	records, err := s.loadRecordsLocked(ctx)
	if err != nil {
		return err
	}

	var found *BackupRecord
	var remaining []BackupRecord
	for i := range records {
		if records[i].ID == backupID {
			found = &records[i]
		} else {
			remaining = append(remaining, records[i])
		}
	}
	if found == nil {
		return ErrBackupNotFound
	}
	if found.Status == "running" {
		return ErrBackupInProgress
	}

	// 删除不完整时保留记录，否则管理员将失去重试清理所需的全部对象 key。
	if err := s.deleteBackupObjects(ctx, found); err != nil {
		return err
	}

	return s.saveRecordsLocked(ctx, remaining)
}

// GetBackupDownloadURL 获取备份文件预签名下载 URL
func (s *BackupService) GetBackupDownloadURL(ctx context.Context, backupID string) (BackupDownloadResponse, error) {
	var download BackupDownloadResponse
	record, err := s.GetBackupRecord(ctx, backupID)
	if err != nil {
		return download, err
	}
	if record.Status != "completed" {
		return download, infraerrors.BadRequest("BACKUP_NOT_COMPLETED", "backup is not completed")
	}

	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return download, err
	}
	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return download, err
	}
	if len(record.Parts) > 0 {
		parts := append([]BackupPart(nil), record.Parts...)
		sort.Slice(parts, func(i, j int) bool { return parts[i].Index < parts[j].Index })
		for i, part := range parts {
			if part.Index != i+1 || part.S3Key == "" || part.SizeBytes <= 0 {
				return download, fmt.Errorf("invalid backup part metadata at index %d", i+1)
			}
			url, err := objectStore.PresignURL(ctx, part.S3Key, time.Hour)
			if err != nil {
				return download, fmt.Errorf("presign backup part %d: %w", part.Index, err)
			}
			download.Parts = append(download.Parts, BackupDownloadPart{Index: part.Index, SizeBytes: part.SizeBytes, URL: url})
		}
		return download, nil
	}
	if record.S3Key == "" {
		return download, errors.New("backup object key is empty")
	}

	url, err := objectStore.PresignURL(ctx, record.S3Key, 1*time.Hour)
	if err != nil {
		return download, fmt.Errorf("presign url: %w", err)
	}
	download.URL = url
	return download, nil
}

// ─── 内部方法 ───

func (s *BackupService) loadS3Config(ctx context.Context) (*BackupS3Config, error) {
	cfg, err := s.loadStoredS3Config(ctx)
	if err != nil || cfg == nil {
		return cfg, err
	}
	// 解密 SecretAccessKey；兼容旧版明文存储。
	if cfg.SecretAccessKey != "" {
		decrypted, err := s.encryptor.Decrypt(cfg.SecretAccessKey)
		if err != nil {
			logger.LegacyPrintf("service.backup", "[Backup] S3 SecretAccessKey 解密失败（可能是旧的未加密数据）: %v", err)
		} else {
			cfg.SecretAccessKey = decrypted
		}
	}
	return cfg, nil
}

// loadStoredS3Config 返回数据库中的原始密文，仅供需要原样保留 Secret 的更新路径使用。
func (s *BackupService) loadStoredS3Config(ctx context.Context) (*BackupS3Config, error) {
	raw, err := s.settingRepo.GetValue(ctx, settingKeyBackupS3Config)
	if err != nil || raw == "" {
		return nil, nil //nolint:nilnil // no config is a valid state
	}
	var cfg BackupS3Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, ErrBackupS3ConfigCorrupt
	}
	return &cfg, nil
}

func (s *BackupService) getOrCreateStore(ctx context.Context, cfg *BackupS3Config) (BackupObjectStore, error) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	if s.store != nil && s.s3Cfg != nil {
		return s.store, nil
	}

	if cfg == nil {
		return nil, ErrBackupS3NotConfigured
	}

	store, err := s.storeFactory(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s.store = store
	s.s3Cfg = cfg
	return store, nil
}

func (s *BackupService) buildS3Key(cfg *BackupS3Config, fileName string) string {
	prefix := strings.TrimRight(cfg.Prefix, "/")
	if prefix == "" {
		prefix = "backups"
	}
	return fmt.Sprintf("%s/%s/%s", prefix, time.Now().Format("2006/01/02"), fileName)
}

// loadRecords 加载备份记录，区分"无数据"和"数据损坏"
func (s *BackupService) loadRecords(ctx context.Context) ([]BackupRecord, error) {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	return s.loadRecordsLocked(ctx)
}

// loadRecordsLocked 在已持有 recordsMu 锁的情况下加载记录
func (s *BackupService) loadRecordsLocked(ctx context.Context) ([]BackupRecord, error) {
	raw, err := s.settingRepo.GetValue(ctx, settingKeyBackupRecords)
	if err != nil || raw == "" {
		return nil, nil //nolint:nilnil // no records is a valid state
	}
	var records []BackupRecord
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		return nil, ErrBackupRecordsCorrupt
	}
	return records, nil
}

// saveRecordsLocked 在已持有 recordsMu 锁的情况下保存记录
func (s *BackupService) saveRecordsLocked(ctx context.Context, records []BackupRecord) error {
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, settingKeyBackupRecords, string(data))
}

// saveRecord 保存单条记录（带互斥锁保护）
func (s *BackupService) saveRecord(ctx context.Context, record *BackupRecord) error {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()

	records, _ := s.loadRecordsLocked(ctx)

	// 更新已有记录或追加
	found := false
	for i := range records {
		if records[i].ID == record.ID {
			records[i] = *record
			found = true
			break
		}
	}
	if !found {
		records = append(records, *record)
	}

	// 限制记录数量
	if len(records) > maxBackupRecords {
		records = records[len(records)-maxBackupRecords:]
	}

	return s.saveRecordsLocked(ctx, records)
}

func (s *BackupService) cleanupOldBackups(ctx context.Context, schedule *BackupScheduleConfig) error {
	if schedule == nil {
		return nil
	}

	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()

	records, err := s.loadRecordsLocked(ctx)
	if err != nil {
		return err
	}

	// 按时间倒序
	sort.Slice(records, func(i, j int) bool {
		return records[i].StartedAt > records[j].StartedAt
	})

	var toDelete []BackupRecord
	var toKeep []BackupRecord

	for i, r := range records {
		shouldDelete := false

		// 按保留份数清理
		if schedule.RetainCount > 0 && i >= schedule.RetainCount {
			shouldDelete = true
		}

		// 按保留天数清理
		if schedule.RetainDays > 0 && r.StartedAt != "" {
			startedAt, err := time.Parse(time.RFC3339, r.StartedAt)
			if err == nil && time.Since(startedAt) > time.Duration(schedule.RetainDays)*24*time.Hour {
				shouldDelete = true
			}
		}

		if shouldDelete && r.Status == "completed" {
			toDelete = append(toDelete, r)
		} else {
			toKeep = append(toKeep, r)
		}
	}

	var cleanupErrs []error
	deletedCount := 0
	for _, r := range toDelete {
		if err := s.deleteBackupObjects(ctx, &r); err != nil {
			toKeep = append(toKeep, r)
			cleanupErrs = append(cleanupErrs, fmt.Errorf("cleanup backup %s: %w", r.ID, err))
			continue
		}
		deletedCount++
	}

	if len(toDelete) > 0 {
		if err := s.saveRecordsLocked(ctx, toKeep); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
		if deletedCount > 0 {
			logger.LegacyPrintf("service.backup", "[Backup] 自动清理了 %d 个过期备份", deletedCount)
		}
		return errors.Join(cleanupErrs...)
	}
	return nil
}

func backupObjectKeys(record *BackupRecord) []string {
	if record == nil {
		return nil
	}
	keys := make([]string, 0, len(record.Parts)+1)
	seen := make(map[string]struct{}, len(record.Parts)+1)
	appendKey := func(key string) {
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	appendKey(record.S3Key)
	for _, part := range record.Parts {
		appendKey(part.S3Key)
	}
	return keys
}

func (s *BackupService) deleteBackupObjects(ctx context.Context, record *BackupRecord) error {
	if len(backupObjectKeys(record)) == 0 {
		return nil
	}
	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return err
	}
	if s3Cfg == nil {
		return nil
	}
	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return err
	}
	return deleteBackupObjectKeys(ctx, objectStore, record)
}

func deleteBackupObjectKeys(ctx context.Context, store BackupObjectStore, record *BackupRecord) error {
	var errs []error
	for _, key := range backupObjectKeys(record) {
		if err := store.Delete(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("delete backup object %q: %w", key, err))
		}
	}
	return errors.Join(errs...)
}
