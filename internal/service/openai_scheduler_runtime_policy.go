package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SettingKeyOpenAIAdvancedSchedulerEnabled     = "openai_advanced_scheduler_enabled"
	SettingKeyOpenAISchedulerPolicyRevision      = "openai_scheduler_policy_revision"
	SettingKeyOpenAISchedulerPolicyPreset        = "openai_scheduler_policy_preset"
	SettingKeyOpenAISchedulerStickySoftEnabled   = "openai_scheduler_sticky_soft_enabled"
	SettingKeyOpenAISchedulerTopK                = "openai_scheduler_top_k"
	SettingKeyOpenAISchedulerPreferSubscription  = "openai_scheduler_prefer_subscription"
	SettingKeyOpenAISchedulerPriorityWeight      = "openai_scheduler_priority_weight"
	SettingKeyOpenAISchedulerLoadWeight          = "openai_scheduler_load_weight"
	SettingKeyOpenAISchedulerQueueWeight         = "openai_scheduler_queue_weight"
	SettingKeyOpenAISchedulerErrorRateWeight     = "openai_scheduler_error_rate_weight"
	SettingKeyOpenAISchedulerTTFTWeight          = "openai_scheduler_ttft_weight"
	SettingKeyOpenAISchedulerSessionStickyWeight = "openai_scheduler_session_sticky_weight"
	SettingKeyOpenAISchedulerPolicyUpdatedAt     = "openai_scheduler_policy_updated_at"
	OpenAISchedulerPreviousResponseModeHard      = "hard"
	OpenAISchedulerPolicySourceStartup           = "startup_config"
	OpenAISchedulerPolicySourceDatabase          = "database_setting_and_startup_config"
	openAISchedulerRuntimePolicyCacheTTL         = 5 * time.Second
	openAISchedulerRuntimePolicyErrorTTL         = time.Second
	openAISchedulerRuntimePolicyDBTimeout        = 2 * time.Second
	maxOpenAISchedulerRuntimeTopK                = 1000
	defaultOpenAISchedulerRuntimeTopK            = 7
	OpenAISchedulerPolicyPresetSafeDefault       = "safe_default"
	OpenAISchedulerPolicyPresetConservative      = "conservative"
	OpenAISchedulerPolicyPresetBalanced          = "balanced"
	OpenAISchedulerPolicyPresetLowLatency        = "low_latency"
	OpenAISchedulerPolicyPresetCustom            = "custom"
)

var ErrOpenAISchedulerPolicyRevisionConflict = infraerrors.Conflict(
	"OPENAI_SCHEDULER_POLICY_REVISION_CONFLICT",
	"scheduler policy changed; refresh and retry",
)

// OpenAISchedulerRuntimePolicy 是一次请求冻结的不可变调度策略。固定字段明确
// previous_response_id 和销售计费永远不能被管理员降级成软规则。
type OpenAISchedulerRuntimePolicy struct {
	Revision                 int64     `json:"revision"`
	Preset                   string    `json:"preset"`
	AdvancedEnabled          bool      `json:"advanced_enabled"`
	StickySoftScoringEnabled bool      `json:"sticky_soft_scoring_enabled"`
	TopK                     int       `json:"top_k"`
	PreferSubscriptionPool   bool      `json:"prefer_subscription_pool"`
	PriorityWeight           float64   `json:"priority_weight"`
	LoadWeight               float64   `json:"load_weight"`
	QueueWeight              float64   `json:"queue_weight"`
	ErrorRateWeight          float64   `json:"error_rate_weight"`
	TTFTWeight               float64   `json:"ttft_weight"`
	PreferSoonestResetWeight float64   `json:"prefer_soonest_reset_weight"`
	QuotaHeadroomWeight      float64   `json:"quota_headroom_weight"`
	UpstreamCostEnabled      bool      `json:"upstream_cost_enabled"`
	UpstreamCostWeight       float64   `json:"upstream_cost_weight"`
	SessionStickyWeight      float64   `json:"session_sticky_weight"`
	PreviousResponseMode     string    `json:"previous_response_mode"`
	SalesBillingIsolated     bool      `json:"sales_billing_isolated"`
	UpdatedAt                time.Time `json:"updated_at"`
	Source                   string    `json:"source"`
}

