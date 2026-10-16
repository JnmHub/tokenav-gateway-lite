package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// FundingUpperBound is a pre-dispatch capacity bound, not another selling price.
// Settlement still uses the ordinary RecordUsage cost and its frozen evidence.
type FundingUpperBound struct {
	MicroCents int64
	Kind       string
	Evidence   string
}

var ErrStrictCardEndpointUnsupported = errors.New("strict_card_endpoint_unsupported")

const fundingMaxExactMicroCents int64 = 1<<53 - 1

// ParseFundingTextLimits uses byte-sized input accounting rather than the RPM/TPM
// estimate (which deliberately underestimates tokens). The final forwarded body
// must retain the returned output limit; a deleted limit is not a funding bound.
func ParseFundingTextLimits(body []byte) (inputTokens, outputTokens, completions int, err error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if decoder.Decode(&payload) != nil || len(payload) == 0 {
		return 0, 0, 0, fmt.Errorf("%w: invalid text payload", ErrStrictCardEndpointUnsupported)
	}
	if err := validateFundingTextShape(payload); err != nil {
		return 0, 0, 0, err
	}
	for _, name := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if value, exists := payload[name]; exists {
			number, ok := value.(json.Number)
			limit, parseErr := number.Int64()
			if !ok || parseErr != nil || limit <= 0 || limit > 16_777_216 {
				return 0, 0, 0, fmt.Errorf("%w: %s must be a bounded positive integer", ErrStrictCardEndpointUnsupported, name)
			}
			// Multiple aliases are ambiguous across protocol adapters. The larger
			// limit is safe even if the provider chooses a different accepted alias.
			outputTokens = max(outputTokens, int(limit))
		}
	}
	if outputTokens <= 0 {
		return 0, 0, 0, fmt.Errorf("%w: an enforced maximum output limit is required", ErrStrictCardEndpointUnsupported)
	}
	completions = 1
	if value, exists := payload["n"]; exists {
		number, ok := value.(json.Number)
		count, parseErr := number.Int64()
		if !ok || parseErr != nil || count < 1 || count > 128 {
			return 0, 0, 0, fmt.Errorf("%w: n must be between 1 and 128", ErrStrictCardEndpointUnsupported)
		}
		completions = int(count)
	}
	// Byte-level tokenizers cannot emit more content tokens than input bytes.
	// The extra factor covers protocol framing and one structural token budget
	// per byte, while the fixed allowance covers gateway-added instructions.
	if len(body) > 32<<20 {
		return 0, 0, 0, fmt.Errorf("%w: text input is too large", ErrStrictCardEndpointUnsupported)
	}
	inputTokens = len(body)*8 + 4096
	return inputTokens, outputTokens, completions, nil
}

func validateFundingTextShape(value any) error {
	return validateFundingProtocolTextShape(value, nil, false)
}

// 路径段保留真实对象键和数组层级，字面 "[]" 或含点键不能冒充协议结构。
type fundingTextPathSegment struct {
	key          string
	arrayElement bool
}

type fundingTextPath []fundingTextPathSegment

func (path fundingTextPath) matches(expected ...fundingTextPathSegment) bool {
	if len(path) != len(expected) {
		return false
	}
	for i, segment := range path {
		if segment != expected[i] {
			return false
		}
	}
	return true
}

// 工具结构和调用参数是完整客户端数据，不是历史/媒体控制字段；仍由整个正文长度计入输入上界。
// 只豁免协议规定的数据位置，不能用任意 parameters/arguments 字段绕过真实控制字段检查。
func fundingClientToolDataField(path fundingTextPath, object map[string]any, key string, namespaceTool bool) bool {
	kind, _ := object["type"].(string)
	if namespaceTool {
		name, _ := object["name"].(string)
		return key == "parameters" && kind == "function" && strings.TrimSpace(name) != ""
	}
	field := func(key string) fundingTextPathSegment { return fundingTextPathSegment{key: key} }
	array := fundingTextPathSegment{arrayElement: true}
	switch {
	case path.matches(field("tools"), array):
		return key == "parameters" && kind == "function" || key == "input_schema" && kind == "" && object["name"] != nil
	case path.matches(field("tools"), array, field("function")), path.matches(field("functions"), array):
		return key == "parameters" && object["name"] != nil
	case path.matches(field("input"), array):
		return key == "arguments" && kind == "function_call"
	case path.matches(field("messages"), array, field("tool_calls"), array, field("function")), path.matches(field("messages"), array, field("function_call")):
		return key == "arguments" && object["name"] != nil
	case path.matches(field("messages"), array, field("content"), array), path.matches(field("input"), array, field("content"), array):
		return key == "input" && kind == "tool_use"
	}
	return false
}

