package gatewaylite

import (
	"context"
	"log"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type RuntimeHealthClient interface {
	ProbeControlPlaneHealth(ctx context.Context) (time.Duration, error)
	ReportGatewayHealth(ctx context.Context, req GatewayHealthReportRequest) (*GatewayHealthReportResponse, error)
}

type RuntimeHealthMonitor struct {
	workers                      workerLifetime
	client                RuntimeHealthClient
	stats                 *RuntimeStats
	usageReporter         *RedisUsageReporter
	intervalSource        RuntimeHealthIntervalSource
	modelProbeProvider    RuntimeModelProbeProvider
	modelProbeSettings    RuntimeModelProbeSettings
	mu                    sync.RWMutex
	region                string
	gatewayCode           string
	interval              time.Duration
	activeWindow          time.Duration
	usagePendingThreshold int64
	usageDeadThreshold    int64
	availableModels       []string
	availableModelsSource RuntimeAvailableModelsProvider
	modelPriceProvider    RuntimeModelPriceProvider
	metricsProviders      []RuntimeMetricsProvider
	modelProbeModel       string
	modelProbeInterval    time.Duration
	lastModelProbe        RuntimeModelProbeResult
	lastModelProbeAt      time.Time
	nextModelProbeAt      time.Time
	hasLastModelProbe     bool
	// stageTimeout limits one blocking I/O stage. Optional enrichment shares one
	// budget so a slow provider cannot multiply the reporting delay.
	stageTimeout time.Duration
	reporting    atomic.Bool
}

const defaultRuntimeModelProbeInterval = 5 * time.Minute
const runtimeModelProbeDegradedThreshold = 6 * time.Second
const runtimeModelProbeMinInterval = 15 * time.Second
const runtimeModelProbeMaxInterval = time.Hour
const runtimeModelProbeJitterRatio = 0.1
const runtimeModelProbeMaxJitter = 30 * time.Second

type RuntimeModelProbeProvider interface {
	ProbeRuntimeModel(ctx context.Context, preferredModel string) RuntimeModelProbeResult
}

type RuntimeModelProbeSettings interface {
	RuntimeModelProbeModel() string
	RuntimeModelProbeInterval() time.Duration
}

type RuntimeModelProbeResult struct {
	OK            bool
	Status        string
	StatusCode    int
	Latency       time.Duration
	Model         string
	UpstreamModel string
	Platform      string
	AccountID     int64
	AccountName   string
	Target        string
	Error         string
	CheckedAt     time.Time
	Cached        bool
	ProbeInterval time.Duration
}

type RuntimeModelPriceProvider interface {
	GatewayModelPrices(models []string, gatewayCode string) []GatewayModelPrice
}

type RuntimeMetricsProvider interface {
	RuntimeMetrics(ctx context.Context) map[string]any
}

type RuntimeAvailableModelsProvider interface {
	AvailableModels(ctx context.Context) []string
}

type RuntimeHealthIntervalSource interface {
	RuntimeHealthInterval() time.Duration
}

type RuntimeHealthSettings struct {
	intervalSeconds       atomic.Int64
	modelProbeIntervalSec atomic.Int64
	modelProbeModel       atomic.Value
}

func NewRuntimeHealthSettings(interval time.Duration) *RuntimeHealthSettings {
	settings := &RuntimeHealthSettings{}
	settings.SetInterval(interval)
	return settings
}

func (s *RuntimeHealthSettings) SetInterval(interval time.Duration) {
	if s == nil {
		return
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	s.intervalSeconds.Store(int64(interval.Seconds()))
}

func (s *RuntimeHealthSettings) SetModelProbeInterval(interval time.Duration) {
	if s == nil {
		return
	}
	s.modelProbeIntervalSec.Store(int64(normalizeRuntimeModelProbeInterval(interval).Seconds()))
}

func (s *RuntimeHealthSettings) SetModelProbeModel(model string) {
	if s == nil {
		return
	}
	s.modelProbeModel.Store(strings.TrimSpace(model))
}

func (s *RuntimeHealthSettings) RuntimeHealthInterval() time.Duration {
	if s == nil {
		return 0
	}
	seconds := s.intervalSeconds.Load()
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (s *RuntimeHealthSettings) RuntimeModelProbeModel() string {
	if s == nil {
		return ""
	}
	value := s.modelProbeModel.Load()
	if value == nil {
		return ""
	}
	model, _ := value.(string)
	return strings.TrimSpace(model)
}

func (s *RuntimeHealthSettings) RuntimeModelProbeInterval() time.Duration {
	if s == nil {
		return 0
	}
	seconds := s.modelProbeIntervalSec.Load()
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func NewRuntimeHealthMonitor(client RuntimeHealthClient, stats *RuntimeStats, region, gatewayCode string, interval, activeWindow time.Duration) *RuntimeHealthMonitor {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if activeWindow <= 0 {
		activeWindow = 5 * time.Minute
	}
	return &RuntimeHealthMonitor{
		client:                client,
		stats:                 stats,
		region:                region,
		gatewayCode:           gatewayCode,
		interval:              interval,
		activeWindow:          activeWindow,
		usagePendingThreshold: 1000,
		usageDeadThreshold:    1,
		modelProbeInterval:    defaultRuntimeModelProbeInterval,
		stageTimeout:          defaultUsageReportTimeout,
	}
}

func (m *RuntimeHealthMonitor) WithIntervalSource(source RuntimeHealthIntervalSource) *RuntimeHealthMonitor {
	if m == nil {
		return nil
	}
	m.intervalSource = source
	return m
}

func (m *RuntimeHealthMonitor) WithUsageQueue(reporter *RedisUsageReporter, pendingThreshold, deadThreshold int64) *RuntimeHealthMonitor {
	if m == nil {
		return nil
	}
	if pendingThreshold <= 0 {
		pendingThreshold = 1000
	}
	if deadThreshold <= 0 {
		deadThreshold = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usageReporter = reporter
	m.usagePendingThreshold = pendingThreshold
	m.usageDeadThreshold = deadThreshold
	return m
}

func (m *RuntimeHealthMonitor) WithAvailableModels(models []string) *RuntimeHealthMonitor {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.availableModels = normalizeAvailableModels(models)
	return m
}

func (m *RuntimeHealthMonitor) WithAvailableModelsProvider(provider RuntimeAvailableModelsProvider) *RuntimeHealthMonitor {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.availableModelsSource = provider
	return m
}

func (m *RuntimeHealthMonitor) WithModelPriceProvider(provider RuntimeModelPriceProvider) *RuntimeHealthMonitor {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelPriceProvider = provider
	return m
}

func (m *RuntimeHealthMonitor) WithRuntimeMetricsProvider(provider RuntimeMetricsProvider) *RuntimeHealthMonitor {
	if m == nil || provider == nil {
		return m
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metricsProviders = append(m.metricsProviders, provider)
	return m
}

func (m *RuntimeHealthMonitor) WithModelProbe(provider RuntimeModelProbeProvider, defaultModel string, interval time.Duration) *RuntimeHealthMonitor {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelProbeProvider = provider
	m.modelProbeModel = strings.TrimSpace(defaultModel)
	m.modelProbeInterval = normalizeRuntimeModelProbeInterval(interval)
	return m
}

func (m *RuntimeHealthMonitor) WithModelProbeSettings(settings RuntimeModelProbeSettings) *RuntimeHealthMonitor {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelProbeSettings = settings
	return m
}

func (m *RuntimeHealthMonitor) SetRuntimeConfig(region, gatewayCode string, activeWindow time.Duration, pendingThreshold, deadThreshold int64) {
	if m == nil {
		return
	}
	region = strings.TrimSpace(region)
	if region == "" {
		region = "default"
	}
	gatewayCode = strings.TrimSpace(gatewayCode)
	if gatewayCode == "" {
		gatewayCode = region
	}
	if activeWindow <= 0 {
		activeWindow = 5 * time.Minute
	}
	if pendingThreshold <= 0 {
		pendingThreshold = 1000
	}
	if deadThreshold <= 0 {
		deadThreshold = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.region = region
	m.gatewayCode = gatewayCode
	m.activeWindow = activeWindow
	m.usagePendingThreshold = pendingThreshold
	m.usageDeadThreshold = deadThreshold
}

func (m *RuntimeHealthMonitor) Start(ctx context.Context) {
	if m == nil || m.client == nil {
		return
	}
	m.workers.Go(ctx, m.run)
}

func (m *RuntimeHealthMonitor) ReportOnce(ctx context.Context) {
	if m == nil || m.client == nil {
		return
	}
	m.reportOnce(ctx)
}

func (m *RuntimeHealthMonitor) run(ctx context.Context) {
	m.reportOnce(ctx)
	timer := time.NewTimer(m.currentInterval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.reportOnce(ctx)
			timer.Reset(m.currentInterval())
		}
	}
}

func (m *RuntimeHealthMonitor) currentInterval() time.Duration {
	if m != nil && m.intervalSource != nil {
		if interval := m.intervalSource.RuntimeHealthInterval(); interval > 0 {
			return interval
		}
	}
	if m == nil || m.interval <= 0 {
		return 15 * time.Second
	}
	return m.interval
}

func (m *RuntimeHealthMonitor) reportOnce(ctx context.Context) {
	// The timer and the administrator-triggered sync can overlap. Skipping the
	// duplicate avoids running two real-model probes and two metric collections.
	if !m.reporting.CompareAndSwap(false, true) {
		return
	}
	defer m.reporting.Store(false)
	// A faulty optional provider must not terminate the monitor goroutine. The
	// next timer tick remains able to report even if this cycle is discarded.
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("gateway-lite: runtime health cycle panicked: %v", recovered)
		}
	}()

	m.mu.RLock()
	region := m.region
	gatewayCode := m.gatewayCode
	activeWindow := m.activeWindow
	availableModels := append([]string{}, m.availableModels...)
	availableModelsSource := m.availableModelsSource
	modelPriceProvider := m.modelPriceProvider
	metricsProviders := append([]RuntimeMetricsProvider{}, m.metricsProviders...)
	m.mu.RUnlock()

	probeCtx, probeCancel := context.WithTimeout(ctx, m.currentStageTimeout())
	latency, err := probeControlPlaneHealthSafe(m.client, probeCtx)
	probeCancel()

	status := "operational"
	message := ""
	latencyMS := latencyMilliseconds(latency)
	if latencyMS == 0 {
		latencyMS = 1
	}
	if err != nil {
		status = "degraded"
		message = "control plane health probe failed"
		latencyMS = 0
	}
	// Dynamic models, the real-model probe, Redis queue state and runtime
	// providers are optional enrichment. They intentionally share one deadline;
	// otherwise N slow providers can turn a 10-second timeout into N*10 seconds.
	enrichmentCtx, enrichmentCancel := context.WithTimeout(ctx, m.currentStageTimeout())
	defer enrichmentCancel()
	enrichmentTimeoutStage := ""
	if availableModelsSource != nil {
		dynamicModels := availableModelsSafe(availableModelsSource, enrichmentCtx)
		if enrichmentCtx.Err() != nil {
			enrichmentTimeoutStage = "available_models"
		}
		// 静态配置保留为兜底，动态模型来自本地可调度账号池，避免新增账号后主站仍只看到旧环境变量。
		availableModels = normalizeAvailableModels(append(availableModels, dynamicModels...))
	}
	req := GatewayHealthReportRequest{
		GatewayCode:     gatewayCode,
		Region:          region,
		HealthStatus:    status,
		LatencyMS:       latencyMS,
		OnlineUsers:     m.stats.OnlineUsers(time.Now(), activeWindow),
		AvailableModels: availableModels,
		Message:         message,
		Metadata: map[string]any{
			"source":                "gateway-lite-runtime-health-monitor",
			"probe_target":          "control-plane-health",
			"active_window_seconds": int64(activeWindow.Seconds()),
		},
	}
	if modelPriceProvider != nil && len(availableModels) > 0 {
		req.ModelPrices = gatewayModelPricesSafe(modelPriceProvider, availableModels, gatewayCode)
	}
	// Queue and in-process metrics are operationally more important and much
	// cheaper than a real upstream model request, so collect them first. A slow
	// model probe may consume the remaining budget but cannot hide billing queue
	// backlog or process pressure from this heartbeat.
	if enrichmentTimeoutStage == "" {
		m.fillUsageQueueHealth(enrichmentCtx, &req)
		if enrichmentCtx.Err() != nil {
			enrichmentTimeoutStage = "usage_queue"
		}
	}
	if enrichmentTimeoutStage == "" {
		m.fillRuntimeMetrics(enrichmentCtx, &req, metricsProviders)
		if enrichmentCtx.Err() != nil {
			enrichmentTimeoutStage = "runtime_metrics"
		}
	}
	if enrichmentTimeoutStage == "" {
		if modelProbe, ok := m.currentModelProbeResult(enrichmentCtx); ok {
			applyRuntimeModelProbeResult(&req, modelProbe)
		}
		if enrichmentCtx.Err() != nil {
			enrichmentTimeoutStage = "model_probe"
		}
	}
	if enrichmentTimeoutStage != "" {
		req.Metadata["runtime_health_enrichment_timed_out"] = true
		req.Metadata["runtime_health_enrichment_timeout_stage"] = enrichmentTimeoutStage
	}
	enrichmentCancel()

	// Reporting gets a fresh budget. A model or metric timeout must not hand an
	// already-expired context to the control plane and lose the basic heartbeat.
	reportCtx, reportCancel := context.WithTimeout(ctx, m.currentStageTimeout())
	defer reportCancel()
	resp, reportErr := reportGatewayHealthSafe(m.client, reportCtx, req)
	if reportErr != nil {
		log.Printf("gateway-lite: runtime health report failed: %v", reportErr)
		return
	}
	if resp != nil && !resp.OK {
		log.Printf("gateway-lite: runtime health report rejected: %s", resp.Error)
	}
}

func (m *RuntimeHealthMonitor) currentStageTimeout() time.Duration {
	if m == nil || m.stageTimeout <= 0 {
		return defaultUsageReportTimeout
	}
	return m.stageTimeout
}

func probeControlPlaneHealthSafe(client RuntimeHealthClient, ctx context.Context) (latency time.Duration, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("gateway-lite: control plane health probe panicked: %v", recovered)
			latency = 0
			err = context.Canceled
		}
	}()
	return client.ProbeControlPlaneHealth(ctx)
}

func reportGatewayHealthSafe(client RuntimeHealthClient, ctx context.Context, req GatewayHealthReportRequest) (resp *GatewayHealthReportResponse, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("gateway-lite: runtime health report panicked: %v", recovered)
			resp = nil
			err = context.Canceled
		}
	}()
	return client.ReportGatewayHealth(ctx, req)
}

