package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
)

func TestProbeOpenAIAPIKeyResponsesSupportCNProviders(t *testing.T) {
	tests := []struct {
		name        string
		id          int64
		platform    string
		protocol    string
		wantSupport bool
		wantMode    string
	}{
		{name: "DeepSeek adaptive 支持 Responses", id: 201, platform: PlatformDeepseek, protocol: APIProtocolAdaptive, wantSupport: true, wantMode: string(openai_compat.ResponsesSupportModeForceResponses)},
		{name: "DeepSeek Chat 清除强制 Responses", id: 202, platform: PlatformDeepseek, protocol: APIProtocolChatCompletions, wantSupport: false, wantMode: string(openai_compat.ResponsesSupportModeAuto)},
		{name: "Kimi adaptive 支持 Responses", id: 203, platform: PlatformKimi, protocol: APIProtocolAdaptive, wantSupport: true, wantMode: string(openai_compat.ResponsesSupportModeForceResponses)},
		{name: "Kimi 显式 Responses 落标强制", id: 205, platform: PlatformKimi, protocol: APIProtocolResponses, wantSupport: true, wantMode: string(openai_compat.ResponsesSupportModeForceResponses)},
		{name: "智谱 adaptive 回退 Chat", id: 204, platform: PlatformZhipu, protocol: APIProtocolAdaptive, wantSupport: false, wantMode: string(openai_compat.ResponsesSupportModeAuto)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updates := make(chan map[string]any, 1)
			account := Account{
				ID: test.id, Platform: test.platform, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-test", "api_protocol": test.protocol},
				Extra: map[string]any{
					openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceResponses),
				},
			}
			repo := &snapshotUpdateAccountRepo{
				stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
				updateExtraCalls:      updates,
			}
			service := &AccountTestService{accountRepo: repo}

			service.ProbeOpenAIAPIKeyResponsesSupport(context.Background(), account.ID)

			got := <-updates
			require.Equal(t, test.wantSupport, got[openai_compat.ExtraKeyResponsesSupported])
			require.Equal(t, test.wantMode, got[openai_compat.ExtraKeyResponsesMode])
		})
	}
}

func TestDecideResponsesProbeSupport(t *testing.T) {
	fnCall := []byte(`{"output":[{"type":"reasoning"},{"type":"function_call","name":"probe_ping"}]}`)
	reasoningOnly := []byte(`{"output":[{"type":"reasoning"}]}`)

	cases := []struct {
		name   string
		status int
		body   []byte
		want   bool
	}{
		// Endpoint clearly absent on third-party OpenAI-compatible upstreams.
		{"404 endpoint absent", 404, fnCall, false},
		{"405 method not allowed", 405, fnCall, false},
		// 2xx: tool capability is judged by presence of a function_call output item.
		{"200 with function_call", 200, fnCall, true},
		// Volcengine Ark coding/v3 × kimi-k2.6: reasoning only, no function_call.
		{"200 reasoning only", 200, reasoningOnly, false},
		{"200 invalid json", 200, []byte("not-json"), false},
		{"200 no output field", 200, []byte(`{"status":"completed"}`), false},
		// Non-2xx (other than 404/405): endpoint exists, capability undecidable -> conservative true.
		{"400 conservative true", 400, reasoningOnly, true},
		{"401 conservative true", 401, nil, true},
		{"500 conservative true", 500, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, decideResponsesProbeSupport(tc.status, tc.body))
		})
	}
}

func TestResponsesProbeBodyHasFunctionCall(t *testing.T) {
	require.True(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[{"type":"function_call"}]}`)))
	require.True(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[{"type":"reasoning"},{"type":"function_call"}]}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[{"type":"reasoning"}]}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[]}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`{}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`garbage`)))
}

func TestResponsesProbeVerdictIsConclusive(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"completed", 200, `{"status":"completed","output":[]}`, true},
		{"incomplete max output tokens", 200, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, false},
		{"incomplete content filter", 200, `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`, true},
		{"failed in 200", 200, `{"status":"failed"}`, false},
		{"missing status", 200, `{"output":[]}`, true},
		{"non json", 200, `not-json`, true},
		{"404 ignores response status", 404, `{"status":"failed"}`, true},
		{"500 ignores response status", 500, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, responsesProbeVerdictIsConclusive(tt.status, []byte(tt.body)))
		})
	}
}

func TestSelectResponsesProbeModel(t *testing.T) {
	// No model_mapping -> fall back to DefaultTestModel (OpenAI official APIKey).
	require.Equal(t, openai.DefaultTestModel, selectResponsesProbeModel(&Account{}))

	// model_mapping values are upstream models; pick first by sort for reproducibility.
	acct := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{
			"client-b": "zeta-model",
			"client-a": "alpha-model",
		},
	}}
	require.Equal(t, "alpha-model", selectResponsesProbeModel(acct))

	// Wildcard / blank upstream values are skipped.
	acctWild := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{
			"a": "*",
			"b": "  ",
			"c": "real-model",
		},
	}}
	require.Equal(t, "real-model", selectResponsesProbeModel(acctWild))

	// Only wildcard mappings -> DefaultTestModel.
	acctAllWild := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{"a": "gpt-*"},
	}}
	require.Equal(t, openai.DefaultTestModel, selectResponsesProbeModel(acctAllWild))
}
