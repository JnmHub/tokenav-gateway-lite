//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGrokMediaGenerationGateCoversImagesAndVideo(t *testing.T) {
	tests := []struct {
		name     string
		endpoint GrokMediaEndpoint
		want     bool
	}{
		{name: "image generation", endpoint: GrokMediaEndpointImagesGenerations, want: true},
		{name: "image edit", endpoint: GrokMediaEndpointImagesEdits, want: true},
		{name: "video generation", endpoint: GrokMediaEndpointVideosGenerations, want: true},
		{name: "video edit", endpoint: GrokMediaEndpointVideosEdits, want: true},
		{name: "video extension", endpoint: GrokMediaEndpointVideosExtensions, want: true},
		{name: "video status", endpoint: GrokMediaEndpointVideoStatus, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.endpoint.IsGenerationRequest())
		})
	}
}

func TestGrokMediaVideoPathHelpersRejectUnsafeRequestIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/req_123/content", nil)

	require.NotEmpty(t, GrokMediaVideoRequestSessionHash("req_123"))
	require.Equal(t, "/v1/videos/req_123/content", grokMediaContentProxyURL(context, "req_123"))
	require.True(t, isGrokMediaVideoContentURL("https://api.x.ai/v1/videos/req_123/content", "req_123"))

	for _, requestID := range []string{"../admin", "req/id", `req\\id`, "req id", "req%2fid", "模型"} {
		require.Empty(t, GrokMediaVideoRequestSessionHash(requestID), requestID)
		require.Empty(t, grokMediaContentProxyURL(context, requestID), requestID)
		require.False(t, isGrokMediaVideoContentURL("https://api.x.ai/v1/videos/req_123/content", requestID), requestID)
	}
}

