//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type grokUnauthorizedRetryRepo struct {
	*tokenRefreshAccountRepo
	updates map[int64]map[string]any
}

func (r *grokUnauthorizedRetryRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	if r.updates == nil {
		r.updates = make(map[int64]map[string]any)
	}
	r.updates[id] = updates
	return nil
}

func TestPatchGrokResponsesBodyUsesGrok45AndDropsUnsupportedFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"grok",
		"input":"hello",
		"prompt_cache_retention":"24h",
		"safety_identifier":"user-1",
		"presence_penalty":0.1,
		"frequency_penalty":0.2,
		"stop":["done"],
		"external_web_access":true,
		"tools":[
			{"type":"namespace","namespace":"functions"},
			{"type":"function","name":"kept_fn","parameters":{"type":"object"}}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))
	require.Equal(t, "grok-4.5", gjson.GetBytes(patched, "model").String())
	require.False(t, gjson.GetBytes(patched, "prompt_cache_retention").Exists())
	require.False(t, gjson.GetBytes(patched, "safety_identifier").Exists())
	require.False(t, gjson.GetBytes(patched, "presence_penalty").Exists())
	require.False(t, gjson.GetBytes(patched, "frequency_penalty").Exists())
	require.False(t, gjson.GetBytes(patched, "stop").Exists())
	require.NotContains(t, string(patched), "external_web_access")
	require.Len(t, gjson.GetBytes(patched, "tools").Array(), 1)
	require.Equal(t, "kept_fn", gjson.GetBytes(patched, "tools.0.name").String())
}

func TestPatchGrokResponsesBodyDropsRedundantViewImageForCurrentInlineImage(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"model":"grok-4.6",
		"input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}],
		"tools":[{"type":"function","name":"view_image"},{"type":"function","name":"shell_command"}]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.6")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, `tools.#(name=="view_image")`).Exists())
	require.Equal(t, "shell_command", gjson.GetBytes(patched, "tools.0.name").String())
}

func TestPatchGrokResponsesBodyKeepsExplicitViewImage(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}],
		"tools":[{"type":"function","name":"view_image"}],
		"tool_choice":{"type":"function","name":"view_image"}
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.6")
	require.NoError(t, err)
	require.Equal(t, "view_image", gjson.GetBytes(patched, "tools.0.name").String())
}

func TestStripRedundantGrokChatViewImageTool(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],
		"tools":[{"type":"function","function":{"name":"view_image"}},{"type":"function","function":{"name":"shell_command"}}]
	}`)

	patched, err := stripRedundantGrokChatViewImageTool(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, `tools.#(function.name=="view_image")`).Exists())
	require.Equal(t, "shell_command", gjson.GetBytes(patched, "tools.0.function.name").String())
}

func TestPatchGrokResponsesBodySanitizesComposerReasoningParameters(t *testing.T) {
	tests := []struct {
		name          string
		upstreamModel string
		wantReasoning bool
	}{
		{name: "composer fast", upstreamModel: "grok-composer-2.5-fast"},
		{name: "composer shorthand", upstreamModel: "grok-composer"},
		{name: "composer legacy alias", upstreamModel: "composer-2.5"},
		{name: "provider-prefixed composer", upstreamModel: "xai/grok-composer-2.5-fast"},
		{name: "grok 4.5", upstreamModel: "grok-4.5", wantReasoning: true},
		{name: "grok 4.6", upstreamModel: "grok-4.6", wantReasoning: true},
		{name: "grok 4.6 latest", upstreamModel: "grok-4.6-latest", wantReasoning: true},
	}
	body := []byte(`{"model":"grok","input":"hello","reasoning":{"effort":"medium"},"reasoning_effort":"medium","reasoningEffort":"medium"}`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patched, err := patchGrokResponsesBody(body, tt.upstreamModel)
			require.NoError(t, err)
			if tt.wantReasoning {
				require.Equal(t, "medium", gjson.GetBytes(patched, "reasoning.effort").String())
				require.Equal(t, "medium", gjson.GetBytes(patched, "reasoning_effort").String())
				return
			}
			require.False(t, gjson.GetBytes(patched, "reasoning").Exists())
			require.False(t, gjson.GetBytes(patched, "reasoning_effort").Exists())
			require.False(t, gjson.GetBytes(patched, "reasoningEffort").Exists())
		})
	}
}