func validateFundingProtocolTextShape(value any, path fundingTextPath, namespaceTool bool) error {
	switch typed := value.(type) {
	case map[string]any:
		kind, _ := typed["type"].(string)
		name, _ := typed["name"].(string)
		// 只有真实工具根及每一级具名 namespace 才能继续进入子工具数组；
		// 不从任意 tools 字段推断数据位置，也不豁免命名空间自身的历史/媒体控制字段。
		namespace := (namespaceTool || path.matches(fundingTextPathSegment{key: "tools"}, fundingTextPathSegment{arrayElement: true})) && kind == "namespace" && strings.TrimSpace(name) != ""
		for rawKey, child := range typed {
			key := strings.ToLower(strings.TrimSpace(rawKey))
			if fundingClientToolDataField(path, typed, rawKey, namespaceTool) {
				continue
			}
			if (key == "previous_response_id" || key == "conversation" || key == "session") && child != nil && child != "" {
				return fmt.Errorf("%w: retained response history requires a durable input bound", ErrStrictCardEndpointUnsupported)
			}
			if key == "background" && child == true {
				return fmt.Errorf("%w: background text execution requires durable asynchronous settlement", ErrStrictCardEndpointUnsupported)
			}
			if key == "modalities" || key == "output_modalities" {
				if modalities, ok := child.([]any); ok {
					for _, modality := range modalities {
						if modality != "text" {
							return fmt.Errorf("%w: non-text output has no verified token-price bound", ErrStrictCardEndpointUnsupported)
						}
					}
				}
			}
			if key == "prompt" {
				if prompt, ok := child.(map[string]any); ok && prompt["id"] != nil {
					return fmt.Errorf("%w: stored prompts require a durable input bound", ErrStrictCardEndpointUnsupported)
				}
			}
			if key == "image_url" || key == "input_audio" || key == "audio" || key == "video" || key == "file_id" || key == "file_data" {
				return fmt.Errorf("%w: media input has no verified text-token bound", ErrStrictCardEndpointUnsupported)
			}
			if key == "type" {
				kind, _ := child.(string)
				kind = strings.ToLower(strings.TrimSpace(kind))
				if strings.Contains(kind, "image") || strings.Contains(kind, "audio") || strings.Contains(kind, "video") || strings.Contains(kind, "file") || strings.Contains(kind, "web_search") || strings.Contains(kind, "computer") || strings.Contains(kind, "code_interpreter") || strings.Contains(kind, "mcp") {
					return fmt.Errorf("%w: %s needs its own maximum preauthorization", ErrStrictCardEndpointUnsupported, kind)
				}
			}
			_, toolArray := child.([]any)
			childTools := namespace && toolArray && (rawKey == "tools" || rawKey == "children")
			if err := validateFundingProtocolTextShape(child, append(path, fundingTextPathSegment{key: rawKey}), childTools); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			_, toolObject := child.(map[string]any)
			if err := validateFundingProtocolTextShape(child, append(path, fundingTextPathSegment{arrayElement: true}), namespaceTool && toolObject); err != nil {
				return err
			}
		}
	}
	return nil
}

