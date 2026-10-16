package ingress

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RejectionReason 是入口层可预期拒绝的稳定分类。
// 这里故意不携带 path、IP、请求头、正文或 API Key，避免观测链路反向成为敏感数据库。
type RejectionReason string

const (
	RejectHeaderTooLarge RejectionReason = "header_too_large"
	RejectBodyTooLarge   RejectionReason = "body_too_large"
	RejectInvalidPath    RejectionReason = "invalid_path"
	RejectMalformed      RejectionReason = "malformed_request"
	RejectAuthRateLimit  RejectionReason = "auth_rate_limited"
	RejectAdminRateLimit RejectionReason = "admin_rate_limited"
	RejectCodexClient    RejectionReason = "codex_client_denied"
)

const (
	defaultRetentionMinutes = 24 * 60
	defaultSnapshotMinutes  = 60
)

var knownReasons = []RejectionReason{
	RejectHeaderTooLarge,
	RejectBodyTooLarge,
	RejectInvalidPath,
	RejectMalformed,
	RejectAuthRateLimit,
	RejectAdminRateLimit,
	RejectCodexClient,
}

type minuteBucket struct {
	minute int64
	counts map[RejectionReason]uint64
}

type requestMinuteSlot struct {
	mu     sync.Mutex
	minute atomic.Int64
	count  atomic.Uint64
}

// ReasonCount 是某类拒绝在查询时间窗内的聚合值。
type ReasonCount struct {
	Reason RejectionReason `json:"reason"`
	Count  uint64          `json:"count"`
}

// TrendPoint 是一分钟粒度的拒绝趋势。
type TrendPoint struct {
	BucketStart time.Time                  `json:"bucket_start"`
	Total       uint64                     `json:"total"`
	Counts      map[RejectionReason]uint64 `json:"counts"`
}

// Snapshot 只返回节点、时间窗和计数，不存储或暴露单次请求明细。
type Snapshot struct {
	Node                 string        `json:"node"`
	WindowMinutes        int           `json:"window_minutes"`
	WindowStart          time.Time     `json:"window_start"`
	WindowEnd            time.Time     `json:"window_end"`
	RequestCount         uint64        `json:"request_count"`
	RejectionCount       uint64        `json:"rejection_count"`
	RejectionRatePercent float64       `json:"rejection_rate_percent"`
	TopReasons           []ReasonCount `json:"top_reasons"`
	Trend                []TrendPoint  `json:"trend"`
}

// Health 用于后台和 gateway-lite runtime health（运行健康）上报。
type Health struct {
	Node                  string    `json:"node"`
	Status                string    `json:"status"`
	RetentionMinutes      int       `json:"retention_minutes"`
	RequestsTotal         uint64    `json:"requests_total"`
	RejectionsTotal       uint64    `json:"rejections_total"`
	RejectionRatePercent  float64   `json:"rejection_rate_percent"`
	LastRequestObservedAt time.Time `json:"last_request_observed_at,omitempty"`
	SensitiveFieldsStored bool      `json:"sensitive_fields_stored"`
}

// Aggregator 在内存中保留最近 24 小时的分钟聚合。
// 普通请求只执行两次 atomic（原子）操作；只有真正被拒绝时才会加锁更新 map。
type Aggregator struct {
	mu               sync.Mutex
	buckets          map[int64]*minuteBucket
	requestSlots     [defaultRetentionMinutes]requestMinuteSlot
	requestsTotal    atomic.Uint64
	rejectionsTotal  atomic.Uint64
	lastRequestNanos atomic.Int64
	node             atomic.Value // string
	now              func() time.Time
}

func NewAggregator() *Aggregator {
	a := &Aggregator{
		buckets: make(map[int64]*minuteBucket),
		now:     time.Now,
	}
	a.node.Store("local")
	return a
}

func (a *Aggregator) SetNode(node string) {
	if a == nil {
		return
	}
	node = strings.TrimSpace(node)
	if node == "" {
		node = "local"
	}
	a.node.Store(node)
}

