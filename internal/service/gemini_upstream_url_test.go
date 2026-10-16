package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildGeminiAIStudioModelActionURL(t *testing.T) {
	const base = "https://generativelanguage.googleapis.com"
	got, err := buildGeminiAIStudioModelActionURL(base, "gemini-2.5-pro", "generateContent", false)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/gemini-2.5-pro:generateContent", got)
	got, err = buildGeminiAIStudioModelActionURL(base+"/", " gemini-2.5-flash ", "streamGenerateContent", true)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", got)
}

func TestBuildGeminiAIStudioModelActionURLRejectsUnsafeModel(t *testing.T) {
	const base = "https://generativelanguage.googleapis.com"
	for _, model := range []string{"../../x", "..", ".", `..\x`, "gemini?a=b", "gemini#x", "gemini%2f..", "gemini pro", "gemini@001", "models/gemini"} {
		_, err := buildGeminiAIStudioModelActionURL(base, model, "generateContent", false)
		require.Error(t, err, model)
		require.False(t, IsSafeGeminiModelPathSegment(model))
	}
	_, err := buildGeminiAIStudioModelActionURL(base, "gemini-2.5-pro", "deleteModel", false)
	require.Error(t, err)
}
