package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAppendBillingCacheRuntimeMetricsIncludesSplitQueues(t *testing.T) {
	cfg := &config.Config{}
	cfg.Billing.CacheWrite = config.BillingCacheWriteConfig{
		CriticalWorkerCount:   3,
		CriticalBufferSize:    17,
		BestEffortWorkerCount: 2,
		BestEffortBufferSize:  9,
		TimeoutMilliseconds:   100,
	}
	svc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)

	metrics := map[string]any{}
	appendBillingCacheRuntimeMetrics(metrics, svc)

	require.Equal(t, 3, metrics["billing_cache_critical_worker_count"])
	require.Equal(t, 17, metrics["billing_cache_critical_queue_capacity"])
	require.Equal(t, 2, metrics["billing_cache_best_effort_worker_count"])
	require.Equal(t, 9, metrics["billing_cache_best_effort_queue_capacity"])
	require.Contains(t, metrics, "billing_cache_timeout_milliseconds")
	require.Contains(t, metrics, "billing_cache_active_worker_count")
	require.Contains(t, metrics, "billing_cache_critical_active_worker_count")
	require.Contains(t, metrics, "billing_cache_best_effort_active_worker_count")
	require.Contains(t, metrics, "billing_cache_critical_sync_fallback_failed_total")
	require.Contains(t, metrics, "billing_cache_best_effort_dropped_closed_total")
	require.Contains(t, metrics, "billing_cache_best_effort_deferred_length")
	require.Contains(t, metrics, "billing_cache_best_effort_merged_total")
	require.Contains(t, metrics, "billing_cache_best_effort_sync_fallback_total")
	require.Contains(t, metrics, "billing_cache_ordering_wait_total")
	require.Contains(t, metrics, "billing_cache_reload_total")
}
