package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

// ExecutionPricingSnapshot 只保存非秘密计价材料，不包含账号凭据、资金控制器或请求正文。
// 派发上界与完成结算使用同一份执行价，避免管理改价或重启后重新取价突破原授权。
type ExecutionPricingSnapshot struct {
	Version                   int                          `json:"version"`
	Model                     string                       `json:"model"`
	PricingAt                 time.Time                    `json:"pricing_at"`
	RateMultiplier            float64                      `json:"rate_multiplier"`
	RatePolicyAudit           *gatewaylite.RatePolicyAudit `json:"rate_policy_audit,omitempty"`
	ServiceTier               string                       `json:"service_tier,omitempty"`
	ReasoningEffort           string                       `json:"reasoning_effort,omitempty"`
	LongContextBillingEnabled *bool                        `json:"long_context_billing_enabled,omitempty"`
	LongContextGroupEnabled   *bool                        `json:"long_context_group_enabled,omitempty"`
	Resolved                  *ResolvedPricingSnapshot     `json:"resolved"`
}

// ResolvedPricingSnapshot 显式导出解析结果的私有字段，普通 JSON(ResolvedPricing)
// 会丢失渠道分时倍率和长上下文开关，不能作为异步恢复材料。
type ResolvedPricingSnapshot struct {
	Mode                      BillingMode          `json:"mode"`
	BasePricing               *ModelPricing        `json:"base_pricing,omitempty"`
	Intervals                 []PricingInterval    `json:"intervals,omitempty"`
	RequestTiers              []PricingInterval    `json:"request_tiers,omitempty"`
	DefaultPerRequestPrice    float64              `json:"default_per_request_price"`
	Source                    string               `json:"source"`
	SupportsCacheBreakdown    bool                 `json:"supports_cache_breakdown"`
	ChannelPricing            *ChannelModelPricing `json:"channel_pricing,omitempty"`
	LongContextPricingEnabled bool                 `json:"long_context_pricing_enabled"`
}

func copyPricingValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneModelPricing(pricing *ModelPricing) *ModelPricing {
	if pricing == nil {
		return nil
	}
	copy := *pricing
	copy.FastMultiplier = copyPricingValue(pricing.FastMultiplier)
	copy.FlexMultiplier = copyPricingValue(pricing.FlexMultiplier)
	copy.MaxReasoningEffortMultiplier = copyPricingValue(pricing.MaxReasoningEffortMultiplier)
	return &copy
}

func clonePricingIntervals(intervals []PricingInterval) []PricingInterval {
	if intervals == nil {
		return nil
	}
	copy := append([]PricingInterval{}, intervals...)
	for i := range copy {
		p := &copy[i]
		p.MaxTokens = copyPricingValue(p.MaxTokens)
		p.InputPrice = copyPricingValue(p.InputPrice)
		p.OutputPrice = copyPricingValue(p.OutputPrice)
		p.CacheWritePrice = copyPricingValue(p.CacheWritePrice)
		p.CacheWrite1hPrice = copyPricingValue(p.CacheWrite1hPrice)
		p.CacheReadPrice = copyPricingValue(p.CacheReadPrice)
		p.InputMultiplier = copyPricingValue(p.InputMultiplier)
		p.OutputMultiplier = copyPricingValue(p.OutputMultiplier)
		p.CacheWriteMultiplier = copyPricingValue(p.CacheWriteMultiplier)
		p.CacheReadMultiplier = copyPricingValue(p.CacheReadMultiplier)
		p.PerRequestPrice = copyPricingValue(p.PerRequestPrice)
	}
	return copy
}

func cloneExecutionChannelPricing(pricing *ChannelModelPricing) *ChannelModelPricing {
	if pricing == nil {
		return nil
	}
	copy := *pricing
	copy.Models = append([]string(nil), pricing.Models...)
	copy.Intervals = clonePricingIntervals(pricing.Intervals)
	copy.InputPrice = copyPricingValue(pricing.InputPrice)
	copy.OutputPrice = copyPricingValue(pricing.OutputPrice)
	copy.CacheWritePrice = copyPricingValue(pricing.CacheWritePrice)
	copy.CacheWrite1hPrice = copyPricingValue(pricing.CacheWrite1hPrice)
	copy.CacheReadPrice = copyPricingValue(pricing.CacheReadPrice)
	copy.FastMultiplier = copyPricingValue(pricing.FastMultiplier)
	copy.FlexMultiplier = copyPricingValue(pricing.FlexMultiplier)
	copy.MaxReasoningEffortMultiplier = copyPricingValue(pricing.MaxReasoningEffortMultiplier)
	copy.ImageInputPrice = copyPricingValue(pricing.ImageInputPrice)
	copy.ImageOutputPrice = copyPricingValue(pricing.ImageOutputPrice)
	copy.PerRequestPrice = copyPricingValue(pricing.PerRequestPrice)
	if pricing.TimePricing != nil {
		copy.TimePricing = copyPricingValue(pricing.TimePricing)
		copy.TimePricing.Periods = append([]ChannelTimePricingPeriod(nil), pricing.TimePricing.Periods...)
	}
	return &copy
}