// OpenAISchedulerRuntimePolicyUpdate 只开放白名单字段。只读硬约束不接收客户端输入。
type OpenAISchedulerRuntimePolicyUpdate struct {
	ExpectedRevision         int64   `json:"expected_revision"`
	Preset                   string  `json:"preset"`
	AdvancedEnabled          bool    `json:"advanced_enabled"`
	StickySoftScoringEnabled bool    `json:"sticky_soft_scoring_enabled"`
	TopK                     int     `json:"top_k"`
	PreferSubscriptionPool   bool    `json:"prefer_subscription_pool"`
	PriorityWeight           float64 `json:"priority_weight"`
	LoadWeight               float64 `json:"load_weight"`
	QueueWeight              float64 `json:"queue_weight"`
	ErrorRateWeight          float64 `json:"error_rate_weight"`
	TTFTWeight               float64 `json:"ttft_weight"`
	PreferSoonestResetWeight float64 `json:"prefer_soonest_reset_weight"`
	QuotaHeadroomWeight      float64 `json:"quota_headroom_weight"`
	UpstreamCostEnabled      bool    `json:"upstream_cost_enabled"`
	UpstreamCostWeight       float64 `json:"upstream_cost_weight"`
	SessionStickyWeight      float64 `json:"session_sticky_weight"`
}

type cachedOpenAISchedulerRuntimePolicy struct {
	policy    *OpenAISchedulerRuntimePolicy
	expiresAt int64
}

// OpenAISchedulerPolicyCASRepository 是生产仓储的可选扩展，避免给所有测试桩
// 扩大 SettingRepository 接口。生产保存路径没有非原子降级。
type OpenAISchedulerPolicyCASRepository interface {
	CompareAndSwapMultiple(ctx context.Context, revisionKey, expectedRevision, nextRevision string, values map[string]string) (bool, error)
}

func (s *SettingService) defaultOpenAISchedulerRuntimePolicy() OpenAISchedulerRuntimePolicy {
	policy := OpenAISchedulerRuntimePolicy{
		Preset:               OpenAISchedulerPolicyPresetSafeDefault,
		TopK:                 defaultOpenAISchedulerRuntimeTopK,
		PriorityWeight:       1,
		LoadWeight:           1,
		QueueWeight:          0.7,
		ErrorRateWeight:      0.8,
		TTFTWeight:           0.5,
		PreviousResponseMode: OpenAISchedulerPreviousResponseModeHard,
		SalesBillingIsolated: true,
		Source:               OpenAISchedulerPolicySourceStartup,
	}
	if s != nil && s.cfg != nil {
		ws := s.cfg.Gateway.OpenAIWS
		if ws.LBTopK > 0 {
			policy.TopK = ws.LBTopK
		}
		weights := ws.SchedulerScoreWeights
		if weights.Priority+weights.Load+weights.Queue+weights.ErrorRate+weights.TTFT > 0 {
			policy.PriorityWeight = weights.Priority
			policy.LoadWeight = weights.Load
			policy.QueueWeight = weights.Queue
			policy.ErrorRateWeight = weights.ErrorRate
			policy.TTFTWeight = weights.TTFT
		}
	}
	upstream := CurrentUpstreamBillingRuntimeSettings()
	policy.UpstreamCostEnabled = upstream.CostSchedulingEnabled
	policy.UpstreamCostWeight = upstream.UpstreamCostWeight
	policy.QuotaHeadroomWeight = upstream.QuotaHeadroomWeight
	policy.PreferSoonestResetWeight = upstream.PreferSoonestResetWeight
	return normalizeOpenAISchedulerRuntimePolicy(policy)
}

