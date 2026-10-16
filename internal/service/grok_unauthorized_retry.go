package service

import (
	"context"
	"errors"
)

type grokUnauthorizedRetryContextKey struct{}
type grokRefreshedAccessTokenContextKey struct{}

func grokUnauthorizedRetryUsed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	used, _ := ctx.Value(grokUnauthorizedRetryContextKey{}).(bool)
	return used
}

func contextWithGrokUnauthorizedRetry(ctx context.Context, refreshedToken ...string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, grokUnauthorizedRetryContextKey{}, true)
	if len(refreshedToken) > 0 && refreshedToken[0] != "" {
		ctx = context.WithValue(ctx, grokRefreshedAccessTokenContextKey{}, refreshedToken[0])
	}
	return ctx
}

func grokRefreshedAccessTokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	token, _ := ctx.Value(grokRefreshedAccessTokenContextKey{}).(string)
	return token
}

func (s *OpenAIGatewayService) refreshGrokAfterUnauthorized(ctx context.Context, account *Account) (string, error) {
	if s == nil || s.grokTokenProvider == nil {
		return "", errors.New("grok token provider is not configured")
	}
	return s.grokTokenProvider.RefreshAfterUnauthorized(ctx, account)
}

func shouldRetryGrokUnauthorized(ctx context.Context, account *Account, statusCode int) bool {
	return statusCode == 401 && account != nil && account.IsGrokOAuth() && !grokUnauthorizedRetryUsed(ctx)
}
