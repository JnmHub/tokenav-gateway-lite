package gatewaylite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type runtimeHealthSink struct {
	mu           sync.Mutex
	reports      []GatewayHealthReportRequest
	latency      time.Duration
	zeroLatency  bool
	err          error
	reportErr    error
	reportCtxErr error
	reportBudget time.Duration
}

type runtimeAvailableModelsProviderFunc func(context.Context) []string

func (f runtimeAvailableModelsProviderFunc) AvailableModels(ctx context.Context) []string {
	return f(ctx)
}

type runtimeMetricsProviderFunc func(context.Context) map[string]any

func (f runtimeMetricsProviderFunc) RuntimeMetrics(ctx context.Context) map[string]any {
	return f(ctx)
}

type runtimeModelProbeProviderFunc func(context.Context, string) RuntimeModelProbeResult

func (f runtimeModelProbeProviderFunc) ProbeRuntimeModel(ctx context.Context, preferredModel string) RuntimeModelProbeResult {
	return f(ctx, preferredModel)
}

func (s *runtimeHealthSink) ProbeControlPlaneHealth(context.Context) (time.Duration, error) {
	if s.zeroLatency {
		return 0, s.err
	}
	if s.latency == 0 {
		s.latency = 42 * time.Millisecond
	}
	return s.latency, s.err
}

func (s *runtimeHealthSink) ReportGatewayHealth(ctx context.Context, req GatewayHealthReportRequest) (*GatewayHealthReportResponse, error) {
	if s.reportErr != nil {
		return nil, s.reportErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportCtxErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		s.reportBudget = time.Until(deadline)
	}
	s.reports = append(s.reports, req)
	return &GatewayHealthReportResponse{OK: true}, nil
}

func (s *runtimeHealthSink) lastReport() (GatewayHealthReportRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reports) == 0 {
		return GatewayHealthReportRequest{}, false
	}
	return s.reports[len(s.reports)-1], true
}

func (s *runtimeHealthSink) reportState() (count int, ctxErr error, budget time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reports), s.reportCtxErr, s.reportBudget
}

func TestRuntimeHealthMonitorReportsLatencyAndOnlineUsers(t *testing.T) {
	now := time.Now()
	stats := NewRuntimeStats()
	stats.RecordUser(42, now)
	stats.RecordUser(43, now.Add(-10*time.Minute))
	sink := &runtimeHealthSink{latency: 57 * time.Millisecond}

	monitor := NewRuntimeHealthMonitor(sink, stats, "sg", "sg-1", time.Second, 5*time.Minute)
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, "sg-1", report.GatewayCode)
	require.Equal(t, "sg", report.Region)
	require.Equal(t, "operational", report.HealthStatus)
	require.Equal(t, 57, report.LatencyMS)
	require.Equal(t, 1, report.OnlineUsers)
	require.Equal(t, "gateway-lite-runtime-health-monitor", report.Metadata["source"])
}

func TestRuntimeHealthMonitorUsesUpdatedRegionAndGatewayIdentity(t *testing.T) {
	sink := &runtimeHealthSink{latency: 10 * time.Millisecond}
	monitor := NewRuntimeHealthMonitor(sink, NewRuntimeStats(), "old-region", "old-node", time.Second, 5*time.Minute)
	monitor.SetRuntimeConfig("us", "us-1", 5*time.Minute, 1000, 1)
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, "us-1", report.GatewayCode)
	require.Equal(t, "us", report.Region)
}

func TestRuntimeHealthMonitorRoundsSubMillisecondLatencyUp(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{latency: 500 * time.Microsecond}

	monitor := NewRuntimeHealthMonitor(sink, stats, "hk", "openai-hk-t1", time.Second, 5*time.Minute)
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, 1, report.LatencyMS)
}

func TestRuntimeHealthMonitorReportsAtLeastOneMillisecondOnSuccessfulZeroLatencyProbe(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{zeroLatency: true}

	monitor := NewRuntimeHealthMonitor(sink, stats, "hk", "openai-hk-t1", time.Second, 5*time.Minute)
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, "operational", report.HealthStatus)
	require.Equal(t, 1, report.LatencyMS)
}

