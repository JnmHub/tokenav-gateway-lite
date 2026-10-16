package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSanitizeOpenAIResponsesToolParameterTypes(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"automation_update","parameters":{"type":null,"properties":{}}}]}`)
	original := append([]byte(nil), body...)
	sanitized, changed, err := sanitizeOpenAIResponsesToolParameterTypes(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "object", gjson.GetBytes(sanitized, "tools.0.parameters.type").String())
	require.Equal(t, original, body)
}

func TestSanitizeOpenAIResponsesToolParameterTypesLeavesMissingType(t *testing.T) {
	body := []byte(`{"tools":[{"parameters":{"properties":{}}}]}`)
	sanitized, changed, err := sanitizeOpenAIResponsesToolParameterTypes(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, sanitized)
}