func availableModelsSafe(provider RuntimeAvailableModelsProvider, ctx context.Context) (models []string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("gateway-lite: available models provider panicked: %v", recovered)
			models = nil
		}
	}()
	return provider.AvailableModels(ctx)
}

func gatewayModelPricesSafe(provider RuntimeModelPriceProvider, models []string, gatewayCode string) (prices []GatewayModelPrice) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("gateway-lite: model price provider panicked: %v", recovered)
			prices = nil
		}
	}()
	return provider.GatewayModelPrices(models, gatewayCode)
}

func (m *RuntimeHealthMonitor) currentModelProbeResult(ctx context.Context) (RuntimeModelProbeResult, bool) {
	if m == nil {
		return RuntimeModelProbeResult{}, false
	}
	now := time.Now()
	m.mu.RLock()
	provider := m.modelProbeProvider
	model := strings.TrimSpace(m.modelProbeModel)
	interval := m.modelProbeInterval
	settings := m.modelProbeSettings
	last := m.lastModelProbe
	lastAt := m.lastModelProbeAt
	nextAt := m.nextModelProbeAt
	hasLast := m.hasLastModelProbe
	m.mu.RUnlock()

	if settings != nil {
		if settingModel := strings.TrimSpace(settings.RuntimeModelProbeModel()); settingModel != "" {
			model = settingModel
		}
		if settingInterval := settings.RuntimeModelProbeInterval(); settingInterval > 0 {
			interval = settingInterval
		}
	}
	interval = normalizeRuntimeModelProbeInterval(interval)
	if provider == nil {
		return RuntimeModelProbeResult{}, false
	}
	if hasLast && last.ProbeInterval == interval && !nextAt.IsZero() && now.Before(nextAt) {
		last.Cached = true
		last.ProbeInterval = interval
		return last, true
	}
	if hasLast && last.ProbeInterval == interval && nextAt.IsZero() && now.Sub(lastAt) < interval {
		last.Cached = true
		last.ProbeInterval = interval
		return last, true
	}

	probeCtx, cancel := context.WithTimeout(ctx, m.currentStageTimeout())
	defer cancel()
	result, ok := probeRuntimeModelSafe(provider, probeCtx, model)
	if !ok {
		if hasLast {
			last.Cached = true
			last.ProbeInterval = interval
			return last, true
		}
		return RuntimeModelProbeResult{}, false
	}
	result = normalizeRuntimeModelProbeResult(result, interval, time.Now())

	m.mu.Lock()
	m.lastModelProbe = result
	m.lastModelProbeAt = result.CheckedAt
	m.nextModelProbeAt = result.CheckedAt.Add(runtimeModelProbeNextDelay(interval))
	m.hasLastModelProbe = true
	m.mu.Unlock()

	return result, true
}

