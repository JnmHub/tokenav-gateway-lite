package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

// EnsureOAuthInvocationTextFunding shares the card pipeline's actual price
// resolver and byte/output limits. Returned context must replace the caller's
// context even on failure: it owns any ambiguous main-site reservation.
func (s *OpenAIGatewayService) EnsureOAuthInvocationTextFunding(ctx context.Context, apiKey *APIKey, body []byte, requestedModel, endpoint string, billingModels ...string) (context.Context, error) {
	if !gatewaylite.HasOAuthInvocationCredential(ctx) {
		return ctx, nil
	}
	if !gatewaylite.OAuthInvocationEndpointSupported(endpoint) {
		return ctx, gatewaylite.ErrOAuthInvocationUnsupported
	}
	if len(billingModels) == 0 {
		billingModels = []string{requestedModel}
	}
	bound, err := s.FundingOpenAITextUpperBound(ctx, apiKey, body, billingModels...)
	if err != nil {
		return ctx, err
	}
	control := gatewaylite.DefaultControlPlaneClientRef()
	quota := gatewaylite.DefaultRedisQuota()
	if control == nil || !control.Configured() || quota == nil || !quota.Enabled() {
		return ctx, gatewaylite.ErrOAuthInvocationUnavailable
	}
	if runtimeRef := gatewaylite.DefaultRuntimeConfigRef(); runtimeRef != nil {
		quota = quota.WithPrefix(runtimeRef.Current().RedisPrefix)
	}
	input, output, count, err := ParseFundingTextLimits(body)
	if err != nil {
		return ctx, err
	}
	digest := sha256.Sum256(body)
	// Share the card path's evidence contract; this records protocol limits, not
	// another price estimate or a second authority for the same funding amount.
	proof := gatewaylite.FundingBoundEvidence(bound.Evidence)
	proof["endpoint"] = endpoint
	proof["input_tokens_upper"] = input
	proof["max_output_tokens"] = output
	proof["completions"] = count
	proof["forwarded_body_sha256"] = hex.EncodeToString(digest[:])
	proof["billing_models"] = append([]string(nil), billingModels...)
	ctx = gatewaylite.ContextWithOAuthInvocationBoundProof(ctx, proof)
	return gatewaylite.EnsureOAuthInvocationUpperBound(ctx, control, quota, bound.MicroCents, bound.Evidence, requestedModel, endpoint)
}
