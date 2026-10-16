//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestGrokOAuthBuildAccountCredentialsUsesCLIDefault(t *testing.T) {
	svc := NewGrokOAuthService(nil, nil)
	defer svc.Stop()
	credentials := svc.BuildAccountCredentials(&GrokTokenInfo{
		AccessToken: "access-token",
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	})

	require.Equal(t, xai.DefaultCLIBaseURL, credentials["base_url"])
}

type grokOAuthClientStub struct {
	refreshResponse *xai.TokenResponse
	ssoResponse     *xai.TokenResponse
	exchangeCalls   int
}

func (s *grokOAuthClientStub) ExchangeCode(context.Context, string, string, string, string, string) (*xai.TokenResponse, error) {
	s.exchangeCalls++
	return &xai.TokenResponse{}, nil
}

func (s *grokOAuthClientStub) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return s.refreshResponse, nil
}

func (s *grokOAuthClientStub) ConvertSSOToBuild(context.Context, string, string) (*xai.TokenResponse, error) {
	return s.ssoResponse, nil
}

func TestGrokOAuthServiceRefreshTokenPreservesOriginalRefreshTokenWhenNotRotated(t *testing.T) {
	svc := NewGrokOAuthService(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: "new-access-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	defer svc.Stop()

	info, err := svc.RefreshToken(context.Background(), "original-refresh-token", "", "client-id")
	require.NoError(t, err)
	require.Equal(t, "new-access-token", info.AccessToken)
	require.Equal(t, "original-refresh-token", info.RefreshToken)
	require.Equal(t, "client-id", info.ClientID)
}

func TestGrokOAuthServiceRefreshAccountTokenUsesNewAccessTokenTier(t *testing.T) {
	svc := NewGrokOAuthService(nil, &grokOAuthClientStub{refreshResponse: &xai.TokenResponse{
		AccessToken: grokTierJWT(map[string]any{"tier": 0}),
		TokenType:   "Bearer",
		ExpiresIn:   3600,
	}})
	defer svc.Stop()

	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
		"refresh_token": "refresh-token", "subscription_tier": "supergrok_heavy",
	}}
	info, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "free", info.SubscriptionTier)
}

func TestGrokOAuthServiceRefreshAccountTokenKeepsTierWithoutAccessTokenClaim(t *testing.T) {
	svc := NewGrokOAuthService(nil, &grokOAuthClientStub{refreshResponse: &xai.TokenResponse{
		AccessToken: "opaque-access-token",
		IDToken:     grokTierJWT(map[string]any{"tier": 5}),
		ExpiresIn:   3600,
	}})
	defer svc.Stop()

	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
		"refresh_token": "refresh-token", "subscription_tier": "supergrok_lite",
	}}
	info, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "supergrok_lite", info.SubscriptionTier)
}

func grokTierJWT(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestGrokOAuthServiceMissingClientReturnsExplicitError(t *testing.T) {
	svc := NewGrokOAuthService(nil, nil)
	defer svc.Stop()

	_, err := svc.RefreshToken(context.Background(), "refresh-token", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_CLIENT_NOT_CONFIGURED")

	_, err = svc.ConvertFromSSO(context.Background(), "sso-token", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_CLIENT_NOT_CONFIGURED")

	auth, err := svc.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)
	_, err = svc.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "authorization-code",
		State:     auth.State,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_CLIENT_NOT_CONFIGURED")

	client := &grokOAuthClientStub{}
	svc.oauthClient = client
	_, err = svc.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "authorization-code",
		State:     auth.State,
	})
	require.NoError(t, err)
	require.Equal(t, 1, client.exchangeCalls)
}

func TestGrokOAuthServiceExchangeCodeRequiresStateForCallbackURLAndConsumesSession(t *testing.T) {
	client := &grokOAuthClientStub{}
	svc := NewGrokOAuthService(nil, client)
	defer svc.Stop()

	auth, err := svc.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)

	_, err = svc.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "http://127.0.0.1:56121/callback?code=code-without-state",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_STATE_REQUIRED")
	require.Zero(t, client.exchangeCalls)

	_, err = svc.ExchangeCode(context.Background(), &GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "code-with-state",
		State:     auth.State,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_SESSION_NOT_FOUND")
	require.Zero(t, client.exchangeCalls)
}
