package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func upstreamModelSyncTestConfig() *config.Config {
	return &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		},
	}
}

func TestBuildV1ModelsURL(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://api.anthropic.com/v1/models", buildV1ModelsURL("https://api.anthropic.com"))
	require.Equal(t, "https://api.anthropic.com/v1/models", buildV1ModelsURL("https://api.anthropic.com/v1"))
	require.Equal(t, "https://api.anthropic.com/v1/models", buildV1ModelsURL("https://api.anthropic.com/v1/models"))
	require.Equal(t, "https://gateway.example.com/antigravity/v1/models", buildV1ModelsURL("https://gateway.example.com/antigravity/"))
}

func TestBuildGeminiModelsURL(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://generativelanguage.googleapis.com/v1beta/models", buildGeminiModelsURL("https://generativelanguage.googleapis.com"))
	require.Equal(t, "https://generativelanguage.googleapis.com/v1beta/models", buildGeminiModelsURL("https://generativelanguage.googleapis.com/v1beta"))
	require.Equal(t, "https://generativelanguage.googleapis.com/v1beta/models", buildGeminiModelsURL("https://generativelanguage.googleapis.com/v1beta/models"))
}

func TestExtractUpstreamModelIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "openai and anthropic data array",
			body: `{"data":[{"id":"claude-sonnet-4-5"},{"id":"gpt-5"},{"id":"gpt-5"},{"id":""}]}`,
			want: []string{"claude-sonnet-4-5", "gpt-5"},
		},
		{
			name: "gemini models array strips prefix",
			body: `{"models":[{"name":"models/gemini-2.5-pro"},{"name":"gemini-2.5-flash"}]}`,
			want: []string{"gemini-2.5-flash", "gemini-2.5-pro"},
		},
		{
			name: "top level array",
			body: `[{"id":"z-model"},{"name":"models/a-model"}]`,
			want: []string{"a-model", "z-model"},
		},
		{
			name: "chatgpt codex manifest uses slug",
			body: `{"models":[{"slug":"gpt-5.4"},{"slug":"gpt-5.3-codex"}]}`,
			want: []string{"gpt-5.3-codex", "gpt-5.4"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := extractUpstreamModelIDs([]byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestExtractGrokUpstreamModelIDs(t *testing.T) {
	t.Parallel()

	models, err := extractGrokUpstreamModelIDs([]byte(`{"data":[{"id":"display-id","model":"grok-4.5"},{"modelId":"grok-build-0.1"},{"model_id":"grok-composer-2.5-fast"},{"name":"Grok Meta Display Name","_meta":{"model":"grok-meta"}},{"name":"grok-name"},{"id":"grok-safe","_meta":"not-an-object"}]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"grok-4.5", "grok-build-0.1", "grok-composer-2.5-fast", "grok-meta", "grok-name", "grok-safe"}, models)
}

func TestBuildUpstreamModelsRequestsForAPIKeyAccounts(t *testing.T) {
	t.Parallel()

	svc := &AccountTestService{cfg: upstreamModelSyncTestConfig()}
	ctx := context.Background()

	anthropicReq, err := svc.buildAnthropicUpstreamModelsRequest(ctx, &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "anthropic-key",
			"base_url": "https://anthropic.example.com/v1",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://anthropic.example.com/v1/models", anthropicReq.URL.String())
	require.Equal(t, "anthropic-key", anthropicReq.Header.Get("x-api-key"))
	require.Equal(t, "2023-06-01", anthropicReq.Header.Get("anthropic-version"))

	anthropicBearerReq, err := svc.buildAnthropicUpstreamModelsRequest(ctx, &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "anthropic-bearer-key",
			"base_url": "https://anthropic.example.com/v1",
		},
		Extra: map[string]any{
			anthropicAPIKeyAuthSchemeExtraKey: AnthropicAPIKeyAuthSchemeAuthorizationBearer,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "Bearer anthropic-bearer-key", anthropicBearerReq.Header.Get("Authorization"))
	require.Empty(t, anthropicBearerReq.Header.Get("x-api-key"))

	openAIReq, err := svc.buildOpenAIUpstreamModelsRequest(ctx, &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "openai-key",
			"base_url": "https://openai.example.com",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://openai.example.com/v1/models", openAIReq.URL.String())
	require.Equal(t, "Bearer openai-key", openAIReq.Header.Get("Authorization"))

	grokReq, err := svc.buildGrokUpstreamModelsRequest(ctx, &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "grok-key",
			"base_url": "https://api.x.ai/v1",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/models", grokReq.URL.String())
	require.Equal(t, "Bearer grok-key", grokReq.Header.Get("Authorization"))

	geminiReq, err := svc.buildGeminiUpstreamModelsRequest(ctx, &Account{
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "gemini-key",
			"base_url": "https://generativelanguage.googleapis.com/v1beta",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://generativelanguage.googleapis.com/v1beta/models", geminiReq.URL.String())
	require.Equal(t, "gemini-key", geminiReq.Header.Get("x-goog-api-key"))

	antigravityReq, err := svc.buildAntigravityAPIKeyModelsRequest(ctx, &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "antigravity-key",
			"base_url": "https://gateway.example.com/antigravity",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example.com/antigravity/v1/models", antigravityReq.URL.String())
	require.Equal(t, "antigravity-key", antigravityReq.Header.Get("x-api-key"))
}

func TestBuildOpenAIUpstreamModelsRequestSupportsOAuthPATAndAgentIdentity(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	svc := &AccountTestService{
		cfg:                 upstreamModelSyncTestConfig(),
		openAITokenProvider: NewOpenAITokenProvider(nil, nil, nil),
	}

	tests := []struct {
		name           string
		account        *Account
		wantAuthPrefix string
		wantToken      string
	}{
		{
			name: "ordinary oauth",
			account: &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
				"access_token": "oauth-model-token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"chatgpt_account_id": "account-oauth-models",
			}},
			wantAuthPrefix: "Bearer ",
			wantToken:      "oauth-model-token",
		},
		{
			name: "personal access token",
			account: &Account{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
				"auth_mode": OpenAIAuthModePersonalAccessToken, "access_token": "pat-model-token", "chatgpt_account_id": "account-pat-models",
			}},
			wantAuthPrefix: "Bearer ",
			wantToken:      "pat-model-token",
		},
		{
			name: "agent identity",
			account: &Account{ID: 33, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
				"auth_mode": OpenAIAuthModeAgentIdentity, "agent_runtime_id": key.runtimeID,
				"agent_private_key": privateKey, "task_id": key.taskID, "chatgpt_account_id": "account-agent-models",
			}},
			wantAuthPrefix: "AgentAssertion ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := svc.buildOpenAIUpstreamModelsRequest(context.Background(), tt.account)
			require.NoError(t, err)
			require.Equal(t, "https://chatgpt.com/backend-api/codex/models?client_version="+codexCanonicalClientVersion(), req.URL.String())
			require.Equal(t, "chatgpt.com", req.Host)
			require.Equal(t, "codex-tui", req.Header.Get("Originator"))
			require.Equal(t, codexCanonicalClientVersion(), req.Header.Get("Version"))
			require.Equal(t, codexCLIUserAgent, req.Header.Get("User-Agent"))
			require.Equal(t, tt.account.GetChatGPTAccountID(), req.Header.Get("chatgpt-account-id"))
			require.True(t, strings.HasPrefix(req.Header.Get("Authorization"), tt.wantAuthPrefix))
			if tt.wantToken != "" {
				require.Equal(t, "Bearer "+tt.wantToken, req.Header.Get("Authorization"))
			}
			require.NotContains(t, req.Header.Get("Authorization"), privateKey)
		})
	}
}

func TestFetchUpstreamSupportedModelsAgentIdentityRecoversInvalidTaskOnce(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	account := &Account{
		ID: 34, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode": OpenAIAuthModeAgentIdentity, "agent_runtime_id": key.runtimeID,
			"agent_private_key": privateKey, "task_id": "task-models-old", "chatgpt_account_id": "account-agent-models-recovery",
		},
	}
	repo := &accountTestAgentIdentityRepo{account: account}
	registerCalls := 0
	registerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		registerCalls++
		_, _ = io.WriteString(w, `{"task_id":"task-models-new"}`)
	}))
	defer registerServer.Close()
	oldBase := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = registerServer.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.4"},{"slug":"gpt-5.3-codex"}]}`))},
	}}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5.3-codex", "gpt-5.4"}, models)
	require.Equal(t, 1, registerCalls)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "task-models-old", decodeAgentAssertionTask(t, upstream.requests[0].Header.Get("Authorization")))
	require.Equal(t, "task-models-new", decodeAgentAssertionTask(t, upstream.requests[1].Header.Get("Authorization")))
	require.NotContains(t, upstream.requests[0].Header.Get("Authorization"), privateKey)
	require.Equal(t, 0, repo.setErrorCalls)
}