// CloneResolvedPricing 包括所有价格/阈值/倍率指针与渠道私有字段，不能共享管理员可改的对象。
func CloneResolvedPricing(pricing *ResolvedPricing) *ResolvedPricing {
	if pricing == nil {
		return nil
	}
	copy := *pricing
	copy.BasePricing = cloneModelPricing(pricing.BasePricing)
	copy.Intervals = clonePricingIntervals(pricing.Intervals)
	copy.RequestTiers = clonePricingIntervals(pricing.RequestTiers)
	copy.channelPricing = cloneExecutionChannelPricing(pricing.channelPricing)
	return &copy
}

func snapshotResolvedPricing(pricing *ResolvedPricing) *ResolvedPricingSnapshot {
	copy := CloneResolvedPricing(pricing)
	if copy == nil {
		return nil
	}
	return &ResolvedPricingSnapshot{Mode: copy.Mode, BasePricing: copy.BasePricing, Intervals: copy.Intervals,
		RequestTiers: copy.RequestTiers, DefaultPerRequestPrice: copy.DefaultPerRequestPrice,
		Source: copy.Source, SupportsCacheBreakdown: copy.SupportsCacheBreakdown,
		ChannelPricing: copy.channelPricing, LongContextPricingEnabled: copy.longContextPricingEnabled}
}

func (s *ResolvedPricingSnapshot) Restore() *ResolvedPricing {
	if s == nil {
		return nil
	}
	return CloneResolvedPricing(&ResolvedPricing{Mode: s.Mode, BasePricing: s.BasePricing, Intervals: s.Intervals,
		RequestTiers: s.RequestTiers, DefaultPerRequestPrice: s.DefaultPerRequestPrice,
		Source: s.Source, SupportsCacheBreakdown: s.SupportsCacheBreakdown,
		channelPricing: s.ChannelPricing, longContextPricingEnabled: s.LongContextPricingEnabled})
}

func (s *ExecutionPricingSnapshot) Clone() *ExecutionPricingSnapshot {
	if s == nil {
		return nil
	}
	copy := *s
	if s.RatePolicyAudit != nil {
		policy := s.RatePolicyAudit.Clone()
		copy.RatePolicyAudit = &policy
	}
	copy.LongContextBillingEnabled = copyPricingValue(s.LongContextBillingEnabled)
	copy.LongContextGroupEnabled = copyPricingValue(s.LongContextGroupEnabled)
	copy.Resolved = snapshotResolvedPricing(s.Resolved.Restore())
	return &copy
}

