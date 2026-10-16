package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// The wrapper inspects the final outbound body, including protocol conversion
// and retry stripping. A pre-routing estimate alone cannot prove an output cap.
type fundingHTTPUpstream struct {
	inner    HTTPUpstream
	billing  *BillingService
	resolver *ModelPricingResolver
}

func newFundingHTTPUpstream(inner HTTPUpstream, billing *BillingService, resolver *ModelPricingResolver) HTTPUpstream {
	if inner == nil {
		return nil
	}
	return &fundingHTTPUpstream{inner: inner, billing: billing, resolver: resolver}
}
func (u *fundingHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	if err := u.admit(req); err != nil {
		return nil, err
	}
	return u.inner.Do(req, proxyURL, accountID, concurrency)
}
func (u *fundingHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	if err := u.admit(req); err != nil {
		return nil, err
	}
	return u.inner.DoWithTLS(req, proxyURL, accountID, concurrency, profile)
}
func (u *fundingHTTPUpstream) admit(req *http.Request) error {
	if req == nil {
		return errors.New("upstream request is nil")
	}
	a, funded := gatewaylite.FundingFromContext(req.Context())
	if !funded {
		// Delegated credentials skip legacy leases. Missing admission must therefore
		// stop here, including when routed through another platform's service.
		if gatewaylite.HasOAuthInvocationCredential(req.Context()) {
			return gatewaylite.ErrRequestQuotaUnavailable
		}
		return nil
	}
	// Polling existing tasks cannot execute work or rematch their original card.
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		return nil
	}
	path := strings.TrimRight(req.URL.Path, "/")
	text := strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/messages")
	if text && !strings.Contains(path, "/backend-api/") {
		if req.GetBody == nil {
			return fmt.Errorf("%w: final request body cannot be verified", ErrStrictCardEndpointUnsupported)
		}
		reader, err := req.GetBody()
		if err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(reader, (32<<20)+1))
		_ = reader.Close()
		if err != nil {
			return err
		}
		if err := ensureFinalFundingText(req.Context(), u.billing, u.resolver, path, body); err != nil {
			return err
		}
	} else {
		// Media creation must have a frozen, protocol-specific maximum. OAuth
		// Codex and arbitrary endpoints have no validated output enforcement.
		media := strings.Contains(path, "/images/") || strings.Contains(path, "/videos") || strings.HasSuffix(path, "/contents/generations/tasks")
		if strings.Contains(path, "/images/") {
			// 现有媒体报价没有持久化创建执行价和完整计量上界。严格卡路径须先阻断，
			// 不能用固定报价占位后在完成时回读热更新价格突破原授权。
			return fmt.Errorf("%w: %s has no frozen media execution price", ErrStrictCardEndpointUnsupported, path)
		}
		if !media || a.Disposition != "authorized" || a.Status != "active" || a.BoundEvidence["kind"] != "fixed_media" {
			return fmt.Errorf("%w: %s has no verified maximum", ErrStrictCardEndpointUnsupported, path)
		}
		if err := gatewaylite.EnsureFundingUpperBound(req.Context(), a.UpperBoundMicroCents, "existing_media_maximum_cents"); err != nil {
			return err
		}
	}
	return gatewaylite.PrepareFundingDispatch(req.Context())
}