func TestRuntimeHealthMonitorUsesDynamicIntervalSource(t *testing.T) {
	settings := NewRuntimeHealthSettings(60 * time.Second)
	monitor := NewRuntimeHealthMonitor(&runtimeHealthSink{}, NewRuntimeStats(), "sg", "sg-1", 15*time.Second, 5*time.Minute).
		WithIntervalSource(settings)

	require.Equal(t, 60*time.Second, monitor.currentInterval())

	settings.SetInterval(180 * time.Second)
	require.Equal(t, 180*time.Second, monitor.currentInterval())
}

func TestRuntimeHealthMonitorReportsConfiguredAvailableModels(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{latency: 10 * time.Millisecond}

	monitor := NewRuntimeHealthMonitor(sink, stats, "sg", "sg-1", time.Second, 5*time.Minute).
		WithAvailableModels([]string{" gpt-4o ", "claude-sonnet-4", "gpt-4o", ""})
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, []string{"claude-sonnet-4", "gpt-4o"}, report.AvailableModels)
}

func TestRuntimeHealthMonitorMergesDynamicAvailableModels(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{latency: 10 * time.Millisecond}

	monitor := NewRuntimeHealthMonitor(sink, stats, "sg", "sg-1", time.Second, 5*time.Minute).
		WithAvailableModels([]string{"gpt-5.5", "mimo-v2.5"}).
		WithAvailableModelsProvider(runtimeAvailableModelsProviderFunc(func(context.Context) []string {
			return []string{"mimo-v2-flash", "mimo-v2.5", " "}
		}))
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, []string{"gpt-5.5", "mimo-v2-flash", "mimo-v2.5"}, report.AvailableModels)
}

func TestRuntimeHealthMonitorReportFailureDoesNotStopNextReport(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{
		latency:   10 * time.Millisecond,
		reportErr: errors.New("temporary report failure"),
	}

	monitor := NewRuntimeHealthMonitor(sink, stats, "sg", "sg-1", time.Second, 5*time.Minute).
		WithAvailableModels([]string{"gpt-4o"})
	monitor.reportOnce(context.Background())

	_, ok := sink.lastReport()
	require.False(t, ok)

	sink.reportErr = nil
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, []string{"gpt-4o"}, report.AvailableModels)
}

func TestRuntimeHealthMonitorMergesRuntimeMetrics(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{latency: 10 * time.Millisecond}

	monitor := NewRuntimeHealthMonitor(sink, stats, "sg", "sg-1", time.Second, 5*time.Minute).
		WithRuntimeMetricsProvider(runtimeMetricsProviderFunc(func(context.Context) map[string]any {
			return map[string]any{
				" usage_record_waiting_tasks ": uint64(3),
				"billing_cache_queue_length":   7,
				"":                             "ignored",
			}
		}))
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.EqualValues(t, 3, report.Metadata["usage_record_waiting_tasks"])
	require.EqualValues(t, 7, report.Metadata["billing_cache_queue_length"])
	require.NotContains(t, report.Metadata, "")
}

func TestRuntimeHealthMonitorControlPlaneTimeoutStillReports(t *testing.T) {
	client := &blockingRuntimeHealthClient{
		probe: func(ctx context.Context) (time.Duration, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		},
	}
	monitor := NewRuntimeHealthMonitor(client, NewRuntimeStats(), "sg", "sg-1", time.Second, 5*time.Minute)
	monitor.stageTimeout = 20 * time.Millisecond

	// The production client obeys cancellation; this simulates an unavailable
	// control plane without making the test wait for the real ten-second budget.
	started := time.Now()
	monitor.reportOnce(context.Background())

	require.Less(t, time.Since(started), 250*time.Millisecond)
	require.Equal(t, 1, client.reportCount())
}

