package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSetOpenAICodexRoutingHintCanonicalizesAndOwnsHeader(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, tt := range []struct {
		name, tier, want string
	}{
		{name: "fast", tier: "fast", want: "model=gpt-5.6-codex;tier=priority"},
		{name: "flex", tier: "flex", want: "model=gpt-5.6-codex;tier=flex"},
		{name: "default", tier: "default", want: "model=gpt-5.6-codex"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			setOpenAICodexRoutingHint(headers, oauth, "gpt-5.6-codex", tt.tier)
			require.Equal(t, tt.want, headers.Get(openAICodexRoutingHintHeader))
		})
	}

	headers := make(http.Header)
	headers[openAICodexRoutingHintHeader] = []string{"model=spoof"}
	setOpenAICodexRoutingHint(headers, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, "gpt-5.6", "priority")
	for key := range headers {
		require.False(t, strings.EqualFold(key, openAICodexRoutingHintHeader))
	}
}

func TestOpenAIOAuthHTTPBuildersSendRoutingHintWithoutLegacyBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-codex","service_tier":"fast"}`)
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "test-account",
		},
	}
	svc := &OpenAIGatewayService{}

	for _, passthrough := range []bool{false, true} {
		name := "ordinary"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set("OpenAI-Beta", "responses=experimental")

			var req *http.Request
			var err error
			if passthrough {
				req, err = svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "test-token")
			} else {
				req, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "test-token", false, "", true)
			}
			require.NoError(t, err)
			require.Equal(t, "model=gpt-5.6-codex;tier=priority", req.Header.Get(openAICodexRoutingHintHeader))
			if passthrough {
				require.Empty(t, req.Header.Get("OpenAI-Beta"), "OAuth 透传应剥离旧客户端的 legacy beta")
			}
		})
	}
}

func TestOpenAIHTTPPassthroughPreservesIndependentBetaTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-codex"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Add("OpenAI-Beta", "responses=experimental, future_feature=v1")

	req, err := (&OpenAIGatewayService{}).buildUpstreamRequestOpenAIPassthrough(
		context.Background(), c,
		&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		body, "test-token",
	)
	require.NoError(t, err)
	require.Equal(t, []string{"future_feature=v1"}, req.Header.Values("OpenAI-Beta"))
}

func TestNormalizeOpenAICompactRequestBodyPreservesRoutingServiceTier(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[],"service_tier":"priority","stream":true}`)
	normalized, changed, err := normalizeOpenAICompactRequestBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, `{"model":"gpt-5.6-sol","input":[],"service_tier":"priority"}`, string(normalized))
}