func ensureFinalFundingText(ctx context.Context, billing *BillingService, resolver *ModelPricingResolver, path string, body []byte) error {
	a, ok := gatewaylite.FundingFromContext(ctx)
	if !ok {
		return nil
	}
	// Each protocol has its own accepted cap; accepting an unrelated alias would
	// let a retry remove the real limit while retaining a misleading unused field.
	fields := []string{"max_output_tokens"}
	if strings.HasSuffix(path, "/chat/completions") {
		fields = []string{"max_completion_tokens", "max_tokens"}
	}
	if strings.HasSuffix(path, "/messages") {
		fields = []string{"max_tokens"}
	}
	accepted := false
	for _, field := range fields {
		if gjson.GetBytes(body, field).Exists() {
			accepted = true
		}
	}
	if !accepted {
		return fmt.Errorf("%w: final %s output limit is missing", ErrStrictCardEndpointUnsupported, path)
	}
	input, output, count, err := ParseFundingTextLimits(body)
	if err != nil {
		return err
	}
	group := gatewayTokenRequestBillingGroupFromContext(ctx)
	if group == nil {
		group, _ = ctx.Value(ctxkey.Group).(*Group)
	}
	if group == nil {
		return ErrModelPricingUnavailable
	}
	rate := group.RateMultiplier
	if policy, ok := EffectiveRatePolicyFromContext(ctx); ok {
		rate = policy.EffectiveRateMultiplier
	} else if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
		rate = meta.RateMultiplier
	}
	forwardedModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if forwardedModel == "" {
		return fmt.Errorf("%w: final execution model is missing", ErrStrictCardEndpointUnsupported)
	}
	models := usageBillingModelCandidates(a.RequestedModel, forwardedModel)
	if resolver != nil && resolver.channelService != nil {
		mapping := resolver.channelService.ResolveChannelMapping(ctx, group.ID, a.RequestedModel)
		models = usageBillingModelCandidates(a.RequestedModel, append(models, mapping.MappedModel)...)
	}
	maximum := FundingUpperBound{}
	account, _ := ctx.Value(fundingDispatchAccountKey{}).(*Account)
	for _, model := range models {
		if strings.TrimSpace(model) == "" {
			continue
		}
		if _, frozen := ExecutionPricingSnapshotFromContext(ctx, model); !frozen {
			pricingResolver := resolver
			if pricingResolver == nil {
				pricingResolver = NewModelPricingResolver(nil, billing)
			}
			pricingInput := PricingInput{Model: model, Group: group}
			if pricingResolver.channelService != nil {
				pricingInput.GroupID = &group.ID
			}
			resolved := pricingResolver.Resolve(ctx, pricingInput)
			groupLongContext := group.AllowOpenAILongContextBilling
			reasoning := ""
			if effort := extractReasoningEffortAuditFromBody(body, "reasoning.effort", "reasoning_effort", "output_config.effort"); effort != nil {
				reasoning = *effort
			}
			snapshot, freezeErr := FreezeExecutionPricing(CostInput{
				Ctx: ctx, Model: model, Resolved: resolved, RateMultiplier: rate,
				ServiceTier: gjson.GetBytes(body, "service_tier").String(), ReasoningEffort: reasoning,
				LongContextBillingEnabled: openAILongContextBillingGate(&APIKey{Group: group}, account), LongContextGroupEnabled: &groupLongContext,
			})
			if freezeErr != nil {
				return freezeErr
			}
			if err := StoreExecutionPricingSnapshot(ctx, snapshot); err != nil {
				return errors.Join(gatewaylite.ErrRequestQuotaUnavailable, err)
			}
		}
		bound, err := billing.FundingTextUpperBound(ctx, resolver, group, model, input, output, count, rate)
		if err != nil {
			return err
		}
		if bound.MicroCents > maximum.MicroCents {
			maximum = bound
		}
	}
	if maximum.MicroCents <= 0 {
		return ErrModelPricingUnavailable
	}
	if gatewaylite.HasOAuthInvocationCredential(ctx) {
		// Conversion/replay can expand both input and output. Resize must carry the
		// final body's proof rather than the original client payload's evidence.
		proof := gatewaylite.FundingBoundEvidence(maximum.Evidence)
		digest := sha256.Sum256(body)
		proof["endpoint"] = a.Endpoint
		proof["input_tokens_upper"] = input
		proof["max_output_tokens"] = output
		proof["completions"] = count
		proof["forwarded_body_sha256"] = hex.EncodeToString(digest[:])
		proof["billing_models"] = models
		ctx = gatewaylite.ContextWithOAuthInvocationBoundProof(ctx, proof)
	}
	return gatewaylite.EnsureFundingUpperBound(ctx, maximum.MicroCents, maximum.Evidence)
}

