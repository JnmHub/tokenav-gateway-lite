package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// BillingCacheService returns the one service instance used by request
// handlers. Admin runtime configuration must update this exact instance rather
// than constructing a disconnected worker pool.
func (h *Handlers) BillingCacheService() *service.BillingCacheService {
	if h == nil {
		return nil
	}
	if h.Gateway != nil && h.Gateway.billingCacheService != nil {
		return h.Gateway.billingCacheService
	}
	if h.OpenAIGateway != nil {
		return h.OpenAIGateway.billingCacheService
	}
	return nil
}

// RuntimeMetrics 汇总网关请求热路径里的本地队列/工作池指标。
// 这里只读内存状态，避免健康上报反过来给 Redis/DB 增加额外压力。
func (h *Handlers) RuntimeMetrics(ctx context.Context) map[string]any {
	metrics := map[string]any{}
	if h == nil || (ctx != nil && ctx.Err() != nil) {
		return metrics
	}
	billingCacheService := h.BillingCacheService()
	var usageRecordWorkerPool *service.UsageRecordWorkerPool
	var concurrencyService *service.ConcurrencyService
	if h.Gateway != nil {
		usageRecordWorkerPool = h.Gateway.usageRecordWorkerPool
		if h.Gateway.concurrencyHelper != nil {
			concurrencyService = h.Gateway.concurrencyHelper.concurrencyService
		}
	}
	if h.OpenAIGateway != nil {
		if usageRecordWorkerPool == nil {
			usageRecordWorkerPool = h.OpenAIGateway.usageRecordWorkerPool
		}
		if concurrencyService == nil && h.OpenAIGateway.concurrencyHelper != nil {
			concurrencyService = h.OpenAIGateway.concurrencyHelper.concurrencyService
		}
	}
	appendBillingCacheRuntimeMetrics(metrics, billingCacheService)
	appendUsageRecordWorkerRuntimeMetrics(metrics, usageRecordWorkerPool)
	appendAccountSlotReleaseRuntimeMetrics(metrics, concurrencyService)
	appendUserAPIKeySlotReleaseRuntimeMetrics(metrics, concurrencyService)
	return metrics
}

func appendAccountSlotReleaseRuntimeMetrics(metrics map[string]any, svc *service.ConcurrencyService) {
	if metrics == nil || svc == nil {
		return
	}
	stats := svc.AccountSlotReleaseRuntimeStats()
	metrics["account_slot_release_queue_length"] = stats.QueueLength
	metrics["account_slot_release_queue_capacity"] = stats.QueueCapacity
	metrics["account_slot_release_enqueued_total"] = stats.EnqueuedTotal
	metrics["account_slot_release_batch_total"] = stats.BatchTotal
	metrics["account_slot_release_completed_total"] = stats.CompletedTotal
	metrics["account_slot_release_batch_failure_total"] = stats.BatchFailureTotal
	metrics["account_slot_release_queue_full_total"] = stats.QueueFullTotal
	metrics["account_slot_release_sync_fallback_total"] = stats.SyncFallbackTotal
}

func appendUserAPIKeySlotReleaseRuntimeMetrics(metrics map[string]any, svc *service.ConcurrencyService) {
	if metrics == nil || svc == nil {
		return
	}
	stats := svc.UserAPIKeySlotReleaseRuntimeStats()
	metrics["user_api_key_slot_release_queue_length"] = stats.QueueLength
	metrics["user_api_key_slot_release_queue_capacity"] = stats.QueueCapacity
	metrics["user_api_key_slot_release_enqueued_total"] = stats.EnqueuedTotal
	metrics["user_api_key_slot_release_batch_total"] = stats.BatchTotal
	metrics["user_api_key_slot_release_completed_total"] = stats.CompletedTotal
	metrics["user_api_key_slot_release_batch_failure_total"] = stats.BatchFailureTotal
	metrics["user_api_key_slot_release_retry_total"] = stats.RetryTotal
	metrics["user_api_key_slot_release_queue_full_total"] = stats.QueueFullTotal
	metrics["user_api_key_slot_release_sync_fallback_total"] = stats.SyncFallbackTotal
}

