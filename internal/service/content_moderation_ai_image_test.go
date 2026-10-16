package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func tinyDataURL(t *testing.T, payload string) string {
	t.Helper()
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(payload))
}

func TestContentModerationAIImageExtractionAndReview(t *testing.T) {
	cfg := defaultContentModerationAIConfig()
	body := `{"input":[{"role":"user","content":[{"type":"input_text","text":"look at this"},{"type":"input_image","image_url":"` + tinyDataURL(t, "img-bytes-a") + `"},{"type":"input_image","image_url":"` + tinyDataURL(t, "img-bytes-b") + `"}]}]}`
	input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(body), cfg)
	require.NoError(t, err)
	require.Len(t, input.Images, 2)
	require.Equal(t, "i0001", input.Images[0].ID)
	require.Equal(t, "image/png", input.Images[0].MediaType)
	require.Equal(t, "i0002", input.Images[1].ID)
	require.Len(t, input.Fragments, 1)
	require.Equal(t, "look at this", input.Fragments[0].Text)
	require.Equal(t, body, string(input.ForwardBody))
}

func TestContentModerationAIImageNoLocalCountOrByteLimit(t *testing.T) {
	// 超过旧版 64 张和 4 MiB 单图边界仍完整抽取，由实际模型能力决定是否可处理。
	parts := make([]any, 0, 65)
	for i := 0; i < 64; i++ {
		parts = append(parts, map[string]any{"type": "input_image", "image_url": tinyDataURL(t, "small image")})
	}
	large := strings.Repeat("x", 4*1024*1024+1)
	parts = append(parts, map[string]any{"type": "input_image", "image_url": tinyDataURL(t, large)})
	body := []byte(aiJSON(map[string]any{"input": []any{map[string]any{"role": "user", "content": parts}}}))
	input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, body, defaultContentModerationAIConfig())
	require.NoError(t, err)
	require.Len(t, input.Images, 65)
	require.Equal(t, len(large), input.Images[64].Bytes)
	require.Equal(t, body, input.ForwardBody)
}

func TestContentModerationAIImageDisabledSkipsWithoutDecoding(t *testing.T) {
	cfg := defaultContentModerationAIConfig()
	cfg.ImageReviewEnabled = false
	for _, imageURL := range []string{tinyDataURL(t, "img"), "data:image/png;base64,%%%", "data:image/png", "https://example.invalid/image"} {
		body := `{"input":[{"role":"user","content":[{"type":"input_text","text":"safe text"},{"type":"input_image","image_url":"` + imageURL + `"}]}]}`
		input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(body), cfg)
		require.NoError(t, err)
		require.Empty(t, input.Images, "disabled review must not carry image bytes")
		require.Len(t, input.Fragments, 1)
		require.Contains(t, input.UncoveredFields, "image_skipped:input/0/content/1")
		require.Equal(t, body, string(input.ForwardBody))
	}
}

func TestContentModerationAIImageMalformedDataURIPreservesEvidence(t *testing.T) {
	for _, imageURL := range []string{
		"data:image/png", "data:image/png,bytes", "data:image/png;base64", "data:image/png;base64,%%%", "data:image/;base64,YQ==",
	} {
		t.Run(imageURL, func(t *testing.T) {
			body := `{"input":[{"role":"user","content":[{"type":"input_text","text":"preserved safe text"},{"type":"input_image","image_url":"` + imageURL + `"}]}]}`
			var input *ContentModerationAICanonicalInput
			var err error
			require.NotPanics(t, func() {
				input, err = CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(body), defaultContentModerationAIConfig())
			})
			require.Error(t, err)
			require.NotNil(t, input)
			require.Equal(t, body, string(input.ForwardBody))
			require.Len(t, input.Fragments, 1)
			require.Equal(t, "preserved safe text", input.Fragments[0].Text)
			require.NotEmpty(t, input.InputHash)
			code, _, _ := contentModerationAIErrorDetails(err)
			require.Equal(t, "ai_invalid_image_encoding", code)
		})
	}
}

func TestContentModerationAIImageFailureKeepsAllKnownText(t *testing.T) {
	body := []byte(`{"input":[{"type":"input_image","image_url":"data:image/png;base64,%%%","description":"local image description"},{"type":"input_text","text":"text after malformed image"},{"type":"input_image","image_url":"https://example.invalid/image","text":"text beside remote image"}]}`)
	input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, body, defaultContentModerationAIConfig())
	require.Error(t, err)
	code, _, _ := contentModerationAIErrorDetails(err)
	require.Equal(t, "ai_invalid_image_encoding", code)
	require.Equal(t, body, input.ForwardBody)
	require.Empty(t, input.Images)
	require.Len(t, input.Fragments, 3)
	for _, text := range []string{"local image description", "text after malformed image", "text beside remote image"} {
		require.Contains(t, aiJSON(input.Fragments), text)
	}
	require.Contains(t, input.UncoveredFields, "image_invalid:input/0")
	require.Contains(t, input.UncoveredFields, "image_not_inline:input/2")
}