// FreezeExecutionPricing 冻结最终派发时解析的价格；初次准入只能估算，不能写入最终执行仓。
// 完成时只补计量值，派发后的单价、档位、时点与倍率不得回读或通过扩大授权补救。
func FreezeExecutionPricing(input CostInput) (*ExecutionPricingSnapshot, error) {
	if strings.TrimSpace(input.Model) == "" || !finiteNonNegative(input.RateMultiplier) {
		return nil, errors.New("execution pricing model or multiplier is invalid")
	}
	resolved := input.Resolved
	if resolved == nil && input.Resolver != nil {
		resolved = input.Resolver.Resolve(input.Ctx, PricingInput{Model: input.Model, GroupID: input.GroupID, Group: input.Group})
	}
	if resolved == nil || ((resolved.Mode == "" || resolved.Mode == BillingModeToken) && resolved.BasePricing == nil && len(resolved.Intervals) == 0) {
		return nil, ErrModelPricingUnavailable
	}
	at := input.PricingAt
	if at.IsZero() {
		at = time.Now()
	}
	copy := CloneResolvedPricing(resolved)
	if input.LongContextGroupEnabled != nil {
		copy.longContextPricingEnabled = *input.LongContextGroupEnabled
	}
	var policy *gatewaylite.RatePolicyAudit
	if input.Ctx != nil {
		if current, ok := EffectiveRatePolicyFromContext(input.Ctx); ok {
			cloned := current.Clone()
			policy = &cloned
		}
	}
	snapshot := &ExecutionPricingSnapshot{Version: 1, Model: strings.TrimSpace(input.Model), PricingAt: at,
		RateMultiplier: input.RateMultiplier, RatePolicyAudit: policy, ServiceTier: input.ServiceTier, ReasoningEffort: input.ReasoningEffort,
		LongContextBillingEnabled: copyPricingValue(input.LongContextBillingEnabled),
		LongContextGroupEnabled:   copyPricingValue(input.LongContextGroupEnabled), Resolved: snapshotResolvedPricing(copy)}
	if _, err := snapshot.ApplyToCostInput(CostInput{}); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// validateExecutionResolvedPricing 拒绝损坏恢复材料，零价仍是合法的显式免费配置。
func validateExecutionResolvedPricing(resolved *ResolvedPricingSnapshot) error {
	if resolved == nil || !resolved.Mode.IsValid() || !finiteNonNegative(resolved.DefaultPerRequestPrice) {
		return ErrModelPricingUnavailable
	}
	if (resolved.Mode == "" || resolved.Mode == BillingModeToken) && resolved.BasePricing == nil && len(resolved.Intervals) == 0 {
		return ErrModelPricingUnavailable
	}
	if p := resolved.BasePricing; p != nil {
		values := []float64{p.InputPricePerToken, p.InputPricePerTokenPriority, p.ImageInputPricePerToken,
			p.ImageCacheReadPricePerToken, p.OutputPricePerToken, p.OutputPricePerTokenPriority,
			p.CacheCreationPricePerToken, p.CacheCreationPricePerTokenPriority, p.CacheReadPricePerToken,
			p.CacheReadPricePerTokenPriority, p.CacheCreation5mPrice, p.CacheCreation1hPrice,
			p.LongContextInputMultiplier, p.LongContextOutputMultiplier, p.ImageOutputPricePerToken,
			p.LongContextInputPricePerToken, p.LongContextInputPricePerTokenPriority,
			p.LongContextOutputPricePerToken, p.LongContextOutputPricePerTokenPriority,
			p.LongContextCacheCreationPricePerToken, p.LongContextCacheCreationPricePerTokenPriority,
			p.LongContextCacheReadPricePerToken, p.LongContextCacheReadPricePerTokenPriority}
		if p.LongContextInputThreshold < 0 || !executionPricingValuesValid(values) ||
			!executionPricingPointersValid(p.FastMultiplier, p.FlexMultiplier, p.MaxReasoningEffortMultiplier) {
			return ErrModelPricingUnavailable
		}
	}
	if err := validateExecutionPricingIntervals(resolved.Intervals, BillingModeToken); err != nil {
		return err
	}
	if err := validateExecutionPricingIntervals(resolved.RequestTiers, resolved.Mode); err != nil {
		return err
	}
	if p := resolved.ChannelPricing; p != nil {
		if !p.BillingMode.IsValid() || !executionPricingPointersValid(p.InputPrice, p.OutputPrice,
			p.CacheWritePrice, p.CacheWrite1hPrice, p.CacheReadPrice, p.FastMultiplier,
			p.FlexMultiplier, p.MaxReasoningEffortMultiplier, p.ImageInputPrice, p.ImageOutputPrice, p.PerRequestPrice) {
			return ErrModelPricingUnavailable
		}
		if err := validateExecutionPricingIntervals(p.Intervals, p.BillingMode); err != nil {
			return err
		}
		if err := validateChannelTimePricing(p.TimePricing); err != nil {
			return errors.Join(ErrModelPricingUnavailable, err)
		}
	}
	return nil
}

func executionPricingValuesValid(values []float64) bool {
	for _, value := range values {
		if !finiteNonNegative(value) {
			return false
		}
	}
	return true
}

func executionPricingPointersValid(values ...*float64) bool {
	for _, value := range values {
		if value != nil && !finiteNonNegative(*value) {
			return false
		}
	}
	return true
}

func validateExecutionPricingIntervals(intervals []PricingInterval, mode BillingMode) error {
	if err := ValidateIntervals(intervals, mode); err != nil {
		return errors.Join(ErrModelPricingUnavailable, err)
	}
	for _, p := range intervals {
		if !executionPricingPointersValid(p.InputPrice, p.OutputPrice, p.CacheWritePrice, p.CacheWrite1hPrice,
			p.CacheReadPrice, p.InputMultiplier, p.OutputMultiplier, p.CacheWriteMultiplier,
			p.CacheReadMultiplier, p.PerRequestPrice) {
			return ErrModelPricingUnavailable
		}
	}
	return nil
}

// ApplyToCostInput 保留完成时的真实用量，只恢复原执行计价材料；无有效快照时明确失败。
func (s *ExecutionPricingSnapshot) ApplyToCostInput(input CostInput) (CostInput, error) {
	if s == nil || s.Version != 1 || s.Resolved == nil || s.PricingAt.IsZero() || !finiteNonNegative(s.RateMultiplier) || strings.TrimSpace(s.Model) == "" {
		return input, errors.New("execution pricing snapshot is unavailable or invalid")
	}
	if input.Model != "" && !strings.EqualFold(strings.TrimSpace(input.Model), s.Model) {
		return input, errors.New("execution pricing snapshot model mismatch")
	}
	if err := validateExecutionResolvedPricing(s.Resolved); err != nil {
		return input, err
	}
	if policy := s.RatePolicyAudit; policy != nil {
		delta := math.Abs(policy.EffectiveRateMultiplier - s.RateMultiplier)
		if !policy.Valid() || delta > math.Max(1, math.Abs(s.RateMultiplier))*1e-12 {
			return input, errors.New("execution pricing rate policy is invalid or inconsistent")
		}
	}
	input.Model, input.PricingAt, input.RateMultiplier = s.Model, s.PricingAt, s.RateMultiplier
	input.ServiceTier, input.ReasoningEffort = s.ServiceTier, s.ReasoningEffort
	input.LongContextBillingEnabled = copyPricingValue(s.LongContextBillingEnabled)
	input.LongContextGroupEnabled = copyPricingValue(s.LongContextGroupEnabled)
	input.Resolved = s.Resolved.Restore()
	return input, nil
}

type executionPricingContextKey struct{}
type executionPricingStore struct {
	mu        sync.RWMutex
	snapshots map[string]*ExecutionPricingSnapshot
}

// 仓对象可以在准入阶段安装，但只保存最终派发价；子上下文共享线程安全仓，读写均使用深副本。
func ContextWithExecutionPricing(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(executionPricingContextKey{}).(*executionPricingStore); ok {
		return ctx
	}
	return context.WithValue(ctx, executionPricingContextKey{}, &executionPricingStore{snapshots: make(map[string]*ExecutionPricingSnapshot)})
}

// ContextWithNewExecutionPricing 为独立消费轮次安装新仓，不能继承连接前一轮的同模型价格。
func ContextWithNewExecutionPricing(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, executionPricingContextKey{}, &executionPricingStore{snapshots: make(map[string]*ExecutionPricingSnapshot)})
}