func TestRuntimeHealthMonitorModelProbeTimeoutStillReportsWithFreshContext(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	usageReporter := NewRedisUsageReporter(redisClient, "runtime-health-order", nil)
	sink := &runtimeHealthSink{latency: time.Millisecond}
	monitor := NewRuntimeHealthMonitor(sink, NewRuntimeStats(), "sg", "sg-1", time.Second, 5*time.Minute).
		WithUsageQueue(usageReporter, 1000, 1).
		WithModelProbe(runtimeModelProbeProviderFunc(func(ctx context.Context, _ string) RuntimeModelProbeResult {
			<-ctx.Done()
			return RuntimeModelProbeResult{Status: "error", Error: ctx.Err().Error()}
		}), "gpt-5.5", 5*time.Minute)
	// Give the preceding miniredis queue snapshot enough budget on slow Windows
	// CI hosts; the model probe itself still deterministically consumes the deadline.
	monitor.stageTimeout = 100 * time.Millisecond

	started := time.Now()
	monitor.reportOnce(context.Background())

	require.Less(t, time.Since(started), 500*time.Millisecond)
	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, "error", report.HealthStatus)
	// Queue health is collected before the expensive real-model probe, so the
	// probe can consume the remaining shared budget without hiding queue state.
	require.Equal(t, "operational", report.UsageQueueStatus)
	require.Equal(t, true, report.Metadata["runtime_health_enrichment_timed_out"])
	require.Equal(t, "model_probe", report.Metadata["runtime_health_enrichment_timeout_stage"])
	_, reportCtxErr, reportBudget := sink.reportState()
	require.NoError(t, reportCtxErr)
	// Optional enrichment consumed its own budget, but the final control-plane
	// report received a newly-created deadline rather than the expired one.
	require.Greater(t, reportBudget, 50*time.Millisecond)
}

func TestRuntimeHealthMonitorMetricsTimeoutStillReportsWithFreshContext(t *testing.T) {
	sink := &runtimeHealthSink{latency: time.Millisecond}
	monitor := NewRuntimeHealthMonitor(sink, NewRuntimeStats(), "sg", "sg-1", time.Second, 5*time.Minute).
		WithRuntimeMetricsProvider(runtimeMetricsProviderFunc(func(ctx context.Context) map[string]any {
			<-ctx.Done()
			return map[string]any{"metrics_context_expired": true}
		}))
	monitor.stageTimeout = 20 * time.Millisecond

	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, true, report.Metadata["metrics_context_expired"])
	require.Equal(t, true, report.Metadata["runtime_health_enrichment_timed_out"])
	require.Equal(t, "runtime_metrics", report.Metadata["runtime_health_enrichment_timeout_stage"])
	_, reportCtxErr, reportBudget := sink.reportState()
	require.NoError(t, reportCtxErr)
	require.Greater(t, reportBudget, 10*time.Millisecond)
}

func TestRuntimeHealthMonitorProviderPanicDoesNotStopCurrentOrNextReport(t *testing.T) {
	sink := &runtimeHealthSink{latency: time.Millisecond}
	monitor := NewRuntimeHealthMonitor(sink, NewRuntimeStats(), "sg", "sg-1", time.Second, 5*time.Minute).
		WithRuntimeMetricsProvider(runtimeMetricsProviderFunc(func(context.Context) map[string]any {
			panic("broken metrics provider")
		})).
		WithModelProbe(runtimeModelProbeProviderFunc(func(context.Context, string) RuntimeModelProbeResult {
			panic("broken model probe")
		}), "gpt-5.5", 5*time.Minute)

	monitor.reportOnce(context.Background())
	monitor.reportOnce(context.Background())

	count, _, _ := sink.reportState()
	require.Equal(t, 2, count)
}

func TestRuntimeHealthMonitorSkipsConcurrentDuplicateAndAllowsLaterCycle(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	client := &blockingRuntimeHealthClient{
		probe: func(context.Context) (time.Duration, error) {
			once.Do(func() { close(started) })
			<-release
			return time.Millisecond, nil
		},
	}
	monitor := NewRuntimeHealthMonitor(client, NewRuntimeStats(), "sg", "sg-1", time.Second, 5*time.Minute)

	done := make(chan struct{})
	go func() {
		monitor.reportOnce(context.Background())
		close(done)
	}()
	<-started

	duplicateDone := make(chan struct{})
	go func() {
		monitor.reportOnce(context.Background())
		close(duplicateDone)
	}()
	select {
	case <-duplicateDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("concurrent duplicate report blocked")
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first report did not finish")
	}
	monitor.reportOnce(context.Background())
	require.Equal(t, 2, client.reportCount())
}

