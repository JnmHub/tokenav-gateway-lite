package routes

import (
	"context"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var gatewayLiteStartedAt = time.Now().UTC()

// These response structs are a deliberate allow-list. Never serialize Config,
// SystemSettings, environment variables, or the raw runtime metrics map here:
// each of those contains or may later gain credentials.
type gatewayRuntimeSummary struct {
	Service                gatewayRuntimeService    `json:"service"`
	Database               gatewayRuntimeDatabase   `json:"database"`
	Cache                  gatewayRuntimeCache      `json:"cache"`
	Billing                gatewayRuntimeBilling    `json:"billing"`
	Usage                  gatewayRuntimeUsage      `json:"usage"`
	Timeouts               gatewayRuntimeTimeouts   `json:"timeouts"`
	WebSocket              gatewayRuntimeWebSocket  `json:"websocket"`
	Scheduling             gatewayRuntimeScheduling `json:"scheduling"`
	CompositeGroupsEnabled bool                     `json:"composite_groups_enabled"`
	ReadOnly               bool                     `json:"read_only"`
}

type gatewayRuntimeService struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Runtime       string `json:"runtime"`
	StartedAt     string `json:"started_at"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Source        string `json:"source"`
}

type gatewayRuntimeDatabase struct {
	Provider             string `json:"provider"`
	MaxOpen              int    `json:"max_open"`
	MaxIdle              int    `json:"max_idle"`
	MaxLifetimeMinutes   int    `json:"max_lifetime_minutes"`
	MaxIdleTimeMinutes   int    `json:"max_idle_time_minutes"`
	QuotaFlusherEnabled  bool   `json:"quota_flusher_enabled"`
	QuotaFlushIntervalMS int    `json:"quota_flush_interval_ms"`
	QuotaFlushBatchSize  int    `json:"quota_flush_batch_size"`
	Source               string `json:"source"`
}

type gatewayRuntimeCache struct {
	Provider            string `json:"provider"`
	PoolSize            int    `json:"pool_size"`
	MinIdle             int    `json:"min_idle"`
	DialTimeoutSeconds  int    `json:"dial_timeout_seconds"`
	ReadTimeoutSeconds  int    `json:"read_timeout_seconds"`
	WriteTimeoutSeconds int    `json:"write_timeout_seconds"`
	TLSEnabled          bool   `json:"tls_enabled"`
	Source              string `json:"source"`
}

type gatewayRuntimeBilling struct {
	Preset                    string `json:"preset"`
	Revision                  int64  `json:"revision"`
	CriticalWorkers           int64  `json:"critical_workers"`
	CriticalActiveWorkers     int64  `json:"critical_active_workers"`
	CriticalCapacity          int64  `json:"critical_capacity"`
	CriticalPending           int64  `json:"critical_pending"`
	CriticalFullTotal         int64  `json:"critical_full_total"`
	CriticalFallbackTotal     int64  `json:"critical_fallback_total"`
	CriticalFallbackFailTotal int64  `json:"critical_fallback_fail_total"`
	BestEffortWorkers         int64  `json:"best_effort_workers"`
	BestEffortActiveWorkers   int64  `json:"best_effort_active_workers"`
	BestEffortCapacity        int64  `json:"best_effort_capacity"`
	BestEffortPending         int64  `json:"best_effort_pending"`
	BestEffortDeferred        int64  `json:"best_effort_deferred"`
	BestEffortMergedTotal     int64  `json:"best_effort_merged_total"`
	BestEffortFallbackTotal   int64  `json:"best_effort_fallback_total"`
	BestEffortFallbackFailed  int64  `json:"best_effort_fallback_failed"`
	BestEffortDroppedTotal    int64  `json:"best_effort_dropped_total"`
	OrderingWaitTotal         int64  `json:"ordering_wait_total"`
	ReloadTotal               int64  `json:"reload_total"`
	WriteTimeoutMS            int    `json:"write_timeout_ms"`
	Source                    string `json:"source"`
}

type gatewayRuntimeUsage struct {
	LocalWorkers      int64  `json:"local_workers"`
	LocalRunning      int64  `json:"local_running"`
	LocalCapacity     int64  `json:"local_capacity"`
	LocalPending      int64  `json:"local_pending"`
	LocalCompleted    int64  `json:"local_completed"`
	LocalFailed       int64  `json:"local_failed"`
	LocalDropped      int64  `json:"local_dropped"`
	ReportWorkers     int    `json:"report_workers"`
	ReportBatchSize   int    `json:"report_batch_size"`
	LocalOverflowMode string `json:"local_overflow_mode"`
	Source            string `json:"source"`
}

type gatewayRuntimeTimeouts struct {
	ControlPlaneMS                   int    `json:"control_plane_ms"`
	ResponseHeaderSeconds            int    `json:"response_header_seconds"`
	OpenAIResponseHeaderSeconds      int    `json:"openai_response_header_seconds"`
	OpenAIFirstOutputSeconds         int    `json:"openai_first_output_seconds"`
	OpenAIHighReasoningOutputSeconds int    `json:"openai_high_reasoning_output_seconds"`
	StreamDataIntervalSeconds        int    `json:"stream_data_interval_seconds"`
	ImageStreamDataIntervalSeconds   int    `json:"image_stream_data_interval_seconds"`
	Source                           string `json:"source"`
}

type gatewayRuntimeWebSocket struct {
	ConfiguredEnabled         bool   `json:"configured_enabled"`
	ForceHTTP                 bool   `json:"force_http"`
	EffectiveEnabled          bool   `json:"effective_enabled"`
	ModeRouterV2              bool   `json:"mode_router_v2"`
	IngressMode               string `json:"ingress_mode"`
	ClientFirstMessageSeconds int    `json:"client_first_message_seconds"`
	InterTurnIdleSeconds      int    `json:"inter_turn_idle_seconds"`
	MaxConnectionsPerAccount  int    `json:"max_connections_per_account"`
	Source                    string `json:"source"`
}

type gatewayRuntimeScheduling struct {
	AdvancedEnabled          bool    `json:"advanced_enabled"`
	AdvancedSettingAvailable bool    `json:"advanced_setting_available"`
	StickyEscapeEnabled      bool    `json:"sticky_escape_enabled"`
	StickyEscapeTTFTMS       int     `json:"sticky_escape_ttft_ms"`
	StickyEscapeErrorRate    float64 `json:"sticky_escape_error_rate"`
	FallbackQueueEnabled     bool    `json:"fallback_queue_enabled"`
	FallbackMaxWaiting       int     `json:"fallback_max_waiting"`
	FallbackWaitMS           int64   `json:"fallback_wait_ms"`
	FallbackSelectionMode    string  `json:"fallback_selection_mode"`
	LoadBatchEnabled         bool    `json:"load_batch_enabled"`
	LoadBatchCacheTTLMS      int     `json:"load_batch_cache_ttl_ms"`
	Source                   string  `json:"source"`
}

func registerGatewayLiteRuntimeSummaryRoute(
	admin *gin.RouterGroup,
	h *handler.Handlers,
	cfg *config.Config,
	settingService *service.SettingService,
) {
	admin.GET("/ops/runtime/summary", func(c *gin.Context) {
		metrics := map[string]any{}
		if h != nil {
			metrics = h.RuntimeMetrics(c.Request.Context())
		}
		advancedEnabled, settingsAvailable := readAdvancedSchedulerSetting(c.Request.Context(), settingService)
		compositeEnabled := cfg != nil && cfg.GatewayLite.CompositeGroupsEnabled
		if settingService != nil {
			compositeEnabled = settingService.CompositeGroupsEnabled()
		}
		response.Success(c, buildGatewayRuntimeSummary(cfg, metrics, advancedEnabled, settingsAvailable, time.Now().UTC(), compositeEnabled))
	})
}

func readAdvancedSchedulerSetting(ctx context.Context, settingService *service.SettingService) (bool, bool) {
	if settingService == nil {
		return false, false
	}
	settings, err := settingService.GetAllSettings(ctx)
	if err != nil || settings == nil {
		// Do not return the storage error: it may contain connection details.
		return false, false
	}
	return settings.OpenAIAdvancedSchedulerEnabled, true
}

func buildGatewayRuntimeSummary(
	cfg *config.Config,
	metrics map[string]any,
	advancedEnabled bool,
	settingsAvailable bool,
	now time.Time,
	compositeEnabledOverride ...bool,
) gatewayRuntimeSummary {
	if cfg == nil {
		cfg = &config.Config{}
	}
	startedAt := gatewayLiteStartedAt
	if now.Before(startedAt) {
		startedAt = now
	}
	criticalDropped := runtimeMetricInt64(metrics, "billing_cache_critical_sync_fallback_failed_total")
	bestEffortDropped := runtimeMetricInt64(metrics, "billing_cache_best_effort_dropped_full_total") +
		runtimeMetricInt64(metrics, "billing_cache_best_effort_dropped_closed_total")
	compositeEnabled := cfg.GatewayLite.CompositeGroupsEnabled
	if len(compositeEnabledOverride) > 0 {
		compositeEnabled = compositeEnabledOverride[0]
	}
	return gatewayRuntimeSummary{
		Service: gatewayRuntimeService{
			Name:          "gateway-lite",
			Version:       gatewayLiteBuildVersion(),
			Runtime:       runtime.Version(),
			StartedAt:     startedAt.Format(time.RFC3339),
			UptimeSeconds: int64(now.Sub(startedAt).Seconds()),
			Source:        "runtime_memory",
		},
		Database: gatewayRuntimeDatabase{
			Provider:             "postgresql",
			MaxOpen:              cfg.Database.MaxOpenConns,
			MaxIdle:              cfg.Database.MaxIdleConns,
			MaxLifetimeMinutes:   cfg.Database.ConnMaxLifetimeMinutes,
			MaxIdleTimeMinutes:   cfg.Database.ConnMaxIdleTimeMinutes,
			QuotaFlusherEnabled:  cfg.Database.UserPlatformQuotaFlusherEnabled,
			QuotaFlushIntervalMS: cfg.Database.UserPlatformQuotaFlushIntervalMs,
			QuotaFlushBatchSize:  cfg.Database.UserPlatformQuotaFlushBatchSize,
			Source:               "startup_config",
		},
		Cache: gatewayRuntimeCache{
			Provider:            "redis",
			PoolSize:            cfg.Redis.PoolSize,
			MinIdle:             cfg.Redis.MinIdleConns,
			DialTimeoutSeconds:  cfg.Redis.DialTimeoutSeconds,
			ReadTimeoutSeconds:  cfg.Redis.ReadTimeoutSeconds,
			WriteTimeoutSeconds: cfg.Redis.WriteTimeoutSeconds,
			TLSEnabled:          cfg.Redis.EnableTLS,
			Source:              "startup_config",
		},
		Billing: gatewayRuntimeBilling{
			Preset:                    runtimeMetricStringOr(metrics, "billing_cache_preset", cfg.Billing.CacheWrite.Preset),
			Revision:                  runtimeMetricInt64(metrics, "billing_cache_revision"),
			CriticalWorkers:           runtimeMetricInt64Or(metrics, "billing_cache_critical_worker_count", int64(cfg.Billing.CacheWrite.CriticalWorkerCount)),
			CriticalActiveWorkers:     runtimeMetricInt64(metrics, "billing_cache_critical_active_worker_count"),
			CriticalCapacity:          runtimeMetricInt64Or(metrics, "billing_cache_critical_queue_capacity", int64(cfg.Billing.CacheWrite.CriticalBufferSize)),
			CriticalPending:           runtimeMetricInt64(metrics, "billing_cache_critical_queue_length"),
			CriticalFullTotal:         runtimeMetricInt64(metrics, "billing_cache_critical_queue_full_total"),
			CriticalFallbackTotal:     runtimeMetricInt64(metrics, "billing_cache_critical_sync_fallback_total"),
			CriticalFallbackFailTotal: criticalDropped,
			BestEffortWorkers:         runtimeMetricInt64Or(metrics, "billing_cache_best_effort_worker_count", int64(cfg.Billing.CacheWrite.BestEffortWorkerCount)),
			BestEffortActiveWorkers:   runtimeMetricInt64(metrics, "billing_cache_best_effort_active_worker_count"),
			BestEffortCapacity:        runtimeMetricInt64Or(metrics, "billing_cache_best_effort_queue_capacity", int64(cfg.Billing.CacheWrite.BestEffortBufferSize)),
			BestEffortPending:         runtimeMetricInt64(metrics, "billing_cache_best_effort_queue_length"),
			BestEffortDeferred:        runtimeMetricInt64(metrics, "billing_cache_best_effort_deferred_length"),
			BestEffortMergedTotal:     runtimeMetricInt64(metrics, "billing_cache_best_effort_merged_total"),
			BestEffortFallbackTotal:   runtimeMetricInt64(metrics, "billing_cache_best_effort_sync_fallback_total"),
			BestEffortFallbackFailed:  runtimeMetricInt64(metrics, "billing_cache_best_effort_sync_fallback_failed_total"),
			BestEffortDroppedTotal:    bestEffortDropped,
			OrderingWaitTotal:         runtimeMetricInt64(metrics, "billing_cache_ordering_wait_total"),
			ReloadTotal:               runtimeMetricInt64(metrics, "billing_cache_reload_total"),
			WriteTimeoutMS:            int(runtimeMetricInt64Or(metrics, "billing_cache_timeout_milliseconds", int64(cfg.Billing.CacheWrite.TimeoutMilliseconds))),
			Source:                    "runtime_memory_and_startup_config",
		},
		Usage: gatewayRuntimeUsage{
			LocalWorkers:      runtimeMetricInt64Or(metrics, "usage_record_max_concurrency", int64(cfg.Gateway.UsageRecord.WorkerCount)),
			LocalRunning:      runtimeMetricInt64(metrics, "usage_record_running_workers"),
			LocalCapacity:     runtimeMetricInt64Or(metrics, "usage_record_queue_capacity", int64(cfg.Gateway.UsageRecord.QueueSize)),
			LocalPending:      runtimeMetricInt64(metrics, "usage_record_waiting_tasks"),
			LocalCompleted:    runtimeMetricInt64(metrics, "usage_record_completed_tasks_total"),
			LocalFailed:       runtimeMetricInt64(metrics, "usage_record_failed_tasks_total"),
			LocalDropped:      runtimeMetricInt64(metrics, "usage_record_dropped_tasks_total"),
			ReportWorkers:     cfg.GatewayLite.UsageReportWorkers,
			ReportBatchSize:   cfg.GatewayLite.UsageReportBatchSize,
			LocalOverflowMode: cfg.Gateway.UsageRecord.OverflowPolicy,
			Source:            "runtime_memory_and_startup_config",
		},
		Timeouts: gatewayRuntimeTimeouts{
			ControlPlaneMS:                   cfg.GatewayLite.ControlPlaneTimeoutMS,
			ResponseHeaderSeconds:            cfg.Gateway.ResponseHeaderTimeout,
			OpenAIResponseHeaderSeconds:      cfg.Gateway.OpenAIResponseHeaderTimeout,
			OpenAIFirstOutputSeconds:         cfg.Gateway.OpenAIFirstOutputTimeoutSeconds,
			OpenAIHighReasoningOutputSeconds: cfg.Gateway.OpenAIFirstOutputHighReasoningTimeoutSeconds,
			StreamDataIntervalSeconds:        cfg.Gateway.StreamDataIntervalTimeout,
			ImageStreamDataIntervalSeconds:   cfg.Gateway.ImageStreamDataIntervalTimeout,
			Source:                           "startup_config",
		},
		WebSocket: gatewayRuntimeWebSocket{
			ConfiguredEnabled:         cfg.Gateway.OpenAIWS.Enabled,
			ForceHTTP:                 cfg.Gateway.OpenAIWS.ForceHTTP,
			EffectiveEnabled:          cfg.Gateway.OpenAIWS.Enabled && !cfg.Gateway.OpenAIWS.ForceHTTP,
			ModeRouterV2:              cfg.Gateway.OpenAIWS.ModeRouterV2Enabled,
			IngressMode:               cfg.Gateway.OpenAIWS.IngressModeDefault,
			ClientFirstMessageSeconds: cfg.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds,
			InterTurnIdleSeconds:      cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds,
			MaxConnectionsPerAccount:  cfg.Gateway.OpenAIWS.MaxConnsPerAccount,
			Source:                    "startup_config",
		},
		Scheduling: gatewayRuntimeScheduling{
			AdvancedEnabled:          advancedEnabled,
			AdvancedSettingAvailable: settingsAvailable,
			StickyEscapeEnabled:      cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled,
			StickyEscapeTTFTMS:       cfg.Gateway.OpenAIScheduler.StickyEscapeTTFTMs,
			StickyEscapeErrorRate:    cfg.Gateway.OpenAIScheduler.StickyEscapeErrorRate,
			FallbackQueueEnabled:     cfg.Gateway.Scheduling.FallbackQueueEnabled,
			FallbackMaxWaiting:       cfg.Gateway.Scheduling.FallbackMaxWaiting,
			FallbackWaitMS:           cfg.Gateway.Scheduling.FallbackWaitTimeout.Milliseconds(),
			FallbackSelectionMode:    cfg.Gateway.Scheduling.FallbackSelectionMode,
			LoadBatchEnabled:         cfg.Gateway.Scheduling.LoadBatchEnabled,
			LoadBatchCacheTTLMS:      cfg.Gateway.Scheduling.LoadBatchCacheTTLMS,
			Source:                   "database_setting_and_startup_config",
		},
		CompositeGroupsEnabled: compositeEnabled,
		ReadOnly:               true,
	}
}

func runtimeMetricInt64Or(metrics map[string]any, name string, fallback int64) int64 {
	if value, ok := runtimeMetricInt64Value(metrics, name); ok {
		return value
	}
	return fallback
}

func runtimeMetricStringOr(metrics map[string]any, name, fallback string) string {
	if metrics == nil {
		return fallback
	}
	value, ok := metrics[name].(string)
	if !ok {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func runtimeMetricInt64(metrics map[string]any, name string) int64 {
	value, _ := runtimeMetricInt64Value(metrics, name)
	return value
}

func runtimeMetricInt64Value(metrics map[string]any, name string) (int64, bool) {
	if metrics == nil {
		return 0, false
	}
	value, ok := metrics[name]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(typed), true
	case float64:
		return int64(typed), true
	default:
		return 0, false
	}
}

func gatewayLiteBuildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "development"
	}
	version := strings.TrimSpace(info.Main.Version)
	if version == "" || version == "(devel)" {
		return "development"
	}
	return version
}
