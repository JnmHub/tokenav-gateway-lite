// Package servertiming provides a deliberately small, request-scoped timing
// collector for gateway diagnostics. Only fixed public stage names can reach a
// response header; database names, account IDs, hosts and arbitrary labels are
// never accepted.
//
// [tokenav 定制] 保留本地实现，官方 v0.2.5 对应改动见 commit 说明。
// 本文件的固定 stage（auth/quota/route/firstbyte/settle）+ FinalHeaderName +
// Add/Set + Controller 采样控制器是 tokenav 轻量网关中间件的对外契约，
// 被 internal/server/middleware/gateway_server_timing*.go 与
// internal/service/ops_upstream_context.go 直接引用，必须保持。
// 官方 v0.2.5 的 servertiming 走的是另一套 metrics/dep_ 通用埋点模型
// （Record/ObserveDependency/WrapRoundTripper），两者在同一 package 内共存：
// 我方提供固定的网关阶段语义，官方提供通用埋点与出站依赖计时。
package servertiming

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	HeaderName      = "Server-Timing"
	FinalHeaderName = "Server-Timing-Final"

	// [tokenav 定制] 固定网关阶段名：只有这些名字允许进入响应头。
	StageAuth      = "auth"
	StageQuota     = "quota"
	StageRoute     = "route"
	StageFirstByte = "firstbyte"
	StageSettle    = "settle"

	maxSampleBasisPoints = 10_000
	maxTemporaryDuration = time.Hour
)

var stageOrder = [...]string{StageAuth, StageQuota, StageRoute, StageFirstByte, StageSettle}

// [tokenav 定制] contextKey 由本地实现提供（官方 collector.go 中同名类型已删除）。
type contextKey struct{}

// [tokenav 定制] 我方 Collector 只存聚合时长；官方 metrics 字段由 collector.go
// 中的官方实现单独维护，二者通过 metrics 字段桥接（见本文件末尾）。
type Collector struct {
	startedAt time.Time

	mu            sync.Mutex
	durations     map[string]time.Duration
	recorded      map[string]bool
	upstreamStart time.Time
	firstByteOnce sync.Once

	// [tokenav 定制] 官方 v0.2.5 通用埋点所需的 metrics 存储。
	metrics     map[string]*metric
	cacheStatus string
}

func New(startedAt time.Time) *Collector {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &Collector{
		startedAt: startedAt,
		durations: make(map[string]time.Duration, len(stageOrder)),
		recorded:  make(map[string]bool, len(stageOrder)),
		metrics:   make(map[string]*metric),
	}
}

func WithCollector(ctx context.Context, collector *Collector) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if collector == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, collector)
}

func FromContext(ctx context.Context) (*Collector, bool) {
	if ctx == nil {
		return nil, false
	}
	collector, ok := ctx.Value(contextKey{}).(*Collector)
	return collector, ok && collector != nil
}

func Active(ctx context.Context) bool {
	_, ok := FromContext(ctx)
	return ok
}

// Add 累加某个网关阶段的耗时；仅接受固定 stage 名，防止任意标签进入响应头。
func Add(ctx context.Context, stage string, duration time.Duration) {
	if collector, ok := FromContext(ctx); ok {
		collector.Add(stage, duration)
	}
}

// Set 覆盖某个网关阶段的耗时（用于 firstbyte 这类只能记一次的阶段）。
func Set(ctx context.Context, stage string, duration time.Duration) {
	if collector, ok := FromContext(ctx); ok {
		collector.Set(stage, duration)
	}
}

func Observe(ctx context.Context, stage string) func() {
	if !validStage(stage) || !Active(ctx) {
		return func() {}
	}
	startedAt := time.Now()
	var once sync.Once
	return func() {
		once.Do(func() { Add(ctx, stage, time.Since(startedAt)) })
	}
}

func (c *Collector) Add(stage string, duration time.Duration) {
	if c == nil || !validStage(stage) || duration < 0 {
		return
	}
	c.mu.Lock()
	c.durations[stage] += duration
	c.recorded[stage] = true
	c.mu.Unlock()
}

func (c *Collector) Set(stage string, duration time.Duration) {
	if c == nil || !validStage(stage) || duration < 0 {
		return
	}
	c.mu.Lock()
	c.durations[stage] = duration
	c.recorded[stage] = true
	c.mu.Unlock()
}

// MarkUpstreamStart records the safe boundary after routing and immediately
// before the upstream call. It is never rendered as a separate internal name.
func MarkUpstreamStart(ctx context.Context) {
	collector, ok := FromContext(ctx)
	if !ok {
		return
	}
	collector.mu.Lock()
	collector.upstreamStart = time.Now()
	collector.mu.Unlock()
}