type blockingRuntimeHealthClient struct {
	mu      sync.Mutex
	probe   func(context.Context) (time.Duration, error)
	reports int
}

func (c *blockingRuntimeHealthClient) ProbeControlPlaneHealth(ctx context.Context) (time.Duration, error) {
	return c.probe(ctx)
}

func (c *blockingRuntimeHealthClient) ReportGatewayHealth(context.Context, GatewayHealthReportRequest) (*GatewayHealthReportResponse, error) {
	c.mu.Lock()
	c.reports++
	c.mu.Unlock()
	return &GatewayHealthReportResponse{OK: true}, nil
}

func (c *blockingRuntimeHealthClient) reportCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reports
}

func TestRuntimeHealthMonitorUsesRealModelProbeForHealthyStatus(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{err: errors.New("control plane health failed")}

	monitor := NewRuntimeHealthMonitor(sink, stats, "sg", "sg-1", time.Second, 5*time.Minute).
		WithModelProbe(runtimeModelProbeProviderFunc(func(_ context.Context, preferredModel string) RuntimeModelProbeResult {
			return RuntimeModelProbeResult{
				OK:            true,
				StatusCode:    200,
				Latency:       123 * time.Millisecond,
				Model:         preferredModel,
				UpstreamModel: "gpt-4o-mini",
				Platform:      "openai",
				AccountID:     99,
				AccountName:   "live-account",
				Target:        "model-chat-completions",
			}
		}), "gpt-4o-mini", 5*time.Minute)
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, "operational", report.HealthStatus)
	require.Equal(t, 123, report.LatencyMS)
	require.Equal(t, "gpt-4o-mini", report.Metadata["probe_model"])
	require.Equal(t, "gpt-4o-mini", report.Metadata["probe_upstream_model"])
	require.Equal(t, "openai", report.Metadata["probe_platform"])
	require.EqualValues(t, 99, report.Metadata["probe_account_id"])
	require.Equal(t, "live-account", report.Metadata["probe_account_name"])
	require.Equal(t, "gateway-lite-real-model-probe", report.Metadata["probe_source"])
	require.Equal(t, true, report.Metadata["probe_ok"])
	require.Contains(t, report.Metadata, "probe_error")
	require.Equal(t, "", report.Metadata["probe_error"])
	require.Equal(t, "control plane health probe failed", report.Message)
}

func TestApplyRuntimeModelProbeResultClearsPreviousErrorOnSuccess(t *testing.T) {
	req := GatewayHealthReportRequest{
		Metadata: map[string]any{"probe_error": "old upstream failure"},
	}
	applyRuntimeModelProbeResult(&req, RuntimeModelProbeResult{
		OK:         true,
		Status:     "operational",
		StatusCode: 200,
		Error:      "stale error must not survive a successful probe",
		CheckedAt:  time.Now(),
	})

	require.Contains(t, req.Metadata, "probe_error")
	require.Equal(t, "", req.Metadata["probe_error"])
}

func TestRuntimeHealthMonitorMarks429ModelProbeCritical(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{latency: 10 * time.Millisecond}

	monitor := NewRuntimeHealthMonitor(sink, stats, "sg", "sg-1", time.Second, 5*time.Minute).
		WithModelProbe(runtimeModelProbeProviderFunc(func(context.Context, string) RuntimeModelProbeResult {
			return RuntimeModelProbeResult{
				OK:         false,
				StatusCode: 429,
				Latency:    25 * time.Millisecond,
				Model:      "gpt-5.5",
				Platform:   "openai",
				Error:      "rate limit exceeded",
			}
		}), "gpt-5.5", 5*time.Minute)
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, "error", report.HealthStatus)
	require.Equal(t, 25, report.LatencyMS)
	require.Equal(t, false, report.Metadata["probe_ok"])
	require.Equal(t, 429, report.Metadata["probe_status_code"])
	require.Equal(t, "rate limit exceeded", report.Metadata["probe_error"])
	require.Contains(t, report.Message, "real model health probe failed")
}