func TestPatchGrokResponsesBodyNormalizesReasoningEffortAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		body          string
		upstreamModel string
		path          string
		want          string
	}{
		{name: "minimal nested", body: `{"input":"hi","reasoning":{"effort":"minimal"}}`, upstreamModel: "grok-4.5", path: "reasoning.effort", want: "low"},
		{name: "xhigh stays high for 4.5", body: `{"input":"hi","reasoning_effort":"xhigh"}`, upstreamModel: "grok-4.5", path: "reasoning_effort", want: "high"},
		{name: "xhigh nested for 4.6", body: `{"input":"hi","reasoning":{"effort":"xhigh"}}`, upstreamModel: "grok-4.6", path: "reasoning.effort", want: "xhigh"},
		{name: "xhigh snake for 4.6 latest", body: `{"input":"hi","reasoning_effort":"xhigh"}`, upstreamModel: "grok-4.6-latest", path: "reasoning_effort", want: "xhigh"},
		{name: "max camel stays high", body: `{"input":"hi","reasoningEffort":"max"}`, upstreamModel: "grok-4.6", path: "reasoning_effort", want: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patched, err := patchGrokResponsesBody([]byte(tt.body), tt.upstreamModel)
			require.NoError(t, err)
			require.Equal(t, tt.want, gjson.GetBytes(patched, tt.path).String(), string(patched))
			require.False(t, gjson.GetBytes(patched, "reasoningEffort").Exists())
		})
	}
}

func TestNormalizeGrokChatReasoningEffort(t *testing.T) {
	patched, err := normalizeGrokChatReasoningEffort([]byte(`{"reasoningEffort":"ultra"}`), "x-ai/grok-4.6")
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(patched, "reasoning_effort").String())
	require.False(t, gjson.GetBytes(patched, "reasoningEffort").Exists())

	patched, err = normalizeGrokChatReasoningEffort([]byte(`{"reasoning_effort":"xhigh"}`), "x-ai/grok-4.6")
	require.NoError(t, err)
	require.Equal(t, "xhigh", gjson.GetBytes(patched, "reasoning_effort").String())

	patched, err = normalizeGrokChatReasoningEffort([]byte(`{"reasoning_effort":"high"}`), "grok-composer-2.5-fast")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "reasoning_effort").Exists())
}