func probeRuntimeModelSafe(provider RuntimeModelProbeProvider, ctx context.Context, model string) (result RuntimeModelProbeResult, ok bool) {
	ok = true
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("gateway-lite: runtime model probe panicked: %v", recovered)
			result = RuntimeModelProbeResult{}
			ok = false
		}
	}()
	result = provider.ProbeRuntimeModel(ctx, model)
	return result, ok
}

func normalizeRuntimeModelProbeResult(result RuntimeModelProbeResult, interval time.Duration, checkedAt time.Time) RuntimeModelProbeResult {
	result.Model = strings.TrimSpace(result.Model)
	result.Status = strings.ToLower(strings.TrimSpace(result.Status))
	result.UpstreamModel = strings.TrimSpace(result.UpstreamModel)
	result.Platform = strings.TrimSpace(result.Platform)
	result.AccountName = strings.TrimSpace(result.AccountName)
	result.Target = strings.TrimSpace(result.Target)
	result.Error = strings.TrimSpace(result.Error)
	if result.Target == "" {
		result.Target = "model-chat-completions"
	}
	if result.CheckedAt.IsZero() {
		result.CheckedAt = checkedAt
	}
	if interval <= 0 {
		interval = defaultRuntimeModelProbeInterval
	}
	interval = normalizeRuntimeModelProbeInterval(interval)
	result.ProbeInterval = interval
	result.Cached = false
	return result
}

func normalizeRuntimeModelProbeInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return defaultRuntimeModelProbeInterval
	}
	if interval < runtimeModelProbeMinInterval {
		return runtimeModelProbeMinInterval
	}
	if interval > runtimeModelProbeMaxInterval {
		return runtimeModelProbeMaxInterval
	}
	return interval
}

func runtimeModelProbeNextDelay(interval time.Duration) time.Duration {
	interval = normalizeRuntimeModelProbeInterval(interval)
	jitter := time.Duration(float64(interval) * runtimeModelProbeJitterRatio)
	if jitter > runtimeModelProbeMaxJitter {
		jitter = runtimeModelProbeMaxJitter
	}
	if jitter <= 0 {
		return interval
	}
	// 真实模型探测只验证上游可用性，加入小幅 ±jitter 可避免多网关同时向上游打点。
	offset := time.Duration(rand.Int64N(int64(2*jitter)+1)) - jitter //nolint:gosec // 健康探测调度抖动不涉及安全随机。
	delay := interval + offset
	if delay < runtimeModelProbeMinInterval {
		return runtimeModelProbeMinInterval
	}
	if delay > runtimeModelProbeMaxInterval {
		return runtimeModelProbeMaxInterval
	}
	return delay
}

func applyRuntimeModelProbeResult(req *GatewayHealthReportRequest, result RuntimeModelProbeResult) {
	if req == nil {
		return
	}
	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}
	status, message := runtimeModelProbeHealth(result)
	req.HealthStatus = status
	if result.Latency > 0 {
		req.LatencyMS = latencyMilliseconds(result.Latency)
	}
	if message != "" {
		if req.Message == "" {
			req.Message = message
		} else if !strings.Contains(req.Message, message) {
			req.Message = req.Message + "; " + message
		}
	}
	req.Metadata["probe_source"] = "gateway-lite-real-model-probe"
	req.Metadata["probe_target"] = result.Target
	req.Metadata["probe_ok"] = result.OK
	req.Metadata["probe_cached"] = result.Cached
	req.Metadata["probe_interval_seconds"] = int64(result.ProbeInterval.Seconds())
	req.Metadata["probe_checked_at_millis"] = result.CheckedAt.UnixMilli()
	if result.Model != "" {
		req.Metadata["probe_model"] = result.Model
	}
	if result.UpstreamModel != "" {
		req.Metadata["probe_upstream_model"] = result.UpstreamModel
	}
	if result.Platform != "" {
		req.Metadata["probe_platform"] = result.Platform
	}
	if result.AccountID > 0 {
		req.Metadata["probe_account_id"] = result.AccountID
	}
	if result.AccountName != "" {
		req.Metadata["probe_account_name"] = result.AccountName
	}
	if result.Status != "" {
		req.Metadata["probe_status"] = result.Status
	}
	if result.StatusCode > 0 {
		req.Metadata["probe_status_code"] = result.StatusCode
	}
	if result.Latency > 0 {
		req.Metadata["probe_latency_ms"] = latencyMilliseconds(result.Latency)
	}
	probeError := strings.TrimSpace(result.Error)
	if result.OK {
		probeError = ""
	}
	// 主站会增量合并节点 metadata（元数据）。成功时也必须显式发送空值，
	// 否则上一次失败留下的 probe_error 会一直残留，造成“已恢复但仍显示旧错误”。
	req.Metadata["probe_error"] = probeError
}