func TestValidateAndNormalizeGrokVideoTransforms(t *testing.T) {
	validEdit := []byte(`{"model":"grok-imagine-video","prompt":"add a hat","video":{"video_url":"data:video/mp4;base64,aWQ="}}`)
	require.NoError(t, ValidateGrokMediaRequest(GrokMediaEndpointVideosEdits, "application/json", validEdit))
	editInfo := ParseGrokMediaRequest("application/json", validEdit)
	require.Len(t, editInfo.MediaInputs, 1)
	require.Equal(t, GrokMediaInputRoleVideo, editInfo.MediaInputs[0].Role)
	require.False(t, editInfo.HasInputImage())
	require.False(t, gjson.GetBytes(editInfo.ModerationBody(), "images").Exists(), "MP4 must not be sent as an image moderation input")
	require.Equal(t, 0, GrokMediaTaskDurationSeconds(GrokMediaEndpointVideosEdits, editInfo))
	require.Equal(t, VideoBillingMaxDurationSeconds, GrokMediaReservationDurationSeconds(GrokMediaEndpointVideosEdits, editInfo))

	validExtension := []byte(`{"model":"grok-imagine-video","prompt":"continue","seconds":5,"video":{"url":"https://example.com/source.mp4"}}`)
	require.NoError(t, ValidateGrokMediaRequest(GrokMediaEndpointVideosExtensions, "application/json", validExtension))
	extensionInfo := ParseGrokMediaRequest("application/json", validExtension)
	require.Equal(t, 5, GrokMediaTaskDurationSeconds(GrokMediaEndpointVideosExtensions, extensionInfo))
	require.Equal(t, VideoBillingResolution720P, GrokMediaBillingResolution(GrokMediaEndpointVideosExtensions, extensionInfo))

	defaultExtension := ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-video","prompt":"continue","video":{"file_id":"file-video-1"}}`))
	require.Equal(t, grokVideoExtensionDefaultSeconds, GrokMediaTaskDurationSeconds(GrokMediaEndpointVideosExtensions, defaultExtension))

	tests := []struct {
		name     string
		endpoint GrokMediaEndpoint
		body     string
		want     string
	}{
		{name: "video must be object", endpoint: GrokMediaEndpointVideosEdits, body: `{"model":"grok-imagine-video","prompt":"edit","video":"https://example.com/a.mp4"}`, want: "video must be an object"},
		{name: "conflicting aliases", endpoint: GrokMediaEndpointVideosEdits, body: `{"model":"grok-imagine-video","prompt":"edit","video":{"url":"https://example.com/a.mp4","video_url":"https://example.com/b.mp4"}}`, want: "conflicting url and video_url"},
		{name: "file and URL conflict", endpoint: GrokMediaEndpointVideosEdits, body: `{"model":"grok-imagine-video","prompt":"edit","video":{"url":"https://example.com/a.mp4","file_id":"file-1"}}`, want: "mutually exclusive"},
		{name: "private URL", endpoint: GrokMediaEndpointVideosEdits, body: `{"model":"grok-imagine-video","prompt":"edit","video":{"url":"http://127.0.0.1/a.mp4"}}`, want: "public http/https"},
		{name: "non mp4 URL", endpoint: GrokMediaEndpointVideosEdits, body: `{"model":"grok-imagine-video","prompt":"edit","video":{"url":"https://example.com/a.mov"}}`, want: "end with .mp4"},
		{name: "edit rejects ignored duration", endpoint: GrokMediaEndpointVideosEdits, body: `{"model":"grok-imagine-video","prompt":"edit","duration":5,"video":{"url":"https://example.com/a.mp4"}}`, want: "not supported for video edits"},
		{name: "extension duration low", endpoint: GrokMediaEndpointVideosExtensions, body: `{"model":"grok-imagine-video","prompt":"extend","duration":1,"video":{"url":"https://example.com/a.mp4"}}`, want: "between 2 and 10"},
		{name: "extension duration high", endpoint: GrokMediaEndpointVideosExtensions, body: `{"model":"grok-imagine-video","prompt":"extend","duration":11,"video":{"url":"https://example.com/a.mp4"}}`, want: "between 2 and 10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGrokMediaRequest(tt.endpoint, "application/json", []byte(tt.body))
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestValidateGrokMediaResolvedHostsRejectsPrivateAndReboundDNS(t *testing.T) {
	info := ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-video","reference_images":[{"url":"https://media.example/image.png?X-Amz-Signature=must-not-leak"}]}`))
	public := []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}
	private := []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}

	require.NoError(t, validateGrokMediaResolvedHosts(context.Background(), info, func(context.Context, string) ([]net.IPAddr, error) {
		return public, nil
	}))

	err := validateGrokMediaResolvedHosts(context.Background(), info, func(context.Context, string) ([]net.IPAddr, error) {
		return append(public, private...), nil
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "media.example")
	require.NotContains(t, err.Error(), "must-not-leak")
	require.NotContains(t, err.Error(), "127.0.0.1")

	calls := 0
	rebindingLookup := func(context.Context, string) ([]net.IPAddr, error) {
		calls++
		if calls == 1 {
			return public, nil
		}
		return private, nil
	}
	require.NoError(t, validateGrokMediaResolvedHosts(context.Background(), info, rebindingLookup))
	require.Error(t, validateGrokMediaResolvedHosts(context.Background(), info, rebindingLookup), "forward-phase DNS recheck must reject a public-to-private rebind")
}

func TestValidateGrokMediaRequestContextChecksLiteralAndSkipsDataURLDNS(t *testing.T) {
	publicBody := []byte(`{"model":"grok-imagine-video","image":{"url":"https://93.184.216.34/image.png"}}`)
	require.NoError(t, ValidateGrokMediaRequestContext(context.Background(), GrokMediaEndpointVideosGenerations, "application/json", publicBody))

	privateBody := []byte(`{"model":"grok-imagine-video","image":{"url":"https://127.0.0.1/image.png?token=must-not-leak"}}`)
	err := ValidateGrokMediaRequestContext(context.Background(), GrokMediaEndpointVideosGenerations, "application/json", privateBody)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "must-not-leak")

	dataInfo := ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-video","image":{"url":"data:image/png;base64,YQ=="}}`))
	lookupCalled := false
	require.NoError(t, validateGrokMediaResolvedHosts(context.Background(), dataInfo, func(context.Context, string) ([]net.IPAddr, error) {
		lookupCalled = true
		return nil, errors.New("must not be called")
	}))
	require.False(t, lookupCalled)
}

func TestGrokMediaCapabilityFiltersAccountsByMappedModel(t *testing.T) {
	textOnly := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"grok-4.5": "grok-4.5"},
		},
	}
	media := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"grok-imagine": "grok-imagine-image-quality"},
		},
	}

	require.False(t, accountSupportsOpenAICapabilities(textOnly, "", OpenAIImagesCapabilityBasic, "grok-imagine"))
	require.True(t, accountSupportsOpenAICapabilities(media, "", OpenAIImagesCapabilityBasic, "grok-imagine"))
}

func TestSupportsGrokMediaAccountRequiresGrokPlatform(t *testing.T) {
	require.True(t, supportsGrokMediaAccount(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth}))
	require.True(t, supportsGrokMediaAccount(&Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}))
	require.False(t, supportsGrokMediaAccount(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}))
	require.False(t, supportsGrokMediaAccount(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
	require.False(t, supportsGrokMediaAccount(&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}))
}

func TestGrokMediaAccountBaseURLSeparatesOAuthMediaFromText(t *testing.T) {
	oauth := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": xai.DefaultCLIBaseURL,
		},
	}
	apiKey := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://grok-key-relay.example.com/v1",
		},
	}

	require.Equal(t, xai.DefaultCLIBaseURL, oauth.GetGrokBaseURL(), "OAuth text keeps using the CLI proxy")
	require.Equal(t, xai.DefaultBaseURL, grokMediaAccountBaseURL(oauth), "OAuth media uses the official Imagine API")
	require.Equal(t, "https://grok-key-relay.example.com/v1", grokMediaAccountBaseURL(apiKey), "API Key custom upstream remains unchanged")
}

func TestExtractGrokMediaModelSupportsJSONAndMultipart(t *testing.T) {
	require.Equal(t, "grok-imagine", ExtractGrokMediaModel("application/json", []byte(`{"model":"grok-imagine"}`)))

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("prompt", "draw a cat"))
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	require.NoError(t, writer.Close())

	require.Equal(t, "grok-imagine-edit", ExtractGrokMediaModel(writer.FormDataContentType(), buf.Bytes()))
}