func TestPatchGrokResponsesBodyPreservesConversationToolsAndStructuredOutput(t *testing.T) {
	body := []byte(`{
		"model":"grok-latest",
		"instructions":"Answer as JSON.",
		"input":[
			{"role":"system","content":[{"type":"input_text","text":"system rule"}]},
			{"role":"user","content":[{"type":"input_text","text":"first"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"prior answer"}]},
			{"role":"user","content":[{"type":"input_text","text":"second"}]}
		],
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"id":{"type":"string"}}}}],
		"tool_choice":{"type":"function","name":"lookup"},
		"text":{"format":{"type":"json_schema","name":"answer","strict":true,"schema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}}}
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.Equal(t, "grok-4.5", gjson.GetBytes(patched, "model").String())
	require.Equal(t, "Answer as JSON.", gjson.GetBytes(patched, "instructions").String())
	require.Len(t, gjson.GetBytes(patched, "input").Array(), 4)
	require.Equal(t, "system", gjson.GetBytes(patched, "input.0.role").String())
	require.Equal(t, "lookup", gjson.GetBytes(patched, "tools.0.name").String())
	require.Equal(t, "lookup", gjson.GetBytes(patched, "tool_choice.name").String())
	require.Equal(t, "json_schema", gjson.GetBytes(patched, "text.format.type").String())
	require.True(t, gjson.GetBytes(patched, "text.format.strict").Bool())
}

func TestPatchGrokResponsesBodyPreservesReasoningAndLongConversation(t *testing.T) {
	longPrompt := strings.Repeat("long-context-segment-", 32*1024)
	payload := map[string]any{
		"model": "grok-latest",
		"input": []any{
			map[string]any{"role": "system", "content": "Keep all context."},
			map[string]any{"role": "user", "content": longPrompt},
			map[string]any{"role": "assistant", "content": "prior answer"},
			map[string]any{"role": "user", "content": "continue"},
		},
		"reasoning": map[string]any{
			"effort":  "high",
			"summary": "auto",
		},
		"max_output_tokens": 4096,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Greater(t, len(body), 512*1024)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.Equal(t, "grok-4.5", gjson.GetBytes(patched, "model").String())
	require.Len(t, gjson.GetBytes(patched, "input").Array(), 4)
	require.Equal(t, longPrompt, gjson.GetBytes(patched, "input.1.content").String())
	require.Equal(t, "high", gjson.GetBytes(patched, "reasoning.effort").String())
	require.Equal(t, "auto", gjson.GetBytes(patched, "reasoning.summary").String())
	require.EqualValues(t, 4096, gjson.GetBytes(patched, "max_output_tokens").Int())
}

func TestPatchGrokResponsesBodyDropsToolChoiceWhenReferencedToolWasFiltered(t *testing.T) {
	body := []byte(`{
		"model":"grok",
		"input":"hello",
		"tools":[{"type":"namespace","namespace":"removed"},{"type":"function","name":"kept","parameters":{"type":"object"}}],
		"tool_choice":{"type":"namespace","namespace":"removed"}
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.Len(t, gjson.GetBytes(patched, "tools").Array(), 1)
	require.Equal(t, "kept", gjson.GetBytes(patched, "tools.0.name").String())
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())
}

func TestPatchGrokResponsesBodyDropsAdditionalToolsInputCarrier(t *testing.T) {
	body := []byte(`{
		"model":"grok",
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
		],
		"tools":[{"type":"function","name":"lookup"}]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")

	require.NoError(t, err)
	require.Len(t, gjson.GetBytes(patched, "input").Array(), 1)
	require.Equal(t, "message", gjson.GetBytes(patched, "input.0.type").String())
	require.Equal(t, "lookup", gjson.GetBytes(patched, "tools.0.name").String(), "top-level tools must remain available")
}

func TestPatchGrokResponsesBodyRejectsInvalidJSON(t *testing.T) {
	_, err := patchGrokResponsesBody([]byte(`{"model":`), "grok-4.5")
	require.ErrorContains(t, err, "invalid json")
}

func TestForwardGrokResponsesRejectsImageGenerationModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-image-quality","input":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	account := &Account{
		ID:          52,
		Name:        "grok-image-on-responses",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-api-key"},
	}
	svc := &OpenAIGatewayService{}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-imagine-image-quality", false, time.Now())

	require.Error(t, err)
	require.Nil(t, result)
	require.ErrorContains(t, err, "is an image model and is not available on /v1/responses")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Contains(t, gjson.Get(recorder.Body.String(), "error.message").String(), "/v1/images/generations")
	require.True(t, IsResponseCommitted(c))
}

func TestIsGrokImageGenerationModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"grok-imagine", true},
		{"grok-imagine-image-quality", true},
		{"grok-imagine-edit", true},
		{"grok-imagine-image-hd", true},
		{"grok-imagine-video", false},
		{"grok-imagine-video-1.5", false},
		{"xai/grok-imagine-image-quality", true},
		{"grok-4.5", false},
		{"grok-composer", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokImageGenerationModel(tt.model))
		})
	}
}

func TestForwardGrokResponsesPreservesInvalidParameterError(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":"hello","temperature":"invalid"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          51,
		Name:        "grok-invalid-parameter",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "xai-api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"error":{"type":"invalid_request_error","param":"temperature","message":"temperature must be a number"}
		}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.5", false, time.Now())

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "temperature must be a number")
	require.Contains(t, recorder.Body.String(), "invalid_request_error")
	require.Equal(t, "invalid", gjson.GetBytes(upstream.lastBody, "temperature").String())
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestBuildGrokResponsesRequestUsesAccountBaseURLAndBearerToken(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")

	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": "https://xai.test/v1/",
		},
	}

	req, err := buildGrokResponsesRequest(context.Background(), nil, account, []byte(`{"model":"grok-4.5"}`), "access-token", "cache-identity", nil)
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, "https://xai.test/v1/responses", req.URL.String())
	require.Equal(t, "Bearer access-token", req.Header.Get("Authorization"))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Contains(t, req.Header.Get("Accept"), "text/event-stream")
	require.Equal(t, "cache-identity", req.Header.Get(grokConversationIDHeader))
}

func TestOpenAIGatewayServiceGetAccessTokenSupportsGrokAPIKey(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "xai-api-key",
		},
	}

	token, tokenType, err := svc.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "xai-api-key", token)
	require.Equal(t, "apikey", tokenType)
}

func TestResolveGrokCacheIdentityIsolatesDifferentAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok-4.5","input":"hello","prompt_cache_key":"shared-session"}`)

	buildContext := func(apiKeyID int64) *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Set("api_key", &APIKey{ID: apiKeyID})
		return c
	}

	firstKey := resolveGrokCacheIdentity(buildContext(7101), body, "", "grok-4.5")
	secondKey := resolveGrokCacheIdentity(buildContext(7102), body, "", "grok-4.5")
	require.NotEmpty(t, firstKey)
	require.NotEmpty(t, secondKey)
	require.NotEqual(t, firstKey, secondKey, "different main-site API keys must not share a Grok cache identity")
}

func TestForwardGrokResponsesStreamingUsesXAIAndCapturesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok","input":"hi","stream":true,"reasoning_effort":"high","prompt_cache_key":"shared-response-session"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("OpenAI-Beta", "responses=experimental")
	c.Set("api_key", &APIKey{ID: 7001})

	account := &Account{
		ID:          52,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":            "access-token",
			"expires_at":              time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"base_url":                xai.DefaultCLIBaseURL,
			"header_override_enabled": true,
			HeaderOverridesCredentialKey: map[string]any{
				"x-tenant": "tenant-a",
			},
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{52: account},
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_grok","model":"grok-4.5","usage":{"input_tokens":5,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"Xai-Request-Id":                 []string{"xai-stream-req"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"8"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
		codexSnapshotThrottle: &accountWriteThrottle{
			minInterval: time.Nanosecond,
			lastByID:    make(map[int64]time.Time),
		},
	}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", true, time.Now())
	require.NoError(t, err)
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Equal(t, []string{"tenant-a"}, upstream.lastReq.Header["x-tenant"])
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, resolveGrokCacheIdentity(c, body, "", "grok-4.5"), gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.True(t, result.Stream)
	require.Equal(t, "resp_grok", result.ResponseID)
	require.Equal(t, "xai-stream-req", result.RequestID)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.CacheReadInputTokens)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, recorder.Body.String(), "response.output_text.delta")
	require.NotNil(t, repo.updates[52][grokQuotaSnapshotExtraKey])
}

