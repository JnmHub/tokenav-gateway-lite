package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesLiteMarkers(t *testing.T) {
	tests := []struct {
		name   string
		header string
		body   []byte
		want   bool
	}{
		{name: "http true", header: " true ", want: true},
		{name: "http case insensitive", header: "TRUE", want: true},
		{name: "http false", header: "false", want: false},
		{name: "websocket metadata", body: []byte(`{"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`), want: true},
		{name: "websocket metadata false", body: []byte(`{"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"false"}}`), want: false},
		{name: "websocket invalid json", body: []byte(`{"client_metadata":`), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.header != "" {
				require.Equal(t, tt.want, isOpenAIResponsesLiteHeader(tt.header))
				return
			}
			require.Equal(t, tt.want, isOpenAIResponsesLiteWebSocketPayload(tt.body))
		})
	}
}

func TestIsImageGenerationIntent(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		model    string
		body     []byte
		want     bool
	}{
		{
			name:     "images endpoint",
			endpoint: "/v1/images/generations",
			body:     []byte(`{"model":"gpt-image-2"}`),
			want:     true,
		},
		{
			name:     "image model",
			endpoint: "/v1/responses",
			model:    "gpt-image-2",
			body:     []byte(`{"model":"gpt-image-2"}`),
			want:     true,
		},
		{
			name:     "image tool",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`),
			want:     true,
		},
		{
			name:     "image tool choice",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","tool_choice":{"type":"image_generation"}}`),
			want:     true,
		},
		{
			name:     "required tool choice alone is text",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","tool_choice":"required"}`),
			want:     false,
		},
		{
			name:     "text only gpt 5.4",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","input":"write code"}`),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsImageGenerationIntent(tt.endpoint, tt.model, tt.body))
		})
	}
}

func TestIsImageGenerationIntentRecognizesCodexNamespaceTools(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "top level namespace",
			body: `{"model":"gpt-5.6-sol","tools":[{"type":"namespace","name":"image_gen"}]}`,
		},
		{
			name: "responses lite additional tools",
			body: `{"model":"gpt-5.6-sol","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, IsImageGenerationIntent("/v1/responses", "gpt-5.6-sol", []byte(tc.body)))
		})
	}
}

func TestIsExplicitImageGenerationIntentSeparatesPassiveNamespace(t *testing.T) {
	passiveTopLevel := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],"tool_choice":"auto"}`)
	passiveAdditional := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}]}`)
	require.False(t, IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.6-sol", passiveTopLevel))
	require.False(t, IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.6-sol", passiveAdditional))
	require.True(t, IsImageGenerationIntent("/v1/responses", "gpt-5.6-sol", passiveTopLevel))
	require.True(t, IsImageGenerationIntent("/v1/responses", "gpt-5.6-sol", passiveAdditional))

	require.True(t, IsExplicitImageGenerationIntent(
		"/v1/responses", "gpt-5.6-sol",
		[]byte(`{"model":"gpt-5.6-sol","tools":[{"type":"image_generation"}]}`),
	))
	require.True(t, IsExplicitImageGenerationIntent(
		"/v1/responses", "gpt-5.6-sol",
		[]byte(`{"model":"gpt-5.6-sol","input":[{"type":"additional_tools","tools":[{"type":"image_generation"}]}]}`),
	))
	require.True(t, IsExplicitImageGenerationIntent(
		"/v1/responses", "gpt-5.6-sol",
		[]byte(`{"model":"gpt-5.6-sol","tool_choice":{"function":{"namespace":"image_gen","name":"imagegen"}}}`),
	))
	require.True(t, IsExplicitImageGenerationIntent("/v1/responses", "gpt-image-2", nil))
}

func TestIsImageGenerationIntentForPlatformTreatsGrokNamespaceAsPassive(t *testing.T) {
	passive := []byte(`{"model":"grok-4.5","tools":[{"type":"namespace","name":"image_gen"}]}`)
	require.False(t, IsImageGenerationIntentForPlatform("/v1/responses", "grok-4.5", passive, PlatformGrok))
	require.True(t, IsImageGenerationIntentForPlatform("/v1/responses", "gpt-5.6-sol", passive, PlatformOpenAI))

	native := []byte(`{"model":"grok-4.5","tools":[{"type":"image_generation"}]}`)
	require.True(t, IsImageGenerationIntentForPlatform("/v1/responses", "grok-4.5", native, PlatformGrok))

	explicit := []byte(`{"model":"grok-4.5","tool_choice":{"function":{"namespace":"image_gen","name":"imagegen"}}}`)
	require.True(t, IsImageGenerationIntentForPlatform("/v1/responses", "grok-4.5", explicit, PlatformGrok))
}

func TestIsImageGenerationIntentMapRecognizesAdditionalTools(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-sol",
		"input": []any{
			map[string]any{
				"type": "additional_tools",
				"tools": []any{
					map[string]any{"type": "namespace", "name": "image_gen"},
				},
			},
		},
	}
	require.True(t, IsImageGenerationIntentMap("/v1/responses", "gpt-5.6-sol", reqBody))
}

func TestResolveOpenAIResponsesImageRoutingModelRecognizesAdditionalTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}]}`)
	require.Equal(t, "gpt-image-2", ResolveOpenAIResponsesImageRoutingModel(body, "gpt-5.6-sol"))
}