func runtimeModelProbeHealth(result RuntimeModelProbeResult) (string, string) {
	status := normalizeRuntimeModelProbeStatus(result)
	if status == "operational" {
		return "operational", ""
	}
	if status == "degraded" {
		return "degraded", "real model health probe slow"
	}
	message := "real model health probe failed"
	if result.Error != "" {
		message = message + ": " + result.Error
	}
	return status, message
}

func normalizeRuntimeModelProbeStatus(result RuntimeModelProbeResult) string {
	switch strings.ToLower(strings.TrimSpace(result.Status)) {
	case "operational", "healthy":
		if result.Latency >= runtimeModelProbeDegradedThreshold {
			return "degraded"
		}
		return "operational"
	case "degraded", "warning":
		return "degraded"
	case "failed", "content_failed", "content_error":
		return "failed"
	case "error", "critical", "unhealthy", "offline", "down":
		return "error"
	}
	if result.OK {
		if result.Latency >= runtimeModelProbeDegradedThreshold {
			return "degraded"
		}
		return "operational"
	}
	if result.StatusCode >= 200 && result.StatusCode < 300 {
		return "failed"
	}
	return "error"
}

func normalizeAvailableModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func latencyMilliseconds(latency time.Duration) int {
	if latency <= 0 {
		return 0
	}
	ms := int(latency.Milliseconds())
	if ms <= 0 {
		return 1
	}
	return ms
}