// ContextWithExecutionPricingFromContext 将执行材料复制到后台记账上下文，不复制资金控制器或执行权限。
// 后台使用独立仓，避免父请求后续重路由/写入影响已经提交的记账工作。
func ContextWithExecutionPricingFromContext(base, parent context.Context) context.Context {
	if base == nil {
		base = context.Background()
	}
	copied := &executionPricingStore{snapshots: make(map[string]*ExecutionPricingSnapshot)}
	if parent != nil {
		if original, ok := parent.Value(executionPricingContextKey{}).(*executionPricingStore); ok && original != nil {
			original.mu.RLock()
			for model, snapshot := range original.snapshots {
				copied.snapshots[model] = snapshot.Clone()
			}
			original.mu.RUnlock()
		}
	}
	base = context.WithValue(base, executionPricingContextKey{}, copied)
	if policy, ok := EffectiveRatePolicyFromContext(parent); ok {
		base = ContextWithEffectiveRatePolicy(base, policy.Clone())
	}
	return base
}

func StoreExecutionPricingSnapshot(ctx context.Context, snapshot *ExecutionPricingSnapshot) error {
	if _, err := snapshot.ApplyToCostInput(CostInput{}); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("execution pricing context is unavailable")
	}
	store, ok := ctx.Value(executionPricingContextKey{}).(*executionPricingStore)
	if !ok || store == nil {
		return errors.New("execution pricing context is unavailable")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(snapshot.Model))
	if _, exists := store.snapshots[key]; !exists {
		// 同一模型首次最终派发的冻结值为准，初次准入不得占据这个位置。
		store.snapshots[key] = snapshot.Clone()
	}
	return nil
}

func ExecutionPricingSnapshotFromContext(ctx context.Context, model string) (*ExecutionPricingSnapshot, bool) {
	if ctx == nil {
		return nil, false
	}
	store, ok := ctx.Value(executionPricingContextKey{}).(*executionPricingStore)
	if !ok || store == nil {
		return nil, false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	snapshot := store.snapshots[strings.ToLower(strings.TrimSpace(model))]
	return snapshot.Clone(), snapshot != nil
}

func ContextWithExecutionPricingSnapshot(ctx context.Context, snapshot *ExecutionPricingSnapshot) context.Context {
	// 异步恢复使用独立仓，不能继承免费轮询或其他任务已经安装的执行价格。
	ctx = context.WithValue(ctx, executionPricingContextKey{}, &executionPricingStore{snapshots: make(map[string]*ExecutionPricingSnapshot)})
	_ = StoreExecutionPricingSnapshot(ctx, snapshot)
	if snapshot != nil && snapshot.RatePolicyAudit != nil && snapshot.RatePolicyAudit.Valid() {
		ctx = ContextWithEffectiveRatePolicy(ctx, snapshot.RatePolicyAudit.Clone())
	}
	return ctx
}