func TestResolveOpenAIResponsesImageBillingConfigUsesCurrentBodyModel(t *testing.T) {
	imageModel, imageSize, err := resolveOpenAIResponsesImageBillingConfigFromBody(
		[]byte(`{"model":"mapped-image-model","tools":[{"type":"image_generation","size":"1024x1024"}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "mapped-image-model", imageModel)
	require.Equal(t, "1K", imageSize)
}

func TestResolveOpenAIResponsesImageBillingConfigToolModelWins(t *testing.T) {
	imageModel, imageSize, err := resolveOpenAIResponsesImageBillingConfigFromBody(
		[]byte(`{"model":"mapped-text-model","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1536x1024"}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", imageModel)
	require.Equal(t, "2K", imageSize)
}

func TestResolveOpenAIResponsesImageRoutingModelDefaultsImageTool(t *testing.T) {
	model := ResolveOpenAIResponsesImageRoutingModel(
		[]byte(`{"model":"gpt-5","tools":[{"type":"image_generation","size":"1024x1024"}]}`),
		"gpt-5",
	)
	require.Equal(t, "gpt-image-2", model)
}

func TestResolveOpenAIResponsesImageRoutingModelPrefersMappedImageFallback(t *testing.T) {
	model := ResolveOpenAIResponsesImageRoutingModel(
		[]byte(`{"model":"draw-alias","input":"draw"}`),
		"gpt-image-2",
	)
	require.Equal(t, "gpt-image-2", model)
}

func TestResolveOpenAIResponsesImageBillingConfigFromBodyIgnoresUnrelatedLargeInput(t *testing.T) {
	cfg, err := resolveOpenAIResponsesImageBillingConfigDetailedFromBody(
		[]byte(`{"model":"mapped-text-model","tools":[{"type":"image_generation","model":"gpt-image-2","size":"2048x1152"}],"input":[{"type":"message","content":[{"type":"input_text","text":"hi","nonce":1e1000000}]}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", cfg.Model)
	require.Equal(t, "2K", cfg.SizeTier)
	require.Equal(t, "2048x1152", cfg.InputSize)
}

func TestResolveOpenAIResponsesImageBillingConfigSupportsOfficialAndCustomSizes(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		wantTier string
	}{
		{
			name:     "official 2k landscape",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","model":"gpt-image-2","size":"2048x1152"}]}`),
			wantTier: "2K",
		},
		{
			name:     "official 4k landscape",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","model":"gpt-image-2","size":"3840x2160"}]}`),
			wantTier: "4K",
		},
		{
			name:     "custom valid 2k",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1280x768"}]}`),
			wantTier: "2K",
		},
		{
			name:     "default image tool model supports flexible size",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","size":"2048x1152"}]}`),
			wantTier: "2K",
		},
		{
			name:     "top level image size is moved into billing",
			body:     []byte(`{"model":"gpt-image-2","size":"2048x2048","tools":[{"type":"image_generation","model":"gpt-image-2"}]}`),
			wantTier: "2K",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageModel, imageSize, err := resolveOpenAIResponsesImageBillingConfigFromBody(tt.body, "requested-model")
			require.NoError(t, err)
			require.NotEmpty(t, imageModel)
			require.Equal(t, tt.wantTier, imageSize)
		})
	}
}

func TestResolveOpenAIResponsesImageBillingConfigDoesNotRejectUnknownSizes(t *testing.T) {
	imageModel, imageSize, err := resolveOpenAIResponsesImageBillingConfigFromBody(
		[]byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","model":"gpt-image-1.5","size":"2048x1152"}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-1.5", imageModel)
	require.Equal(t, "2K", imageSize)
}