func TestBuildGrokUpstreamModelsRequestRejectsAmbiguousBaseURL(t *testing.T) {
	t.Parallel()

	svc := &AccountTestService{cfg: upstreamModelSyncTestConfig()}
	_, err := svc.buildGrokUpstreamModelsRequest(context.Background(), &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "grok-key",
			"base_url": "https://api.x.ai/v1?api_key=secret",
		},
	})
	require.Error(t, err)

	var syncErr *UpstreamModelSyncError
	require.True(t, errors.As(err, &syncErr))
	require.Equal(t, UpstreamModelSyncErrorConfiguration, syncErr.Kind)
	require.Contains(t, syncErr.SafeMessage(), "Invalid Grok base URL")
}

func TestBuildGrokUpstreamModelsRequestSupportsOAuth(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:       19,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":            "grok-oauth-token",
			"expires_at":              time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"base_url":                "https://api.x.ai/v1",
			"header_override_enabled": true,
			HeaderOverridesCredentialKey: map[string]any{
				"x-tenant": "tenant-a",
			},
		},
	}
	svc := &AccountTestService{
		cfg:               upstreamModelSyncTestConfig(),
		grokTokenProvider: NewGrokTokenProvider(nil, nil),
	}

	req, err := svc.buildGrokUpstreamModelsRequest(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/models", req.URL.String())
	require.Equal(t, "Bearer grok-oauth-token", req.Header.Get("Authorization"))
	require.Equal(t, []string{"tenant-a"}, req.Header["x-tenant"])
}