// FundingTextUpperBound follows the actual resolver/price-policy chain and takes
// the largest possible tariff before dispatch. Taking the max across intervals
// is necessary: a short actual context may cost more than the estimated tier.
func (s *BillingService) FundingTextUpperBound(ctx context.Context, resolver *ModelPricingResolver, group *Group, model string, inputTokens, outputTokens, completions int, rate float64) (FundingUpperBound, error) {
	if s == nil || inputTokens <= 0 || outputTokens <= 0 || completions <= 0 || !finiteNonNegative(rate) {
		return FundingUpperBound{}, fmt.Errorf("%w: invalid text bound", ErrStrictCardEndpointUnsupported)
	}
	if resolver == nil {
		resolver = NewModelPricingResolver(nil, s)
	}
	pricingInput := PricingInput{Model: strings.TrimSpace(model), Group: group}
	// A resolver without channel storage still prices from the ordinary catalog.
	// Passing GroupID into that resolver would dereference its absent channel
	// service, rather than producing a reliable catalog-based capacity bound.
	if group != nil && resolver.channelService != nil {
		pricingInput.GroupID = &group.ID
	}
	var resolved *ResolvedPricing
	pricingAt := time.Now()
	if snapshot, frozen := ExecutionPricingSnapshotFromContext(ctx, model); frozen {
		// Final dispatch installs the price before bounding it. Reuse that exact
		// deep snapshot for admission and settlement across admin updates.
		resolved = snapshot.Resolved.Restore()
		rate, pricingAt = snapshot.RateMultiplier, snapshot.PricingAt
	} else {
		resolved = resolver.Resolve(ctx, pricingInput)
	}
	if resolved == nil {
		return FundingUpperBound{}, ErrModelPricingUnavailable
	}
	if resolved.Mode == BillingModePerRequest {
		unit := resolved.DefaultPerRequestPrice
		for _, tier := range resolved.RequestTiers {
			if tier.PerRequestPrice != nil {
				if !finiteNonNegative(*tier.PerRequestPrice) {
					return FundingUpperBound{}, ErrModelPricingUnavailable
				}
				unit = math.Max(unit, *tier.PerRequestPrice)
			}
		}
		return fundingBoundFromUSD(unit*float64(completions)*rate*fundingMaximumTimeMultiplier(resolved), "per_request:"+model)
	}
	if resolved.Mode != "" && resolved.Mode != BillingModeToken {
		return FundingUpperBound{}, fmt.Errorf("%w: text resolved to %s", ErrStrictCardEndpointUnsupported, resolved.Mode)
	}
	if resolved.BasePricing == nil {
		return FundingUpperBound{}, ErrModelPricingUnavailable
	}
	prices := []*ModelPricing{resolved.BasePricing}
	for i := range resolved.Intervals {
		prices = append(prices, intervalToModelPricing(&resolved.Intervals[i], resolved.BasePricing, resolved.channelPricing))
	}
	maximum := 0.0
	for _, original := range prices {
		pricing := s.applyModelSpecificPricingPolicyEx(model, original, resolved.Source == PricingSourceLiteLLM, pricingAt)
		if !validFundingTokenPricing(pricing) {
			return FundingUpperBound{}, ErrModelPricingUnavailable
		}
		for _, tier := range []string{"", "priority", "flex", OpenAIFastTierUltrafast} {
			for _, longContext := range []bool{false, true} {
				// Reserve every possible input classification. Existing adapters can
				// report input/cache counters with different inclusion semantics; a
				// conservative sum prevents either convention exceeding the hold.
				tokens := UsageTokens{InputTokens: inputTokens, CacheReadTokens: inputTokens,
					CacheCreationTokens: inputTokens * 2, CacheCreation5mTokens: inputTokens, CacheCreation1hTokens: inputTokens,
					OutputTokens: outputTokens * completions}
				copyPricing := *pricing
				if longContext {
					copyPricing.LongContextInputThreshold = 1
				}
				cost := s.computeTokenBreakdown(&copyPricing, tokens, rate, tier, longContext)
				cost.ActualCost *= math.Max(1, maxReasoningEffortBillingMultiplier(model, "max", pricing))
				cost.ActualCost *= fundingMaximumTimeMultiplier(resolved)
				// Default DeepSeek pricing can cross a peak interval during a long
				// request. Its real tariff peaks at twice the normal price.
				if resolved.Source == PricingSourceLiteLLM && isDeepSeekModel(model) {
					cost.ActualCost *= 2
				}
				if !finiteNonNegative(cost.ActualCost) {
					return FundingUpperBound{}, ErrModelPricingUnavailable
				}
				maximum = math.Max(maximum, cost.ActualCost)
			}
		}
	}
	return fundingBoundFromUSD(maximum, fmt.Sprintf("text:%s,input_bytes_upper_tokens=%d,output_tokens=%d,n=%d,tariff_max", model, inputTokens, outputTokens, completions))
}

func validFundingTokenPricing(pricing *ModelPricing) bool {
	if pricing == nil {
		return false
	}
	for _, value := range []float64{pricing.InputPricePerToken, pricing.OutputPricePerToken,
		pricing.InputPricePerTokenPriority, pricing.OutputPricePerTokenPriority,
		pricing.CacheReadPricePerToken, pricing.CacheCreationPricePerToken,
		pricing.CacheReadPricePerTokenPriority, pricing.CacheCreationPricePerTokenPriority,
		pricing.CacheCreation5mPrice, pricing.CacheCreation1hPrice,
		pricing.LongContextInputPricePerToken, pricing.LongContextOutputPricePerToken,
		pricing.LongContextInputPricePerTokenPriority, pricing.LongContextOutputPricePerTokenPriority,
		pricing.LongContextInputMultiplier, pricing.LongContextOutputMultiplier} {
		if !finiteNonNegative(value) {
			return false
		}
	}
	for _, value := range []*float64{pricing.FastMultiplier, pricing.FlexMultiplier, pricing.MaxReasoningEffortMultiplier} {
		if value != nil && !finiteNonNegative(*value) {
			return false
		}
	}
	return true
}