func (m *RuntimeHealthMonitor) fillUsageQueueHealth(ctx context.Context, req *GatewayHealthReportRequest) {
	if m == nil || req == nil {
		return
	}
	m.mu.RLock()
	usageReporter := m.usageReporter
	pendingThreshold := m.usagePendingThreshold
	deadThreshold := m.usageDeadThreshold
	m.mu.RUnlock()
	if usageReporter == nil || !usageReporter.Enabled() {
		return
	}
	queueCtx, cancel := context.WithTimeout(ctx, m.currentStageTimeout())
	stats, err := usageReporter.UsageQueueStats(queueCtx)
	cancel()

	status := "operational"
	message := ""
	if err != nil {
		status = "degraded"
		message = "usage queue stats unavailable"
	} else if stats.DeadCount >= deadThreshold {
		status = "error"
		message = "usage queue dead stream has events"
	} else if stats.PendingCount >= pendingThreshold {
		status = "degraded"
		message = "usage queue backlog exceeds threshold"
	}
	req.UsageQueueStatus = status
	req.UsageQueueStreamLength = stats.StreamLength
	req.UsageQueuePendingCount = stats.PendingCount
	req.UsageQueueDeadCount = stats.DeadCount
	if req.Message == "" && message != "" {
		req.Message = message
	}
	req.Metadata["usage_queue_pending_max"] = pendingThreshold
	req.Metadata["usage_queue_dead_max"] = deadThreshold
}

func (m *RuntimeHealthMonitor) fillRuntimeMetrics(ctx context.Context, req *GatewayHealthReportRequest, providers []RuntimeMetricsProvider) {
	if req == nil || len(providers) == 0 {
		return
	}
	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}
	for _, provider := range providers {
		if ctx.Err() != nil {
			break
		}
		if provider == nil {
			continue
		}
		metrics := runtimeMetricsSafe(provider, ctx)
		for key, value := range metrics {
			key = strings.TrimSpace(key)
			if key == "" || value == nil {
				continue
			}
			req.Metadata[key] = value
		}
	}
}

func runtimeMetricsSafe(provider RuntimeMetricsProvider, ctx context.Context) (metrics map[string]any) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("gateway-lite: runtime metrics provider panicked: %v", recovered)
			metrics = nil
		}
	}()
	return provider.RuntimeMetrics(ctx)
}