// WriteFundingAdmissionError keeps capacity errors distinct from account faults,
// so a denied hold does not trigger retries against every upstream account.
func (s *OpenAIGatewayService) EnsureFundingWebSocketPayload(ctx context.Context, account *Account, body []byte) error {
	if !gatewaylite.HasRequestFunding(ctx) {
		if gatewaylite.HasOAuthInvocationCredential(ctx) {
			return gatewaylite.ErrRequestQuotaUnavailable
		}
		return nil
	}
	if account == nil || account.Type != AccountTypeAPIKey || account.Platform != PlatformOpenAI {
		return fmt.Errorf("%w: this websocket upstream has no verified output cap", ErrStrictCardEndpointUnsupported)
	}
	ctx = context.WithValue(ctx, fundingDispatchAccountKey{}, account)
	if err := ensureFinalFundingText(ctx, s.billingService, s.resolver, "/responses", body); err != nil {
		return err
	}
	return gatewaylite.PrepareFundingDispatch(ctx)
}
func (s *OpenAIGatewayService) ensureFundingWSPayload(ctx context.Context, account *Account, body any) error {
	if !gatewaylite.HasRequestFunding(ctx) {
		if gatewaylite.HasOAuthInvocationCredential(ctx) {
			return gatewaylite.ErrRequestQuotaUnavailable
		}
		return nil
	}
	// The WS JSON writer encodes the complete response.create frame, including
	// its trailing newline. Hash and price those exact bytes, not the HTTP body
	// before the frame adapter adds execution fields.
	var encoded strings.Builder
	if err := json.NewEncoder(&encoded).Encode(body); err != nil {
		return err
	}
	return s.EnsureFundingWebSocketPayload(ctx, account, []byte(encoded.String()))
}

type fundingDispatchAccountKey struct{}

func (s *OpenAIGatewayService) ensureInitialOAuthTextFunding(ctx context.Context, c *gin.Context, account *Account, body []byte, models ...string) (context.Context, error) {
	if gatewaylite.HasRequestFunding(ctx) || gatewaylite.HasOAuthInvocationCredential(ctx) {
		ctx = ContextWithExecutionPricing(ctx)
		ctx = context.WithValue(ctx, fundingDispatchAccountKey{}, account)
		c.Request = c.Request.WithContext(ctx)
	}
	if !gatewaylite.HasOAuthInvocationCredential(ctx) {
		return ctx, nil
	}
	value, _ := c.Get("api_key")
	apiKey, _ := value.(*APIKey)
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	// Consent and card scope belong to the client model frozen before routing.
	// The mapped body remains a price candidate, never a replacement identity.
	if requested, ok := clientRequestedResponseModel(ctx); ok {
		model = requested
	}
	if auth, ok := gatewaylite.FundingFromContext(ctx); ok {
		model = auth.RequestedModel
	}
	models = usageBillingModelCandidates(model, append(models, gjson.GetBytes(body, "model").String())...)
	next, err := s.EnsureOAuthInvocationTextFunding(ctx, apiKey, body, model, c.Request.URL.Path, models...)
	// Keep ambiguous reservations reachable by the finalizer on every failure.
	if next != nil {
		c.Request = c.Request.WithContext(next)
	}
	if err != nil {
		WriteFundingAdmissionError(c, err)
	}
	return next, err
}

func isFundingAdmissionError(err error) bool {
	return errors.Is(err, ErrStrictCardEndpointUnsupported) || errors.Is(err, ErrModelPricingUnavailable) || errors.Is(err, gatewaylite.ErrRequestQuotaInsufficient) || errors.Is(err, gatewaylite.ErrRequestQuotaUnavailable)
}

func WriteFundingAdmissionError(c interface{ JSON(int, any) }, err error) {
	status, code, message := http.StatusServiceUnavailable, "FUNDING_UNAVAILABLE", "资金授权暂不可用，请稍后重试"
	if errors.Is(err, ErrStrictCardEndpointUnsupported) {
		status, code, message = http.StatusBadRequest, "strict_card_endpoint_unsupported", "此请求缺少可验证的费用上界或上游输出限制"
	}
	if errors.Is(err, gatewaylite.ErrRequestQuotaInsufficient) {
		status, code, message = http.StatusTooManyRequests, "INSUFFICIENT_QUOTA", "时效卡及钱包可用额度不足"
	}
	c.JSON(status, map[string]any{"error": map[string]any{"type": code, "code": code, "message": message}})
}

// PreserveFundingOutputLimit only changes standard API-key Responses requests.
// Codex internal endpoints strip these parameters because they do not support
// them; those routes must fail closed instead of pretending a cap is enforced.
func preserveFundingResponsesOutputLimit(ctx context.Context, account *Account) bool {
	return gatewaylite.HasRequestFunding(ctx) && account != nil && account.Type == AccountTypeAPIKey && (account.Platform == PlatformOpenAI || account.UsesNativeCNResponses())
}