func normalizeOpenAISchedulerRuntimePolicy(policy OpenAISchedulerRuntimePolicy) OpenAISchedulerRuntimePolicy {
	if policy.Revision < 0 {
		policy.Revision = 0
	}
	policy.Preset = normalizeOpenAISchedulerPolicyPreset(policy.Preset)
	if policy.TopK <= 0 {
		policy.TopK = defaultOpenAISchedulerRuntimeTopK
	}
	if policy.TopK > maxOpenAISchedulerRuntimeTopK {
		policy.TopK = maxOpenAISchedulerRuntimeTopK
	}
	policy.PriorityWeight = normalizeSchedulerSoftWeight(policy.PriorityWeight)
	policy.LoadWeight = normalizeSchedulerSoftWeight(policy.LoadWeight)
	policy.QueueWeight = normalizeSchedulerSoftWeight(policy.QueueWeight)
	policy.ErrorRateWeight = normalizeSchedulerSoftWeight(policy.ErrorRateWeight)
	policy.TTFTWeight = normalizeSchedulerSoftWeight(policy.TTFTWeight)
	policy.PreferSoonestResetWeight = normalizeSchedulerSoftWeight(policy.PreferSoonestResetWeight)
	policy.QuotaHeadroomWeight = normalizeSchedulerSoftWeight(policy.QuotaHeadroomWeight)
	policy.UpstreamCostWeight = normalizeSchedulerSoftWeight(policy.UpstreamCostWeight)
	policy.SessionStickyWeight = normalizeSchedulerSoftWeight(policy.SessionStickyWeight)
	policy.PreviousResponseMode = OpenAISchedulerPreviousResponseModeHard
	policy.SalesBillingIsolated = true
	return policy
}

func normalizeOpenAISchedulerPolicyPreset(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case OpenAISchedulerPolicyPresetSafeDefault:
		return OpenAISchedulerPolicyPresetSafeDefault
	case OpenAISchedulerPolicyPresetConservative:
		return OpenAISchedulerPolicyPresetConservative
	case OpenAISchedulerPolicyPresetBalanced:
		return OpenAISchedulerPolicyPresetBalanced
	case OpenAISchedulerPolicyPresetLowLatency:
		return OpenAISchedulerPolicyPresetLowLatency
	default:
		return OpenAISchedulerPolicyPresetCustom
	}
}

func validateOpenAISchedulerRuntimePolicyUpdate(input OpenAISchedulerRuntimePolicyUpdate) error {
	if input.ExpectedRevision < 0 {
		return fmt.Errorf("expected_revision must be non-negative")
	}
	if input.TopK < 1 || input.TopK > maxOpenAISchedulerRuntimeTopK {
		return fmt.Errorf("top_k must be between 1 and %d", maxOpenAISchedulerRuntimeTopK)
	}
	weights := []struct {
		name  string
		value float64
	}{
		{"priority_weight", input.PriorityWeight},
		{"load_weight", input.LoadWeight},
		{"queue_weight", input.QueueWeight},
		{"error_rate_weight", input.ErrorRateWeight},
		{"ttft_weight", input.TTFTWeight},
		{"prefer_soonest_reset_weight", input.PreferSoonestResetWeight},
		{"quota_headroom_weight", input.QuotaHeadroomWeight},
		{"upstream_cost_weight", input.UpstreamCostWeight},
		{"session_sticky_weight", input.SessionStickyWeight},
	}
	total := 0.0
	for _, item := range weights {
		if math.IsNaN(item.value) || math.IsInf(item.value, 0) || item.value < 0 || item.value > maxOpenAISchedulerSoftWeight {
			return fmt.Errorf("%s must be a finite number between 0 and %.1f", item.name, maxOpenAISchedulerSoftWeight)
		}
		total += item.value
	}
	if input.AdvancedEnabled && total == 0 {
		return fmt.Errorf("at least one scheduler weight must be positive when advanced scheduling is enabled")
	}
	return nil
}