// RecordFirstByte converts the upstream boundary into the public firstbyte
// stage exactly once, immediately before the first client response write.
func RecordFirstByte(ctx context.Context, at time.Time) {
	collector, ok := FromContext(ctx)
	if !ok {
		return
	}
	collector.firstByteOnce.Do(func() {
		collector.mu.Lock()
		if collector.recorded[StageFirstByte] {
			collector.mu.Unlock()
			return
		}
		startedAt := collector.upstreamStart
		if !startedAt.IsZero() && !at.Before(startedAt) {
			collector.durations[StageFirstByte] = at.Sub(startedAt)
			collector.recorded[StageFirstByte] = true
		}
		collector.mu.Unlock()
	})
}

// HeaderValue 渲染 tokenav 网关的 Server-Timing 响应头：total + 已记录的固定
// 阶段。仅固定 stage 名可能出现在输出中，任意标签无法注入。
func HeaderValue(ctx context.Context, endedAt time.Time) string {
	collector, ok := FromContext(ctx)
	if !ok {
		return ""
	}
	return collector.HeaderValue(endedAt)
}

// HeaderValue 渲染 tokenav 网关的 Server-Timing 响应头。
// [tokenav 定制] 保留本地实现，官方 v0.2.5 对应改动见 commit 说明。
// 官方同功能方法为 HeaderValueWithCache(endedAt, cacheStatus)。
func (c *Collector) HeaderValue(endedAt time.Time) string {
	if c == nil {
		return ""
	}
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	if endedAt.Before(c.startedAt) {
		endedAt = c.startedAt
	}
	c.mu.Lock()
	durations := make(map[string]time.Duration, len(c.durations))
	recorded := make(map[string]bool, len(c.recorded))
	for key, value := range c.durations {
		durations[key] = value
	}
	for key, value := range c.recorded {
		recorded[key] = value
	}
	c.mu.Unlock()

	parts := []string{"total;dur=" + formatDuration(endedAt.Sub(c.startedAt))}
	for _, stage := range stageOrder {
		if recorded[stage] {
			parts = append(parts, stage+";dur="+formatDuration(durations[stage]))
		}
	}
	return strings.Join(parts, ", ")
}

func validStage(stage string) bool {
	for _, candidate := range stageOrder {
		if stage == candidate {
			return true
		}
	}
	return false
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	return strconv.FormatFloat(float64(value)/float64(time.Millisecond), 'f', 1, 64)
}

type ControllerStatus struct {
	SampleBasisPoints int        `json:"sample_basis_points"`
	SamplePercent     float64    `json:"sample_percent"`
	TemporaryEnabled  bool       `json:"temporary_enabled"`
	TemporaryUntil    *time.Time `json:"temporary_until,omitempty"`
	CollectedTotal    uint64     `json:"collected_total"`
}

// Controller makes the disabled path two atomic reads. Sampling uses a
// deterministic sequence so it needs no global random-number lock.
type Controller struct {
	sampleBasisPoints atomic.Int64
	temporaryUntilNS  atomic.Int64
	sequence          atomic.Uint64
	collected         atomic.Uint64
}

func NewController(sampleBasisPoints int) *Controller {
	controller := &Controller{}
	controller.SetSampleBasisPoints(sampleBasisPoints)
	return controller
}

func (c *Controller) SetSampleBasisPoints(value int) {
	if c == nil {
		return
	}
	if value < 0 {
		value = 0
	}
	if value > maxSampleBasisPoints {
		value = maxSampleBasisPoints
	}
	c.sampleBasisPoints.Store(int64(value))
}

func (c *Controller) ShouldCollect(now time.Time) bool {
	if c == nil {
		return false
	}
	if until := c.temporaryUntilNS.Load(); until > now.UnixNano() {
		c.collected.Add(1)
		return true
	}
	basisPoints := c.sampleBasisPoints.Load()
	if basisPoints <= 0 {
		return false
	}
	if basisPoints >= maxSampleBasisPoints || int64(c.sequence.Add(1)%maxSampleBasisPoints) < basisPoints {
		c.collected.Add(1)
		return true
	}
	return false
}

func (c *Controller) EnableTemporary(now time.Time, duration time.Duration) time.Time {
	if c == nil {
		return time.Time{}
	}
	if duration <= 0 {
		duration = 15 * time.Minute
	}
	if duration > maxTemporaryDuration {
		duration = maxTemporaryDuration
	}
	until := now.Add(duration).UTC()
	c.temporaryUntilNS.Store(until.UnixNano())
	return until
}

func (c *Controller) DisableTemporary() {
	if c != nil {
		c.temporaryUntilNS.Store(0)
	}
}

func (c *Controller) Status(now time.Time) ControllerStatus {
	if c == nil {
		return ControllerStatus{}
	}
	basisPoints := int(c.sampleBasisPoints.Load())
	status := ControllerStatus{
		SampleBasisPoints: basisPoints,
		SamplePercent:     float64(basisPoints) / 100,
		CollectedTotal:    c.collected.Load(),
	}
	if untilNS := c.temporaryUntilNS.Load(); untilNS > now.UnixNano() {
		until := time.Unix(0, untilNS).UTC()
		status.TemporaryEnabled = true
		status.TemporaryUntil = &until
	}
	return status
}

var defaultController = NewController(0)

func DefaultController() *Controller { return defaultController }