func TestForwardGrokResponsesAPIKeyUsesOfficialXAIEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":"hello","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	account := &Account{
		ID: 522, Name: "grok-api-key-responses", Platform: PlatformGrok,
		Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-responses-key", "base_url": xai.DefaultBaseURL},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"grok-apikey-responses"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_grok_apikey","object":"response","model":"grok-4.5","output":[],"usage":{"input_tokens":4,"output_tokens":2}}`,
		)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.5", false, time.Now())

	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-responses-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "resp_grok_apikey", result.ResponseID)
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Contains(t, recorder.Body.String(), "resp_grok_apikey")
}

func TestForwardGrokResponsesStreamReadFailureBeforeOutputTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":"hi","stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	account := &Account{
		ID:          521,
		Name:        "grok-stream-failure",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "xai-api-key",
			"base_url": xai.DefaultCLIBaseURL,
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &streamReadCloser{err: io.ErrUnexpectedEOF},
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.5", true, time.Now())

	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Empty(t, recorder.Body.String())
}

func TestForwardAsChatCompletionsForGrokUsesRawXAIChatEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{
		"model":"grok",
		"messages":[{"role":"system","content":"Return JSON."},{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"type":"object"}}},
		"prompt_cache_key":"body-cache-key",
		"stream":false
	}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("x-grok-conv-id", "chat-conversation")
	c.Set("api_key", &APIKey{ID: 7002})

	account := &Account{
		ID:          53,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"base_url":     xai.DefaultCLIBaseURL,
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{53: account},
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"application/json"},
			"X-Request-Id":                   []string{"chat-req"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"7"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl_grok","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":4,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":1}}}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg:               rawChatCompletionsTestConfig(),
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
		codexSnapshotThrottle: &accountWriteThrottle{
			minInterval: time.Nanosecond,
			lastByID:    make(map[int64]time.Time),
		},
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, resolveGrokCacheIdentity(c, body, "", "grok-4.5"), upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
	require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "tools.0.function.name").String())
	require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "tool_choice.function.name").String())
	require.Equal(t, "json_schema", gjson.GetBytes(upstream.lastBody, "response_format.type").String())
	require.Equal(t, "grok", result.Model)
	require.Equal(t, "grok-4.5", result.UpstreamModel)
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.Contains(t, recorder.Body.String(), "chatcmpl_grok")
	require.NotNil(t, repo.updates[53][grokQuotaSnapshotExtraKey])
}

func TestForwardAsChatCompletionsForGrokAPIKeyUsesOfficialXAIEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	account := &Account{
		ID: 532, Name: "grok-api-key-chat", Platform: PlatformGrok,
		Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-chat-key", "base_url": xai.DefaultBaseURL},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_grok_apikey","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")

	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-chat-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
	require.Contains(t, recorder.Body.String(), "chatcmpl_grok_apikey")
}

func TestForwardAsAnthropicForGrokUsesXAIResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{
		"model":"grok",
		"max_tokens":32,
		"stream":false,
		"system":"system rule",
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":"prior answer"},
			{"role":"user","content":"second"}
		]
	}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("x-grok-conv-id", "messages-conversation")
	c.Set("api_key", &APIKey{ID: 7003})

	account := &Account{
		ID:          54,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"base_url":     xai.DefaultCLIBaseURL,
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{54: account},
		},
	}
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_grok_messages", "grok-4.5")}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, resolveGrokCacheIdentity(c, nil, "", "grok-4.5"), gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.NotContains(t, string(upstream.lastBody), "chatgpt.com")
	require.Contains(t, string(upstream.lastBody), "system rule")
	require.Contains(t, string(upstream.lastBody), "prior answer")
	require.Equal(t, "grok", result.Model)
	require.Equal(t, "grok-4.5", result.UpstreamModel)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Contains(t, recorder.Body.String(), `"type":"message"`)
	require.Contains(t, recorder.Body.String(), "ok")
}

func TestForwardAsAnthropicForGrokAPIKeyStoresQuotaHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{
		"model":"grok-4.5",
		"max_tokens":32,
		"stream":false,
		"messages":[{"role":"user","content":"hello"}]
	}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	account := &Account{
		ID:          55,
		Name:        "grok-api-key",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "grok-api-key",
			"base_url": xai.DefaultBaseURL,
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{55: account},
		},
	}
	upstreamResponse := openAICompatSSECompletedResponse("resp_grok_messages_apikey", "grok-4.5")
	upstreamResponse.Header.Set("X-Ratelimit-Limit-Requests", "100")
	upstreamResponse.Header.Set("X-Ratelimit-Remaining-Requests", "99")
	upstream := &httpUpstreamRecorder{resp: upstreamResponse}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		accountRepo:  repo,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer grok-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.NotNil(t, repo.updates[55][grokQuotaSnapshotExtraKey])
}

func TestGrokResponsesWebSocketTurnUsesHTTPBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"grok","input":"hi","prompt_cache_key":"ws-conversation"}`)
	c.Set("api_key", &APIKey{ID: 7004})

	account := &Account{
		ID:          55,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"base_url":     xai.DefaultCLIBaseURL,
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{55: account},
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_ws_grok","model":"grok-4.5","usage":{"input_tokens":7,"output_tokens":2}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"9"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		accountRepo:  repo,
	}
	var events [][]byte
	cacheIdentity, err := resolveGrokWSCacheIdentity(c, account, payload, payload, "grok")
	require.NoError(t, err)
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(),
		c,
		account,
		"access-token",
		payload,
		len(payload),
		"grok",
		"",
		"",
		"",
		cacheIdentity,
		1,
		func(message []byte) error {
			events = append(events, append([]byte(nil), message...))
			return nil
		},
	)
	require.NoError(t, err)
	require.True(t, svc.shouldBridgeOpenAIWSHTTP(account, len(payload), "resp_previous"))
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, cacheIdentity, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Len(t, events, 2)
	require.Equal(t, "response.completed", gjson.GetBytes(events[1], "type").String())
	require.Equal(t, "resp_ws_grok", result.RequestID)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestGrokResponsesWebSocketTurnSupportsAPIKeyAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"grok-4.5","input":"hello"}`)
	account := &Account{
		ID: 552, Name: "grok-api-key-websocket", Platform: PlatformGrok,
		Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-ws-key", "base_url": xai.DefaultBaseURL},
	}
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_ws_grok_apikey","model":"grok-4.5","usage":{"input_tokens":5,"output_tokens":2}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	var events [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "xai-ws-key", payload, len(payload),
		"grok-4.5", "", "", "", "", 1,
		func(message []byte) error {
			events = append(events, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-ws-key", upstream.lastReq.Header.Get("Authorization"))
	require.Len(t, events, 2)
	require.Equal(t, "resp_ws_grok_apikey", result.RequestID)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestForwardGrokResponsesFreeFunctionToolsUseCacheCapableMixedRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"function","name":"web_search","parameters":{"type":"object"}}],"tool_choice":"auto","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 7010, Group: &Group{Platform: PlatformGrok}})

	account := &Account{
		ID: 7011, Name: "grok-free", Platform: PlatformGrok, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{
			"access_token":      "free-token",
			"subscription_tier": "free",
			"base_url":          xai.DefaultCLIBaseURL,
			"expires_at":        time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_cache","object":"response","model":"grok-4.5","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		accountRepo:       repo,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
	}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	tools := gjson.GetBytes(upstream.lastBody, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
}

func TestGrokResponsesWebSocketFreeFunctionToolsUseSameCacheRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"grok-4.5","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"function","name":"x_search","parameters":{"type":"object"}}],"tool_choice":"auto"}`)
	c.Set("api_key", &APIKey{ID: 7020, Group: &Group{Platform: PlatformGrok}})
	account := &Account{
		ID: 7021, Name: "grok-free-ws", Platform: PlatformGrok, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{
			"access_token":      "free-token",
			"subscription_tier": "free",
			"base_url":          xai.DefaultCLIBaseURL,
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_ws_cache","model":"grok-4.5","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	cacheIdentity, err := resolveGrokWSCacheIdentity(c, account, payload, payload, "grok-4.5")
	require.NoError(t, err)

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "free-token", payload, len(payload),
		"grok-4.5", "", "", "", cacheIdentity, 1, func([]byte) error { return nil },
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, cacheIdentity, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	tools := gjson.GetBytes(upstream.lastBody, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "x_search", tools[1].Get("type").String())
	require.Equal(t, "web_search", tools[2].Get("type").String())
}

func TestForwardGrokResponsesUnauthorizedRefreshesAndRetriesSameAccountOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(xai.EnvBaseURL, xai.DefaultCLIBaseURL)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok","input":"hi","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	account := &Account{
		ID:          56,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "revoked-token",
			"refresh_token": "refresh-token",
			// Grok refreshes one hour early. Keep this token outside that window so
			// the first request really exercises the upstream 401 refresh path.
			"expires_at": time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			"base_url":   xai.DefaultCLIBaseURL,
		},
	}
	baseRepo := &tokenRefreshAccountRepo{}
	baseRepo.accountsByID = map[int64]*Account{56: account}
	repo := &grokUnauthorizedRetryRepo{tokenRefreshAccountRepo: baseRepo}
	oauthSvc := NewGrokOAuthService(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: "refreshed-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	defer oauthSvc.Stop()
	provider := NewGrokTokenProvider(repo, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), NewGrokTokenRefresher(oauthSvc))

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"expired token"}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":                   []string{"application/json"},
				"Xai-Request-Id":                 []string{"retry-ok"},
				"X-Ratelimit-Limit-Requests":     []string{"10"},
				"X-Ratelimit-Remaining-Requests": []string{"9"},
			},
			Body: io.NopCloser(strings.NewReader(`{"id":"resp_retry","object":"response","model":"grok-4.5","output":[],"usage":{"input_tokens":3,"output_tokens":1}}`)),
		},
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: provider,
		accountRepo:       repo,
		codexSnapshotThrottle: &accountWriteThrottle{
			minInterval: time.Nanosecond,
			lastByID:    make(map[int64]time.Time),
		},
	}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", false, time.Now())
	require.NoError(t, err)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "Bearer revoked-token", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer refreshed-token", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, 1, baseRepo.updateCredentialsCalls)
	require.Equal(t, "resp_retry", result.ResponseID)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
}