func appendBillingCacheRuntimeMetrics(metrics map[string]any, svc *service.BillingCacheService) {
	if metrics == nil || svc == nil {
		return
	}
	stats := svc.RuntimeStats()
	metrics["billing_cache_preset"] = stats.Preset
	metrics["billing_cache_revision"] = stats.Revision
	metrics["billing_cache_timeout_milliseconds"] = stats.TimeoutMilliseconds
	metrics["billing_cache_queue_length"] = stats.QueueLength
	metrics["billing_cache_queue_capacity"] = stats.QueueCapacity
	metrics["billing_cache_worker_count"] = stats.WorkerCount
	metrics["billing_cache_active_worker_count"] = stats.ActiveWorkerCount
	metrics["billing_cache_dropped_full_total"] = stats.DroppedFullTotal
	metrics["billing_cache_dropped_closed_total"] = stats.DroppedClosedTotal
	metrics["billing_cache_dropped_full_recent"] = stats.DroppedFullRecent
	metrics["billing_cache_dropped_closed_recent"] = stats.DroppedClosedRecent
	metrics["billing_cache_stopped"] = stats.Stopped
	metrics["billing_cache_critical_queue_length"] = stats.CriticalQueueLength
	metrics["billing_cache_critical_queue_capacity"] = stats.CriticalQueueCapacity
	metrics["billing_cache_critical_worker_count"] = stats.CriticalWorkerCount
	metrics["billing_cache_critical_active_worker_count"] = stats.CriticalActiveWorkerCount
	metrics["billing_cache_critical_queue_full_total"] = stats.CriticalQueueFullTotal
	metrics["billing_cache_critical_sync_fallback_total"] = stats.CriticalSyncFallbackTotal
	metrics["billing_cache_critical_sync_fallback_failed_total"] = stats.CriticalSyncFallbackFailed
	metrics["billing_cache_best_effort_queue_length"] = stats.BestEffortQueueLength
	metrics["billing_cache_best_effort_queue_capacity"] = stats.BestEffortQueueCapacity
	metrics["billing_cache_best_effort_worker_count"] = stats.BestEffortWorkerCount
	metrics["billing_cache_best_effort_active_worker_count"] = stats.BestEffortActiveWorkerCount
	metrics["billing_cache_best_effort_deferred_length"] = stats.BestEffortDeferredLength
	metrics["billing_cache_best_effort_deferred_capacity"] = stats.BestEffortDeferredCapacity
	metrics["billing_cache_best_effort_deferred_total"] = stats.BestEffortDeferredTotal
	metrics["billing_cache_best_effort_merged_total"] = stats.BestEffortMergedTotal
	metrics["billing_cache_best_effort_sync_fallback_total"] = stats.BestEffortSyncFallbackTotal
	metrics["billing_cache_best_effort_sync_fallback_failed_total"] = stats.BestEffortFallbackFailed
	metrics["billing_cache_best_effort_dropped_full_total"] = stats.BestEffortDroppedFullTotal
	metrics["billing_cache_best_effort_dropped_closed_total"] = stats.BestEffortDroppedCloseTotal
	metrics["billing_cache_ordering_wait_total"] = stats.OrderingWaitTotal
	metrics["billing_cache_reload_total"] = stats.ReloadTotal
}

func appendUsageRecordWorkerRuntimeMetrics(metrics map[string]any, pool *service.UsageRecordWorkerPool) {
	if metrics == nil || pool == nil {
		return
	}
	stats := pool.Stats()
	metrics["usage_record_max_concurrency"] = stats.MaxConcurrency
	metrics["usage_record_running_workers"] = stats.RunningWorkers
	metrics["usage_record_waiting_tasks"] = stats.WaitingTasks
	metrics["usage_record_queue_capacity"] = stats.QueueCapacity
	metrics["usage_record_queue_utilization_percent"] = stats.QueueUtilizationPct
	metrics["usage_record_overflow_policy"] = stats.OverflowPolicy
	metrics["usage_record_overflow_sample_percent"] = stats.OverflowSamplePercent
	metrics["usage_record_task_timeout_seconds"] = stats.TaskTimeoutSeconds
	metrics["usage_record_auto_scale_enabled"] = stats.AutoScaleEnabled
	metrics["usage_record_auto_scale_min_workers"] = stats.AutoScaleMinWorkers
	metrics["usage_record_auto_scale_max_workers"] = stats.AutoScaleMaxWorkers
	metrics["usage_record_auto_scale_up_queue_percent"] = stats.AutoScaleUpPercent
	metrics["usage_record_auto_scale_down_queue_percent"] = stats.AutoScaleDownPercent
	metrics["usage_record_auto_scale_up_step"] = stats.AutoScaleUpStep
	metrics["usage_record_auto_scale_down_step"] = stats.AutoScaleDownStep
	metrics["usage_record_auto_scale_interval_ms"] = stats.AutoScaleIntervalMS
	metrics["usage_record_auto_scale_cooldown_ms"] = stats.AutoScaleCooldownMS
	metrics["usage_record_submitted_tasks_total"] = stats.SubmittedTasks
	metrics["usage_record_completed_tasks_total"] = stats.CompletedTasks
	metrics["usage_record_successful_tasks_total"] = stats.SuccessfulTasks
	metrics["usage_record_failed_tasks_total"] = stats.FailedTasks
	metrics["usage_record_dropped_tasks_total"] = stats.DroppedTasks
	metrics["usage_record_dropped_queue_full_total"] = stats.DroppedQueueFull
	metrics["usage_record_dropped_pool_stopped_total"] = stats.DroppedPoolStopped
	metrics["usage_record_sync_fallback_tasks_total"] = stats.SyncFallbackTasks
}