func (a *Aggregator) nodeName() string {
	if a == nil {
		return "local"
	}
	if value := a.node.Load(); value != nil {
		if node, ok := value.(string); ok && strings.TrimSpace(node) != "" {
			return node
		}
	}
	return "local"
}

// ObserveRequest 只计数，不读取请求的任何内容。
func (a *Aggregator) ObserveRequest() {
	if a == nil {
		return
	}
	now := a.currentTime()
	minute := now.Unix() / 60
	slot := &a.requestSlots[positiveModulo(minute, int64(len(a.requestSlots)))]
	if slot.minute.Load() != minute {
		slot.mu.Lock()
		if slot.minute.Load() != minute {
			slot.count.Store(0)
			slot.minute.Store(minute)
		}
		slot.mu.Unlock()
	}
	slot.count.Add(1)
	a.requestsTotal.Add(1)
	a.lastRequestNanos.Store(now.UnixNano())
}

func (a *Aggregator) Record(reason RejectionReason) {
	if a == nil || !isKnownReason(reason) {
		return
	}
	now := a.currentTime()
	minute := now.Unix() / 60
	a.rejectionsTotal.Add(1)

	a.mu.Lock()
	bucket := a.buckets[minute]
	if bucket == nil {
		bucket = &minuteBucket{minute: minute, counts: make(map[RejectionReason]uint64)}
		a.buckets[minute] = bucket
	}
	bucket.counts[reason]++
	if len(a.buckets) > defaultRetentionMinutes {
		cutoff := minute - defaultRetentionMinutes + 1
		for key := range a.buckets {
			if key < cutoff {
				delete(a.buckets, key)
			}
		}
	}
	a.mu.Unlock()
}

func (a *Aggregator) Snapshot(minutes int) Snapshot {
	if a == nil {
		a = NewAggregator()
	}
	if minutes <= 0 {
		minutes = defaultSnapshotMinutes
	}
	if minutes > defaultRetentionMinutes {
		minutes = defaultRetentionMinutes
	}
	now := a.currentTime().UTC()
	endMinute := now.Unix() / 60
	startMinute := endMinute - int64(minutes) + 1

	reasonTotals := make(map[RejectionReason]uint64, len(knownReasons))
	trendByMinute := make(map[int64]TrendPoint)
	a.mu.Lock()
	for minute, bucket := range a.buckets {
		if minute < startMinute || minute > endMinute || bucket == nil {
			continue
		}
		point := TrendPoint{
			BucketStart: time.Unix(minute*60, 0).UTC(),
			Counts:      make(map[RejectionReason]uint64, len(bucket.counts)),
		}
		for reason, count := range bucket.counts {
			point.Counts[reason] = count
			point.Total += count
			reasonTotals[reason] += count
		}
		trendByMinute[minute] = point
	}
	a.mu.Unlock()

	trend := make([]TrendPoint, 0, len(trendByMinute))
	var rejectionCount uint64
	for minute := startMinute; minute <= endMinute; minute++ {
		point, ok := trendByMinute[minute]
		if !ok {
			point = TrendPoint{
				BucketStart: time.Unix(minute*60, 0).UTC(),
				Counts:      map[RejectionReason]uint64{},
			}
		}
		trend = append(trend, point)
		rejectionCount += point.Total
	}
	top := make([]ReasonCount, 0, len(reasonTotals))
	for _, reason := range knownReasons {
		if count := reasonTotals[reason]; count > 0 {
			top = append(top, ReasonCount{Reason: reason, Count: count})
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Count == top[j].Count {
			return top[i].Reason < top[j].Reason
		}
		return top[i].Count > top[j].Count
	})

	requestCount := a.requestsInWindow(startMinute, endMinute)
	return Snapshot{
		Node:                 a.nodeName(),
		WindowMinutes:        minutes,
		WindowStart:          time.Unix(startMinute*60, 0).UTC(),
		WindowEnd:            now,
		RequestCount:         requestCount,
		RejectionCount:       rejectionCount,
		RejectionRatePercent: percent(rejectionCount, requestCount),
		TopReasons:           top,
		Trend:                trend,
	}
}