func openAISchedulerRuntimePolicyKeys() []string {
	return []string{
		SettingKeyOpenAIAdvancedSchedulerEnabled,
		SettingKeyOpenAISchedulerPolicyRevision,
		SettingKeyOpenAISchedulerPolicyPreset,
		SettingKeyOpenAISchedulerStickySoftEnabled,
		SettingKeyOpenAISchedulerTopK,
		SettingKeyOpenAISchedulerPreferSubscription,
		SettingKeyOpenAISchedulerPriorityWeight,
		SettingKeyOpenAISchedulerLoadWeight,
		SettingKeyOpenAISchedulerQueueWeight,
		SettingKeyOpenAISchedulerErrorRateWeight,
		SettingKeyOpenAISchedulerTTFTWeight,
		SettingKeyOpenAISchedulerSessionStickyWeight,
		SettingKeyOpenAISchedulerPolicyUpdatedAt,
		SettingKeyOpenAIUpstreamCostSchedulingEnabled,
		SettingKeyOpenAIUpstreamCostWeight,
		SettingKeyOpenAIQuotaHeadroomWeight,
		SettingKeyOpenAIPreferSoonestResetWeight,
	}
}

func decodeOpenAISchedulerRuntimePolicy(values map[string]string, defaults OpenAISchedulerRuntimePolicy) OpenAISchedulerRuntimePolicy {
	policy := defaults
	if len(values) == 0 {
		return normalizeOpenAISchedulerRuntimePolicy(policy)
	}
	policy.Source = OpenAISchedulerPolicySourceDatabase
	if value, ok := values[SettingKeyOpenAISchedulerPolicyRevision]; ok {
		if revision, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && revision >= 0 {
			policy.Revision = revision
		}
	}
	if value, ok := values[SettingKeyOpenAISchedulerPolicyPreset]; ok {
		policy.Preset = value
	}
	policy.AdvancedEnabled = parseRuntimeBool(values, SettingKeyOpenAIAdvancedSchedulerEnabled, policy.AdvancedEnabled)
	policy.StickySoftScoringEnabled = parseRuntimeBool(values, SettingKeyOpenAISchedulerStickySoftEnabled, policy.StickySoftScoringEnabled)
	policy.PreferSubscriptionPool = parseRuntimeBool(values, SettingKeyOpenAISchedulerPreferSubscription, policy.PreferSubscriptionPool)
	policy.UpstreamCostEnabled = parseRuntimeBool(values, SettingKeyOpenAIUpstreamCostSchedulingEnabled, policy.UpstreamCostEnabled)
	policy.TopK = parseRuntimeInt(values, SettingKeyOpenAISchedulerTopK, policy.TopK)
	policy.PriorityWeight = parseRuntimeFloat(values, SettingKeyOpenAISchedulerPriorityWeight, policy.PriorityWeight)
	policy.LoadWeight = parseRuntimeFloat(values, SettingKeyOpenAISchedulerLoadWeight, policy.LoadWeight)
	policy.QueueWeight = parseRuntimeFloat(values, SettingKeyOpenAISchedulerQueueWeight, policy.QueueWeight)
	policy.ErrorRateWeight = parseRuntimeFloat(values, SettingKeyOpenAISchedulerErrorRateWeight, policy.ErrorRateWeight)
	policy.TTFTWeight = parseRuntimeFloat(values, SettingKeyOpenAISchedulerTTFTWeight, policy.TTFTWeight)
	policy.SessionStickyWeight = parseRuntimeFloat(values, SettingKeyOpenAISchedulerSessionStickyWeight, policy.SessionStickyWeight)
	policy.UpstreamCostWeight = parseRuntimeFloat(values, SettingKeyOpenAIUpstreamCostWeight, policy.UpstreamCostWeight)
	policy.QuotaHeadroomWeight = parseRuntimeFloat(values, SettingKeyOpenAIQuotaHeadroomWeight, policy.QuotaHeadroomWeight)
	policy.PreferSoonestResetWeight = parseRuntimeFloat(values, SettingKeyOpenAIPreferSoonestResetWeight, policy.PreferSoonestResetWeight)
	if value, ok := values[SettingKeyOpenAISchedulerPolicyUpdatedAt]; ok {
		if updatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
			policy.UpdatedAt = updatedAt
		}
	}
	return normalizeOpenAISchedulerRuntimePolicy(policy)
}

