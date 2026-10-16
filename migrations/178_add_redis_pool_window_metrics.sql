-- Add one-minute Redis connection-pool pressure deltas to ops snapshots.
-- These counters are diagnostic only and do not change health scoring.
ALTER TABLE ops_system_metrics
  ADD COLUMN IF NOT EXISTS redis_pool_wait_count BIGINT,
  ADD COLUMN IF NOT EXISTS redis_pool_wait_duration_ms BIGINT,
  ADD COLUMN IF NOT EXISTS redis_pool_timeouts BIGINT;

COMMENT ON COLUMN ops_system_metrics.redis_pool_wait_count IS 'go-redis PoolStats.WaitCount delta for this metrics window.';
COMMENT ON COLUMN ops_system_metrics.redis_pool_wait_duration_ms IS 'go-redis PoolStats.WaitDurationNs delta converted to milliseconds for this metrics window.';
COMMENT ON COLUMN ops_system_metrics.redis_pool_timeouts IS 'go-redis PoolStats.Timeouts delta for this metrics window.';
