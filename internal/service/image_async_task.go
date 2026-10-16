package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/google/uuid"
)

const (
	ImageTaskStateQueued    = "queued"
	ImageTaskStateRunning   = "running"
	ImageTaskStateSucceeded = "succeeded"
	ImageTaskStateFailed    = "failed"
	ImageTaskStateCancelled = "cancelled"
	ImageTaskStateExpired   = "expired"

	ImageTaskSettlementPending  = "pending"
	ImageTaskSettlementSettled  = "settled"
	ImageTaskSettlementReleased = "released"

	// ExecutionPhase is intentionally internal and does not add another
	// user-visible task state. Once dispatched is durably saved, recovery must
	// never call the non-idempotent image upstream again.
	ImageTaskExecutionPhasePending    = "pending"
	ImageTaskExecutionPhaseDispatched = "dispatched"
	ImageTaskExecutionPhasePersisted  = "persisted"
)

var (
	ErrImageTaskNotFound        = errors.New("image task not found")
	ErrImageTaskForbidden       = errors.New("image task does not belong to caller")
	ErrImageTaskConflict        = errors.New("image task state conflict")
	ErrImageTaskAlreadyRunning  = errors.New("image task has already been dispatched")
	ErrImageTaskQueueFull       = errors.New("image task queue is full")
	ErrImageTaskStorageDisabled = errors.New("image task object storage is unavailable")
	ErrImageTaskInvalidInput    = errors.New("invalid image task input")
	// ErrImageTaskPersistenceUncertain means PostgreSQL may already have
	// committed the task, so the matching quota reservation must be preserved.
	ErrImageTaskPersistenceUncertain = errors.New("image task persistence outcome is uncertain")
)

const imageTaskPersistenceRecoveryTimeout = 5 * time.Second

// ImageTaskConfig keeps image work outside the text request hot path. Every
// value is bounded so a typo cannot create an unbounded queue or worker storm.
type ImageTaskConfig struct {
	Enabled              bool          `json:"enabled"`
	Workers              int           `json:"workers"`
	QueueCapacity        int           `json:"queue_capacity"`
	BatchSize            int           `json:"batch_size"`
	PerUserConcurrency   int           `json:"per_user_concurrency"`
	MaxImagesPerTask     int           `json:"max_images_per_task"`
	MaxAttempts          int           `json:"max_attempts"`
	ExecutionTimeout     time.Duration `json:"execution_timeout"`
	ClaimTTL             time.Duration `json:"claim_ttl"`
	PollInterval         time.Duration `json:"poll_interval"`
	ResultRetention      time.Duration `json:"result_retention"`
	DownloadURLTTL       time.Duration `json:"download_url_ttl"`
	MaxRequestBytes      int64         `json:"max_request_bytes"`
	MaxArtifactBytes     int64         `json:"max_artifact_bytes"`
	MaxTaskArtifactBytes int64         `json:"max_task_artifact_bytes"`
}

func DefaultImageTaskConfig() ImageTaskConfig {
	return ImageTaskConfig{
		Workers:              2,
		QueueCapacity:        256,
		BatchSize:            16,
		PerUserConcurrency:   1,
		MaxImagesPerTask:     4,
		MaxAttempts:          3,
		ExecutionTimeout:     10 * time.Minute,
		ClaimTTL:             15 * time.Minute,
		PollInterval:         time.Second,
		ResultRetention:      7 * 24 * time.Hour,
		DownloadURLTTL:       15 * time.Minute,
		MaxRequestBytes:      1 << 20,
		MaxArtifactBytes:     25 << 20,
		MaxTaskArtifactBytes: 80 << 20,
	}
}

func (c ImageTaskConfig) Validate() error {
	checks := []struct {
		ok  bool
		msg string
	}{
		{c.Workers >= 1 && c.Workers <= 64, "workers must be between 1 and 64"},
		{c.QueueCapacity >= 1 && c.QueueCapacity <= 100000, "queue_capacity must be between 1 and 100000"},
		{c.BatchSize >= 1 && c.BatchSize <= 500, "batch_size must be between 1 and 500"},
		{c.PerUserConcurrency >= 1 && c.PerUserConcurrency <= 16, "per_user_concurrency must be between 1 and 16"},
		{c.MaxImagesPerTask >= 1 && c.MaxImagesPerTask <= 16, "max_images_per_task must be between 1 and 16"},
		{c.MaxAttempts >= 1 && c.MaxAttempts <= 10, "max_attempts must be between 1 and 10"},
		{c.ExecutionTimeout >= 10*time.Second && c.ExecutionTimeout <= time.Hour, "execution_timeout must be between 10s and 1h"},
		{c.ClaimTTL >= c.ExecutionTimeout+3*time.Minute && c.ClaimTTL <= 2*time.Hour, "claim_ttl must exceed execution_timeout by at least 3m and be at most 2h"},
		{c.PollInterval >= 100*time.Millisecond && c.PollInterval <= time.Minute, "poll_interval must be between 100ms and 1m"},
		{c.ResultRetention >= time.Hour && c.ResultRetention <= 90*24*time.Hour, "result_retention must be between 1h and 90d"},
		{c.DownloadURLTTL >= time.Minute && c.DownloadURLTTL <= 24*time.Hour, "download_url_ttl must be between 1m and 24h"},
		{c.MaxRequestBytes >= 16<<10 && c.MaxRequestBytes <= 4<<20, "max_request_bytes must be between 16KiB and 4MiB"},
		{c.MaxArtifactBytes >= 1<<20 && c.MaxArtifactBytes <= 100<<20, "max_artifact_bytes must be between 1MiB and 100MiB"},
		{c.MaxTaskArtifactBytes >= c.MaxArtifactBytes && c.MaxTaskArtifactBytes <= 500<<20, "max_task_artifact_bytes must be at least max_artifact_bytes and at most 500MiB"},
	}
	for _, check := range checks {
		if !check.ok {
			return errors.New(check.msg)
		}
	}
	return nil
}