func TestParseGrokMediaRequestBuildsMultipartModerationBody(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("prompt", "edit this private image"))
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", `form-data; name="image"; filename="input.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader)
	require.NoError(t, err)
	_, err = part.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	info := ParseGrokMediaRequest(writer.FormDataContentType(), buf.Bytes())
	require.Equal(t, "grok-imagine-edit", info.Model)
	require.Equal(t, "edit this private image", info.Prompt)

	moderationBody := info.ModerationBody()
	require.NotEmpty(t, moderationBody)
	require.Equal(t, "edit this private image", gjson.GetBytes(moderationBody, "prompt").String())
	require.True(t, strings.HasPrefix(gjson.GetBytes(moderationBody, "images.0.image_url").String(), "data:image/"))
}

func TestParseGrokMediaVideoRequestResolution(t *testing.T) {
	info := ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-video","prompt":"waves","resolution":"720p"}`))

	require.Equal(t, "grok-imagine-video", info.Model)
	require.Equal(t, "720p", info.Resolution)
}

func TestParseGrokMediaRequestUsesOneOrderedMediaInputSet(t *testing.T) {
	body := []byte(`{
		"model":"grok-imagine-video-1.5",
		"image":{"url":"https://example.com/source.png"},
		"images":[{"image_url":"https://example.com/legacy.png"},"https://example.com/string.png"],
		"reference_images":[{"url":"https://example.com/reference.png"}],
		"mask":{"url":"data:image/png;base64,aW1n"}
	}`)

	info := ParseGrokMediaRequest("application/json", body)
	require.Len(t, info.MediaInputs, 5)
	require.Equal(t, []string{
		"https://example.com/source.png",
		"https://example.com/legacy.png",
		"https://example.com/string.png",
		"https://example.com/reference.png",
		"data:image/png;base64,aW1n",
	}, []string{
		info.MediaInputs[0].URL,
		info.MediaInputs[1].URL,
		info.MediaInputs[2].URL,
		info.MediaInputs[3].URL,
		info.MediaInputs[4].URL,
	})
	for index, input := range info.MediaInputs {
		require.Equal(t, index, input.Order)
		require.NotEmpty(t, input.SourceField)
		require.NotEmpty(t, input.MediaType)
	}
	require.Equal(t, GrokMediaInputRoleReference, info.MediaInputs[3].Role)
	require.Equal(t, GrokMediaInputRoleMask, info.MediaInputs[4].Role)
	require.True(t, info.HasInputImage())

	moderationBody := info.ModerationBody()
	for index, input := range info.MediaInputs {
		require.Equal(t, input.URL, gjson.GetBytes(moderationBody, "images."+strconv.Itoa(index)+".image_url").String())
	}
}

func TestValidateGrokMediaImageInputBoundaries(t *testing.T) {
	tooMany := make([]map[string]string, 0, grokMediaMaxInputImages+1)
	for index := 0; index <= grokMediaMaxInputImages; index++ {
		tooMany = append(tooMany, map[string]string{"url": "https://example.com/image-" + strconv.Itoa(index) + ".png"})
	}
	tooManyBody, err := json.Marshal(map[string]any{"model": "grok-imagine-video", "reference_images": tooMany})
	require.NoError(t, err)

	tests := []struct {
		name string
		body []byte
		want string
	}{
		{name: "conflicting aliases", body: []byte(`{"model":"grok-imagine-video","image":{"url":"https://example.com/a.png","image_url":"https://example.com/b.png"}}`), want: "conflicting url and image_url"},
		{name: "non string alias", body: []byte(`{"model":"grok-imagine-video","image":{"url":123}}`), want: "image.url must be a string"},
		{name: "empty object", body: []byte(`{"model":"grok-imagine-video","image":{}}`), want: "image.url is required"},
		{name: "nested array", body: []byte(`{"model":"grok-imagine-video","reference_images":[[]]}`), want: "reference_images[0] must be a string or object"},
		{name: "unsupported protocol", body: []byte(`{"model":"grok-imagine-video","image":{"url":"ftp://example.com/a.png"}}`), want: "public http/https URL"},
		{name: "private literal", body: []byte(`{"model":"grok-imagine-video","image":{"url":"http://127.0.0.1/a.png"}}`), want: "public http/https URL"},
		{name: "signed URL credentials", body: []byte(`{"model":"grok-imagine-video","image":{"url":"https://user:secret@example.com/a.png"}}`), want: "without credentials or fragment"},
		{name: "invalid data mime", body: []byte(`{"model":"grok-imagine-video","image":{"url":"data:text/plain;base64,aW1n"}}`), want: "base64 PNG, JPEG, WEBP, or GIF"},
		{name: "invalid base64", body: []byte(`{"model":"grok-imagine-video","image":{"url":"data:image/png;base64,%%%"}}`), want: "invalid base64 image data"},
		{name: "video mask", body: []byte(`{"model":"grok-imagine-video","mask":{"url":"https://example.com/mask.png"}}`), want: "mask is only supported"},
		{name: "too many inputs", body: tooManyBody, want: "at most 16 input images"},
		{name: "remote URL too long", body: []byte(`{"model":"grok-imagine-video","image":{"url":"https://example.com/` + strings.Repeat("a", grokMediaMaxRemoteImageURLBytes) + `"}}`), want: "URL exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGrokMediaRequest(GrokMediaEndpointVideosGenerations, "application/json", tt.body)
			require.ErrorContains(t, err, tt.want)
		})
	}

	require.NoError(t, ValidateGrokMediaRequest(
		GrokMediaEndpointVideosGenerations,
		"application/json",
		[]byte(`{"model":"grok-imagine-video-1.5","image":{"url":"data:image/png;base64,aW1n","image_url":"data:image/png;base64,aW1n"},"reference_images":[]}`),
	))
}

