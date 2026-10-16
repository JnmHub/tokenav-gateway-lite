package admin

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/setup"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

type GatewayLiteConfigHandler struct {
	cfg                      *config.Config
	settingService           *service.SettingService
	billingCacheService      *service.BillingCacheService
	billingCacheConfigWriter func(string, config.BillingCacheWriteConfig) error
	billingCacheConfigMu     sync.Mutex
	imageTaskConfigMu        sync.Mutex
	imageTaskRuntimeHash     string
	imageTaskRuntimeEnabled  bool
	imageTaskRuntimeProbe    func(context.Context) (int64, error)
}

// All page-managed sections share one YAML file. Serialize the complete
// read-modify-write transaction so two admin saves cannot silently drop each
// other's section even though the final rename itself is atomic.
var configFileWriteMu sync.Mutex

func NewGatewayLiteConfigHandler(cfg *config.Config, settingService ...*service.SettingService) *GatewayLiteConfigHandler {
	var svc *service.SettingService
	if len(settingService) > 0 {
		svc = settingService[0]
	}
	handler := &GatewayLiteConfigHandler{
		cfg:                      cfg,
		settingService:           svc,
		billingCacheConfigWriter: writeBillingCacheRuntimeConfig,
	}
	if cfg != nil {
		handler.imageTaskRuntimeHash = imageTaskConfigFingerprint(cfg)
		handler.imageTaskRuntimeEnabled = cfg.Gateway.ImageTasks.Enabled && strings.TrimSpace(cfg.Gateway.ImageTasks.DegradedReason) == ""
	}
	return handler
}

func (h *GatewayLiteConfigHandler) SetBillingCacheService(svc *service.BillingCacheService) {
	if h != nil {
		h.billingCacheService = svc
	}
}

// SetImageTaskRuntimeState supplies the effective flag and durable active-task
// count without coupling the admin configuration handler to the request
// handler package. The desired YAML flag may differ after fail-closed startup.
func (h *GatewayLiteConfigHandler) SetImageTaskRuntimeState(enabled bool, probe func(context.Context) (int64, error)) {
	if h != nil {
		h.imageTaskRuntimeEnabled = enabled
		h.imageTaskRuntimeProbe = probe
	}
}

type billingCacheRuntimeConfigResponse struct {
	Revision                   uint64   `json:"revision"`
	Preset                     string   `json:"preset"`
	AvailablePresets           []string `json:"available_presets"`
	CriticalWorkerCount        int      `json:"critical_worker_count"`
	CriticalBufferSize         int      `json:"critical_buffer_size"`
	CriticalQueueLength        int      `json:"critical_queue_length"`
	CriticalQueueCapacity      int      `json:"critical_queue_capacity"`
	BestEffortWorkerCount      int      `json:"best_effort_worker_count"`
	BestEffortBufferSize       int      `json:"best_effort_buffer_size"`
	BestEffortQueueLength      int      `json:"best_effort_queue_length"`
	BestEffortQueueCapacity    int      `json:"best_effort_queue_capacity"`
	TimeoutMilliseconds        int      `json:"timeout_milliseconds"`
	CriticalActiveWorkers      int64    `json:"critical_active_workers"`
	BestEffortActiveWorkers    int64    `json:"best_effort_active_workers"`
	BestEffortDeferredLength   int      `json:"best_effort_deferred_length"`
	BestEffortDeferredCapacity int      `json:"best_effort_deferred_capacity"`
}

type billingCacheRuntimeConfigUpdateRequest struct {
	ExpectedRevision      *uint64 `json:"expected_revision"`
	Preset                string  `json:"preset"`
	CriticalWorkerCount   int     `json:"critical_worker_count"`
	CriticalBufferSize    int     `json:"critical_buffer_size"`
	BestEffortWorkerCount int     `json:"best_effort_worker_count"`
	BestEffortBufferSize  int     `json:"best_effort_buffer_size"`
	TimeoutMilliseconds   int     `json:"timeout_milliseconds"`
}

// GetBillingCacheRuntimeConfig returns a strict allow-list of effective queue
// values. It intentionally exposes neither Redis settings nor credentials.
func (h *GatewayLiteConfigHandler) GetBillingCacheRuntimeConfig(c *gin.Context) {
	if h == nil || h.billingCacheService == nil {
		response.Error(c, http.StatusServiceUnavailable, "billing_cache_service_not_ready")
		return
	}
	response.Success(c, billingCacheRuntimeResponse(h.billingCacheService))
}

