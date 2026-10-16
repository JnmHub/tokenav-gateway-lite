package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	OpenAILongContextInputEstimateFailedCode       = "OPENAI_LONG_CONTEXT_INPUT_ESTIMATE_FAILED"
	OpenAILongContextPricingUnavailableCode        = "OPENAI_LONG_CONTEXT_PRICING_UNAVAILABLE"
	OpenAILongContextBillingAccountUnavailableCode = "OPENAI_LONG_CONTEXT_BILLING_ACCOUNT_UNAVAILABLE"
)

type openAILongContextRoutingContextKey struct{}

// OpenAILongContextRoutingDecision is computed once before account selection.
// It is intentionally request-scoped: temporary cooldowns may change candidates,
// but must never rewrite the persisted account or group billing policy.
type OpenAILongContextRoutingDecision struct {
	Active                         bool
	Model                          string
	EstimatedInputTokens           int
	InputTokenThreshold            int
	RequireProviderLongContextCost bool
}

// OpenAILongContextRoutingError is safe to surface to API clients. Cause is
// retained for logs and errors.Is/errors.As checks without exposing internals.
type OpenAILongContextRoutingError struct {
	StatusCode int
	ErrorType  string
	Code       string
	Message    string
	Cause      error
}

func (e *OpenAILongContextRoutingError) Error() string {
	if e == nil {
		return "OpenAI long-context routing failed"
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "OpenAI long-context routing failed"
}

func (e *OpenAILongContextRoutingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newOpenAILongContextRoutingError(statusCode int, code, message string, cause error) error {
	if statusCode <= 0 {
		statusCode = http.StatusServiceUnavailable
	}
	return &OpenAILongContextRoutingError{
		StatusCode: statusCode,
		ErrorType:  "api_error",
		Code:       strings.TrimSpace(code),
		Message:    strings.TrimSpace(message),
		Cause:      cause,
	}
}

func OpenAILongContextRoutingErrorDetails(err error) (statusCode int, errorType, code, message string, ok bool) {
	var routingErr *OpenAILongContextRoutingError
	if !errors.As(err, &routingErr) || routingErr == nil {
		return 0, "", "", "", false
	}
	return routingErr.StatusCode, routingErr.ErrorType, routingErr.Code, routingErr.Message, true
}

func openAILongContextRoutingDecisionFromContext(ctx context.Context) (OpenAILongContextRoutingDecision, bool) {
	if ctx == nil {
		return OpenAILongContextRoutingDecision{}, false
	}
	decision, ok := ctx.Value(openAILongContextRoutingContextKey{}).(OpenAILongContextRoutingDecision)
	return decision, ok
}

func withOpenAILongContextRoutingDecision(ctx context.Context, decision OpenAILongContextRoutingDecision) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAILongContextRoutingContextKey{}, decision)
}

func openAILongContextRoutingDecisionForTokens(group *Group, model string, threshold, estimatedInputTokens int) OpenAILongContextRoutingDecision {
	decision := OpenAILongContextRoutingDecision{
		Model:                strings.TrimSpace(model),
		EstimatedInputTokens: estimatedInputTokens,
		InputTokenThreshold:  threshold,
	}
	if group == nil || group.Platform != PlatformOpenAI || threshold <= 0 || estimatedInputTokens <= threshold {
		return decision
	}
	decision.Active = true
	decision.RequireProviderLongContextCost = group.AllowOpenAILongContextBilling
	return decision
}

// PrepareOpenAILongContextRouting resolves the model's structured threshold and
// estimates input tokens before scheduling. It never falls back to a hard-coded
// threshold because routing and billing must consume the same pricing facts.
func (s *OpenAIGatewayService) PrepareOpenAILongContextRouting(
	ctx context.Context,
	group *Group,
	platform string,
	body []byte,
	modelCandidates ...string,
) (context.Context, OpenAILongContextRoutingDecision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	platform = normalizeOpenAICompatiblePlatform(platform)
	if group == nil || group.Platform != PlatformOpenAI || platform != PlatformOpenAI || len(body) == 0 {
		return ctx, OpenAILongContextRoutingDecision{}, nil
	}

	model, threshold := s.resolveOpenAILongContextThreshold(modelCandidates...)
	if threshold <= 0 {
		decision := OpenAILongContextRoutingDecision{Model: model}
		return withOpenAILongContextRoutingDecision(ctx, decision), decision, nil
	}

	estimatedInputTokens, err := estimateOpenAIInputTokensFromRequestBody(model, body)
	if err != nil {
		return ctx, OpenAILongContextRoutingDecision{}, newOpenAILongContextRoutingError(
			http.StatusBadRequest,
			OpenAILongContextInputEstimateFailedCode,
			"Unable to determine whether the input exceeds the long-context threshold; the request was blocked to prevent incorrect billing. / 无法判断输入是否超过长上下文阈值，为避免错误计费已拒绝请求。",
			err,
		)
	}

	decision := openAILongContextRoutingDecisionForTokens(group, model, threshold, estimatedInputTokens)
	routingCtx := withOpenAILongContextRoutingDecision(ctx, decision)
	if !decision.Active || !decision.RequireProviderLongContextCost {
		return routingCtx, decision, nil
	}
	if ready, field := s.openAIModelLongContextPricingReady(model); !ready {
		return ctx, OpenAILongContextRoutingDecision{}, newOpenAILongContextRoutingError(
			http.StatusBadRequest,
			OpenAILongContextPricingUnavailableCode,
			fmt.Sprintf("Long-context pricing is incomplete for model %s (%s). / 模型 %s 的长上下文价格配置不完整（%s）。", model, field, model, field),
			ErrModelPricingUnavailable,
		)
	}
	return routingCtx, decision, nil
}