func TestOpenAIImageOutputCounterDeduplicatesFinalImages(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEData([]byte(`{"type":"response.image_generation_call.partial_image","partial_image_b64":"abc"}`))
	counter.AddSSEData([]byte(`{"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","result":"final-a","size":"1024x1024"}}`))
	counter.AddSSEData([]byte(`{"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","result":"final-a"},{"id":"ig_2","type":"image_generation_call","result":"final-b","size":"3840x2160"}]}}`))
	require.Equal(t, 2, counter.Count())
	require.Equal(t, []string{"1024x1024", "3840x2160"}, counter.Sizes())
}

func TestOpenAIImageOutputCounterCountsImagesAPIStreamShapes(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEData([]byte(`{"type":"image_generation.completed","id":"ig_complete","b64_json":"final-a"}`))
	counter.AddSSEData([]byte(`{"type":"response.output_item.done","item":{"id":"ig_item","type":"image_generation_call","result":"final-b"}}`))
	counter.AddSSEData([]byte(`{"type":"response.completed","response":{"output":[{"id":"ig_done","type":"image_generation_call","result":"final-c"}]}}`))
	require.Equal(t, 3, counter.Count())

	dataCounter := newOpenAIImageOutputCounter()
	dataCounter.AddSSEData([]byte(`{"data":[{"b64_json":"a"},{"b64_json":"b"}]}`))
	dataCounter.AddSSEData([]byte(`{"data":[{"b64_json":"a"},{"b64_json":"b"},{"b64_json":"c"}]}`))
	require.Equal(t, 3, dataCounter.Count())
}

func TestOpenAIImageOutputCounterCountsMultilineSSEDataPayload(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEData([]byte("{\"type\":\"image_generation.completed\",\n\"b64_json\":\"final-a\"}"))
	require.Equal(t, 1, counter.Count())
}

func TestOpenAIImageOutputCounterCountsMultilineSSEBodyPayload(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(
		"data: {\"type\":\"image_generation.completed\",\n" +
			"data: \"b64_json\":\"final-a\"}\n\n" +
			"data: [DONE]\n\n",
	)
	require.Equal(t, 1, counter.Count())
}

func TestOpenAIImageOutputCounterFallsBackForInvalidMultilineSSEBody(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(
		"data: {\"type\":\"image_generation.completed\",\"b64_json\":\"final-a\"}\n" +
			"data: {\"type\":\"image_generation.completed\",\"b64_json\":\"final-b\"}\n\n",
	)
	require.Equal(t, 2, counter.Count())
}

func TestCollectOpenAIResponseImageOutputSizesFromJSONBytes(t *testing.T) {
	body := []byte(`{
		"output": [
			{"id":"ig_1","type":"image_generation_call","result":"final-a","size":"3840x2160"},
			{"id":"ig_2","type":"image_generation_call","result":"final-b","size":"1024x1024"}
		]
	}`)

	require.Equal(t, 2, countOpenAIResponseImageOutputsFromJSONBytes(body))
	require.Equal(t, []string{"3840x2160", "1024x1024"}, collectOpenAIResponseImageOutputSizesFromJSONBytes(body))
}

func TestCollectOpenAIResponseImageOutputSizesFromImagesAPIData(t *testing.T) {
	body := []byte(`{
		"data": [
			{"b64_json":"final-a","size":"2048x1152"},
			{"b64_json":"final-b","size":"2048x1152"}
		]
	}`)

	require.Equal(t, 2, countOpenAIResponseImageOutputsFromJSONBytes(body))
	require.Equal(t, []string{"2048x1152", "2048x1152"}, collectOpenAIResponseImageOutputSizesFromJSONBytes(body))
}

func TestCollectOpenAIImageOutputSizesFromSSEBody(t *testing.T) {
	body := "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"final-a\",\"size\":\"3840x2160\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"final-a\"},{\"id\":\"ig_2\",\"type\":\"image_generation_call\",\"result\":\"final-b\",\"size\":\"1024x1024\"}]}}\n\n" +
		"data: [DONE]\n\n"

	require.Equal(t, 2, countOpenAIImageOutputsFromSSEBody(body))
	require.Equal(t, []string{"3840x2160", "1024x1024"}, collectOpenAIImageOutputSizesFromSSEBody(body))
}