func parseRuntimeBool(values map[string]string, key string, fallback bool) bool {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

func parseRuntimeInt(values map[string]string, key string, fallback int) int {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

func parseRuntimeFloat(values map[string]string, key string, fallback float64) float64 {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return value
}

func openAISchedulerRuntimePolicyValues(policy OpenAISchedulerRuntimePolicy) map[string]string {
	return map[string]string{
		SettingKeyOpenAIAdvancedSchedulerEnabled:      strconv.FormatBool(policy.AdvancedEnabled),
		SettingKeyOpenAISchedulerPolicyRevision:       strconv.FormatInt(policy.Revision, 10),
		SettingKeyOpenAISchedulerPolicyPreset:         policy.Preset,
		SettingKeyOpenAISchedulerStickySoftEnabled:    strconv.FormatBool(policy.StickySoftScoringEnabled),
		SettingKeyOpenAISchedulerTopK:                 strconv.Itoa(policy.TopK),
		SettingKeyOpenAISchedulerPreferSubscription:   strconv.FormatBool(policy.PreferSubscriptionPool),
		SettingKeyOpenAISchedulerPriorityWeight:       strconv.FormatFloat(policy.PriorityWeight, 'f', -1, 64),
		SettingKeyOpenAISchedulerLoadWeight:           strconv.FormatFloat(policy.LoadWeight, 'f', -1, 64),
		SettingKeyOpenAISchedulerQueueWeight:          strconv.FormatFloat(policy.QueueWeight, 'f', -1, 64),
		SettingKeyOpenAISchedulerErrorRateWeight:      strconv.FormatFloat(policy.ErrorRateWeight, 'f', -1, 64),
		SettingKeyOpenAISchedulerTTFTWeight:           strconv.FormatFloat(policy.TTFTWeight, 'f', -1, 64),
		SettingKeyOpenAISchedulerSessionStickyWeight:  strconv.FormatFloat(policy.SessionStickyWeight, 'f', -1, 64),
		SettingKeyOpenAISchedulerPolicyUpdatedAt:      policy.UpdatedAt.UTC().Format(time.RFC3339Nano),
		SettingKeyOpenAIUpstreamCostSchedulingEnabled: strconv.FormatBool(policy.UpstreamCostEnabled),
		SettingKeyOpenAIUpstreamCostWeight:            strconv.FormatFloat(policy.UpstreamCostWeight, 'f', -1, 64),
		SettingKeyOpenAIQuotaHeadroomWeight:           strconv.FormatFloat(policy.QuotaHeadroomWeight, 'f', -1, 64),
		SettingKeyOpenAIPreferSoonestResetWeight:      strconv.FormatFloat(policy.PreferSoonestResetWeight, 'f', -1, 64),
	}
}

func (s *SettingService) loadOpenAISchedulerRuntimePolicy(ctx context.Context) (OpenAISchedulerRuntimePolicy, error) {
	defaults := s.defaultOpenAISchedulerRuntimePolicy()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	values, err := s.settingRepo.GetMultiple(ctx, openAISchedulerRuntimePolicyKeys())
	if err != nil {
		return OpenAISchedulerRuntimePolicy{}, err
	}
	return decodeOpenAISchedulerRuntimePolicy(values, defaults), nil
}

func (s *SettingService) storeOpenAISchedulerRuntimePolicy(policy OpenAISchedulerRuntimePolicy, ttl time.Duration) *OpenAISchedulerRuntimePolicy {
	policy = normalizeOpenAISchedulerRuntimePolicy(policy)
	immutable := policy
	s.openAISchedulerRuntimePolicyCache.Store(&cachedOpenAISchedulerRuntimePolicy{
		policy:    &immutable,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
	return &immutable
}

func (s *SettingService) refreshOpenAISchedulerRuntimePolicy(ctx context.Context) (*OpenAISchedulerRuntimePolicy, error) {
	if s == nil {
		policy := (&SettingService{}).defaultOpenAISchedulerRuntimePolicy()
		return &policy, nil
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAISchedulerRuntimePolicyDBTimeout)
	defer cancel()
	policy, err := s.loadOpenAISchedulerRuntimePolicy(dbCtx)
	if err != nil {
		if cached, ok := s.openAISchedulerRuntimePolicyCache.Load().(*cachedOpenAISchedulerRuntimePolicy); ok && cached != nil && cached.policy != nil {
			s.openAISchedulerRuntimePolicyCache.Store(&cachedOpenAISchedulerRuntimePolicy{policy: cached.policy, expiresAt: time.Now().Add(openAISchedulerRuntimePolicyErrorTTL).UnixNano()})
			return cached.policy, err
		}
		return s.storeOpenAISchedulerRuntimePolicy(s.defaultOpenAISchedulerRuntimePolicy(), openAISchedulerRuntimePolicyErrorTTL), err
	}
	return s.storeOpenAISchedulerRuntimePolicy(policy, openAISchedulerRuntimePolicyCacheTTL), nil
}

// CurrentOpenAISchedulerRuntimePolicy 返回当前不可变指针。首次加载同步执行；之后
// 过期先返回旧值，再以 singleflight 后台刷新，多并发请求不会排队等数据库。
func (s *SettingService) CurrentOpenAISchedulerRuntimePolicy(ctx context.Context) *OpenAISchedulerRuntimePolicy {
	if s == nil {
		policy := (&SettingService{}).defaultOpenAISchedulerRuntimePolicy()
		return &policy
	}
	if cached, ok := s.openAISchedulerRuntimePolicyCache.Load().(*cachedOpenAISchedulerRuntimePolicy); ok && cached != nil && cached.policy != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.policy
		}
		s.openAISchedulerRuntimePolicySF.DoChan("openai_scheduler_runtime_policy", func() (any, error) {
			return s.refreshOpenAISchedulerRuntimePolicy(context.Background())
		})
		return cached.policy
	}
	// 生产启动阶段会显式 Warm；手工构造的服务和单元测试未 Warm 时只使用
	// 启动默认，不把首次客户请求变成一次隐式数据库读取。
	return s.storeOpenAISchedulerRuntimePolicy(s.defaultOpenAISchedulerRuntimePolicy(), openAISchedulerRuntimePolicyCacheTTL)
}

func (s *SettingService) WarmOpenAISchedulerRuntimePolicy(ctx context.Context) error {
	_, err := s.refreshOpenAISchedulerRuntimePolicy(ctx)
	return err
}

func (s *SettingService) UpdateOpenAISchedulerRuntimePolicy(ctx context.Context, input OpenAISchedulerRuntimePolicyUpdate) (*OpenAISchedulerRuntimePolicy, error) {
	if err := validateOpenAISchedulerRuntimePolicyUpdate(input); err != nil {
		return nil, infraerrors.BadRequest("OPENAI_SCHEDULER_POLICY_INVALID", err.Error())
	}
	casRepo, ok := s.settingRepo.(OpenAISchedulerPolicyCASRepository)
	if !ok {
		return nil, fmt.Errorf("setting repository does not support scheduler policy CAS")
	}
	now := time.Now().UTC()
	policy := normalizeOpenAISchedulerRuntimePolicy(OpenAISchedulerRuntimePolicy{
		Revision:                 input.ExpectedRevision + 1,
		Preset:                   input.Preset,
		AdvancedEnabled:          input.AdvancedEnabled,
		StickySoftScoringEnabled: input.StickySoftScoringEnabled,
		TopK:                     input.TopK,
		PreferSubscriptionPool:   input.PreferSubscriptionPool,
		PriorityWeight:           input.PriorityWeight,
		LoadWeight:               input.LoadWeight,
		QueueWeight:              input.QueueWeight,
		ErrorRateWeight:          input.ErrorRateWeight,
		TTFTWeight:               input.TTFTWeight,
		PreferSoonestResetWeight: input.PreferSoonestResetWeight,
		QuotaHeadroomWeight:      input.QuotaHeadroomWeight,
		UpstreamCostEnabled:      input.UpstreamCostEnabled,
		UpstreamCostWeight:       input.UpstreamCostWeight,
		SessionStickyWeight:      input.SessionStickyWeight,
		PreviousResponseMode:     OpenAISchedulerPreviousResponseModeHard,
		SalesBillingIsolated:     true,
		UpdatedAt:                now,
		Source:                   OpenAISchedulerPolicySourceDatabase,
	})
	values := openAISchedulerRuntimePolicyValues(policy)
	swapped, err := casRepo.CompareAndSwapMultiple(
		ctx,
		SettingKeyOpenAISchedulerPolicyRevision,
		strconv.FormatInt(input.ExpectedRevision, 10),
		strconv.FormatInt(policy.Revision, 10),
		values,
	)
	if err != nil {
		return nil, err
	}
	if !swapped {
		return nil, ErrOpenAISchedulerPolicyRevisionConflict
	}
	// 数据库提交成功后才替换内存；失败路径继续使用上一份确认快照。
	stored := s.storeOpenAISchedulerRuntimePolicy(policy, openAISchedulerRuntimePolicyCacheTTL)
	openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{
		enabled:   stored.AdvancedEnabled,
		expiresAt: time.Now().Add(openAIAdvancedSchedulerSettingCacheTTL).UnixNano(),
	})
	upstream := CurrentUpstreamBillingRuntimeSettings()
	upstream.CostSchedulingEnabled = stored.UpstreamCostEnabled
	upstream.UpstreamCostWeight = stored.UpstreamCostWeight
	upstream.QuotaHeadroomWeight = stored.QuotaHeadroomWeight
	upstream.PreferSoonestResetWeight = stored.PreferSoonestResetWeight
	storeUpstreamBillingRuntimeSettings(upstream)
	return stored, nil
}

func (s *SettingService) ResetOpenAISchedulerRuntimePolicy(ctx context.Context, expectedRevision int64) (*OpenAISchedulerRuntimePolicy, error) {
	defaults := s.defaultOpenAISchedulerRuntimePolicy()
	return s.UpdateOpenAISchedulerRuntimePolicy(ctx, OpenAISchedulerRuntimePolicyUpdate{
		ExpectedRevision:         expectedRevision,
		Preset:                   OpenAISchedulerPolicyPresetSafeDefault,
		AdvancedEnabled:          false,
		StickySoftScoringEnabled: false,
		TopK:                     defaults.TopK,
		PreferSubscriptionPool:   false,
		PriorityWeight:           defaults.PriorityWeight,
		LoadWeight:               defaults.LoadWeight,
		QueueWeight:              defaults.QueueWeight,
		ErrorRateWeight:          defaults.ErrorRateWeight,
		TTFTWeight:               defaults.TTFTWeight,
		PreferSoonestResetWeight: 0,
		QuotaHeadroomWeight:      0,
		UpstreamCostEnabled:      false,
		UpstreamCostWeight:       0,
		SessionStickyWeight:      0,
	})
}