type ImageTaskArtifact struct {
	// ObjectKey is persisted in the private task payload. HTTP handlers must map
	// ImageTask to a public DTO that omits this field and exposes only owned or
	// signed download URLs.
	ObjectKey   string `json:"object_key"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

// ImageTask persists only encrypted request data and non-secret policy facts.
// API keys, upstream credentials and OAuth tokens must never be copied here.
type ImageTask struct {
	ID                 string          `json:"id"`
	IdempotencyKey     string          `json:"idempotency_key,omitempty"`
	PayloadHash        string          `json:"payload_hash"`
	UserID             int64           `json:"user_id"`
	OrganizationID     int64           `json:"organization_id,omitempty"`
	BillingSubjectType string          `json:"billing_subject_type,omitempty"`
	BillingSubjectID   int64           `json:"billing_subject_id,omitempty"`
	APIKeyID           int64           `json:"api_key_id,omitempty"`
	GatewayKeyID       string          `json:"gateway_key_id,omitempty"`
	GroupID            *int64          `json:"group_id,omitempty"`
	Model              string          `json:"model"`
	Endpoint           string          `json:"endpoint"`
	ContentType        string          `json:"content_type"`
	ImageCount         int             `json:"image_count"`
	EncryptedRequest   string          `json:"-"`
	PolicySnapshot     json.RawMessage `json:"policy_snapshot,omitempty"`
	BillingSnapshot    json.RawMessage `json:"billing_snapshot,omitempty"`
	// ExecutionSnapshot contains only the non-secret upstream/result facts
	// required to retry billing without generating the image a second time.
	ExecutionSnapshot     json.RawMessage                   `json:"execution_snapshot,omitempty"`
	ReservationID         string                            `json:"reservation_id,omitempty"`
	QuotaRequestID        string                            `json:"quota_request_id,omitempty"`
	LeaseID               string                            `json:"lease_id,omitempty"`
	Region                string                            `json:"region,omitempty"`
	GatewayID             string                            `json:"gateway_id,omitempty"`
	RateMultiplier        float64                           `json:"rate_multiplier,omitempty"`
	RatePolicyAudit       *gatewaylite.RatePolicyAudit      `json:"rate_policy_audit,omitempty"`
	BillingSettlementMode gatewaylite.BillingSettlementMode `json:"billing_settlement_mode,omitempty"`
	FundingSnapshot       *gatewaylite.FundingAuthorization `json:"funding_snapshot,omitempty"`
	EntryGatewayNodeID    int64                             `json:"entry_gateway_node_id,omitempty"`
	EntryGlobalGroupID    int64                             `json:"entry_global_group_id,omitempty"`
	ReservedCents         int64                             `json:"reserved_cents"`
	ActualCents           int64                             `json:"actual_cents"`
	SettlementState       string                            `json:"settlement_state"`
	State                 string                            `json:"state"`
	ExecutionPhase        string                            `json:"execution_phase,omitempty"`
	Progress              int                               `json:"progress"`
	Attempt               int                               `json:"attempt"`
	MaxAttempts           int                               `json:"max_attempts"`
	NextAttemptAt         time.Time                         `json:"next_attempt_at"`
	ClaimOwner            string                            `json:"-"`
	ClaimUntil            *time.Time                        `json:"-"`
	CancelRequested       bool                              `json:"cancel_requested"`
	ErrorCode             string                            `json:"error_code,omitempty"`
	ErrorMessage          string                            `json:"error_message,omitempty"`
	Artifacts             []ImageTaskArtifact               `json:"artifacts,omitempty"`
	CreatedAt             time.Time                         `json:"created_at"`
	UpdatedAt             time.Time                         `json:"updated_at"`
	StartedAt             *time.Time                        `json:"started_at,omitempty"`
	CompletedAt           *time.Time                        `json:"completed_at,omitempty"`
	ExpiresAt             time.Time                         `json:"expires_at"`
	SettlementCompletedAt *time.Time                        `json:"settlement_completed_at,omitempty"`
	// ExecutorSettled is set only after the concrete executor has durably
	// recorded the usage correction. A worker crash before SaveClaimed simply
	// replays the same financial request ID; RecordUsage remains idempotent.
	ExecutorSettled bool `json:"executor_settled,omitempty"`
}

func (t *ImageTask) Normalize(now time.Time, cfg ImageTaskConfig) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(t.ID) == "" {
		t.ID = uuid.NewString()
	}
	t.ID = strings.ToLower(strings.TrimSpace(t.ID))
	t.IdempotencyKey = strings.TrimSpace(t.IdempotencyKey)
	t.GatewayKeyID = strings.TrimSpace(t.GatewayKeyID)
	t.Model = strings.TrimSpace(t.Model)
	t.Endpoint = strings.TrimSpace(t.Endpoint)
	t.ContentType = strings.TrimSpace(t.ContentType)
	if t.State == "" {
		t.State = ImageTaskStateQueued
	}
	if t.SettlementState == "" {
		t.SettlementState = ImageTaskSettlementPending
	}
	if t.ExecutionPhase == "" {
		if t.State == ImageTaskStateRunning {
			// Rows created before execution_phase existed are treated
			// conservatively: a running request may already have reached upstream.
			t.ExecutionPhase = ImageTaskExecutionPhaseDispatched
		} else {
			t.ExecutionPhase = ImageTaskExecutionPhasePending
		}
	}
	if t.ImageCount <= 0 {
		t.ImageCount = 1
	}
	if t.MaxAttempts <= 0 {
		t.MaxAttempts = cfg.MaxAttempts
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
	}
	if t.NextAttemptAt.IsZero() {
		t.NextAttemptAt = now
	}
	if t.ExpiresAt.IsZero() {
		t.ExpiresAt = now.Add(cfg.ResultRetention)
	}
}

func (t *ImageTask) Terminal() bool {
	if t == nil {
		return true
	}
	switch t.State {
	case ImageTaskStateSucceeded, ImageTaskStateFailed, ImageTaskStateCancelled, ImageTaskStateExpired:
		return true
	default:
		return false
	}
}

func (t *ImageTask) FinanciallyFinal() bool {
	if t == nil || !t.Terminal() {
		return false
	}
	return t.SettlementState == ImageTaskSettlementSettled || t.SettlementState == ImageTaskSettlementReleased
}

func (t *ImageTask) BelongsTo(userID, apiKeyID int64, gatewayKeyID string) bool {
	if t == nil || t.UserID != userID {
		return false
	}
	if strings.TrimSpace(t.GatewayKeyID) != "" {
		return strings.TrimSpace(t.GatewayKeyID) == strings.TrimSpace(gatewayKeyID)
	}
	return t.APIKeyID > 0 && t.APIKeyID == apiKeyID
}

func (t *ImageTask) BillingRequestID() string {
	if t == nil {
		return ""
	}
	if value := strings.TrimSpace(t.QuotaRequestID); value != "" {
		return value
	}
	if value := strings.TrimSpace(t.ID); value != "" {
		return "image-task:" + value
	}
	return ""
}

func (t *ImageTask) UsageContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || strings.TrimSpace(t.QuotaRequestID) == "" || t.UserID <= 0 {
		return ctx
	}
	groupID := int64(0)
	if t.GroupID != nil {
		groupID = *t.GroupID
	}
	var audit *gatewaylite.RatePolicyAudit
	if t.RatePolicyAudit != nil && t.RatePolicyAudit.Valid() {
		cloned := t.RatePolicyAudit.Clone()
		audit = &cloned
	}
	var funding *gatewaylite.RequestFunding
	if t.FundingSnapshot != nil {
		quota := gatewaylite.DefaultRedisQuota()
		if runtime := gatewaylite.DefaultRuntimeConfigRef(); runtime != nil && quota != nil {
			quota = quota.WithPrefix(runtime.Current().RedisPrefix)
		}
		funding = gatewaylite.NewRequestFunding(*t.FundingSnapshot, gatewaylite.DefaultControlPlaneClientRef(), quota)
	}
	return gatewaylite.ContextWithRequestMeta(ctx, gatewaylite.RequestMeta{
		Funding: funding, EntryGatewayNodeID: t.EntryGatewayNodeID, EntryGlobalGroupID: t.EntryGlobalGroupID,
		RequestID: t.QuotaRequestID, KeyID: t.GatewayKeyID, UserID: t.UserID,
		OrganizationID: t.OrganizationID, BillingSubjectType: t.BillingSubjectType, BillingSubjectID: t.BillingSubjectID,
		LeaseID: t.LeaseID, Region: t.Region, GatewayID: t.GatewayID, GroupID: groupID,
		RateMultiplier: t.RateMultiplier, RatePolicyAudit: audit, AccountRateMultiplier: 1,
		AsyncSettlement: true, BillingSettlementMode: t.BillingSettlementMode,
	})
}

type ImageTaskCreateInput struct {
	IdempotencyKey   string
	UserID           int64
	APIKeyID         int64
	GatewayKeyID     string
	GroupID          *int64
	Model            string
	Endpoint         string
	ContentType      string
	ImageCount       int
	RequestPayload   []byte
	PolicySnapshot   json.RawMessage
	BillingSnapshot  json.RawMessage
	MaxReservedCents int64
}

type ImageTaskReservation struct {
	ID            string
	ReservedCents int64
}

type ImageTaskStore interface {
	Ready(ctx context.Context) error
	FindByIdempotency(ctx context.Context, userID, apiKeyID int64, gatewayKeyID, idempotencyKey string) (*ImageTask, error)
	Create(ctx context.Context, task *ImageTask, queueCapacity, perUserConcurrency int) (*ImageTask, bool, error)
	Get(ctx context.Context, id string) (*ImageTask, error)
	GetByUser(ctx context.Context, id string, userID int64) (*ImageTask, error)
	ListByOwner(ctx context.Context, userID, apiKeyID int64, gatewayKeyID string, limit int, cursor string) ([]*ImageTask, string, error)
	ListByUser(ctx context.Context, userID int64, limit int, cursor string) ([]*ImageTask, string, error)
	ListByUserState(ctx context.Context, userID int64, state string, limit int, cursor string) ([]*ImageTask, string, error)
	ListAll(ctx context.Context, state string, limit int, cursor string) ([]*ImageTask, string, error)
	QueueDepth(ctx context.Context) (int64, error)
	ActiveForUser(ctx context.Context, userID int64) (int, error)
	ClaimDue(ctx context.Context, owner string, now time.Time, limit int, ttl time.Duration) ([]*ImageTask, error)
	SaveClaimed(ctx context.Context, task *ImageTask, owner string) error
	SaveUnclaimedTerminal(ctx context.Context, task *ImageTask) error
	RequestCancel(ctx context.Context, id string, userID, apiKeyID int64, gatewayKeyID string, now time.Time) (*ImageTask, error)
	RequestCancelForUser(ctx context.Context, id string, userID int64, now time.Time) (*ImageTask, error)
	ListCleanupDue(ctx context.Context, now time.Time, limit int) ([]*ImageTask, error)
	DeleteFinal(ctx context.Context, id string) error
}

type ImageTaskBilling interface {
	Reserve(ctx context.Context, task *ImageTask, maxCents int64) (ImageTaskReservation, error)
	Settle(ctx context.Context, task *ImageTask, actualCents int64) error
	Release(ctx context.Context, task *ImageTask) error
}

type ImageTaskReplayBilling interface {
	ReserveReplay(ctx context.Context, task *ImageTask, maxCents int64) (ImageTaskReservation, error)
}

type ImageTaskObjectStore interface {
	Put(ctx context.Context, key string, body io.Reader, contentType string, maxBytes int64) (size int64, sha256Hex string, err error)
	Open(ctx context.Context, key string) (io.ReadCloser, string, int64, error)
	Delete(ctx context.Context, key string) error
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type ImageTaskExecutionArtifact struct {
	FileName    string
	ContentType string
	Reader      io.ReadCloser
}

type ImageTaskExecutionResult struct {
	Artifacts      []ImageTaskExecutionArtifact
	ActualCents    int64
	BillingSettled bool
}

type ImageTaskExecutor interface {
	Execute(ctx context.Context, task *ImageTask, requestPayload []byte) (ImageTaskExecutionResult, error)
}

// ImageTaskExecutionSettler is implemented by durable executors that can retry
// the financial record after upstream success without repeating the upstream
// generation. The task's encrypted request is not needed for this stage.
type ImageTaskExecutionSettler interface {
	Settle(ctx context.Context, task *ImageTask) error
}

// ImageTaskService owns submission, visibility and durable background work.
// The database is the queue; the wake channel only reduces polling latency and
// may be dropped without losing a task.
type ImageTaskService struct {
	store     ImageTaskStore
	billing   ImageTaskBilling
	objects   ImageTaskObjectStore
	executor  ImageTaskExecutor
	encryptor SecretEncryptor
	cfg       ImageTaskConfig
	workerID  string
	wake      chan struct{}
	stop      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	wg        sync.WaitGroup
	running   atomic.Int64
	activeMu  sync.Mutex
	active    map[string]context.CancelFunc

	// [tokenav 定制] 保留本地实现
	// storageResolver 由 NewImageTaskServiceWithResolver 注入（见
	// image_task_resolver_bridge.go）。它只用于在合并 v0.2.5 后承接官方
	// wire 接线中「后台设置可热切换对象存储」的语义，不影响本地原有的
	// cfg.Enabled + 依赖齐全 的启用判定与调度行为。
	storageResolver ImageStorageResolver
}

func NewImageTaskService(store ImageTaskStore, billing ImageTaskBilling, objects ImageTaskObjectStore, executor ImageTaskExecutor, encryptor SecretEncryptor, cfg ImageTaskConfig) (*ImageTaskService, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid image task config: %w", err)
	}
	if cfg.Enabled && (store == nil || billing == nil || objects == nil || executor == nil || encryptor == nil) {
		return nil, errors.New("enabled image task service requires store, billing, object store, executor and encryptor")
	}
	return &ImageTaskService{
		store: store, billing: billing, objects: objects, executor: executor, encryptor: encryptor, cfg: cfg,
		workerID: uuid.NewString(), wake: make(chan struct{}, 1), stop: make(chan struct{}),
		active: make(map[string]context.CancelFunc),
	}, nil
}

func (s *ImageTaskService) Start() {
	if s == nil || !s.cfg.Enabled {
		return
	}
	s.startOnce.Do(func() {
		for i := 0; i < s.cfg.Workers; i++ {
			s.wg.Add(1)
			go s.worker(i)
		}
		s.wg.Add(1)
		go s.cleanupWorker()
	})
}

func (s *ImageTaskService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stop)
		// A shutdown must not wait for a slow upstream until the full image
		// timeout. Cancelling only the worker context leaves the durable task
		// queued for a later process; it does not cancel the customer's task.
		s.activeMu.Lock()
		for _, cancel := range s.active {
			cancel()
		}
		s.activeMu.Unlock()
	})
	s.wg.Wait()
}

func (s *ImageTaskService) Submit(ctx context.Context, in ImageTaskCreateInput) (*ImageTask, bool, error) {
	if s == nil || !s.cfg.Enabled {
		return nil, false, ErrImageTaskStorageDisabled
	}
	if in.UserID <= 0 || (in.APIKeyID <= 0 && strings.TrimSpace(in.GatewayKeyID) == "") {
		return nil, false, errors.New("authenticated image task owner is required")
	}
	if len(in.RequestPayload) == 0 || strings.TrimSpace(in.Model) == "" {
		return nil, false, errors.New("model and request payload are required")
	}
	if int64(len(in.RequestPayload)) > s.cfg.MaxRequestBytes {
		return nil, false, fmt.Errorf("image task request exceeds %d bytes", s.cfg.MaxRequestBytes)
	}
	if in.ImageCount <= 0 || in.ImageCount > s.cfg.MaxImagesPerTask {
		return nil, false, fmt.Errorf("image_count must be between 1 and %d", s.cfg.MaxImagesPerTask)
	}
	hash := sha256.Sum256(in.RequestPayload)
	payloadHash := hex.EncodeToString(hash[:])
	idempotencyKey := strings.TrimSpace(in.IdempotencyKey)
	if idempotencyKey != "" {
		existing, err := s.store.FindByIdempotency(ctx, in.UserID, in.APIKeyID, in.GatewayKeyID, idempotencyKey)
		switch {
		case err == nil:
			if existing.PayloadHash != payloadHash {
				return nil, false, fmt.Errorf("%w: idempotency key was used for a different request", ErrImageTaskConflict)
			}
			// Idempotent reads must succeed even when the queue is now full or
			// the user's balance has changed after the original reservation.
			return existing, true, nil
		case !errors.Is(err, ErrImageTaskNotFound):
			return nil, false, fmt.Errorf("lookup image task idempotency key: %w", err)
		}
	}
	ciphertext, err := s.encryptor.Encrypt(string(in.RequestPayload))
	if err != nil {
		return nil, false, fmt.Errorf("encrypt image task request: %w", err)
	}
	now := time.Now().UTC()
	task := &ImageTask{
		ID: uuid.NewString(), IdempotencyKey: idempotencyKey, PayloadHash: payloadHash,
		UserID: in.UserID, APIKeyID: in.APIKeyID, GatewayKeyID: strings.TrimSpace(in.GatewayKeyID), GroupID: in.GroupID,
		Model: in.Model, Endpoint: in.Endpoint, ContentType: in.ContentType, ImageCount: in.ImageCount,
		EncryptedRequest: ciphertext, PolicySnapshot: cloneJSON(in.PolicySnapshot), BillingSnapshot: cloneJSON(in.BillingSnapshot),
		State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending, MaxAttempts: s.cfg.MaxAttempts,
	}
	if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
		task.OrganizationID = meta.OrganizationID
		task.BillingSubjectType = strings.TrimSpace(meta.BillingSubjectType)
		task.BillingSubjectID = meta.BillingSubjectID
	}
	task.Normalize(now, s.cfg)
	reservation, err := s.billing.Reserve(ctx, task, in.MaxReservedCents)
	if err != nil {
		return nil, false, fmt.Errorf("reserve image task quota: %w", err)
	}
	task.ReservationID = strings.TrimSpace(reservation.ID)
	task.ReservedCents = reservation.ReservedCents
	created, replayed, err := s.store.Create(ctx, task, s.cfg.QueueCapacity, s.cfg.PerUserConcurrency)
	if err != nil {
		return s.recoverSubmitAfterCreateError(ctx, task, payloadHash, err)
	}
	if replayed && created.ID != task.ID {
		_ = s.billing.Release(context.WithoutCancel(ctx), task)
	}
	if !replayed {
		s.signal()
	}
	return created, replayed, nil
}

func (s *ImageTaskService) recoverSubmitAfterCreateError(ctx context.Context, task *ImageTask, payloadHash string, createErr error) (*ImageTask, bool, error) {
	// A lost COMMIT acknowledgement is indistinguishable from rollback at the
	// call site. Re-read the unique owner/idempotency identity from the primary
	// before deciding whether the Redis reservation may be released.
	if task == nil || strings.TrimSpace(task.IdempotencyKey) == "" {
		return nil, false, fmt.Errorf("%w: persist image task: %v", ErrImageTaskPersistenceUncertain, createErr)
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageTaskPersistenceRecoveryTimeout)
	defer cancel()
	existing, lookupErr := s.store.FindByIdempotency(
		recoveryCtx, task.UserID, task.APIKeyID, task.GatewayKeyID, task.IdempotencyKey,
	)
	switch {
	case lookupErr == nil:
		if existing == nil || !strings.EqualFold(strings.TrimSpace(existing.PayloadHash), strings.TrimSpace(payloadHash)) {
			if existing == nil || existing.ID == task.ID {
				return nil, false, fmt.Errorf("%w: committed image task could not be verified", ErrImageTaskPersistenceUncertain)
			}
			_ = s.billing.Release(context.WithoutCancel(ctx), task)
			return nil, false, fmt.Errorf("%w: idempotency key was used for a different request", ErrImageTaskConflict)
		}
		if existing.ID == task.ID {
			// This task committed and only its acknowledgement was lost. Keep its
			// original reservation and wake workers exactly as after a normal create.
			s.signal()
			return existing, false, nil
		}
		// A concurrent request won the unique identity. Its reservation remains
		// authoritative; this request's unused reservation can be released.
		_ = s.billing.Release(context.WithoutCancel(ctx), task)
		return existing, true, nil
	case errors.Is(lookupErr, ErrImageTaskNotFound):
		// A primary read confirming absence is the only safe refund boundary.
		_ = s.billing.Release(context.WithoutCancel(ctx), task)
		return nil, false, fmt.Errorf("persist image task: %w", createErr)
	default:
		// Fail closed. A same-key retry or operator reconciliation can determine
		// the outcome; refunding here could underfund a live queued task.
		return nil, false, fmt.Errorf("%w: persist image task: %v; verify: %v", ErrImageTaskPersistenceUncertain, createErr, lookupErr)
	}
}

func (s *ImageTaskService) GetOwned(ctx context.Context, id string, userID, apiKeyID int64, gatewayKeyID string) (*ImageTask, error) {
	task, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !task.BelongsTo(userID, apiKeyID, gatewayKeyID) {
		return nil, ErrImageTaskForbidden
	}
	return task, nil
}

func (s *ImageTaskService) ListOwned(ctx context.Context, userID, apiKeyID int64, gatewayKeyID string, limit int, cursor string) ([]*ImageTask, string, error) {
	if s == nil || !s.cfg.Enabled || s.store == nil {
		return nil, "", ErrImageTaskStorageDisabled
	}
	return s.store.ListByOwner(ctx, userID, apiKeyID, gatewayKeyID, limit, cursor)
}

// ListForUser is used only by the authenticated user console. The user ID is
// derived from the JWT session by the handler; callers cannot provide another
// owner ID. API-key endpoints continue to use the narrower ListOwned method.
func (s *ImageTaskService) ListForUser(ctx context.Context, userID int64, limit int, cursor string) ([]*ImageTask, string, error) {
	if s == nil || !s.cfg.Enabled || s.store == nil {
		return nil, "", ErrImageTaskStorageDisabled
	}
	if userID <= 0 {
		return nil, "", ErrImageTaskForbidden
	}
	return s.store.ListByUser(ctx, userID, limit, cursor)
}

func (s *ImageTaskService) ListForAdmin(ctx context.Context, state string, limit int, cursor string) ([]*ImageTask, string, error) {
	if s == nil || !s.cfg.Enabled || s.store == nil {
		return nil, "", ErrImageTaskStorageDisabled
	}
	return s.store.ListAll(ctx, state, limit, cursor)
}

func (s *ImageTaskService) ListForAdminUser(ctx context.Context, userID int64, state string, limit int, cursor string) ([]*ImageTask, string, error) {
	if s == nil || !s.cfg.Enabled || s.store == nil {
		return nil, "", ErrImageTaskStorageDisabled
	}
	if userID <= 0 {
		return nil, "", ErrImageTaskNotFound
	}
	return s.store.ListByUserState(ctx, userID, state, limit, cursor)
}

func (s *ImageTaskService) GetForUser(ctx context.Context, id string, userID int64) (*ImageTask, error) {
	if s == nil || !s.cfg.Enabled || s.store == nil {
		return nil, ErrImageTaskStorageDisabled
	}
	if userID <= 0 {
		return nil, ErrImageTaskNotFound
	}
	return s.store.GetByUser(ctx, id, userID)
}

func (s *ImageTaskService) GetForAdmin(ctx context.Context, id string) (*ImageTask, error) {
	if s == nil || !s.cfg.Enabled || s.store == nil {
		return nil, ErrImageTaskStorageDisabled
	}
	return s.store.Get(ctx, id)
}

func (s *ImageTaskService) GetForAdminUser(ctx context.Context, id string, userID int64) (*ImageTask, error) {
	if s == nil || !s.cfg.Enabled || s.store == nil {
		return nil, ErrImageTaskStorageDisabled
	}
	if userID <= 0 {
		return nil, ErrImageTaskNotFound
	}
	return s.store.GetByUser(ctx, id, userID)
}

func (s *ImageTaskService) Cancel(ctx context.Context, id string, userID, apiKeyID int64, gatewayKeyID string) (*ImageTask, error) {
	task, err := s.store.RequestCancel(ctx, id, userID, apiKeyID, gatewayKeyID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return s.finishCancellation(ctx, id, task)
}

func (s *ImageTaskService) finishCancellation(ctx context.Context, id string, task *ImageTask) (*ImageTask, error) {
	if task == nil {
		return nil, ErrImageTaskNotFound
	}
	if task.State == ImageTaskStateCancelled && task.SettlementState == ImageTaskSettlementPending {
		if err := s.release(ctx, task); err != nil {
			return nil, err
		}
		if err := s.store.SaveUnclaimedTerminal(ctx, task); err != nil {
			// A worker may have claimed the just-cancelled row between the
			// cancellation transaction and quota release. Release is idempotent;
			// accept the worker's financially-final state instead of returning a
			// misleading conflict after the cancellation already succeeded.
			if errors.Is(err, ErrImageTaskConflict) {
				latest, reloadErr := s.store.Get(ctx, task.ID)
				if reloadErr == nil && latest.State == ImageTaskStateCancelled && latest.SettlementState == ImageTaskSettlementReleased {
					task = latest
				} else {
					return nil, err
				}
			} else {
				return nil, err
			}
		}
	}
	s.activeMu.Lock()
	cancel := s.active[strings.TrimSpace(id)]
	s.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.signal()
	return task, nil
}

func (s *ImageTaskService) CancelForUser(ctx context.Context, id string, userID int64) (*ImageTask, error) {
	if s == nil || !s.cfg.Enabled || s.store == nil || userID <= 0 {
		return nil, ErrImageTaskNotFound
	}
	task, err := s.store.RequestCancelForUser(ctx, id, userID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return s.finishCancellation(ctx, id, task)
}

func (s *ImageTaskService) DownloadURLs(ctx context.Context, task *ImageTask) ([]string, error) {
	if task == nil || task.State != ImageTaskStateSucceeded || task.SettlementState != ImageTaskSettlementSettled {
		return nil, ErrImageTaskConflict
	}
	urls := make([]string, 0, len(task.Artifacts))
	for index, artifact := range task.Artifacts {
		url, err := s.objects.SignedURL(ctx, artifact.ObjectKey, s.cfg.DownloadURLTTL)
		if err != nil {
			return nil, fmt.Errorf("sign image task artifact: %w", err)
		}
		if strings.HasPrefix(url, "local-image-task://") {
			url = fmt.Sprintf("/v1/images/tasks/%s/artifacts/%d", task.ID, index)
		}
		urls = append(urls, url)
	}
	return urls, nil
}

// OpenOwnedArtifact keeps local-development storage behind the same ownership
// check as task status. S3 deployments normally use short-lived signed URLs,
// but this method remains a safe fallback when a provider cannot presign.
func (s *ImageTaskService) OpenOwnedArtifact(ctx context.Context, id string, index int, userID, apiKeyID int64, gatewayKeyID string) (io.ReadCloser, ImageTaskArtifact, error) {
	task, err := s.GetOwned(ctx, id, userID, apiKeyID, gatewayKeyID)
	if err != nil {
		return nil, ImageTaskArtifact{}, err
	}
	if task.State != ImageTaskStateSucceeded || task.SettlementState != ImageTaskSettlementSettled || index < 0 || index >= len(task.Artifacts) {
		return nil, ImageTaskArtifact{}, ErrImageTaskNotFound
	}
	artifact := task.Artifacts[index]
	body, contentType, size, err := s.objects.Open(ctx, artifact.ObjectKey)
	if err != nil {
		return nil, ImageTaskArtifact{}, err
	}
	artifact.ContentType = firstNonEmpty(strings.TrimSpace(contentType), artifact.ContentType)
	artifact.SizeBytes = size
	return body, artifact, nil
}

func (s *ImageTaskService) OpenUserArtifact(ctx context.Context, id string, index int, userID int64) (io.ReadCloser, ImageTaskArtifact, error) {
	task, err := s.GetForUser(ctx, id, userID)
	if err != nil {
		return nil, ImageTaskArtifact{}, err
	}
	if task.State != ImageTaskStateSucceeded || task.SettlementState != ImageTaskSettlementSettled || index < 0 || index >= len(task.Artifacts) {
		return nil, ImageTaskArtifact{}, ErrImageTaskNotFound
	}
	artifact := task.Artifacts[index]
	body, contentType, size, err := s.objects.Open(ctx, artifact.ObjectKey)
	if err != nil {
		return nil, ImageTaskArtifact{}, err
	}
	artifact.ContentType = firstNonEmpty(strings.TrimSpace(contentType), artifact.ContentType)
	artifact.SizeBytes = size
	return body, artifact, nil
}

func (s *ImageTaskService) ReplayForUser(ctx context.Context, id string, userID int64) (*ImageTask, error) {
	if s == nil || !s.cfg.Enabled || s.store == nil || userID <= 0 {
		return nil, ErrImageTaskNotFound
	}
	original, err := s.store.GetByUser(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	return s.replay(ctx, original)
}

func (s *ImageTaskService) Replay(ctx context.Context, id string) (*ImageTask, error) {
	original, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.replay(ctx, original)
}

func (s *ImageTaskService) replay(ctx context.Context, original *ImageTask) (*ImageTask, error) {
	if !original.FinanciallyFinal() || original.State == ImageTaskStateSucceeded {
		return nil, ErrImageTaskConflict
	}
	now := time.Now().UTC()
	task := *original
	task.ID = uuid.NewString()
	task.IdempotencyKey = ""
	task.ReservationID = ""
	task.QuotaRequestID = ""
	task.LeaseID = ""
	task.ReservedCents = 0
	task.ActualCents = 0
	task.SettlementState = ImageTaskSettlementPending
	task.State = ImageTaskStateQueued
	task.ExecutionPhase = ImageTaskExecutionPhasePending
	task.Progress = 0
	task.Attempt = 0
	task.NextAttemptAt = now
	task.ClaimOwner = ""
	task.ClaimUntil = nil
	task.CancelRequested = false
	task.ErrorCode = ""
	task.ErrorMessage = ""
	task.Artifacts = nil
	task.ExecutionSnapshot = nil
	task.ExecutorSettled = false
	task.CreatedAt = now
	task.UpdatedAt = now
	task.StartedAt = nil
	task.CompletedAt = nil
	task.ExpiresAt = now.Add(s.cfg.ResultRetention)
	task.SettlementCompletedAt = nil
	task.ExecutorSettled = false
	var reservation ImageTaskReservation
	var err error
	if replayBilling, ok := s.billing.(ImageTaskReplayBilling); ok {
		reservation, err = replayBilling.ReserveReplay(ctx, &task, original.ReservedCents)
	} else {
		reservation, err = s.billing.Reserve(ctx, &task, original.ReservedCents)
	}
	if err != nil {
		return nil, fmt.Errorf("reserve replayed image task quota: %w", err)
	}
	task.ReservationID = reservation.ID
	task.ReservedCents = reservation.ReservedCents
	created, _, err := s.store.Create(ctx, &task, s.cfg.QueueCapacity, s.cfg.PerUserConcurrency)
	if err != nil {
		_ = s.billing.Release(context.WithoutCancel(ctx), &task)
		return nil, err
	}
	s.signal()
	return created, nil
}

func (s *ImageTaskService) Config() ImageTaskConfig {
	if s == nil {
		return ImageTaskConfig{}
	}
	return s.cfg
}

func (s *ImageTaskService) Metrics(ctx context.Context) (map[string]int64, error) {
	depth, err := s.store.QueueDepth(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]int64{
		"queue_depth":     depth,
		"workers":         int64(s.cfg.Workers),
		"running_workers": s.running.Load(),
		"queue_capacity":  int64(s.cfg.QueueCapacity),
	}, nil
}

func (s *ImageTaskService) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *ImageTaskService) worker(index int) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	owner := fmt.Sprintf("%s/%d", s.workerID, index)
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
		case <-ticker.C:
		}
		s.runBatch(owner)
	}
}

func (s *ImageTaskService) runBatch(owner string) {
	// Claim exactly one task at a time. Pre-claiming a batch and then executing
	// it serially lets later claims expire while they wait behind a slow image
	// generation; another node could then generate the same image twice.
	// BatchSize therefore limits work per wake-up, not concurrent ownership.
	for processed := 0; processed < s.cfg.BatchSize; processed++ {
		select {
		case <-s.stop:
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		tasks, err := s.store.ClaimDue(ctx, owner, time.Now().UTC(), 1, s.cfg.ClaimTTL)
		cancel()
		if err != nil || len(tasks) == 0 {
			return
		}
		s.process(owner, tasks[0])
	}
}

func (s *ImageTaskService) process(owner string, task *ImageTask) {
	if task == nil {
		return
	}
	s.running.Add(1)
	defer s.running.Add(-1)
	if task.State == ImageTaskStateSucceeded {
		s.finishSettlement(owner, task)
		return
	}
	if task.Terminal() {
		// A failed/cancelled task whose release previously failed may be
		// reclaimed solely to finish the financial release. Never regenerate
		// an image after the task has reached a terminal state.
		s.finishRelease(owner, task)
		return
	}
	if task.ExecutionPhase == ImageTaskExecutionPhaseDispatched || task.State == ImageTaskStateRunning {
		// A previous process durably crossed the dispatch boundary. The upstream
		// may have completed even when no response reached us, so recovery chooses
		// a visible failure over charging the provider for a second generation.
		if err := s.deleteDeterministicTaskArtifacts(context.Background(), task.ID); err != nil {
			task.ErrorCode = "object_cleanup_retry"
			task.ErrorMessage = truncateImageTaskError(err.Error())
			task.NextAttemptAt = time.Now().UTC().Add(imageTaskRetryDelay(task.Attempt))
			_ = s.store.SaveClaimed(context.Background(), task, owner)
			return
		}
		s.failAfterDispatch(owner, task, "execution_outcome_unknown", errors.New("image task was recovered after upstream dispatch"))
		return
	}
	if task.CancelRequested || task.State == ImageTaskStateCancelled {
		task.State = ImageTaskStateCancelled
		s.finishRelease(owner, task)
		return
	}
	if time.Now().UTC().After(task.ExpiresAt) {
		task.State = ImageTaskStateExpired
		s.finishRelease(owner, task)
		return
	}
	// Attempt counts every pre-dispatch worker try, including failures while
	// restoring the encrypted request. Incrementing only immediately before the
	// upstream call made corrupt ciphertext retry forever at attempt=0.
	task.Attempt++
	task.UpdatedAt = time.Now().UTC()
	plaintext, err := s.encryptor.Decrypt(task.EncryptedRequest)
	if err != nil {
		s.failOrRetry(owner, task, "request_decrypt_failed", err)
		return
	}
	payloadDigest := sha256.Sum256([]byte(plaintext))
	if !strings.EqualFold(strings.TrimSpace(task.PayloadHash), hex.EncodeToString(payloadDigest[:])) {
		s.failBeforeDispatch(owner, task, "request_integrity_failed", errors.New("decrypted image request does not match its saved digest"))
		return
	}
	now := time.Now().UTC()
	task.State = ImageTaskStateRunning
	task.ExecutionPhase = ImageTaskExecutionPhaseDispatched
	task.Progress = 5
	task.UpdatedAt = now
	if task.StartedAt == nil {
		task.StartedAt = &now
	}
	if err := s.store.SaveClaimed(context.Background(), task, owner); err != nil {
		return
	}
	execCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ExecutionTimeout)
	s.activeMu.Lock()
	s.active[task.ID] = cancel
	s.activeMu.Unlock()
	result, execErr := s.executor.Execute(execCtx, task, []byte(plaintext))
	cancel()
	s.activeMu.Lock()
	delete(s.active, task.ID)
	s.activeMu.Unlock()
	if execErr != nil {
		closeExecutionArtifacts(result.Artifacts)
		s.failAfterDispatch(owner, task, "execution_outcome_unknown", execErr)
		return
	}
	// Object persistence is bounded inside the claim safety margin. Without
	// this timeout a stalled S3-compatible endpoint could outlive claim_ttl and
	// let another node generate the same task again.
	storeTimeout := s.cfg.ClaimTTL - s.cfg.ExecutionTimeout - time.Minute
	if storeTimeout > 2*time.Minute {
		storeTimeout = 2 * time.Minute
	}
	storeCtx, storeCancel := context.WithTimeout(context.Background(), storeTimeout)
	artifacts, err := s.persistArtifacts(storeCtx, task, result.Artifacts)
	storeCancel()
	if err != nil {
		s.deleteArtifacts(context.Background(), artifacts)
		s.failAfterDispatch(owner, task, "object_persist_failed", err)
		return
	}
	task.Artifacts = artifacts
	task.ActualCents = maxInt64(result.ActualCents, 0)
	task.ExecutorSettled = result.BillingSettled
	task.State = ImageTaskStateSucceeded
	task.ExecutionPhase = ImageTaskExecutionPhasePersisted
	task.Progress = 100
	task.ErrorCode = ""
	task.ErrorMessage = ""
	now = time.Now().UTC()
	task.CompletedAt = &now
	task.UpdatedAt = now
	if err := s.store.SaveClaimed(context.Background(), task, owner); err != nil {
		s.recoverSuccessfulSave(owner, task, err)
		return
	}
	s.finishSettlement(owner, task)
}

func (s *ImageTaskService) recoverSuccessfulSave(owner string, attempted *ImageTask, saveErr error) {
	// Do not delete objects or release quota solely because the success UPDATE
	// returned an error: PostgreSQL may have committed before the connection
	// failed. A bounded primary read decides which durable state owns recovery.
	ctx, cancel := context.WithTimeout(context.Background(), imageTaskPersistenceRecoveryTimeout)
	persisted, lookupErr := s.store.Get(ctx, attempted.ID)
	cancel()
	if lookupErr != nil {
		// Not-found and read failures are both uncertain here: neither proves that
		// the success UPDATE rolled back. Keep objects and reservation; after claim
		// expiry the next worker reads the actual committed row.
		return
	}
	if imageTaskPersistedSuccessMatches(attempted, persisted) {
		if persisted.SettlementState == ImageTaskSettlementPending && persisted.ClaimOwner == owner {
			s.finishSettlement(owner, persisted)
		}
		return
	}
	// A still-owned dispatched row proves the success UPDATE did not commit.
	// Other owners or inconsistent success payloads remain untouched so their
	// current claimant can recover authoritatively.
	if persisted.ClaimOwner != owner || persisted.State != ImageTaskStateRunning || persisted.ExecutionPhase != ImageTaskExecutionPhaseDispatched {
		return
	}
	if err := s.deleteArtifacts(context.Background(), attempted.Artifacts); err != nil {
		persisted.ErrorCode = "object_cleanup_retry"
		persisted.ErrorMessage = truncateImageTaskError(err.Error())
		persisted.NextAttemptAt = time.Now().UTC().Add(imageTaskRetryDelay(persisted.Attempt))
		_ = s.store.SaveClaimed(context.Background(), persisted, owner)
		return
	}
	persisted.Artifacts = nil
	s.failAfterDispatch(owner, persisted, "object_persist_failed", saveErr)
}

func imageTaskPersistedSuccessMatches(attempted, persisted *ImageTask) bool {
	if attempted == nil || persisted == nil || attempted.ID != persisted.ID ||
		persisted.State != ImageTaskStateSucceeded || persisted.ExecutionPhase != ImageTaskExecutionPhasePersisted ||
		len(attempted.Artifacts) == 0 || len(attempted.Artifacts) != len(persisted.Artifacts) {
		return false
	}
	for i := range attempted.Artifacts {
		left, right := attempted.Artifacts[i], persisted.Artifacts[i]
		if left.ObjectKey != right.ObjectKey || left.SHA256 != right.SHA256 || left.SizeBytes != right.SizeBytes {
			return false
		}
	}
	return true
}

func (s *ImageTaskService) deleteDeterministicTaskArtifacts(ctx context.Context, taskID string) error {
	if s == nil || s.objects == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	for i := 0; i < s.cfg.MaxImagesPerTask; i++ {
		if err := s.objects.Delete(ctx, fmt.Sprintf("image-tasks/%s/%02d", taskID, i)); err != nil {
			return err
		}
	}
	return nil
}

func (s *ImageTaskService) persistArtifacts(ctx context.Context, task *ImageTask, inputs []ImageTaskExecutionArtifact) ([]ImageTaskArtifact, error) {
	defer closeExecutionArtifacts(inputs)
	if len(inputs) == 0 || len(inputs) > s.cfg.MaxImagesPerTask {
		return nil, errors.New("executor returned an invalid artifact count")
	}
	result := make([]ImageTaskArtifact, 0, len(inputs))
	var total int64
	for i, input := range inputs {
		if input.Reader == nil {
			return result, errors.New("executor returned an empty artifact")
		}
		key := fmt.Sprintf("image-tasks/%s/%02d", task.ID, i)
		size, digest, err := s.objects.Put(ctx, key, input.Reader, input.ContentType, s.cfg.MaxArtifactBytes)
		if err != nil {
			return result, err
		}
		total += size
		if total > s.cfg.MaxTaskArtifactBytes {
			_ = s.objects.Delete(ctx, key)
			return result, errors.New("image task artifacts exceed total size limit")
		}
		result = append(result, ImageTaskArtifact{ObjectKey: key, FileName: safeArtifactName(input.FileName, i), ContentType: input.ContentType, SizeBytes: size, SHA256: digest})
	}
	return result, nil
}

func (s *ImageTaskService) finishSettlement(owner string, task *ImageTask) {
	if task.SettlementState == ImageTaskSettlementSettled {
		_ = s.store.SaveClaimed(context.Background(), task, owner)
		return
	}
	var err error
	if !task.ExecutorSettled {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if settler, ok := s.executor.(ImageTaskExecutionSettler); ok {
			err = settler.Settle(ctx, task)
			if err == nil {
				task.ExecutorSettled = true
			}
		}
		cancel()
	}
	// RecordUsage and quota commit are two independently idempotent durable
	// boundaries. Once the usage event is accepted, retries skip the executor
	// settler and only finish converting the Redis hold into spent quota.
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = s.billing.Settle(ctx, task, task.ActualCents)
		cancel()
	}
	if err != nil {
		task.ErrorCode = "settlement_retry"
		task.ErrorMessage = truncateImageTaskError(err.Error())
		task.NextAttemptAt = time.Now().UTC().Add(imageTaskRetryDelay(task.Attempt))
		_ = s.store.SaveClaimed(context.Background(), task, owner)
		return
	}
	now := time.Now().UTC()
	task.SettlementState = ImageTaskSettlementSettled
	task.SettlementCompletedAt = &now
	task.ErrorCode = ""
	task.ErrorMessage = ""
	task.UpdatedAt = now
	_ = s.store.SaveClaimed(context.Background(), task, owner)
}

func (s *ImageTaskService) cleanupWorker() {
	defer s.wg.Done()
	// Cleanup is deliberately low-frequency and database-driven. Multiple
	// nodes may race safely because object deletion and final-row deletion are
	// idempotent; this avoids adding another distributed coordinator.
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			_, _ = s.Cleanup(ctx, 200)
			cancel()
		}
	}
}

func (s *ImageTaskService) finishRelease(owner string, task *ImageTask) {
	if task.FundingSnapshot != nil && task.ExecutionPhase == ImageTaskExecutionPhaseDispatched {
		// Persisted dispatch is not a zero-cost terminal result. Preserve the card
		// authorization for reconciliation and never regenerate this task on retry.
		task.NextAttemptAt = time.Now().UTC().Add(time.Hour)
		_ = s.store.SaveClaimed(context.Background(), task, owner)
		return
	}
	if err := s.release(context.Background(), task); err != nil {
		if strings.TrimSpace(task.ErrorCode) == "" {
			task.ErrorCode = "release_retry"
			task.ErrorMessage = truncateImageTaskError(err.Error())
		}
		task.NextAttemptAt = time.Now().UTC().Add(imageTaskRetryDelay(task.Attempt))
		_ = s.store.SaveClaimed(context.Background(), task, owner)
		return
	}
	now := time.Now().UTC()
	task.Progress = 100
	task.CompletedAt = &now
	task.UpdatedAt = now
	_ = s.store.SaveClaimed(context.Background(), task, owner)
}

func (s *ImageTaskService) release(ctx context.Context, task *ImageTask) error {
	if task.SettlementState == ImageTaskSettlementReleased {
		return nil
	}
	if err := s.billing.Release(ctx, task); err != nil {
		return fmt.Errorf("release image task quota: %w", err)
	}
	now := time.Now().UTC()
	task.SettlementState = ImageTaskSettlementReleased
	task.SettlementCompletedAt = &now
	return nil
}

func (s *ImageTaskService) failBeforeDispatch(owner string, task *ImageTask, code string, cause error) {
	now := time.Now().UTC()
	task.State = ImageTaskStateFailed
	task.ExecutionPhase = ImageTaskExecutionPhasePending
	task.Progress = 100
	task.ErrorCode = code
	task.ErrorMessage = truncateImageTaskError(cause.Error())
	task.CompletedAt = &now
	task.UpdatedAt = now
	s.finishRelease(owner, task)
}

func (s *ImageTaskService) failAfterDispatch(owner string, task *ImageTask, code string, cause error) {
	now := time.Now().UTC()
	task.State = ImageTaskStateFailed
	task.ExecutionPhase = ImageTaskExecutionPhaseDispatched
	task.Progress = 100
	task.ErrorCode = code
	task.ErrorMessage = truncateImageTaskError(cause.Error())
	task.CompletedAt = &now
	task.UpdatedAt = now
	s.finishRelease(owner, task)
}

func (s *ImageTaskService) failOrRetry(owner string, task *ImageTask, code string, cause error) {
	if task.ExecutionPhase == ImageTaskExecutionPhaseDispatched || task.State == ImageTaskStateRunning {
		s.failAfterDispatch(owner, task, "execution_outcome_unknown", cause)
		return
	}
	now := time.Now().UTC()
	task.ErrorCode = code
	task.ErrorMessage = truncateImageTaskError(cause.Error())
	task.UpdatedAt = now
	if task.CancelRequested {
		task.State = ImageTaskStateCancelled
		s.finishRelease(owner, task)
		return
	}
	if task.Attempt < task.MaxAttempts && now.Before(task.ExpiresAt) {
		task.State = ImageTaskStateQueued
		task.Progress = 0
		task.NextAttemptAt = now.Add(imageTaskRetryDelay(task.Attempt))
		_ = s.store.SaveClaimed(context.Background(), task, owner)
		return
	}
	task.State = ImageTaskStateFailed
	task.Progress = 100
	task.CompletedAt = &now
	s.finishRelease(owner, task)
}

func (s *ImageTaskService) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	tasks, err := s.store.ListCleanupDue(ctx, time.Now().UTC(), limit)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, task := range tasks {
		if task == nil || !task.FinanciallyFinal() {
			continue
		}
		if err := s.deleteArtifacts(ctx, task.Artifacts); err != nil {
			continue
		}
		if err := s.store.DeleteFinal(ctx, task.ID); err != nil {
			continue
		}
		removed++
	}
	return removed, nil
}

func (s *ImageTaskService) deleteArtifacts(ctx context.Context, artifacts []ImageTaskArtifact) error {
	for _, artifact := range artifacts {
		if err := s.objects.Delete(ctx, artifact.ObjectKey); err != nil {
			return err
		}
	}
	return nil
}

func imageTaskRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<(attempt-1)) * 5 * time.Second
}

func truncateImageTaskError(value string) string {
	value = redactImageTaskError(value)
	if len(value) <= 500 {
		return value
	}
	return value[:500]
}

var (
	imageTaskAuthorizationPattern = regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*(?:bearer\s+)?[^\s,;]+`)
	imageTaskURLCredentialPattern = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^@\s/]+@`)
	imageTaskOpenAIKeyPattern     = regexp.MustCompile(`\bsk-[0-9A-Za-z_-]{8,}\b`)
)

func redactImageTaskError(value string) string {
	value = imageTaskAuthorizationPattern.ReplaceAllString(value, "Authorization: ***")
	value = imageTaskURLCredentialPattern.ReplaceAllString(value, `${1}***:***@`)
	value = imageTaskOpenAIKeyPattern.ReplaceAllString(value, "sk-***")
	return strings.TrimSpace(logredact.RedactText(
		value,
		"authorization", "api_key", "apikey", "api-key", "secret-key", "token", "secret",
	))
}

func safeArtifactName(value string, index int) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\\", "_")
	value = strings.ReplaceAll(value, "/", "_")
	value = strings.ReplaceAll(value, "..", "_")
	if value == "" {
		return fmt.Sprintf("image-%02d.bin", index+1)
	}
	return value
}

func closeExecutionArtifacts(artifacts []ImageTaskExecutionArtifact) {
	for _, artifact := range artifacts {
		if artifact.Reader != nil {
			_ = artifact.Reader.Close()
		}
	}
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