func TestCanonicalizeGrokMediaImageURLFieldsUsesOfficialObjects(t *testing.T) {
	body := []byte(`{
		"image":"https://example.com/source.png",
		"images":[
			{"image_url":"https://example.com/legacy.png"},
			{"url":"https://example.com/same.png","image_url":"https://example.com/same.png"}
		],
		"reference_images":["https://example.com/reference.png"]
	}`)

	out, err := canonicalizeGrokMediaImageURLFields(body, "image", "images", "reference_images")
	require.NoError(t, err)
	require.Equal(t, "https://example.com/source.png", gjson.GetBytes(out, "image.url").String())
	require.Equal(t, "https://example.com/legacy.png", gjson.GetBytes(out, "images.0.url").String())
	require.Equal(t, "https://example.com/same.png", gjson.GetBytes(out, "images.1.url").String())
	require.Equal(t, "https://example.com/reference.png", gjson.GetBytes(out, "reference_images.0.url").String())
	require.False(t, gjson.GetBytes(out, "images.0.image_url").Exists())
	require.False(t, gjson.GetBytes(out, "images.1.image_url").Exists())

	_, err = canonicalizeGrokMediaImageURLFields(
		[]byte(`{"image":{"url":"https://example.com/a.png","image_url":"https://example.com/b.png"}}`),
		"image",
	)
	require.ErrorContains(t, err, "conflicting url and image_url")
}

func TestValidateGrokMediaVideoRequestBillingBoundaries(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: "{\"model\":"},
		{name: "numeric model", body: "{\"model\":123}"},
		{name: "fractional duration", body: "{\"model\":\"grok-imagine-video\",\"duration\":1.5}"},
		{name: "zero duration", body: "{\"model\":\"grok-imagine-video\",\"duration\":0}"},
		{name: "duration above limit", body: "{\"model\":\"grok-imagine-video\",\"duration\":16}"},
		{name: "invalid resolution", body: "{\"model\":\"grok-imagine-video\",\"resolution\":\"4k\"}"},
		{name: "base model 1080p", body: "{\"model\":\"grok-imagine-video\",\"resolution\":\"1080p\"}"},
		{name: "text-only 1.5 preview falls back and rejects 1080p", body: "{\"model\":\"grok-imagine-video-1.5-preview\",\"resolution\":\"1080p\"}"},
		{name: "multiple outputs", body: "{\"model\":\"grok-imagine-video\",\"n\":2}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, ValidateGrokMediaRequest(GrokMediaEndpointVideosGenerations, "application/json", []byte(tt.body)))
		})
	}
}

func TestValidateGrokMediaVideoRequestAllowsDefaultsAndSupported1080P(t *testing.T) {
	require.NoError(t, ValidateGrokMediaRequest(
		GrokMediaEndpointVideosGenerations,
		"application/json",
		[]byte("{\"model\":\"grok-imagine-video\",\"prompt\":\"waves\"}"),
	))
	require.NoError(t, ValidateGrokMediaRequest(
		GrokMediaEndpointVideosGenerations,
		"application/json",
		[]byte("{\"model\":\"grok-imagine-video-1.5\",\"image\":{\"image_url\":\"data:image/png;base64,aW1n\"},\"resolution\":\"1080p\",\"duration\":15}"),
	))
	require.NoError(t, ValidateGrokMediaRequest(
		GrokMediaEndpointVideosGenerations,
		"application/json",
		[]byte("{\"model\":\"grok-imagine-video-1.5-preview\",\"image\":{\"image_url\":\"data:image/png;base64,aW1n\"},\"resolution\":\"1080p\",\"duration\":15}"),
	))
}