func TestBuildAntigravityAPIKeyModelsRequestRejectsOfficialCloudCodeBase(t *testing.T) {
	t.Parallel()

	svc := &AccountTestService{cfg: upstreamModelSyncTestConfig()}
	_, err := svc.buildAntigravityAPIKeyModelsRequest(context.Background(), &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "antigravity-key",
			"base_url": "https://cloudcode-pa.googleapis.com",
		},
	})
	require.Error(t, err)

	var syncErr *UpstreamModelSyncError
	require.True(t, errors.As(err, &syncErr))
	require.Equal(t, UpstreamModelSyncErrorUnsupported, syncErr.Kind)
	require.Contains(t, syncErr.SafeMessage(), "compatible gateway")
}

func TestBuildAnthropicUpstreamModelsRequestRejectsBedrock(t *testing.T) {
	t.Parallel()

	svc := &AccountTestService{cfg: upstreamModelSyncTestConfig()}
	_, err := svc.buildAnthropicUpstreamModelsRequest(context.Background(), &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeBedrock,
	})
	require.Error(t, err)

	var syncErr *UpstreamModelSyncError
	require.True(t, errors.As(err, &syncErr))
	require.Equal(t, UpstreamModelSyncErrorUnsupported, syncErr.Kind)
}

func TestFetchUpstreamSupportedModelsParsesOpenAIResponse(t *testing.T) {
	t.Parallel()

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5"},{"id":"gpt-5"},{"name":"o3"}]}`)),
	}}
	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          upstreamModelSyncTestConfig(),
	}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), &Account{
		ID:       7,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "openai-key",
			"base_url": "https://openai.example.com/v1",
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5", "o3"}, models)
	require.Equal(t, "https://openai.example.com/v1/models", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer openai-key", upstream.lastReq.Header.Get("Authorization"))
}

func TestFetchUpstreamSupportedModelsParsesGrokOAuthResponse(t *testing.T) {
	t.Parallel()

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"grok-4.5"},{"id":"grok-imagine-image"}]}`)),
	}}
	svc := &AccountTestService{
		httpUpstream:      upstream,
		cfg:               upstreamModelSyncTestConfig(),
		grokTokenProvider: NewGrokTokenProvider(nil, nil),
	}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), &Account{
		ID:       20,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "grok-oauth-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"base_url":     "https://api.x.ai/v1",
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"grok-4.5", "grok-imagine-image"}, models)
	require.Equal(t, xai.DefaultBaseURL+"/models", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer grok-oauth-token", upstream.lastReq.Header.Get("Authorization"))
}

func TestFetchUpstreamSupportedModelsDoesNotExposeUpstreamBody(t *testing.T) {
	t.Parallel()

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"SECRET_TOKEN should not be exposed"}`)),
	}}
	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          upstreamModelSyncTestConfig(),
	}

	_, err := svc.FetchUpstreamSupportedModels(context.Background(), &Account{
		ID:       8,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "openai-key",
			"base_url": "https://openai.example.com/v1",
		},
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRET_TOKEN")

	var syncErr *UpstreamModelSyncError
	require.True(t, errors.As(err, &syncErr))
	require.Equal(t, UpstreamModelSyncErrorUpstream, syncErr.Kind)
	require.NotContains(t, syncErr.SafeMessage(), "SECRET_TOKEN")
	require.Contains(t, syncErr.SafeMessage(), "HTTP 502")
}
