package server

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

const gatewayLiteUpstreamMetricsWindow = 5 * time.Minute
const gatewayLiteUpstreamMetricsCacheTTL = time.Minute

// gatewayLiteDBRuntimeMetricsProvider 只读取 sql.DB 的进程内统计，不执行探测 SQL。
// 健康上报因此不会和用户请求争抢数据库连接。
type gatewayLiteDBRuntimeMetricsProvider struct {
	db *sql.DB
}

func (p gatewayLiteDBRuntimeMetricsProvider) RuntimeMetrics(ctx context.Context) map[string]any {
	metrics := map[string]any{}
	if p.db == nil || (ctx != nil && ctx.Err() != nil) {
		return metrics
	}
	stats := p.db.Stats()
	metrics["db_pool_max_open_connections"] = stats.MaxOpenConnections
	metrics["db_pool_open_connections"] = stats.OpenConnections
	metrics["db_pool_in_use"] = stats.InUse
	metrics["db_pool_idle"] = stats.Idle
	metrics["db_pool_wait_count_total"] = stats.WaitCount
	metrics["db_pool_wait_duration_ms_total"] = stats.WaitDuration.Milliseconds()
	metrics["db_pool_max_idle_closed_total"] = stats.MaxIdleClosed
	metrics["db_pool_max_idle_time_closed_total"] = stats.MaxIdleTimeClosed
	metrics["db_pool_max_lifetime_closed_total"] = stats.MaxLifetimeClosed
	return metrics
}

// gatewayLiteUpstreamRuntimeMetricsProvider 每分钟最多查询一次最近五分钟的聚合值。
// 分子只统计 provider（真实上游）错误，并排除业务限额、429/529 和 count_tokens，
// 避免用户输入、内容拦截或额度不足被误判为上游故障。
type gatewayLiteUpstreamRuntimeMetricsProvider struct {
	db *sql.DB

	mu       sync.Mutex
	cachedAt time.Time
	cached   map[string]any
	queryNow func() time.Time
}

func (p *gatewayLiteUpstreamRuntimeMetricsProvider) RuntimeMetrics(ctx context.Context) map[string]any {
	if p == nil || p.db == nil || (ctx != nil && ctx.Err() != nil) {
		return map[string]any{}
	}
	now := time.Now().UTC()
	if p.queryNow != nil {
		now = p.queryNow().UTC()
	}
	// 不等待另一轮缓存刷新；健康上报宁可本轮省略，也不能因锁竞争
	// 把整个 runtime monitor（运行监控）拖住。
	if !p.mu.TryLock() {
		return map[string]any{}
	}
	defer p.mu.Unlock()
	if len(p.cached) > 0 && now.Sub(p.cachedAt) < gatewayLiteUpstreamMetricsCacheTTL {
		return cloneGatewayLiteOperationalMetrics(p.cached)
	}

	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := now.Add(-gatewayLiteUpstreamMetricsWindow)
	var successCount, failureCount int64
	err := p.db.QueryRowContext(queryCtx, `
WITH successful AS (
	SELECT COUNT(*)::BIGINT AS count
	FROM usage_logs
	WHERE created_at >= $1 AND created_at < $2
), upstream_failed AS (
	SELECT COUNT(*)::BIGINT AS count
	FROM ops_error_logs
	WHERE created_at >= $1 AND created_at < $2
	  AND error_owner = 'provider'
	  AND NOT is_business_limited
	  AND NOT is_count_tokens
	  AND COALESCE(upstream_status_code, status_code, 0) NOT IN (429, 529)
)
SELECT successful.count, upstream_failed.count
FROM successful CROSS JOIN upstream_failed
`, start, now).Scan(&successCount, &failureCount)
	metrics := map[string]any{
		"upstream_metrics_window_seconds":    int64(gatewayLiteUpstreamMetricsWindow.Seconds()),
		"upstream_metrics_checked_at_millis": now.UnixMilli(),
	}
	if err != nil {
		// 不把数据库错误原文上报，避免 DSN（连接串）或内部表结构进入告警。
		metrics["upstream_metrics_available"] = false
		metrics["upstream_metrics_error"] = "query_failed"
	} else {
		total := successCount + failureCount
		rate := float64(0)
		if total > 0 {
			rate = float64(failureCount) * 100 / float64(total)
		}
		metrics["upstream_metrics_available"] = true
		metrics["upstream_request_count_5m"] = total
		metrics["upstream_success_count_5m"] = successCount
		metrics["upstream_failure_count_5m"] = failureCount
		metrics["upstream_failure_rate_percent_5m"] = rate
	}
	p.cachedAt = now
	p.cached = metrics
	return cloneGatewayLiteOperationalMetrics(metrics)
}

func cloneGatewayLiteOperationalMetrics(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