func TestNormalizeGrokMediaModelForEndpoint(t *testing.T) {
	tests := []struct {
		name          string
		endpoint      GrokMediaEndpoint
		model         string
		hasInputImage bool
		want          string
	}{
		{name: "image generation alias", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine", want: "grok-imagine-image-quality"},
		{name: "image edit alias", endpoint: GrokMediaEndpointImagesEdits, model: "grok-imagine", want: "grok-imagine-image-quality"},
		{name: "image quality passthrough", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine-image-quality", want: "grok-imagine-image-quality"},
		{name: "image quality dated alias remains overridable", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine-image-quality-20260403", want: "grok-imagine-image-quality-20260403"},
		{name: "image quality latest alias remains overridable", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine-image-quality-latest", want: "grok-imagine-image-quality-latest"},
		{name: "image pro alias remains overridable", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine-image-pro", want: "grok-imagine-image-pro"},
		{name: "image fast passthrough", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine-image", want: "grok-imagine-image"},
		{name: "image fast dated alias remains overridable", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine-image-2026-03-02", want: "grok-imagine-image-2026-03-02"},
		{name: "video passthrough", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video", want: "grok-imagine-video"},
		{name: "video 1.5 text-only fallback", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video-1.5", want: "grok-imagine-video"},
		{name: "video 1.5 preview text-only fallback", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video-1.5-preview", want: "grok-imagine-video"},
		{name: "video 1.5 image-to-video passthrough", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video-1.5", hasInputImage: true, want: "grok-imagine-video-1.5"},
		{name: "video 1.5 preview image-to-video alias remains overridable", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video-1.5-preview", hasInputImage: true, want: "grok-imagine-video-1.5-preview"},
		{name: "video 1.5 dated image-to-video alias remains overridable", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video-1.5-2026-05-30", hasInputImage: true, want: "grok-imagine-video-1.5-2026-05-30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeGrokMediaModelForEndpoint(tt.endpoint, tt.model, tt.hasInputImage))
		})
	}
}

func TestGrokMediaRoutingUsesEffectiveTextToVideoModel(t *testing.T) {
	info := ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-video-1.5","prompt":"waves"}`))
	routingModel := ResolveGrokMediaRoutingModel(GrokMediaEndpointVideosGenerations, info)
	require.Equal(t, "grok-imagine-video", routingModel)

	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"grok-imagine-video": "grok-imagine-video"},
		},
	}
	require.True(t, accountSupportsOpenAICapabilities(account, "", OpenAIImagesCapabilityBasic, routingModel))
	require.False(t, accountSupportsOpenAICapabilities(account, "", OpenAIImagesCapabilityBasic, info.Model))

	previewInfo := ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-video-1.5-preview","prompt":"waves"}`))
	require.Equal(t, "grok-imagine-video", ResolveGrokMediaRoutingModel(GrokMediaEndpointVideosGenerations, previewInfo))
}

func TestForwardGrokMediaImagesGenerationNormalizesImagineAlias(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          61,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-image-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://cdn.example/image.png"}]}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Equal(t, "Bearer api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.JSONEq(t, `{"model":"grok-imagine-image-quality","prompt":"draw a cat"}`, string(upstream.lastBody))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"data":[{"url":"https://cdn.example/image.png"}]}`, recorder.Body.String())
	require.Equal(t, "xai-image-req", result.RequestID)
	require.Equal(t, "grok-imagine", result.Model)
	require.Equal(t, "grok-imagine-image-quality", result.BillingModel)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, ImageBillingSize2K, result.ImageSize)
}

func TestForwardGrokMediaVideoContentUsesOriginalCredentialAndRange(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/task-161/content", nil)
	c.Request.Header.Set("Range", "bytes=0-3")
	account := &Account{
		ID: 161, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "original-key", "base_url": "https://xai.test/v1"},
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"completed","video":{"url":"https://upstream-sub2api.example/v1/videos/task-161/content"}}`)),
		},
		{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Type":  []string{"video/mp4"},
				"Content-Range": []string{"bytes 0-3/10"},
				"Accept-Ranges": []string{"bytes"},
			},
			Body: io.NopCloser(strings.NewReader("data")),
		},
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(
		context.Background(), c, account, GrokMediaEndpointVideoContent, "task-161", nil, "",
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://xai.test/v1/videos/task-161", upstream.requests[0].URL.String())
	require.Equal(t, "Bearer original-key", upstream.requests[0].Header.Get("Authorization"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
	require.Equal(t, "https://xai.test/v1/videos/task-161/content", upstream.requests[1].URL.String())
	require.Equal(t, "Bearer original-key", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, "bytes=0-3", upstream.requests[1].Header.Get("Range"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[1].Context()))
	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, "bytes 0-3/10", recorder.Header().Get("Content-Range"))
	require.Equal(t, "data", recorder.Body.String())
}

func TestGrokMediaEmptyImageOutputIsNotCountedAsProduced(t *testing.T) {
	requestInfo := ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-image","prompt":"draw","n":3}`))
	meta := grokMediaUsageFromResponse(GrokMediaEndpointImagesGenerations, requestInfo, []byte(`{"data":[]}`))
	require.Zero(t, meta.ImageCount)
}

func TestForwardGrokMediaEmptyImageOutputTriggersFailover(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-image","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	account := &Account{
		ID: 62, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.JSONEq(t, `{"data":[]}`, string(failoverErr.ResponseBody))
	require.Empty(t, recorder.Body.String())
}

func TestGrokMediaVideoGenerationCountsOnlyTaskOrRealOutput(t *testing.T) {
	requestInfo := ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-video","duration":8,"resolution":"720p"}`))

	withTask := grokMediaUsageFromResponse(GrokMediaEndpointVideosGenerations, requestInfo, []byte(`{"request_id":"video-task-1"}`))
	require.Equal(t, "video-task-1", withTask.ResponseID)
	require.Equal(t, 1, withTask.VideoCount)

	synchronous := grokMediaUsageFromResponse(GrokMediaEndpointVideosGenerations, requestInfo, []byte(`{"data":[{"url":"https://cdn.example/video.mp4"}]}`))
	require.Empty(t, synchronous.ResponseID)
	require.Equal(t, 1, synchronous.VideoCount)

	empty := grokMediaUsageFromResponse(GrokMediaEndpointVideosGenerations, requestInfo, []byte(`{"data":[]}`))
	require.Empty(t, empty.ResponseID)
	require.Zero(t, empty.VideoCount)
}

func TestForwardGrokMediaImagesGenerationStripsUnsupportedSize(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-image","prompt":"draw a cat","size":"1024x1024"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          65,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1n"}]}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"grok-imagine-image","prompt":"draw a cat"}`, string(upstream.lastBody))
	require.Equal(t, ImageBillingSize1K, result.ImageSize)
	require.Equal(t, "1024x1024", result.ImageInputSize)
}

func TestForwardGrokMediaImagesEditMultipartConvertsToJSON(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	require.NoError(t, writer.WriteField("prompt", "edit this private image"))
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", `form-data; name="image"; filename="input.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader)
	require.NoError(t, err)
	_, err = part.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(buf.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())

	account := &Account{
		ID:          62,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1n"}]}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	_, err = svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesEdits, "", buf.Bytes(), writer.FormDataContentType())
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/images/edits", upstream.lastReq.URL.String())
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.True(t, json.Valid(upstream.lastBody))
	require.Equal(t, "grok-imagine-edit", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "edit this private image", gjson.GetBytes(upstream.lastBody, "prompt").String())
	require.True(t, strings.HasPrefix(gjson.GetBytes(upstream.lastBody, "image.url").String(), "data:image/png;base64,"))
	require.False(t, gjson.GetBytes(upstream.lastBody, "image.image_url").Exists())
}

func TestForwardGrokMediaVideoGenerationReturnsUsageAndResponseID(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-video-1.5","prompt":"waves","resolution":"720p","duration":10}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          63,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-video-generate-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"request_id":"video-request-123","usage":{"prompt_tokens":3,"completion_tokens":4}}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/generations", upstream.lastReq.URL.String())
	require.JSONEq(t, `{"model":"grok-imagine-video","prompt":"waves","resolution":"720p","duration":10}`, string(upstream.lastBody))
	require.Equal(t, "video-request-123", result.ResponseID)
	require.Equal(t, "grok-imagine-video", result.BillingModel)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Zero(t, result.ImageCount)
	require.Empty(t, result.ImageSize)
	require.Equal(t, 1, result.VideoCount)
	require.Equal(t, VideoBillingResolution720P, result.VideoResolution)
	require.Equal(t, 10, result.VideoDurationSeconds)
}