func fundingMaximumTimeMultiplier(resolved *ResolvedPricing) float64 {
	maximum := 1.0
	if resolved != nil && resolved.Source == PricingSourceChannel && resolved.channelPricing != nil && resolved.channelPricing.TimePricing != nil {
		for _, period := range resolved.channelPricing.TimePricing.Periods {
			if finiteNonNegative(period.Multiplier) {
				maximum = math.Max(maximum, period.Multiplier)
			}
		}
	}
	return maximum
}

func fundingBoundFromUSD(cost float64, evidence string) (FundingUpperBound, error) {
	if !finiteNonNegative(cost) || cost*100_000_000 >= float64(fundingMaxExactMicroCents) {
		return FundingUpperBound{}, fmt.Errorf("%w: cost exceeds exact funding range", ErrStrictCardEndpointUnsupported)
	}
	// This ceil applies only to capacity. ActualCents and micro-cent settlement
	// retain their existing rounding; free requests reserve one micro-cent.
	micro := int64(math.Ceil(cost * 100_000_000))
	if micro < 1 {
		micro = 1
	}
	return FundingUpperBound{MicroCents: micro, Kind: "bounded", Evidence: evidence}, nil
}

func (s *OpenAIGatewayService) FundingOpenAITextUpperBound(ctx context.Context, apiKey *APIKey, body []byte, billingModels ...string) (FundingUpperBound, error) {
	if s == nil || s.billingService == nil || apiKey == nil || apiKey.Group == nil {
		return FundingUpperBound{}, ErrModelPricingUnavailable
	}
	input, output, count, err := ParseFundingTextLimits(body)
	if err != nil {
		return FundingUpperBound{}, err
	}
	rate := apiKey.Group.RateMultiplier
	if policy, ok := EffectiveRatePolicyFromContext(ctx); ok {
		rate = policy.EffectiveRateMultiplier
	} else if apiKey.UserRateMultiplier != nil {
		rate = *apiKey.UserRateMultiplier
	}
	var maximum FundingUpperBound
	for _, model := range billingModels {
		if strings.TrimSpace(model) == "" {
			continue
		}
		bound, err := s.billingService.FundingTextUpperBound(ctx, s.resolver, apiKey.Group, model, input, output, count, rate)
		if err != nil {
			return FundingUpperBound{}, err
		}
		if bound.MicroCents > maximum.MicroCents {
			maximum = bound
		}
	}
	if maximum.MicroCents <= 0 {
		return FundingUpperBound{}, ErrModelPricingUnavailable
	}
	return maximum, nil
}

// FundingOpenAIImageUpperBound covers fixed image pricing at every possible
// reported size. Token-billed images need verified pixel/output-token limits;
// they cannot use a fixed per-image quote as a pretend maximum.
func (s *OpenAIGatewayService) FundingOpenAIImageUpperBound(ctx context.Context, apiKey *APIKey, model string, count int) (FundingUpperBound, error) {
	if s == nil || s.billingService == nil || apiKey == nil || count < 1 || count > 16 {
		return FundingUpperBound{}, fmt.Errorf("%w: invalid image count", ErrStrictCardEndpointUnsupported)
	}
	if resolved := s.resolveOpenAIChannelPricing(ctx, model, apiKey); resolved != nil && resolved.Mode == BillingModeToken {
		return FundingUpperBound{}, fmt.Errorf("%w: token-billed images require verified media limits", ErrStrictCardEndpointUnsupported)
	}
	rate := 1.0
	if policy, ok := EffectiveRatePolicyFromContext(ctx); ok {
		rate = policy.EffectiveRateMultiplier
	} else if apiKey.Group != nil {
		rate = apiKey.Group.RateMultiplier
	}
	rate = resolveImageRateMultiplier(apiKey, rate)
	maximum := 0.0
	for _, tier := range []string{"1K", "2K", "4K"} {
		cost := s.calculateOpenAIImageCost(ctx, model, apiKey, &OpenAIForwardResult{ImageCount: count, ImageSize: tier}, rate)
		if cost == nil || !finiteNonNegative(cost.ActualCost) {
			return FundingUpperBound{}, ErrModelPricingUnavailable
		}
		maximum = math.Max(maximum, cost.ActualCost)
	}
	return fundingBoundFromUSD(maximum, fmt.Sprintf("image:%s,n=%d,maximum_size_tariff", model, count))
}
