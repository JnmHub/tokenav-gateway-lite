package gatewaylite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Drain reads the original namespace only. Missing tables/connections and scan
// limits are unknown states, never a zero balance. No key or row is deleted.
func BindingDrainProbe(db *sql.DB, rdb *redis.Client) func(context.Context, BindingRecord) (BindingDrain, error) {
	return func(ctx context.Context, r BindingRecord) (BindingDrain, error) {
		result := BindingDrain{Counts: map[string]int64{}, Unknown: []string{}}
		if r.Current == nil {
			return result, nil
		}
		if db == nil || rdb == nil {
			return result, errors.New("binding_settlement_storage_unavailable")
		}
		queries := []struct{ name, query string }{
			{"sql_retry", `SELECT count(*) FROM gateway_lite_usage_retry_outbox`},
			{"oauth_outbox", `SELECT count(*) FROM gateway_lite_oauth_invocation_recovery WHERE state <> 'completed'`},
			{"penalty_outbox", `SELECT count(*) FROM gateway_lite_content_moderation_penalty_outbox`},
			{"image_tasks", `SELECT count(*) FROM image_async_tasks WHERE state IN ('queued','running') OR settlement_state='pending'`},
			{"grok_video", `SELECT count(*) FROM grok_video_tasks WHERE state NOT IN ('failed','cancelled','billed') OR (state='succeeded' AND billed_at IS NULL)`},
			{"batch_image", bindingBatchImageOutstandingSQL},
			{"seedance_registry", `SELECT count(*) FROM gateway_lite_binding_media WHERE state NOT IN ('settled','released')`},
		}
		var failures []error
		for _, q := range queries {
			var n int64
			if err := db.QueryRowContext(ctx, q.query).Scan(&n); err != nil {
				result.Unknown = append(result.Unknown, q.name+" 查询未完成")
				failures = append(failures, err)
			} else {
				result.Counts[q.name] = n
			}
		}
		prefix := r.Current.RedisPrefix
		for _, entry := range []struct{ name, key string }{{"redis_stream", prefix + ":usage:stream"}, {"redis_dead", prefix + ":usage:dead"}} {
			n, err := rdb.XLen(ctx, entry.key).Result()
			if err != nil {
				failures = append(failures, err)
				result.Unknown = append(result.Unknown, entry.name+" 查询未完成")
			} else {
				result.Counts[entry.name] = n
			}
		}
		pending, err := rdb.XPending(ctx, prefix+":usage:stream", prefix+"-usage-reporters").Result()
		if err != nil && !strings.Contains(err.Error(), "NOGROUP") {
			failures = append(failures, err)
			result.Unknown = append(result.Unknown, "Redis 待确认消息查询未完成")
		} else if pending != nil {
			result.Counts["redis_pending"] = pending.Count
		}
		// Reservation hashes can outlive an HTTP request (async funding). Inspect
		// exact fields, not TTL, and refuse an unbounded scan rather than guessing.
		for _, scan := range []struct{ name, pattern, field string }{{"reserved_requests", prefix + ":req:*", "status"}, {"reserved_leases", prefix + ":lease:*", "reserved_cents"}, {"pending_funding", prefix + ":funding:*", "funding_final"}} {
			var cursor uint64
			seen := 0
			for {
				keys, next, err := rdb.Scan(ctx, cursor, scan.pattern, 100).Result()
				if err != nil {
					failures = append(failures, err)
					result.Unknown = append(result.Unknown, scan.name+" 查询未完成")
					break
				}
				for _, key := range keys {
					seen++
					if seen > 10000 {
						result.Unknown = append(result.Unknown, "资金登记过多，无法在单次检查内确认")
						break
					}
					value, err := rdb.HGet(ctx, key, scan.field).Result()
					if err != nil && !errors.Is(err, redis.Nil) {
						failures = append(failures, err)
						result.Unknown = append(result.Unknown, scan.name+" 状态读取失败")
						break
					}
					if scan.field == "reserved_cents" {
						n, e := strconv.ParseInt(value, 10, 64)
						if e != nil && value != "" {
							result.Unknown = append(result.Unknown, "额度租约计数无效")
						} else if n != 0 {
							result.Counts[scan.name]++
						}
					} else if scan.field == "funding_final" && value != "1" {
						result.Counts[scan.name]++
					} else if value == "reserved" || value == "pending" || value == "active" || value == "funding_reserved" {
						result.Counts[scan.name]++
					}
				}
				cursor = next
				if cursor == 0 || seen > 10000 {
					break
				}
			}
		}
		if r.Current.Legacy {
			verified, evidenceErr := bindingLegacyMediaEvidence(ctx, db, r.Current.BindingID)
			if evidenceErr != nil {
				failures = append(failures, evidenceErr)
			}
			if !verified {
				result.Unknown = append(result.Unknown, "旧 Seedance 历史覆盖未核实：需要原供应商完整任务目录、最终结算证据和主站资金核对；请联系运维核验，或取消断开继续使用原主站。")
			}
		}
		return result, errors.Join(failures...)
	}
}