func TestForwardGrokMediaVideoTransformsUseCanonicalPayloadAndAsyncMetadata(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		endpoint     GrokMediaEndpoint
		path         string
		body         string
		wantDuration int
	}{
		{
			name:         "edit",
			endpoint:     GrokMediaEndpointVideosEdits,
			path:         "/videos/edits",
			body:         `{"model":"grok-imagine-video","prompt":"add a hat","video":{"video_url":"data:video/mp4;base64,aWQ="}}`,
			wantDuration: 0,
		},
		{
			name:         "extension",
			endpoint:     GrokMediaEndpointVideosExtensions,
			path:         "/videos/extensions",
			body:         `{"model":"grok-imagine-video","prompt":"continue","seconds":5,"video":{"video_url":"data:video/mp4;base64,aWQ="}}`,
			wantDuration: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := []byte(tt.body)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			account := &Account{
				ID: 64, Name: "grok", Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"request_id":"video-transform-123"}`)),
			}}
			svc := &OpenAIGatewayService{httpUpstream: upstream}

			result, err := svc.ForwardGrokMedia(context.Background(), c, account, tt.endpoint, "", body, "application/json")
			require.NoError(t, err)
			require.Equal(t, "https://xai.test/v1"+tt.path, upstream.lastReq.URL.String())
			require.Equal(t, "data:video/mp4;base64,aWQ=", gjson.GetBytes(upstream.lastBody, "video.url").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "video.video_url").Exists())
			require.False(t, gjson.GetBytes(upstream.lastBody, "seconds").Exists())
			require.Equal(t, "video-transform-123", result.ResponseID)
			require.Equal(t, 1, result.VideoCount)
			require.Equal(t, VideoBillingResolution720P, result.VideoResolution)
			require.Equal(t, tt.wantDuration, result.VideoDurationSeconds)
			if tt.endpoint == GrokMediaEndpointVideosExtensions {
				require.Equal(t, int64(5), gjson.GetBytes(upstream.lastBody, "duration").Int())
			}
		})
	}
}

func TestForwardGrokMediaVideoRejectsOpenAICompatibleAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-video","prompt":"waves","resolution":"480p","duration":1}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          64,
		Name:        "compatible-grok-relay",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "relay-key",
			"base_url": "https://relay.test/v1",
		},
	}
	svc := &OpenAIGatewayService{}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.Nil(t, result)
	require.ErrorContains(t, err, "not supported for grok media")
}

func TestForwardGrokMediaVideoGenerationPreservesImageToVideoModel(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-video-1.5","prompt":"animate","image":{"image_url":"data:image/png;base64,aW1n"}}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          63,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"request_id":"video-request-456"}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/generations", upstream.lastReq.URL.String())
	require.JSONEq(t, `{"model":"grok-imagine-video-1.5","prompt":"animate","image":{"url":"data:image/png;base64,aW1n"},"duration":8,"resolution":"480p"}`, string(upstream.lastBody))
	require.Equal(t, "video-request-456", result.ResponseID)
	require.Equal(t, "grok-imagine-video-1.5", result.BillingModel)
	// 未指定 duration 时按上游默认 8 秒计费。
	require.Equal(t, VideoBillingDefaultDurationSeconds, result.VideoDurationSeconds)
}

func TestForwardGrokMediaVideoCanonicalizesReferenceImagesInSubmittedOrder(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{
		"model":"grok-imagine-video-1.5",
		"prompt":"animate the references",
		"image":{"image_url":"https://example.com/source.png"},
		"reference_images":[
			{"image_url":"https://example.com/first.png"},
			"https://example.com/second.png",
			{"url":"https://example.com/first.png"}
		],
		"duration":9,
		"resolution":"720p"
	}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          64,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"request_id":"video-reference-request"}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "video-reference-request", result.ResponseID)
	require.Equal(t, "https://example.com/source.png", gjson.GetBytes(upstream.lastBody, "image.url").String())
	require.Equal(t, []string{
		"https://example.com/first.png",
		"https://example.com/second.png",
		"https://example.com/first.png",
	}, []string{
		gjson.GetBytes(upstream.lastBody, "reference_images.0.url").String(),
		gjson.GetBytes(upstream.lastBody, "reference_images.1.url").String(),
		gjson.GetBytes(upstream.lastBody, "reference_images.2.url").String(),
	})
	require.NotContains(t, string(upstream.lastBody), "image_url")
	require.Equal(t, int64(9), gjson.GetBytes(upstream.lastBody, "duration").Int())
	require.Equal(t, "720p", gjson.GetBytes(upstream.lastBody, "resolution").String())
}

func TestForwardGrokMediaVideoStatusUsesGETWithoutBody(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/request-123", nil)

	account := &Account{
		ID:          62,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-video-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"request-123","status":"completed"}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideoStatus, "request-123", nil, "")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/request-123", upstream.lastReq.URL.String())
	require.Equal(t, http.MethodGet, upstream.lastReq.Method)
	require.Equal(t, "Bearer api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("Content-Type"))
	require.Empty(t, upstream.lastBody)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"id":"request-123","status":"completed"}`, recorder.Body.String())
	require.Equal(t, "xai-video-req", result.RequestID)
}

