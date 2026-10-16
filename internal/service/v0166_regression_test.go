package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestV0166CompositePrefixWithoutUpstreamKeepsRequestedModel(t *testing.T) {
	resolver := NewCompositeRouteResolver(&compositeRouteRepoStub{routes: []CompositeModelRoute{{
		ID:             1,
		GroupID:        7,
		PublicModel:    "deepseek-v4",
		MatchType:      CompositeRouteMatchPrefix,
		TargetPlatform: PlatformOpenAI,
		UpstreamModel:  "",
		Endpoint:       CompositeRouteEndpointAny,
		Priority:       100,
		Enabled:        true,
	}}})

	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4"} {
		decision, err := resolver.Resolve(context.Background(), 7, model, CompositeRouteEndpointChatCompletions)
		require.NoError(t, err)
		require.True(t, decision.Matched)
		require.Equal(t, model, decision.UpstreamModel)
	}
}

func TestV0166PoolModeSkippedErrorRestoresRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GeminiMessagesCompatService{}
	poolAccount := func(extra map[string]any) *Account {
		credentials := map[string]any{"pool_mode": true}
		for key, value := range extra {
			credentials[key] = value
		}
		return &Account{ID: 300, Type: AccountTypeAPIKey, Platform: PlatformGemini, Credentials: credentials}
	}

	tests := []struct {
		name              string
		account           *Account
		statusCode        int
		expectFailover    bool
		expectSameAccount bool
	}{
		{name: "500_next_account", account: poolAccount(nil), statusCode: 500, expectFailover: true},
		{name: "429_same_account", account: poolAccount(nil), statusCode: 429, expectFailover: true, expectSameAccount: true},
		{name: "custom_500_same_account", account: poolAccount(map[string]any{"pool_mode_retry_status_codes": []any{float64(500)}}), statusCode: 500, expectFailover: true, expectSameAccount: true},
		{name: "400_passthrough", account: poolAccount(nil), statusCode: 400},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := []byte(`{"error":{"message":"upstream error"}}`)

			failoverErr := svc.skippedErrorPolicyFailoverError(c, tt.account, tt.statusCode, body, "req-v0166")
			if !tt.expectFailover {
				require.Nil(t, failoverErr)
				return
			}
			require.NotNil(t, failoverErr)
			require.Equal(t, tt.expectSameAccount, failoverErr.RetryableOnSameAccount)
			require.True(t, failoverErr.ShouldRetryNextAccount())
		})
	}
}

func TestV0166ClaudeCodeFingerprintSurvivesProxyUserAgent(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli; cch=12345;"}],"messages":[]}`)
	require.True(t, systemHasBillingAttributionBlock(body))
	// v0.1.169 起也识别 SDK/IDE 等官方入口，不再把入口值写死为 cli。
	require.True(t, systemHasBillingAttributionBlock([]byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_entrypoint=sdk;"}]}`)))
	require.False(t, systemHasBillingAttributionBlock([]byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220;"}]}`)))
}

func TestV0166HermesFunctionNamedWebSearchStaysClientSide(t *testing.T) {
	require.False(t, isClaudeWebSearchToolMap(map[string]any{"type": "function", "name": "web_search"}))
	require.True(t, isClaudeWebSearchToolMap(map[string]any{"type": "web_search_20250305", "name": "web_search"}))
}

func TestV0166Gemini36FlashDefaultPriceExists(t *testing.T) {
	body, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	var prices map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &prices))
	require.NotEmpty(t, prices["gemini-3.6-flash"])
}