// UpdateBillingCacheRuntimeConfig uses revision CAS, drains the old worker
// generation, and persists the same preset. A persistence failure restores the
// previous live pool before returning an error.
func (h *GatewayLiteConfigHandler) UpdateBillingCacheRuntimeConfig(c *gin.Context) {
	if h == nil || h.billingCacheService == nil {
		response.Error(c, http.StatusServiceUnavailable, "billing_cache_service_not_ready")
		return
	}
	var request billingCacheRuntimeConfigUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.ExpectedRevision == nil {
		response.Error(c, http.StatusBadRequest, "invalid billing cache runtime config")
		return
	}
	requested := config.BillingCacheWriteConfig{
		Preset:                request.Preset,
		CriticalWorkerCount:   request.CriticalWorkerCount,
		CriticalBufferSize:    request.CriticalBufferSize,
		BestEffortWorkerCount: request.BestEffortWorkerCount,
		BestEffortBufferSize:  request.BestEffortBufferSize,
		TimeoutMilliseconds:   request.TimeoutMilliseconds,
	}

	h.billingCacheConfigMu.Lock()
	defer h.billingCacheConfigMu.Unlock()
	previous := h.billingCacheService.CurrentCacheWriteConfig()
	previousRevision := h.billingCacheService.RuntimeStats().Revision
	if err := h.billingCacheService.ReloadCacheWriteConfigCAS(requested, *request.ExpectedRevision); err != nil {
		if errors.Is(err, service.ErrBillingCacheWriteRevisionConflict) {
			response.Error(c, http.StatusConflict, "billing_cache_revision_conflict")
			return
		}
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	writer := h.billingCacheConfigWriter
	if writer == nil {
		writer = writeBillingCacheRuntimeConfig
	}
	if err := writer(gatewayLiteConfigPath(), requested); err != nil {
		rollbackErr := h.billingCacheService.ReloadCacheWriteConfig(previous)
		if rollbackErr != nil {
			response.Error(c, http.StatusInternalServerError, "billing_cache_config_persist_and_rollback_failed")
			return
		}
		response.Error(c, http.StatusInternalServerError, "billing_cache_config_persist_failed")
		return
	}
	current := h.billingCacheService.RuntimeStats()
	// Only bounded queue facts are logged; no credentials or request content.
	slog.Info("billing cache runtime config changed",
		"audit", true,
		"revision_before", previousRevision,
		"revision_after", current.Revision,
		"preset_before", previous.Preset,
		"preset_after", current.Preset,
	)
	response.Success(c, billingCacheRuntimeResponse(h.billingCacheService))
}

func billingCacheRuntimeResponse(svc *service.BillingCacheService) billingCacheRuntimeConfigResponse {
	current := svc.CurrentCacheWriteConfig()
	stats := svc.RuntimeStats()
	return billingCacheRuntimeConfigResponse{
		Revision:                   stats.Revision,
		Preset:                     current.Preset,
		AvailablePresets:           config.BillingCacheWritePresetNames(),
		CriticalWorkerCount:        current.CriticalWorkerCount,
		CriticalBufferSize:         current.CriticalBufferSize,
		CriticalQueueLength:        stats.CriticalQueueLength,
		CriticalQueueCapacity:      stats.CriticalQueueCapacity,
		BestEffortWorkerCount:      current.BestEffortWorkerCount,
		BestEffortBufferSize:       current.BestEffortBufferSize,
		BestEffortQueueLength:      stats.BestEffortQueueLength,
		BestEffortQueueCapacity:    stats.BestEffortQueueCapacity,
		TimeoutMilliseconds:        current.TimeoutMilliseconds,
		CriticalActiveWorkers:      stats.CriticalActiveWorkerCount,
		BestEffortActiveWorkers:    stats.BestEffortActiveWorkerCount,
		BestEffortDeferredLength:   stats.BestEffortDeferredLength,
		BestEffortDeferredCapacity: stats.BestEffortDeferredCapacity,
	}
}

type gatewayLiteConfigResponse struct {
	Region                           string `json:"region"`
	GatewayCode                      string `json:"gateway_code"`
	RedisPrefix                      string `json:"redis_prefix"`
	AdminLoginPath                   string `json:"admin_login_path"`
	LoginTitle                       string `json:"login_title"`
	LoginSubtitle                    string `json:"login_subtitle"`
	AdminSyncKeyConfigured           bool   `json:"admin_sync_key_configured"`
	ControlPlaneURL                  string `json:"control_plane_url"`
	ControlPlaneTokenConfigured      bool   `json:"control_plane_token_configured"`
	ControlPlaneTimeoutMS            int    `json:"control_plane_timeout_ms"`
	RuntimeHealthIntervalSeconds     int    `json:"runtime_health_interval_seconds"`
	RuntimeActiveWindowSeconds       int    `json:"runtime_active_window_seconds"`
	ConfigSyncIntervalSeconds        int    `json:"config_sync_interval_seconds"`
	CacheInvalidationIntervalSeconds int    `json:"cache_invalidation_interval_seconds"`
	LegacySnapshotSchedulerAuthority bool   `json:"legacy_snapshot_scheduler_authority"`
	UsageQueuePendingAlertThreshold  int64  `json:"usage_queue_pending_alert_threshold"`
	UsageQueueDeadAlertThreshold     int64  `json:"usage_queue_dead_alert_threshold"`
	AllowLive                        bool   `json:"allow_live"`
	ConfigPath                       string `json:"config_path"`
	RestartRequired                  bool   `json:"restart_required"`
	SyncTriggered                    bool   `json:"sync_triggered"`
	SyncError                        string `json:"sync_error,omitempty"`
}

type gatewayLiteConfigUpdateRequest struct {
	Region                           string  `json:"region"`
	GatewayCode                      string  `json:"gateway_code"`
	RedisPrefix                      string  `json:"redis_prefix"`
	AdminLoginPath                   *string `json:"admin_login_path"`
	LoginTitle                       *string `json:"login_title"`
	LoginSubtitle                    *string `json:"login_subtitle"`
	AdminSyncKey                     string  `json:"admin_sync_key"`
	ControlPlaneURL                  string  `json:"control_plane_url"`
	ControlPlaneToken                string  `json:"control_plane_token"`
	ControlPlaneTimeoutMS            int     `json:"control_plane_timeout_ms"`
	RuntimeHealthIntervalSeconds     int     `json:"runtime_health_interval_seconds"`
	RuntimeActiveWindowSeconds       int     `json:"runtime_active_window_seconds"`
	ConfigSyncIntervalSeconds        int     `json:"config_sync_interval_seconds"`
	CacheInvalidationIntervalSeconds int     `json:"cache_invalidation_interval_seconds"`
	LegacySnapshotSchedulerAuthority *bool   `json:"legacy_snapshot_scheduler_authority"`
	UsageQueuePendingAlertThreshold  int64   `json:"usage_queue_pending_alert_threshold"`
	UsageQueueDeadAlertThreshold     int64   `json:"usage_queue_dead_alert_threshold"`
	AllowLive                        *bool   `json:"allow_live"`
}

type gatewayLiteSyncRequest struct {
	Region                    string `json:"region"`
	GatewayCode               string `json:"gateway_code"`
	ControlPlaneURL           string `json:"control_plane_url"`
	ControlPlaneToken         string `json:"control_plane_token"`
	ControlPlaneTimeoutMS     int    `json:"control_plane_timeout_ms"`
	ConfigSyncIntervalSeconds int    `json:"config_sync_interval_seconds"`
}

// GetConfig 返回 gateway-lite（轻量网关）连接主站控制面的启动配置。
// GET /api/v1/admin/gateway-lite/config
func (h *GatewayLiteConfigHandler) GetConfig(c *gin.Context) {
	response.Success(c, h.responseFromConfig(h.effectiveConfig(), gatewayLiteConfigPath(), false))
}

// UpdateConfig 将 gateway-lite（轻量网关）主站连接配置写入 config.yaml。
// PUT /api/v1/admin/gateway-lite/config
func (h *GatewayLiteConfigHandler) UpdateConfig(c *gin.Context) {
	var req gatewayLiteConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	previous := h.effectiveConfig()
	next := previous
	next.Region = strings.TrimSpace(req.Region)
	next.GatewayCode = strings.TrimSpace(req.GatewayCode)
	next.RedisPrefix = strings.TrimSpace(req.RedisPrefix)
	if req.AdminLoginPath != nil {
		next.AdminLoginPath = config.NormalizeGatewayLiteAdminLoginPath(*req.AdminLoginPath)
		if err := config.ValidateGatewayLiteAdminLoginPath(next.AdminLoginPath); err != nil {
			response.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.LoginTitle != nil {
		next.LoginTitle = config.NormalizeGatewayLiteLoginTitle(*req.LoginTitle)
	}
	if req.LoginSubtitle != nil {
		next.LoginSubtitle = config.NormalizeGatewayLiteLoginSubtitle(*req.LoginSubtitle)
	}
	if strings.TrimSpace(req.AdminSyncKey) != "" {
		next.AdminSyncKey = strings.TrimSpace(req.AdminSyncKey)
	}
	next.ControlPlaneURL = strings.TrimSpace(req.ControlPlaneURL)
	if strings.TrimSpace(req.ControlPlaneToken) != "" {
		next.ControlPlaneToken = strings.TrimSpace(req.ControlPlaneToken)
	}
	next.ControlPlaneTimeoutMS = normalizePositiveInt(req.ControlPlaneTimeoutMS, next.ControlPlaneTimeoutMS, 300)
	next.RuntimeHealthIntervalSeconds = normalizePositiveInt(req.RuntimeHealthIntervalSeconds, next.RuntimeHealthIntervalSeconds, 15)
	next.RuntimeActiveWindowSeconds = normalizePositiveInt(req.RuntimeActiveWindowSeconds, next.RuntimeActiveWindowSeconds, 300)
	next.ConfigSyncIntervalSeconds = normalizePositiveInt(req.ConfigSyncIntervalSeconds, next.ConfigSyncIntervalSeconds, 30)
	next.CacheInvalidationIntervalSeconds = normalizePositiveInt(req.CacheInvalidationIntervalSeconds, next.CacheInvalidationIntervalSeconds, 5)
	if req.LegacySnapshotSchedulerAuthority != nil {
		next.LegacySnapshotSchedulerAuthority = *req.LegacySnapshotSchedulerAuthority
	}
	next.UsageQueuePendingAlertThreshold = normalizePositiveInt64(req.UsageQueuePendingAlertThreshold, next.UsageQueuePendingAlertThreshold, 1000)
	next.UsageQueueDeadAlertThreshold = normalizePositiveInt64(req.UsageQueueDeadAlertThreshold, next.UsageQueueDeadAlertThreshold, 1)
	if req.AllowLive != nil {
		next.AllowLive = *req.AllowLive
	}

	if next.Region == "" {
		next.Region = "default"
	}
	if next.ControlPlaneURL != "" {
		if err := config.ValidateAbsoluteHTTPURL(next.ControlPlaneURL); err != nil {
			response.Error(c, http.StatusBadRequest, "control_plane_url must be a valid http(s) URL")
			return
		}
	}

	if manager := gatewaylite.DefaultBindingManager(); manager != nil && !manager.IdentityAllowed(next) {
		response.Error(c, http.StatusConflict, "managed_binding_identity_requires_disconnect")
		return
	}
	configPath := gatewayLiteConfigPath()
	if err := writeGatewayLiteConfig(configPath, next); err != nil {
		response.InternalError(c, "failed to write config.yaml")
		return
	}

	if h.cfg != nil {
		h.cfg.GatewayLite = next
	}
	syncTriggered, syncErr := applyGatewayLiteRuntimeConfig(c.Request.Context(), next)
	resp := h.responseFromConfig(next, configPath, gatewayLiteRestartRequired(previous, next))
	resp.SyncTriggered = syncTriggered
	if syncErr != nil {
		resp.SyncError = syncErr.Error()
	}
	response.Success(c, resp)
}

// SyncNow forces gateway-lite to refresh the latest scheduler/account config from the control plane.
// POST /api/v1/gateway-lite/sync
func (h *GatewayLiteConfigHandler) SyncNow(c *gin.Context) {
	cfg := h.effectiveConfig()
	var req gatewayLiteSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "invalid request body")
		return
	}
	incomingToken := strings.TrimSpace(req.ControlPlaneToken)
	adminSyncKey := strings.TrimSpace(cfg.AdminSyncKey)
	if adminSyncKey == "" {
		response.Error(c, http.StatusServiceUnavailable, "admin_sync_key_not_configured")
		return
	}
	if !validGatewayLiteAdminSyncKey(c, adminSyncKey, h.settingService) {
		response.Error(c, http.StatusUnauthorized, "invalid_gateway_lite_admin_key")
		return
	}
	if requestHasGatewayLiteConfig(req) {
		next := cfg
		if value := strings.TrimSpace(req.Region); value != "" {
			next.Region = value
		}
		if value := strings.TrimSpace(req.GatewayCode); value != "" {
			next.GatewayCode = value
		}
		if value := strings.TrimSpace(req.ControlPlaneURL); value != "" {
			if err := config.ValidateAbsoluteHTTPURL(value); err != nil {
				response.Error(c, http.StatusBadRequest, "control_plane_url must be a valid http(s) URL")
				return
			}
			next.ControlPlaneURL = value
		}
		if incomingToken != "" {
			next.ControlPlaneToken = incomingToken
		}
		next.ControlPlaneTimeoutMS = normalizePositiveInt(req.ControlPlaneTimeoutMS, next.ControlPlaneTimeoutMS, 300)
		next.ConfigSyncIntervalSeconds = normalizePositiveInt(req.ConfigSyncIntervalSeconds, next.ConfigSyncIntervalSeconds, 30)
		if manager := gatewaylite.DefaultBindingManager(); manager != nil && !manager.IdentityAllowed(next) {
			response.Error(c, http.StatusConflict, "managed_binding_identity_requires_disconnect")
			return
		}
		if err := writeGatewayLiteConfig(gatewayLiteConfigPath(), next); err != nil {
			response.InternalError(c, "failed to write config.yaml")
			return
		}
		if h.cfg != nil {
			h.cfg.GatewayLite = next
		}
		if _, err := applyGatewayLiteRuntimeConfig(c.Request.Context(), next); err != nil {
			response.Error(c, http.StatusBadGateway, "runtime_config_apply_failed")
			return
		}
		cfg = next
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	syncer := gatewaylite.DefaultConfigSyncer()
	if syncer == nil {
		response.Error(c, http.StatusServiceUnavailable, "config_syncer_not_ready")
		return
	}
	started := time.Now()
	if err := syncer.SyncFull(ctx); err != nil {
		response.Error(c, http.StatusBadGateway, "config_sync_failed")
		return
	}
	healthReported := false
	if monitor := gatewaylite.DefaultRuntimeHealthMonitor(); monitor != nil {
		monitor.ReportOnce(ctx)
		healthReported = true
	}
	response.Success(c, gin.H{
		"ok":                           true,
		"synced":                       true,
		"health_reported":              healthReported,
		"latency_ms":                   time.Since(started).Milliseconds(),
		"region":                       cfg.Region,
		"gateway_code":                 cfg.GatewayCode,
		"control_plane_url":            cfg.ControlPlaneURL,
		"config_sync_interval_seconds": cfg.ConfigSyncIntervalSeconds,
	})
}

func (h *GatewayLiteConfigHandler) effectiveConfig() config.GatewayLiteConfig {
	if h == nil || h.cfg == nil {
		return config.GatewayLiteConfig{}
	}
	return h.cfg.GatewayLite
}

func (h *GatewayLiteConfigHandler) responseFromConfig(cfg config.GatewayLiteConfig, configPath string, restartRequired bool) gatewayLiteConfigResponse {
	return gatewayLiteConfigResponse{
		Region:                           cfg.Region,
		GatewayCode:                      cfg.GatewayCode,
		RedisPrefix:                      cfg.RedisPrefix,
		AdminLoginPath:                   config.NormalizeGatewayLiteAdminLoginPath(cfg.AdminLoginPath),
		LoginTitle:                       config.NormalizeGatewayLiteLoginTitle(cfg.LoginTitle),
		LoginSubtitle:                    config.NormalizeGatewayLiteLoginSubtitle(cfg.LoginSubtitle),
		AdminSyncKeyConfigured:           strings.TrimSpace(cfg.AdminSyncKey) != "",
		ControlPlaneURL:                  cfg.ControlPlaneURL,
		ControlPlaneTokenConfigured:      strings.TrimSpace(cfg.ControlPlaneToken) != "",
		ControlPlaneTimeoutMS:            cfg.ControlPlaneTimeoutMS,
		RuntimeHealthIntervalSeconds:     cfg.RuntimeHealthIntervalSeconds,
		RuntimeActiveWindowSeconds:       cfg.RuntimeActiveWindowSeconds,
		ConfigSyncIntervalSeconds:        cfg.ConfigSyncIntervalSeconds,
		CacheInvalidationIntervalSeconds: cfg.CacheInvalidationIntervalSeconds,
		LegacySnapshotSchedulerAuthority: cfg.LegacySnapshotSchedulerAuthority,
		UsageQueuePendingAlertThreshold:  cfg.UsageQueuePendingAlertThreshold,
		UsageQueueDeadAlertThreshold:     cfg.UsageQueueDeadAlertThreshold,
		AllowLive:                        cfg.AllowLive,
		ConfigPath:                       configPath,
		RestartRequired:                  restartRequired,
	}
}

func applyGatewayLiteRuntimeConfig(ctx context.Context, cfg config.GatewayLiteConfig) (bool, error) {
	if manager := gatewaylite.DefaultBindingManager(); manager != nil && !manager.IdentityAllowed(cfg) {
		return false, errors.New("managed_binding_identity_requires_disconnect")
	}
	runtimeCfg := gatewayLiteRuntimeConfigFromConfig(cfg)
	applyGatewayLiteRuntimeState(runtimeCfg)
	if strings.TrimSpace(cfg.ControlPlaneURL) == "" {
		return false, nil
	}
	timeout := time.Duration(cfg.ControlPlaneTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 300 * time.Millisecond
	}
	client, err := gatewaylite.NewControlPlaneClient(cfg.ControlPlaneURL, cfg.ControlPlaneToken, timeout)
	if err != nil {
		return false, err
	}
	if ref := gatewaylite.DefaultControlPlaneClientRef(); ref != nil {
		ref.Set(client)
	}
	syncer := gatewaylite.DefaultConfigSyncer()
	if syncer == nil {
		return false, nil
	}
	syncCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := syncer.SyncFull(syncCtx); err != nil {
		return true, err
	}
	if monitor := gatewaylite.DefaultRuntimeHealthMonitor(); monitor != nil {
		monitor.ReportOnce(syncCtx)
	}
	return true, nil
}

func applyGatewayLiteRuntimeState(runtimeCfg gatewaylite.RuntimeConfig) {
	runtimeCfg = gatewaylite.NormalizeRuntimeConfig(runtimeCfg)
	if ref := gatewaylite.DefaultRuntimeConfigRef(); ref != nil {
		ref.Set(runtimeCfg)
	}
	if quota := gatewaylite.DefaultRedisQuota(); quota != nil {
		quota.SetPrefix(runtimeCfg.RedisPrefix)
	}
	if cache := gatewaylite.DefaultRedisKeyCache(); cache != nil {
		cache.SetPrefix(runtimeCfg.RedisPrefix)
	}
	if cache := gatewaylite.DefaultRedisConfigCache(); cache != nil {
		cache.SetPrefix(runtimeCfg.RedisPrefix)
	}
	if reporter, ok := gatewaylite.DefaultUsageReporter().(interface{ SetPrefix(string) }); ok && reporter != nil {
		reporter.SetPrefix(runtimeCfg.RedisPrefix)
	}
	if settings := gatewaylite.DefaultRuntimeHealthSettings(); settings != nil {
		settings.SetInterval(runtimeCfg.RuntimeHealthInterval())
	}
	syncer := gatewaylite.DefaultConfigSyncer()
	if syncer != nil {
		syncer.SetRegion(runtimeCfg.GatewayScope())
		syncer.SetInterval(runtimeCfg.ConfigSyncInterval())
	}
	if monitor := gatewaylite.DefaultRuntimeHealthMonitor(); monitor != nil {
		monitor.SetRuntimeConfig(
			runtimeCfg.Region,
			runtimeCfg.GatewayCode,
			runtimeCfg.RuntimeActiveWindow(),
			runtimeCfg.UsageQueuePendingAlertThreshold,
			runtimeCfg.UsageQueueDeadAlertThreshold,
		)
	}
	if syncer := gatewaylite.DefaultCacheInvalidationSyncer(); syncer != nil {
		syncer.SetRuntimeConfig(runtimeCfg.Region, runtimeCfg.GatewayCode, runtimeCfg.CacheInvalidationInterval())
	}
}

func validGatewayLiteAdminSyncKey(c *gin.Context, token string, settingService *service.SettingService) bool {
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		auth = strings.TrimSpace(auth[7:])
	}
	if auth == "" {
		auth = strings.TrimSpace(c.GetHeader("X-Gateway-Lite-Admin-Key"))
	}
	if auth == "" {
		auth = strings.TrimSpace(c.GetHeader("x-api-key"))
	}
	if auth == "" {
		return false
	}
	token = strings.TrimSpace(token)
	if token != "" && subtle.ConstantTimeCompare([]byte(auth), []byte(token)) == 1 {
		return true
	}
	if settingService == nil {
		return false
	}
	adminKey, err := settingService.GetAdminAPIKey(c.Request.Context())
	if err != nil {
		return false
	}
	adminKey = strings.TrimSpace(adminKey)
	return adminKey != "" && subtle.ConstantTimeCompare([]byte(auth), []byte(adminKey)) == 1
}

func requestHasGatewayLiteConfig(req gatewayLiteSyncRequest) bool {
	return strings.TrimSpace(req.Region) != "" ||
		strings.TrimSpace(req.GatewayCode) != "" ||
		strings.TrimSpace(req.ControlPlaneURL) != "" ||
		strings.TrimSpace(req.ControlPlaneToken) != "" ||
		req.ControlPlaneTimeoutMS > 0 ||
		req.ConfigSyncIntervalSeconds > 0
}

func gatewayLiteRestartRequired(previous, next config.GatewayLiteConfig) bool {
	return false
}

func gatewayLiteConfigPath() string {
	if value := strings.TrimSpace(os.Getenv("SUB2API_CONFIG_FILE")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("CONFIG_FILE")); value != "" {
		return value
	}
	if dataDir := strings.TrimSpace(os.Getenv("DATA_DIR")); dataDir != "" {
		path := filepath.Join(dataDir, setup.ConfigFileName)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if _, err := os.Stat("/app/data/" + setup.ConfigFileName); err == nil {
		return "/app/data/" + setup.ConfigFileName
	}
	if _, err := os.Stat("/etc/sub2api/" + setup.ConfigFileName); err == nil {
		return "/etc/sub2api/" + setup.ConfigFileName
	}
	return setup.GetConfigFilePath()
}

func gatewayLiteRuntimeConfigFromConfig(cfg config.GatewayLiteConfig) gatewaylite.RuntimeConfig {
	return gatewaylite.NormalizeRuntimeConfig(gatewaylite.RuntimeConfig{
		Region:                           cfg.Region,
		GatewayCode:                      cfg.GatewayCode,
		RedisPrefix:                      cfg.RedisPrefix,
		RuntimeHealthIntervalSeconds:     cfg.RuntimeHealthIntervalSeconds,
		RuntimeActiveWindowSeconds:       cfg.RuntimeActiveWindowSeconds,
		ConfigSyncIntervalSeconds:        cfg.ConfigSyncIntervalSeconds,
		CacheInvalidationIntervalSeconds: cfg.CacheInvalidationIntervalSeconds,
		LegacySnapshotSchedulerAuthority: cfg.LegacySnapshotSchedulerAuthority,
		UsageQueuePendingAlertThreshold:  cfg.UsageQueuePendingAlertThreshold,
		UsageQueueDeadAlertThreshold:     cfg.UsageQueueDeadAlertThreshold,
		AllowLive:                        cfg.AllowLive,
	})
}

func writeGatewayLiteConfig(path string, cfg config.GatewayLiteConfig) error {
	configFileWriteMu.Lock()
	defer configFileWriteMu.Unlock()
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = yaml.Unmarshal(data, &root)
	}
	root["gateway_lite"] = map[string]any{
		"region":                              cfg.Region,
		"gateway_code":                        cfg.GatewayCode,
		"redis_prefix":                        cfg.RedisPrefix,
		"admin_login_path":                    config.NormalizeGatewayLiteAdminLoginPath(cfg.AdminLoginPath),
		"login_title":                         config.NormalizeGatewayLiteLoginTitle(cfg.LoginTitle),
		"login_subtitle":                      config.NormalizeGatewayLiteLoginSubtitle(cfg.LoginSubtitle),
		"admin_sync_key":                      cfg.AdminSyncKey,
		"control_plane_url":                   cfg.ControlPlaneURL,
		"control_plane_token":                 cfg.ControlPlaneToken,
		"control_plane_timeout_ms":            cfg.ControlPlaneTimeoutMS,
		"runtime_health_interval_seconds":     cfg.RuntimeHealthIntervalSeconds,
		"runtime_active_window_seconds":       cfg.RuntimeActiveWindowSeconds,
		"config_sync_interval_seconds":        cfg.ConfigSyncIntervalSeconds,
		"cache_invalidation_interval_seconds": cfg.CacheInvalidationIntervalSeconds,
		"legacy_snapshot_scheduler_authority": cfg.LegacySnapshotSchedulerAuthority,
		"usage_queue_pending_alert_threshold": cfg.UsageQueuePendingAlertThreshold,
		"usage_queue_dead_alert_threshold":    cfg.UsageQueueDeadAlertThreshold,
		"allow_live":                          cfg.AllowLive,
	}
	data, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func writeBillingCacheRuntimeConfig(path string, requested config.BillingCacheWriteConfig) error {
	configFileWriteMu.Lock()
	defer configFileWriteMu.Unlock()
	preset, err := config.NormalizeBillingCacheWritePreset(requested.Preset)
	if err != nil {
		return err
	}
	root := map[string]any{}
	if data, readErr := os.ReadFile(path); readErr == nil && len(data) > 0 {
		if err := yaml.Unmarshal(data, &root); err != nil {
			return err
		}
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	billing, _ := root["billing"].(map[string]any)
	if billing == nil {
		billing = map[string]any{}
	}
	cacheWrite := map[string]any{"preset": preset}
	// Zero means "inherit the selected preset". Persist only explicit
	// overrides so choosing another preset later actually changes all values.
	if requested.CriticalWorkerCount != 0 {
		cacheWrite["critical_worker_count"] = requested.CriticalWorkerCount
	}
	if requested.CriticalBufferSize != 0 {
		cacheWrite["critical_buffer_size"] = requested.CriticalBufferSize
	}
	if requested.BestEffortWorkerCount != 0 {
		cacheWrite["best_effort_worker_count"] = requested.BestEffortWorkerCount
	}
	if requested.BestEffortBufferSize != 0 {
		cacheWrite["best_effort_buffer_size"] = requested.BestEffortBufferSize
	}
	if requested.TimeoutMilliseconds != 0 {
		cacheWrite["timeout_milliseconds"] = requested.TimeoutMilliseconds
	}
	billing["cache_write"] = cacheWrite
	root["billing"] = billing
	data, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return replaceConfigFileAtomically(path, data, 0600)
}

func replaceConfigFileAtomically(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".billing-cache-config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	} else {
		// Windows cannot replace an existing file with os.Rename. Move the old
		// file aside, install the complete temp file, and restore on failure.
		backup, backupErr := os.CreateTemp(directory, ".billing-cache-backup-*")
		if backupErr != nil {
			return err
		}
		backupPath := backup.Name()
		if closeErr := backup.Close(); closeErr != nil {
			_ = os.Remove(backupPath)
			return closeErr
		}
		if removeErr := os.Remove(backupPath); removeErr != nil {
			return removeErr
		}
		if moveErr := os.Rename(path, backupPath); moveErr != nil {
			return err
		}
		if installErr := os.Rename(temporaryPath, path); installErr != nil {
			restoreErr := os.Rename(backupPath, path)
			return errors.Join(installErr, restoreErr)
		}
		if removeErr := os.Remove(backupPath); removeErr != nil {
			removeNewErr := os.Remove(path)
			restoreErr := os.Rename(backupPath, path)
			return errors.Join(removeErr, removeNewErr, restoreErr)
		}
		return nil
	}
}

func normalizePositiveInt(value, configured, fallback int) int {
	if value > 0 {
		return value
	}
	if configured > 0 {
		return configured
	}
	return fallback
}

func normalizePositiveInt64(value, configured, fallback int64) int64 {
	if value > 0 {
		return value
	}
	if configured > 0 {
		return configured
	}
	return fallback
}