func TestForwardGrokMediaVideoStatusRewritesOnlyFailureMessage(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/request-123", nil)
	account := &Account{
		ID: 62, Name: "grok", Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"request-123","status":"failed","error":{"code":"content_policy","message":"blocked"}}`,
		)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	ctx := WithGatewayCustomErrorMessage(context.Background(), "请调整请求后重试")

	_, err := svc.ForwardGrokMedia(ctx, c, account, GrokMediaEndpointVideoStatus, "request-123", nil, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "failed", gjson.Get(recorder.Body.String(), "status").String())
	require.Equal(t, "content_policy", gjson.Get(recorder.Body.String(), "error.code").String())
	require.Equal(t, "请调整请求后重试", gjson.Get(recorder.Body.String(), "error.message").String())
}

func TestBindGrokMediaVideoRequestAccountUsesRequestIDStickyHash(t *testing.T) {
	ctx := context.Background()
	groupID := int64(7)
	cache := &stubGatewayCache{}
	svc := &OpenAIGatewayService{cache: cache}

	hash := GrokMediaVideoRequestSessionHash("video-request-123")
	require.NotEmpty(t, hash)
	require.NoError(t, svc.BindGrokMediaVideoRequestAccount(ctx, &groupID, "video-request-123", 63))

	accountID, err := svc.getStickySessionAccountID(ctx, &groupID, hash)
	require.NoError(t, err)
	require.Equal(t, int64(63), accountID)
}

func TestForwardGrokMediaErrorHonorsCustomErrorCodes(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          64,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":                    "api-key",
			"base_url":                   "https://xai.test/v1",
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusBadRequest)},
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-error-req"},
			"Retry-After":    []string{"45"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"do not expose this upstream detail"}}`)),
	}}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{httpUpstream: upstream, accountRepo: repo}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Upstream gateway error")
	require.NotContains(t, recorder.Body.String(), "do not expose")
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestForwardGrokMediaErrorMatrix(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name         string
		status       int
		body         string
		wantFailover bool
		wantBlocked  bool
		wantStatus   int
	}{
		{name: "bad request", status: http.StatusBadRequest, body: `{"error":{"message":"invalid size"}}`, wantStatus: http.StatusBadRequest},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"message":"api key rejected"}}`, wantFailover: true, wantBlocked: true},
		{name: "entitlement forbidden", status: http.StatusForbidden, body: `{"error":{"message":"subscription tier denied"}}`, wantFailover: true},
		{name: "safety forbidden", status: http.StatusForbidden, body: `{"error":{"code":"content_policy_violation","message":"prompt was rejected by the safety system"}}`, wantStatus: http.StatusForbidden},
		{name: "not found", status: http.StatusNotFound, body: `{"error":{"message":"model not found"}}`, wantStatus: http.StatusNotFound},
		{name: "conflict", status: http.StatusConflict, body: `{"error":{"message":"request state conflict"}}`, wantStatus: http.StatusConflict},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"error":{"message":"rate limited"}}`, wantFailover: true, wantBlocked: true},
		{name: "internal error", status: http.StatusInternalServerError, body: `{"error":{"message":"temporary error"}}`, wantFailover: true, wantBlocked: true},
		{name: "service unavailable", status: http.StatusServiceUnavailable, body: `{"error":{"message":"temporarily unavailable"}}`, wantFailover: true, wantBlocked: true},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			requestBody := []byte(`{"model":"grok-imagine-image","prompt":"draw"}`)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(requestBody))
			c.Request.Header.Set("Content-Type", "application/json")
			account := &Account{
				ID:          int64(700 + index),
				Name:        "grok",
				Platform:    PlatformGrok,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
				Credentials: map[string]any{
					"api_key":  "api-key",
					"base_url": "https://xai.test/v1",
				},
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: tt.status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}}
			svc := &OpenAIGatewayService{httpUpstream: upstream, accountRepo: &grokQuotaAccountRepo{}}

			result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", requestBody, "application/json")
			require.Error(t, err)
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, tt.status, failoverErr.StatusCode)
				require.Equal(t, tt.wantBlocked, svc.isOpenAIAccountRuntimeBlocked(account))
				return
			}
			require.False(t, errors.As(err, &failoverErr))
			require.Equal(t, tt.wantStatus, recorder.Code)
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
		})
	}
}