func TestContentModerationAIImageSwitchPreservesKnownTextAndInlineIdentity(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := defaultContentModerationAIConfig()
		cfg.ImageReviewEnabled = enabled
		body := []byte(`{"input":[{"type":"input_image","inline_data":{"mime_type":"image/png","data":"YQ=="},"text":"visible local text"}]}`)
		input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, body, cfg)
		require.NoError(t, err)
		require.Len(t, input.Fragments, 1)
		require.Equal(t, "visible local text", input.Fragments[0].Text)
		if enabled {
			require.Len(t, input.Images, 1, "a single image block must not attach its inline_data twice")
		} else {
			require.Empty(t, input.Images)
			require.Contains(t, input.UncoveredFields, "image_skipped:input/0")
		}
	}
}

func TestContentModerationAIImagePayloadShapes(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, body, mediaType string
	}{
		{"chat", ContentModerationProtocolOpenAIChat, `{"messages":[{"role":"user","content":[{"type":"text","text":"chat img"},{"type":"image_url","image_url":{"url":"` + tinyDataURL(t, "chat-img") + `"}}]}]}`, "image/png"},
		{"gemini snake case", ContentModerationProtocolGemini, `{"contents":[{"role":"user","parts":[{"text":"gemini img"},{"inline_data":{"mime_type":"image/jpeg","data":"` + base64.StdEncoding.EncodeToString([]byte("gemini-img")) + `"}}]}]}`, "image/jpeg"},
		{"gemini camel case", ContentModerationProtocolGemini, `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/webp","data":"` + base64.StdEncoding.EncodeToString([]byte("gemini-img")) + `"}}]}]}`, "image/webp"},
		{"anthropic", ContentModerationProtocolAnthropicMessages, `{"messages":[{"role":"user","content":[{"type":"text","text":"anthropic img"},{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"` + base64.StdEncoding.EncodeToString([]byte("anthropic-img")) + `"}}]}]}`, "image/webp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := CanonicalContentModerationAIInput(tc.protocol, []byte(tc.body), defaultContentModerationAIConfig())
			require.NoError(t, err)
			require.Len(t, input.Images, 1)
			require.Equal(t, tc.mediaType, input.Images[0].MediaType)
			require.Equal(t, tc.body, string(input.ForwardBody))
		})
	}
}

func TestContentModerationAIRemoteImagesAndNonTextRemainUncovered(t *testing.T) {
	for _, tc := range []struct{ body, uncovered string }{
		{`{"input":[{"type":"input_image","image_url":"https://example.invalid/image"}]}`, "image_not_inline:input/0"},
		{`{"input":[{"type":"input_file","file_id":"remote-file"}]}`, "media:input_file"},
		{`{"input":[{"type":"input_audio","data":"opaque audio"}]}`, "media:input_audio"},
		{`{"input":[{"type":"vendor_blob","data":"opaque bytes"}]}`, "content_type:vendor_blob"},
	} {
		input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(tc.body), defaultContentModerationAIConfig())
		require.NoError(t, err)
		require.Empty(t, input.Images)
		require.Empty(t, input.Fragments)
		require.Contains(t, input.UncoveredFields, tc.uncovered)
	}
}

func TestContentModerationAIMultimodalEndToEndReview(t *testing.T) {
	provider, calls, _ := aiSyntheticProvider(t, ContentModerationAIAdapterChat, nil, 200, 0)
	defer provider.Close()
	cfg := aiTestConfig(provider.URL, ContentModerationAIAdapterChat)
	svc := &ContentModerationService{}
	svc.SetAIStore(&aiTestStore{})
	in := aiTestInput(`{"input":[{"role":"user","content":[{"type":"input_text","text":"safe synthetic"},{"type":"input_image","image_url":"` + tinyDataURL(t, "img") + `"}]}]}`)
	d := svc.checkAI(context.Background(), in, cfg, false)
	require.Equal(t, "allow", d.Verdict, "code=%s", d.ErrorCode)
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, 1, d.Coverage.ImageCount)
	require.Equal(t, 1, d.Coverage.FragmentCount)
	require.True(t, d.Coverage.Complete)
	require.True(t, d.ProviderCalled)
	require.Empty(t, d.Categories)
	require.NotEmpty(t, d.Reason)
	wire := aiJSON(d)
	require.NotContains(t, wire, `"fragments":`, "overall result must not imply independent fragment verdicts")
}

func TestContentModerationAIOnlyUncoveredContentSkipsProvider(t *testing.T) {
	provider, calls, _ := aiSyntheticProvider(t, ContentModerationAIAdapterChat, nil, 200, 0)
	defer provider.Close()
	cfg := aiTestConfig(provider.URL, ContentModerationAIAdapterChat)
	cfg.AI.ImageReviewEnabled = false
	svc := &ContentModerationService{}
	svc.SetAIStore(&aiTestStore{})
	in := aiTestInput(`{"input":[{"type":"input_image","image_url":"data:image/png;base64,%%%"}]}`)
	d := svc.checkAI(context.Background(), in, cfg, false)
	require.Equal(t, "allow", d.Verdict)
	require.True(t, d.Allowed)
	require.False(t, d.ProviderCalled)
	require.False(t, d.Coverage.Complete)
	require.Equal(t, "skipped", d.Phase)
	require.Zero(t, calls.Load())
	require.Contains(t, d.Coverage.UncoveredFields, "image_skipped:input/0")
}