func (s *OpenAIGatewayService) resolveOpenAILongContextThreshold(modelCandidates ...string) (string, int) {
	seen := make(map[string]struct{}, len(modelCandidates))
	for _, candidate := range modelCandidates {
		model := strings.TrimSpace(candidate)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		if s != nil && s.billingService != nil {
			if pricing, err := s.billingService.GetModelPricing(model); err == nil && pricing != nil && pricing.LongContextInputThreshold > 0 {
				return model, pricing.LongContextInputThreshold
			}
		}
		if pricing := officialOpenAITextPricingForModel(normalizeKnownOpenAICodexModel(model)); pricing != nil && pricing.LongContextInputTokenThreshold > 0 {
			return model, pricing.LongContextInputTokenThreshold
		}
	}
	return "", 0
}

func (s *OpenAIGatewayService) openAIModelLongContextPricingReady(model string) (bool, string) {
	if s != nil && s.billingService != nil {
		pricing, err := s.billingService.GetModelPricing(model)
		if err != nil || pricing == nil {
			return false, "model_pricing"
		}
		return openAIModelLongContextPricingFieldsReady(model, pricing)
	}
	pricing := officialOpenAITextPricingForModel(normalizeKnownOpenAICodexModel(model))
	if pricing == nil {
		return false, "model_pricing"
	}
	if pricing.InputCostPerTokenAbove272K <= 0 {
		return false, "input_cost_per_token_above_272k"
	}
	if pricing.OutputCostPerTokenAbove272K <= 0 {
		return false, "output_cost_per_token_above_272k"
	}
	if pricing.CacheReadInputTokenCost > 0 && pricing.CacheReadInputTokenCostAbove272K <= 0 {
		return false, "cache_read_cost_per_token_above_272k"
	}
	if requiresBillableOpenAICacheWrites(model) && pricing.CacheCreationInputTokenCostAbove272K <= 0 {
		return false, "cache_creation_cost_per_token_above_272k"
	}
	return true, ""
}

func openAIModelLongContextPricingFieldsReady(model string, pricing *ModelPricing) (bool, string) {
	if pricing == nil {
		return false, "model_pricing"
	}
	if pricing.LongContextInputThreshold <= 0 {
		return false, "long_context_input_threshold"
	}
	if pricing.LongContextInputPricePerToken <= 0 {
		return false, "input_price"
	}
	if pricing.LongContextOutputPricePerToken <= 0 {
		return false, "output_price"
	}
	if pricing.CacheReadPricePerToken > 0 && pricing.LongContextCacheReadPricePerToken <= 0 {
		return false, "cache_read_price"
	}
	if requiresBillableOpenAICacheWrites(model) && pricing.LongContextCacheCreationPricePerToken <= 0 {
		return false, "cache_write_price"
	}
	return true, ""
}

func openAIAccountMatchesLongContextRouting(ctx context.Context, account *Account) bool {
	decision, ok := openAILongContextRoutingDecisionFromContext(ctx)
	if !ok || !decision.Active || account == nil || account.Platform != PlatformOpenAI {
		return true
	}
	// 关闭分组长上下文计费只代表继续按普通价格向用户计费，不能把支持
	// 厂商长上下文成本的账号排除，否则账号池只有这类账号时会误拒绝请求。
	if !decision.RequireProviderLongContextCost {
		return true
	}
	return account.IsOpenAILongContextBillingEnabled()
}

// OpenAIAccountMatchesLongContextRouting is used by WebSocket turn guards after
// the connection has already selected an account.
func OpenAIAccountMatchesLongContextRouting(ctx context.Context, account *Account) bool {
	return openAIAccountMatchesLongContextRouting(ctx, account)
}

func wrapOpenAILongContextSelectionError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, ErrGatewayGroupUnavailable) || errors.Is(err, ErrFallbackQueueFull) || errors.Is(err, ErrNoAvailableCompactAccounts) {
		return err
	}
	decision, ok := openAILongContextRoutingDecisionFromContext(ctx)
	if !ok || !decision.Active {
		return err
	}
	if !errors.Is(err, ErrNoAvailableAccounts) && !isOpenAINoAvailableSelectionMessage(err) {
		return err
	}

	cause := err
	if !errors.Is(cause, ErrNoAvailableAccounts) {
		cause = fmt.Errorf("%w: %v", ErrNoAvailableAccounts, err)
	}
	if !decision.RequireProviderLongContextCost {
		return err
	}
	return newOpenAILongContextRoutingError(
		http.StatusServiceUnavailable,
		OpenAILongContextBillingAccountUnavailableCode,
		"This long-context request requires an account with provider long-context billing enabled, but none is currently available. / 当前长上下文请求需要使用已开启厂商长上下文计费的账号，但目前没有可用账号。",
		cause,
	)
}

func shouldApplyOpenAILongContextBilling(apiKey *APIKey, account *Account) bool {
	if account == nil || !account.IsOpenAILongContextBillingEnabled() {
		return false
	}
	// Standalone deployments without a hydrated group keep the historical
	// account-level behavior. gateway-lite fixed groups always provide Group.
	return apiKey == nil || apiKey.Group == nil || apiKey.Group.AllowOpenAILongContextBilling
}

// openAILongContextBillingGate returns the OpenAI account opt-in as an
// additional billing gate. Other upstream platforms do not expose this
// OpenAI-only switch, so nil preserves their own official pricing ladders.
func openAILongContextBillingGate(apiKey *APIKey, account *Account) *bool {
	if account == nil || !account.IsOpenAI() {
		return nil
	}
	enabled := shouldApplyOpenAILongContextBilling(apiKey, account)
	return &enabled
}