func TestForwardGrokMediaEntitlementForbiddenIsolatesMediaWithoutCoolingText(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-image","prompt":"draw"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	account := &Account{
		ID:   880,
		Name: "grok-text-still-healthy", Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
	}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{
		accountRepo: repo,
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"subscription tier denied"}}`)),
		}},
	}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "media denial must not block text scheduling")
	require.Zero(t, repo.tempUnschedCalls)
	require.Equal(t, 1, repo.updateCalls)
	rawSnapshot := repo.updates[account.ID][GrokMediaEligibilitySnapshotExtraKey]
	snapshot, ok := rawSnapshot.(*GrokMediaEligibilitySnapshot)
	require.True(t, ok)
	require.NotNil(t, snapshot.Eligible)
	require.False(t, *snapshot.Eligible)
	require.Equal(t, "media_request", snapshot.Source)

	isolatedCopy := *account
	isolatedCopy.Extra = map[string]any{GrokMediaEligibilitySnapshotExtraKey: snapshot}
	eligible, reason := isolatedCopy.GrokMediaGenerationEligibility()
	require.False(t, eligible)
	require.Equal(t, "media_request_forbidden", reason)
}

func TestForwardGrokMediaTransportErrorMatrix(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name         string
		err          error
		wantFailover bool
		wantBlocked  bool
	}{
		{name: "timeout fails over without evicting", err: context.DeadlineExceeded, wantFailover: true},
		{name: "connection refused fails over and blocks", err: errors.New("dial tcp: connection refused"), wantFailover: true, wantBlocked: true},
		{name: "client cancellation does not fail over", err: context.Canceled},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			requestBody := []byte(`{"model":"grok-imagine-image","prompt":"draw"}`)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(requestBody))
			account := &Account{
				ID:       int64(800 + index),
				Platform: PlatformGrok,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key":  "api-key",
					"base_url": "https://xai.test/v1",
				},
			}
			svc := &OpenAIGatewayService{httpUpstream: &httpUpstreamRecorder{err: tt.err}}

			result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", requestBody, "application/json")
			require.Error(t, err)
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.Equal(t, tt.wantFailover, errors.As(err, &failoverErr))
			require.Equal(t, tt.wantBlocked, svc.isOpenAIAccountRuntimeBlocked(account))
		})
	}
}

func TestForwardGrokMediaUnauthorizedRefreshesAndRetriesOnce(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-image","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          66,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "revoked-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			"base_url":      "https://xai.test/v1",
		},
	}
	baseRepo := &tokenRefreshAccountRepo{}
	baseRepo.accountsByID = map[int64]*Account{66: account}
	repo := &grokUnauthorizedRetryRepo{tokenRefreshAccountRepo: baseRepo}
	oauthSvc := NewGrokOAuthService(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{AccessToken: "refreshed-token", TokenType: "Bearer", ExpiresIn: 3600},
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
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1n"}]}`)),
		},
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: provider,
		accountRepo:       repo,
	}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "Bearer revoked-token", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer refreshed-token", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, 1, baseRepo.updateCredentialsCalls)
	require.JSONEq(t, `{"data":[{"b64_json":"aW1n"}]}`, recorder.Body.String())
}

func TestForwardGrokMediaAppliesAccountModelMappingAfterCanonicalization(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          67,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
			"model_mapping": map[string]any{
				"grok-imagine-image-quality": "relay-image-v2",
			},
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1n"}]}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "relay-image-v2", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "grok-imagine", result.Model)
	require.Equal(t, "grok-imagine-image-quality", result.BillingModel)
	require.Equal(t, "relay-image-v2", result.UpstreamModel)
}
