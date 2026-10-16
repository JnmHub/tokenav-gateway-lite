//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestV0162GrokEncryptedContentErrorShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"flat", `{"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content."}`, true},
		{"nested", `{"error":{"code":"invalid-argument","message":"Could not decrypt the provided encrypted_content."}}`, true},
		{"explicit code", `{"error":{"code":"invalid_encrypted_content","message":"rejected"}}`, true},
		{"wrong code", `{"code":"bad-request","error":"Could not decrypt the provided encrypted_content."}`, false},
		{"unrelated invalid argument", `{"code":"invalid-argument","error":"model is invalid"}`, false},
		{"ambiguous encrypted message", `{"error":{"message":"encrypted_content is invalid"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokInvalidEncryptedContentResponse(http.StatusBadRequest, []byte(tt.body)))
		})
	}
	require.False(t, isGrokInvalidEncryptedContentResponse(http.StatusUnauthorized, []byte(tests[0].body)))
}

func TestV0162GrokEncryptedReasoningDetectionAndSignatureStrip(t *testing.T) {
	require.True(t, requestHasGrokEncryptedReasoning([]byte(`{"input":{"type":"reasoning","encrypted_content":"cipher"}}`)))
	require.True(t, requestHasGrokEncryptedReasoning([]byte(`{"input":[{"type":"reasoning","encrypted_content":"cipher"}]}`)))
	require.False(t, requestHasGrokEncryptedReasoning([]byte(`{"input":[{"type":"reasoning","encrypted_content":""}]}`)))
	require.False(t, requestHasGrokEncryptedReasoning([]byte(`{"input":[{"type":"message","encrypted_content":"cipher"}]}`)))

	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"old-account"},{"type":"text","text":"answer","signature":"ordinary-field"}]},{"role":"user","content":"continue"}],"metadata":{"signature":"keep-top-level"}}`)
	stripped, changed := stripAnthropicThinkingSignatures(body)
	require.True(t, changed)
	require.Equal(t, "keep", gjson.GetBytes(stripped, "messages.0.content.0.thinking").String())
	require.False(t, gjson.GetBytes(stripped, "messages.0.content.0.signature").Exists())
	require.Equal(t, "ordinary-field", gjson.GetBytes(stripped, "messages.0.content.1.signature").String())
	require.Equal(t, "keep-top-level", gjson.GetBytes(stripped, "metadata.signature").String())

	ctx := markGrokEncryptedContentStripRetried(t.Context())
	require.True(t, grokEncryptedContentStripRetried(ctx))
	require.False(t, grokEncryptedContentStripRetried(t.Context()))
}

func TestV0162PatchGrokResponsesPromotesCodexLiteAdditionalTools(t *testing.T) {
	body := []byte(`{
		"model":"grok",
		"tools":[
			{"type":"function","name":"existing","description":"top-level wins"},
			{"type":"mcp","server_label":"docs"}
		],
		"tool_choice":"auto",
		"input":[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"function","name":"existing","description":"duplicate"},
				{"type":"function","name":"wait"},
				{"type":"mcp","server_label":"docs"},
				{"type":"shell"},
				{"type":"custom","name":"apply_patch"},
				{"type":"namespace","name":"collaboration"}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, `input.#(type=="additional_tools")`).Exists())
	require.Len(t, gjson.GetBytes(patched, "input").Array(), 1)
	tools := gjson.GetBytes(patched, "tools").Array()
	require.Len(t, tools, 4)
	require.Equal(t, "existing", tools[0].Get("name").String())
	require.Equal(t, "top-level wins", tools[0].Get("description").String())
	require.Equal(t, "docs", tools[1].Get("server_label").String())
	require.Equal(t, "wait", tools[2].Get("name").String())
	require.Equal(t, "shell", tools[3].Get("type").String())
	require.False(t, gjson.GetBytes(patched, `tools.#(type=="custom")`).Exists())
	require.False(t, gjson.GetBytes(patched, `tools.#(type=="namespace")`).Exists())
	require.Equal(t, "auto", gjson.GetBytes(patched, "tool_choice").String())
}