func (a *Aggregator) Health() Health {
	if a == nil {
		a = NewAggregator()
	}
	requests := a.requestsTotal.Load()
	rejections := a.rejectionsTotal.Load()
	health := Health{
		Node:                  a.nodeName(),
		Status:                "healthy",
		RetentionMinutes:      defaultRetentionMinutes,
		RequestsTotal:         requests,
		RejectionsTotal:       rejections,
		RejectionRatePercent:  percent(rejections, requests),
		SensitiveFieldsStored: false,
	}
	if nanos := a.lastRequestNanos.Load(); nanos > 0 {
		health.LastRequestObservedAt = time.Unix(0, nanos).UTC()
	}
	return health
}

func (a *Aggregator) RuntimeMetrics() map[string]any {
	health := a.Health()
	recent := a.Snapshot(5)
	metrics := map[string]any{
		"ingress_requests_total":                  health.RequestsTotal,
		"ingress_rejections_total":                health.RejectionsTotal,
		"ingress_rejection_rate_percent":          health.RejectionRatePercent,
		"ingress_rejections_5m_total":             recent.RejectionCount,
		"ingress_rejection_sensitive_data_stored": false,
	}
	// 健康上报只需要 24 小时分类总数。不能调用 Snapshot(1440)，否则每
	// 15 秒上报一次就会白白构造 1440 个分钟趋势 map，制造持续 GC 压力。
	for _, item := range a.reasonCounts(defaultRetentionMinutes) {
		metrics["ingress_rejection_"+string(item.Reason)+"_total"] = item.Count
	}
	return metrics
}

// reasonCounts 只聚合分类总数，不构造 TrendPoint（分钟趋势）。管理页面仍
// 使用 Snapshot；runtime health（运行健康）走这个轻量路径。
func (a *Aggregator) reasonCounts(minutes int) []ReasonCount {
	if a == nil {
		return nil
	}
	if minutes <= 0 {
		minutes = defaultSnapshotMinutes
	}
	if minutes > defaultRetentionMinutes {
		minutes = defaultRetentionMinutes
	}
	endMinute := a.currentTime().UTC().Unix() / 60
	startMinute := endMinute - int64(minutes) + 1
	totals := make(map[RejectionReason]uint64, len(knownReasons))
	a.mu.Lock()
	for minute, bucket := range a.buckets {
		if minute < startMinute || minute > endMinute || bucket == nil {
			continue
		}
		for reason, count := range bucket.counts {
			totals[reason] += count
		}
	}
	a.mu.Unlock()

	out := make([]ReasonCount, 0, len(totals))
	for _, reason := range knownReasons {
		if count := totals[reason]; count > 0 {
			out = append(out, ReasonCount{Reason: reason, Count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Count > out[j].Count
	})
	return out
}

func (a *Aggregator) requestsInWindow(startMinute, endMinute int64) uint64 {
	var total uint64
	for i := range a.requestSlots {
		slot := &a.requestSlots[i]
		minute := slot.minute.Load()
		if minute >= startMinute && minute <= endMinute {
			total += slot.count.Load()
		}
	}
	return total
}

func (a *Aggregator) currentTime() time.Time {
	if a != nil && a.now != nil {
		return a.now()
	}
	return time.Now()
}

func percent(numerator, denominator uint64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) * 100 / float64(denominator)
}

func positiveModulo(value, divisor int64) int64 {
	result := value % divisor
	if result < 0 {
		result += divisor
	}
	return result
}

func isKnownReason(reason RejectionReason) bool {
	for _, candidate := range knownReasons {
		if reason == candidate {
			return true
		}
	}
	return false
}

var defaultAggregator = NewAggregator()

func Default() *Aggregator { return defaultAggregator }

func ObserveRequest() { defaultAggregator.ObserveRequest() }

func Record(reason RejectionReason) { defaultAggregator.Record(reason) }

func SetNode(node string) { defaultAggregator.SetNode(node) }

func CurrentSnapshot(minutes int) Snapshot { return defaultAggregator.Snapshot(minutes) }

func CurrentHealth() Health { return defaultAggregator.Health() }

func RuntimeMetrics() map[string]any { return defaultAggregator.RuntimeMetrics() }