func TestForwardGrokResponsesRepeatedUnauthorizedRefreshesOnlyOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(xai.EnvBaseURL, xai.DefaultCLIBaseURL)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok","input":"hi","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	account := &Account{
		ID:          57,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "revoked-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			"base_url":      xai.DefaultCLIBaseURL,
		},
	}
	baseRepo := &tokenRefreshAccountRepo{}
	baseRepo.accountsByID = map[int64]*Account{57: account}
	repo := &grokUnauthorizedRetryRepo{tokenRefreshAccountRepo: baseRepo}
	oauthSvc := NewGrokOAuthService(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: "refreshed-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	defer oauthSvc.Stop()
	provider := NewGrokTokenProvider(repo, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), NewGrokTokenRefresher(oauthSvc))

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"expired token"}}`)),
		},
		{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"refreshed token rejected"}}`)),
		},
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: provider,
		accountRepo:       repo,
	}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", false, time.Now())
	require.Nil(t, result)
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnauthorized, failoverErr.StatusCode)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "Bearer revoked-token", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer refreshed-token", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, 1, baseRepo.updateCredentialsCalls)
}

func TestHandleGrokAccountUpstreamError429SetsDurableRateLimit(t *testing.T) {
	account := &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"45"}}, nil,
	)

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Equal(t, account.ID, repo.lastRateLimitedID)
	require.WithinDuration(t, before.Add(45*time.Second), repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestHandleGrokAccountUpstreamError429UsesLatestExhaustedWindowReset(t *testing.T) {
	now := time.Now()
	requestReset := now.Add(10 * time.Minute).Truncate(time.Second)
	tokenReset := now.Add(20 * time.Minute).Truncate(time.Second)
	headers := http.Header{
		"X-Ratelimit-Limit-Requests":     []string{"10"},
		"X-Ratelimit-Remaining-Requests": []string{"0"},
		"X-Ratelimit-Reset-Requests":     []string{fmt.Sprintf("%d", requestReset.Unix())},
		"X-Ratelimit-Limit-Tokens":       []string{"1000"},
		"X-Ratelimit-Remaining-Tokens":   []string{"0"},
		"X-Ratelimit-Reset-Tokens":       []string{fmt.Sprintf("%d", tokenReset.Unix())},
	}
	account := &Account{ID: 62, Platform: PlatformGrok, Type: AccountTypeOAuth}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, nil)

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, tokenReset, repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestHandleGrokAccountUpstreamError429UsesFallbackReset(t *testing.T) {
	account := &Account{ID: 63, Platform: PlatformGrok, Type: AccountTypeOAuth}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, nil, nil)

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, before.Add(grokRateLimitFallbackCooldown), repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestGrokRateLimitResetAtUsesFutureWindowAfterRetryAfterExpires(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	observedAt := now.Add(-2 * time.Minute)
	windowReset := now.Add(15 * time.Minute)
	retryAfter := 30
	snapshot := &xai.QuotaSnapshot{
		StatusCode:        http.StatusTooManyRequests,
		UpdatedAt:         observedAt.Format(time.RFC3339),
		RetryAfterSeconds: &retryAfter,
		Requests: &xai.QuotaWindow{
			Limit:     grokInt64PtrForTest(10),
			Remaining: grokInt64PtrForTest(0),
			ResetUnix: grokInt64PtrForTest(windowReset.Unix()),
		},
	}

	resetAt, limited := grokRateLimitResetAt(snapshot, now)

	require.True(t, limited)
	require.WithinDuration(t, windowReset, resetAt, time.Second)
}

func TestUpdateGrokUsageSnapshotExhaustedSuccessBypassesThrottle(t *testing.T) {
	account := &Account{ID: 64, Platform: PlatformGrok, Type: AccountTypeOAuth}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{
		accountRepo:           repo,
		codexSnapshotThrottle: newAccountWriteThrottle(time.Hour),
	}
	now := time.Now()

	svc.updateGrokUsageSnapshot(context.Background(), account, &xai.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Requests: &xai.QuotaWindow{
			Limit: grokInt64PtrForTest(10), Remaining: grokInt64PtrForTest(9),
		},
		UpdatedAt: now.UTC().Format(time.RFC3339),
	})
	resetAt := now.Add(30 * time.Minute).Truncate(time.Second)
	svc.updateGrokUsageSnapshot(context.Background(), account, &xai.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Requests: &xai.QuotaWindow{
			Limit: grokInt64PtrForTest(10), Remaining: grokInt64PtrForTest(0),
			ResetUnix: grokInt64PtrForTest(resetAt.Unix()), ResetAt: resetAt.UTC().Format(time.RFC3339),
		},
		UpdatedAt: now.UTC().Format(time.RFC3339),
	})

	require.Equal(t, 2, repo.updateCalls)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, resetAt, repo.lastRateLimitResetAt, time.Second)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestUpdateGrokUsageSnapshotAvailableSuccessDoesNotRateLimit(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 65, Platform: PlatformGrok, Type: AccountTypeOAuth}

	svc.updateGrokUsageSnapshot(context.Background(), account, &xai.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Requests: &xai.QuotaWindow{
			Limit: grokInt64PtrForTest(10), Remaining: grokInt64PtrForTest(1),
		},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})

	require.Equal(t, 1, repo.updateCalls)
	require.Zero(t, repo.rateLimitedCalls)
}

func TestPatchGrokResponsesBody_StripsReasoningContentNull(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"model":"grok-latest",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}],"content":null,"encrypted_content":null},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))
	reasoning := gjson.GetBytes(patched, "input.1")
	require.Equal(t, "reasoning", reasoning.Get("type").String())
	require.True(t, reasoning.Get("summary").Exists())
	require.True(t, reasoning.Get("encrypted_content").Exists())
	require.False(t, reasoning.Get("content").Exists())
}

func TestPatchGrokResponsesBody_PreservesNonNullAndMultipleReasoningItems(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"model":"grok-latest",
		"input":[
			{"type":"reasoning","content":null,"summary":[]},
			{"type":"message","role":"user","content":null},
			{"type":"reasoning","content":"real content","summary":[]},
			{"type":"reasoning","content":null,"summary":[]}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	items := gjson.GetBytes(patched, "input").Array()
	require.Len(t, items, 4)
	require.False(t, items[0].Get("content").Exists())
	require.True(t, items[1].Get("content").Exists(), "non-reasoning null content must remain unchanged")
	require.Equal(t, "real content", items[2].Get("content").String())
	require.False(t, items[3].Get("content").Exists())
}