func TestRuntimeHealthMonitorCachesRealModelProbe(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{latency: 10 * time.Millisecond}
	calls := 0

	monitor := NewRuntimeHealthMonitor(sink, stats, "sg", "sg-1", time.Second, 5*time.Minute).
		WithModelProbe(runtimeModelProbeProviderFunc(func(context.Context, string) RuntimeModelProbeResult {
			calls++
			return RuntimeModelProbeResult{
				OK:         true,
				StatusCode: 200,
				Latency:    11 * time.Millisecond,
				Model:      "gpt-4o-mini",
				Platform:   "openai",
			}
		}), "gpt-4o-mini", 5*time.Minute)

	monitor.reportOnce(context.Background())
	monitor.reportOnce(context.Background())

	require.Equal(t, 1, calls)
	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, true, report.Metadata["probe_cached"])
	require.EqualValues(t, 300, report.Metadata["probe_interval_seconds"])
	require.Contains(t, report.Metadata, "probe_error")
	require.Equal(t, "", report.Metadata["probe_error"])
}

func TestRuntimeModelProbeIntervalBounds(t *testing.T) {
	require.Equal(t, defaultRuntimeModelProbeInterval, normalizeRuntimeModelProbeInterval(0))
	require.Equal(t, runtimeModelProbeMinInterval, normalizeRuntimeModelProbeInterval(time.Second))
	require.Equal(t, 5*time.Minute, normalizeRuntimeModelProbeInterval(5*time.Minute))
	require.Equal(t, runtimeModelProbeMaxInterval, normalizeRuntimeModelProbeInterval(2*time.Hour))

	settings := NewRuntimeHealthSettings(time.Second)
	settings.SetModelProbeInterval(time.Second)
	require.Equal(t, runtimeModelProbeMinInterval, settings.RuntimeModelProbeInterval())
	settings.SetModelProbeInterval(2 * time.Hour)
	require.Equal(t, runtimeModelProbeMaxInterval, settings.RuntimeModelProbeInterval())
}

func TestRuntimeModelProbeNextDelayUsesBoundedJitter(t *testing.T) {
	for i := 0; i < 50; i++ {
		delay := runtimeModelProbeNextDelay(5 * time.Minute)
		require.GreaterOrEqual(t, delay, 270*time.Second)
		require.LessOrEqual(t, delay, 330*time.Second)
	}
	minDelay := runtimeModelProbeNextDelay(time.Second)
	require.GreaterOrEqual(t, minDelay, runtimeModelProbeMinInterval)
	require.LessOrEqual(t, minDelay, runtimeModelProbeMinInterval+time.Duration(float64(runtimeModelProbeMinInterval)*runtimeModelProbeJitterRatio))

	maxDelay := runtimeModelProbeNextDelay(2 * time.Hour)
	require.GreaterOrEqual(t, maxDelay, runtimeModelProbeMaxInterval-runtimeModelProbeMaxJitter)
	require.LessOrEqual(t, maxDelay, runtimeModelProbeMaxInterval)
}

func TestRuntimeHealthMonitorMarks2xxWrongAnswerAsFailed(t *testing.T) {
	stats := NewRuntimeStats()
	sink := &runtimeHealthSink{latency: 10 * time.Millisecond}

	monitor := NewRuntimeHealthMonitor(sink, stats, "sg", "sg-1", time.Second, 5*time.Minute).
		WithModelProbe(runtimeModelProbeProviderFunc(func(context.Context, string) RuntimeModelProbeResult {
			return RuntimeModelProbeResult{
				OK:         false,
				Status:     "failed",
				StatusCode: 200,
				Latency:    30 * time.Millisecond,
				Model:      "gpt-5.5",
				Platform:   "openai",
				Error:      "challenge mismatch",
			}
		}), "gpt-5.5", 5*time.Minute)
	monitor.reportOnce(context.Background())

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, "failed", report.HealthStatus)
	require.Equal(t, "failed", report.Metadata["probe_status"])
	require.Equal(t, 200, report.Metadata["probe_status_code"])
	require.Contains(t, report.Message, "challenge mismatch")
}
