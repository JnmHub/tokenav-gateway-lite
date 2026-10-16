package gatewaylite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type oauthInvocationCredentialKey struct{}
type oauthInvocationRequestKey struct{}

type oauthInvocationCredential struct {
	// Credential stays in the live request context only. Durable usage and async
	// recovery contain a reservation, never an OAuth token or a Key secret.
	rawToken string
	gateway  string
	region   string
	resolved OAuthInvocationResolveResponse
}

func IsOAuthInvocationCredential(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "tokenav_oa_")
}

func ResolveOAuthInvocationCredential(ctx context.Context, control OAuthInvocationControlPlane, raw, gatewayCode, region string) (*OAuthInvocationResolveResponse, error) {
	if control == nil || !IsOAuthInvocationCredential(raw) || strings.TrimSpace(gatewayCode) == "" {
		return nil, ErrOAuthInvocationDenied
	}
	response, err := control.ResolveOAuthInvocation(ctx, OAuthInvocationResolveRequest{
		InvocationToken: strings.TrimSpace(raw), GatewayCode: strings.TrimSpace(gatewayCode), Region: strings.TrimSpace(region),
	})
	if err != nil {
		var httpErr *ControlPlaneHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 {
			return nil, errors.Join(ErrOAuthInvocationDenied, err)
		}
		return nil, errors.Join(ErrOAuthInvocationUnavailable, err)
	}
	if err := validateOAuthInvocationResolve(response, time.Now()); err != nil {
		return nil, err
	}
	return cloneOAuthInvocationResolve(response), nil
}

func ContextWithOAuthInvocationCredential(ctx context.Context, raw, gatewayCode, region string, resolved *OAuthInvocationResolveResponse) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !IsOAuthInvocationCredential(raw) || strings.TrimSpace(gatewayCode) == "" {
		return ctx, ErrOAuthInvocationDenied
	}
	if err := validateOAuthInvocationResolve(resolved, time.Now()); err != nil {
		return ctx, err
	}
	credential := &oauthInvocationCredential{rawToken: strings.TrimSpace(raw), gateway: strings.TrimSpace(gatewayCode), region: strings.TrimSpace(region), resolved: *cloneOAuthInvocationResolve(resolved)}
	return context.WithValue(ctx, oauthInvocationCredentialKey{}, credential), nil
}

func HasOAuthInvocationCredential(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	credential, _ := ctx.Value(oauthInvocationCredentialKey{}).(*oauthInvocationCredential)
	return credential != nil
}

// ContextForOAuthInvocationTurn keeps the live credential but resets every
// financial identity. Reusing a handshake/previous turn request ID could replay
// an old successful reservation after expiration or bypass RPM and concurrency.
func ContextForOAuthInvocationTurn(ctx context.Context, serverTurnRequestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, oauthInvocationRequestKey{}, (*RequestOAuthInvocation)(nil))
	if meta, ok := RequestMetaFromContext(ctx); ok {
		meta.RequestID = strings.TrimSpace(serverTurnRequestID)
		meta.Funding = nil
		meta.Reservation = nil
		meta.LeaseID = ""
		meta.AsyncSettlement = false
		meta.AsyncPersistenceUncertain = false
		meta.UpstreamConsumed = false
		meta.ActualCentsOverride = nil
		ctx = ContextWithRequestMeta(ctx, meta)
	}
	return ctx
}

func OAuthInvocationRequestFromContext(ctx context.Context) (*RequestOAuthInvocation, bool) {
	if ctx == nil {
		return nil, false
	}
	request, _ := ctx.Value(oauthInvocationRequestKey{}).(*RequestOAuthInvocation)
	return request, request != nil
}

func validateOAuthInvocationResolve(response *OAuthInvocationResolveResponse, now time.Time) error {
	if response == nil || !response.OK || response.DelegationID == "" || response.Key == nil || response.Invocation == nil {
		return ErrOAuthInvocationDenied
	}
	key, scope := response.Key, response.Invocation
	if response.ExpiresAtMillis <= now.UnixMilli() || (key.ExpiresAt > 0 && key.ExpiresAt <= now.Unix()) {
		return ErrOAuthInvocationExpired
	}
	if !key.Active() || key.KeyID == "" || key.UserID <= 0 || key.OrganizationID != 0 ||
		(key.BillingSubjectType != "" && key.BillingSubjectType != "user") ||
		(key.BillingSubjectID > 0 && key.BillingSubjectID != key.UserID) {
		return ErrOAuthInvocationDenied
	}
	if !scope.CostConsent || scope.GatewayNodeID <= 0 || scope.GroupID <= 0 || len(scope.Models) == 0 ||
		scope.MaxCostCents <= 0 || scope.MaxCostCents > maxFundingExactInteger/actualCostMicroCentsScale ||
		scope.ExpiresIn <= 0 || scope.RateLimitRPM <= 0 || scope.Concurrency <= 0 ||
		key.KeyDefaultGatewayNodeID != scope.GatewayNodeID || key.Group == nil || key.Group.ID != scope.GroupID {
		return ErrOAuthInvocationDenied
	}
	for _, model := range scope.Models {
		if strings.TrimSpace(model) == "" || strings.ContainsAny(model, "*?[]") {
			return ErrOAuthInvocationDenied
		}
	}
	return nil
}

func cloneOAuthInvocationResolve(response *OAuthInvocationResolveResponse) *OAuthInvocationResolveResponse {
	// The snapshot includes nested group maps/slices. A deep copy prevents local
	// runtime hydration or a WS turn from expanding another turn's frozen scope.
	body, _ := json.Marshal(response)
	var cloned OAuthInvocationResolveResponse
	_ = json.Unmarshal(body, &cloned)
	return &cloned
}

func oauthInvocationModelAllowed(scope OAuthInvocation, requested string) bool {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return false
	}
	for _, model := range scope.Models {
		if requested == model {
			return true
		}
	}
	return false
}

func OAuthInvocationEndpointSupported(endpoint string) bool {
	switch strings.TrimSuffix(strings.TrimSpace(endpoint), "/") {
	case "/v1/responses", "/responses", "/backend-api/codex/responses", "/openai/v1/responses", "/v1/chat/completions", "/chat/completions", "/v1/messages", "/messages":
		return true
	default:
		return false
	}
}
