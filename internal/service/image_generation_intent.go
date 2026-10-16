package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

const (
	openAIResponsesEndpoint          = "/v1/responses"
	openAIResponsesCompactEndpoint   = "/v1/responses/compact"
	responsesLiteHeader              = "X-OpenAI-Internal-Codex-Responses-Lite"
	responsesLiteHeaderKey           = "x-openai-internal-codex-responses-lite"
	responsesLiteWSMetadataKey       = "ws_request_header_x_openai_internal_codex_responses_lite"
	imageGenerationPermissionMessage = "Image generation is not enabled for this group"
)

// IsOpenAIImageGenerationModel exposes the shared model-family decision to
// handlers that must reject an incompatible endpoint before billing or routing.
func IsOpenAIImageGenerationModel(model string) bool {
	return isOpenAIImageGenerationModel(model)
}

func isOpenAIResponsesLiteHeader(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

func isOpenAIResponsesLiteWebSocketPayload(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	return isOpenAIResponsesLiteHeader(gjson.GetBytes(body, "client_metadata."+responsesLiteWSMetadataKey).String())
}

// ImageGenerationPermissionMessage returns the stable end-user error text for disabled groups.
func ImageGenerationPermissionMessage() string {
	return imageGenerationPermissionMessage
}

// GroupAllowsImageGeneration preserves ungrouped-key behavior and enforces the flag when a group is present.
func GroupAllowsImageGeneration(group *Group) bool {
	return group == nil || group.AllowImageGeneration
}

// IsImageGenerationIntent classifies requests that can produce generated images.
func IsImageGenerationIntent(endpoint string, requestedModel string, body []byte) bool {
	if IsImageGenerationEndpoint(endpoint) {
		return true
	}
	if isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); isOpenAIImageGenerationModel(model) {
		return true
	}
	if openAIJSONToolsContainImageGeneration(gjson.GetBytes(body, "tools")) {
		return true
	}
	if openAIJSONInputContainsImageGenTool(gjson.GetBytes(body, "input")) {
		return true
	}
	return openAIJSONToolChoiceSelectsImageGeneration(gjson.GetBytes(body, "tool_choice"))
}

// IsExplicitImageGenerationIntent 只识别会真实触发生图的硬信号：图片端点、
// 图片模型、原生 image_generation 工具或显式选择 image_gen.imagegen。
// Codex 被动声明的 image_gen namespace 不属于显式意图，不能据此触发权限、
// 图片并发槽或 Responses capability（能力）过滤。
func IsExplicitImageGenerationIntent(endpoint string, requestedModel string, body []byte) bool {
	if IsImageGenerationEndpoint(endpoint) || isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	if isOpenAIImageGenerationModel(strings.TrimSpace(gjson.GetBytes(body, "model").String())) {
		return true
	}
	if openAIJSONToolsContainNativeImageGeneration(gjson.GetBytes(body, "tools")) ||
		openAIJSONInputContainsNativeImageGeneration(gjson.GetBytes(body, "input")) {
		return true
	}
	return openAIJSONToolChoiceSelectsExplicitImageGeneration(gjson.GetBytes(body, "tool_choice"))
}

// IsImageGenerationIntentForPlatform keeps Codex's passive image_gen namespace
// available on ordinary Grok Responses calls. A namespace catalog declaration
// alone is capability advertisement, not a request to generate an image; an
// image model, native image_generation tool or explicit tool choice still is.
func IsImageGenerationIntentForPlatform(endpoint string, requestedModel string, body []byte, platform string) bool {
	if !strings.EqualFold(strings.TrimSpace(platform), PlatformGrok) {
		return IsImageGenerationIntent(endpoint, requestedModel, body)
	}
	if IsImageGenerationEndpoint(endpoint) || isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	if isOpenAIImageGenerationModel(strings.TrimSpace(gjson.GetBytes(body, "model").String())) {
		return true
	}
	if openAIJSONToolsContainNativeImageGeneration(gjson.GetBytes(body, "tools")) {
		return true
	}
	return openAIJSONToolChoiceSelectsExplicitImageGeneration(gjson.GetBytes(body, "tool_choice"))
}

// IsImageGenerationIntentMap is the map-backed variant used after service-side request mutation.
func IsImageGenerationIntentMap(endpoint string, requestedModel string, reqBody map[string]any) bool {
	if IsImageGenerationEndpoint(endpoint) {
		return true
	}
	if isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if reqBody == nil {
		return false
	}
	if isOpenAIImageGenerationModel(firstNonEmptyString(reqBody["model"])) {
		return true
	}
	if hasOpenAIImageGenerationTool(reqBody) {
		return true
	}
	return openAIAnyToolChoiceSelectsImageGeneration(reqBody["tool_choice"])
}

// IsImageGenerationEndpoint identifies dedicated generated-image endpoints.
func IsImageGenerationEndpoint(endpoint string) bool {
	switch normalizeImageGenerationEndpoint(endpoint) {
	case "/v1/images/generations", "/v1/images/edits", "/images/generations", "/images/edits":
		return true
	default:
		return false
	}
}

func normalizeImageGenerationEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(strings.ToLower(endpoint))
	if endpoint == "" {
		return ""
	}
	endpoint = strings.TrimPrefix(endpoint, "https://api.openai.com")
	if idx := strings.IndexByte(endpoint, '?'); idx >= 0 {
		endpoint = endpoint[:idx]
	}
	return strings.TrimRight(endpoint, "/")
}

func openAIJSONToolsContainImageGeneration(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if isOpenAIImageGenerationType(openAIJSONString(item.Get("type"))) {
			found = true
			return false
		}
		if isImageGenNamespaceTool(item) {
			found = true
			return false
		}
		return true
	})
	return found
}

func openAIJSONToolsContainNativeImageGeneration(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, item gjson.Result) bool {
		found = isOpenAIImageGenerationType(openAIJSONString(item.Get("type")))
		return !found
	})
	return found
}

func isOpenAIImageGenerationType(value string) bool {
	return strings.TrimSpace(value) == "image_generation"
}

func isOpenAIImageGenNamespaceName(value string) bool {
	return strings.TrimSpace(value) == "image_gen"
}

func isImageGenNamespaceTool(tool gjson.Result) bool {
	return openAIJSONString(tool.Get("type")) == "namespace" &&
		isOpenAIImageGenNamespaceName(openAIJSONString(tool.Get("name")))
}

func openAIJSONInputContainsImageGenTool(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if openAIJSONString(item.Get("type")) != "additional_tools" {
			return true
		}
		if openAIJSONToolsContainImageGeneration(item.Get("tools")) {
			found = true
			return false
		}
		return true
	})
	return found
}

func openAIJSONInputContainsNativeImageGeneration(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if openAIJSONString(item.Get("type")) == "additional_tools" &&
			openAIJSONToolsContainNativeImageGeneration(item.Get("tools")) {
			found = true
			return false
		}
		return true
	})
	return found
}

// openAIRequestBodyHasImageGenerationDeclaration 同时识别标准 Responses 工具、
// namespace（命名空间）工具、Responses Lite additional_tools 和显式 tool_choice。
func openAIRequestBodyHasImageGenerationDeclaration(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	return openAIJSONToolsContainImageGeneration(gjson.GetBytes(body, "tools")) ||
		openAIJSONInputContainsImageGenTool(gjson.GetBytes(body, "input")) ||
		openAIJSONToolChoiceSelectsImageGeneration(gjson.GetBytes(body, "tool_choice"))
}

func openAIRequestBodyImageGenerationToolNeedsNormalization(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	needsNormalization := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if openAIJSONString(item.Get("type")) != "image_generation" {
			return true
		}
		// 只有旧字段需要迁移时才进入 map 修改，纯计费读取保持 raw 路径。
		if item.Get("format").Exists() || item.Get("compression").Exists() {
			needsNormalization = true
			return false
		}
		return true
	})
	return needsNormalization
}

func openAIJSONToolChoiceSelectsImageGeneration(choice gjson.Result) bool {
	if !choice.Exists() {
		return false
	}
	if choice.Type == gjson.String {
		return isOpenAIImageGenerationType(choice.String())
	}
	if !choice.IsObject() {
		return false
	}
	choiceType := openAIJSONString(choice.Get("type"))
	if isOpenAIImageGenerationType(choiceType) {
		return true
	}
	if choiceType == "namespace" &&
		(isOpenAIImageGenNamespaceName(openAIJSONString(choice.Get("name"))) ||
			isOpenAIImageGenNamespaceName(openAIJSONString(choice.Get("namespace")))) {
		return true
	}
	if tool := choice.Get("tool"); tool.IsObject() && openAIJSONToolChoiceSelectsImageGeneration(tool) {
		return true
	}
	if isOpenAIImageGenerationType(openAIJSONString(choice.Get("function.name"))) {
		return true
	}
	return false
}

func openAIJSONToolChoiceSelectsExplicitImageGeneration(choice gjson.Result) bool {
	if openAIJSONToolChoiceSelectsImageGeneration(choice) {
		return true
	}
	if !choice.IsObject() {
		return false
	}
	if tool := choice.Get("tool"); tool.IsObject() && openAIJSONToolChoiceSelectsExplicitImageGeneration(tool) {
		return true
	}
	if isOpenAIImageGenFunctionReference(openAIJSONString(choice.Get("namespace")), openAIJSONString(choice.Get("name"))) {
		return true
	}
	if fn := choice.Get("function"); fn.IsObject() {
		return isOpenAIImageGenFunctionReference(openAIJSONString(fn.Get("namespace")), openAIJSONString(fn.Get("name")))
	}
	return false
}

func isOpenAIImageGenFunctionReference(namespace, name string) bool {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "image_gen" && name == "imagegen" {
		return true
	}
	return name == "image_gen.imagegen" || name == "image_gen__imagegen"
}

func openAIAnyToolChoiceSelectsImageGeneration(choice any) bool {
	switch v := choice.(type) {
	case string:
		return isOpenAIImageGenerationType(v)
	case map[string]any:
		choiceType := strings.TrimSpace(firstNonEmptyString(v["type"]))
		if isOpenAIImageGenerationType(choiceType) {
			return true
		}
		if choiceType == "namespace" &&
			(isOpenAIImageGenNamespaceName(firstNonEmptyString(v["name"])) ||
				isOpenAIImageGenNamespaceName(firstNonEmptyString(v["namespace"]))) {
			return true
		}
		if tool, ok := v["tool"].(map[string]any); ok && openAIAnyToolChoiceSelectsImageGeneration(tool) {
			return true
		}
		if fn, ok := v["function"].(map[string]any); ok && isOpenAIImageGenerationType(firstNonEmptyString(fn["name"])) {
			return true
		}
	}
	return false
}

func getAPIKeyFromContext(c interface{ Get(string) (any, bool) }) *APIKey {
	if c == nil {
		return nil
	}
	v, exists := c.Get("api_key")
	if !exists {
		return nil
	}
	apiKey, _ := v.(*APIKey)
	return apiKey
}

func apiKeyGroup(apiKey *APIKey) *Group {
	if apiKey == nil {
		return nil
	}
	return apiKey.Group
}

type OpenAIResponsesImageBillingConfig struct {
	Model     string
	SizeTier  string
	InputSize string
}

func resolveOpenAIResponsesImageBillingConfigDetailed(reqBody map[string]any, fallbackModel string) (OpenAIResponsesImageBillingConfig, error) {
	imageModel := ""
	imageSize := ""
	hasImageTool := false
	if reqBody != nil {
		rawTools, _ := reqBody["tools"].([]any)
		for _, rawTool := range rawTools {
			toolMap, ok := rawTool.(map[string]any)
			if !ok || strings.TrimSpace(firstNonEmptyString(toolMap["type"])) != "image_generation" {
				continue
			}
			hasImageTool = true
			imageModel = strings.TrimSpace(firstNonEmptyString(toolMap["model"]))
			imageSize = strings.TrimSpace(firstNonEmptyString(toolMap["size"]))
			break
		}
		if imageSize == "" {
			imageSize = strings.TrimSpace(firstNonEmptyString(reqBody["size"]))
		}
	}
	if imageModel == "" && reqBody != nil {
		bodyModel := strings.TrimSpace(firstNonEmptyString(reqBody["model"]))
		if isOpenAIImageBillingModelAlias(bodyModel) || !hasImageTool {
			imageModel = bodyModel
		}
	}
	if imageModel == "" && hasImageTool {
		imageModel = "gpt-image-2"
	}
	if imageModel == "" {
		imageModel = strings.TrimSpace(fallbackModel)
	}
	sizeTier := normalizeOpenAIImageSizeTier(imageSize)
	return OpenAIResponsesImageBillingConfig{
		Model:     imageModel,
		SizeTier:  sizeTier,
		InputSize: imageSize,
	}, nil
}

func resolveOpenAIResponsesImageBillingConfigFromBody(body []byte, fallbackModel string) (string, string, error) {
	cfg, err := resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, fallbackModel)
	if err != nil {
		return "", "", err
	}
	return cfg.Model, cfg.SizeTier, nil
}

func resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body []byte, fallbackModel string) (OpenAIResponsesImageBillingConfig, error) {
	imageModel := ""
	imageSize := ""
	hasImageTool := false
	if len(body) > 0 && gjson.ValidBytes(body) {
		tools := gjson.GetBytes(body, "tools")
		if tools.IsArray() {
			tools.ForEach(func(_, item gjson.Result) bool {
				if openAIJSONString(item.Get("type")) != "image_generation" {
					return true
				}
				hasImageTool = true
				imageModel = openAIJSONString(item.Get("model"))
				imageSize = openAIJSONString(item.Get("size"))
				return false
			})
		}
		if imageSize == "" {
			imageSize = openAIJSONString(gjson.GetBytes(body, "size"))
		}
		if imageModel == "" {
			bodyModel := openAIJSONString(gjson.GetBytes(body, "model"))
			if isOpenAIImageBillingModelAlias(bodyModel) || !hasImageTool {
				imageModel = bodyModel
			}
		}
	}
	if imageModel == "" && hasImageTool {
		imageModel = "gpt-image-2"
	}
	if imageModel == "" {
		imageModel = strings.TrimSpace(fallbackModel)
	}
	return OpenAIResponsesImageBillingConfig{
		Model:     imageModel,
		SizeTier:  normalizeOpenAIImageSizeTier(imageSize),
		InputSize: imageSize,
	}, nil
}

// ResolveOpenAIResponsesImageRoutingModel returns the image model that must be
// available before routing a /v1/responses image_generation request. This keeps
// dispatch from selecting a text-only account and discovering the image gap only
// after the upstream call has already failed.
func ResolveOpenAIResponsesImageRoutingModel(body []byte, fallbackModel string) string {
	fallbackModel = strings.TrimSpace(fallbackModel)
	imageModel := ""
	hasImageTool := false
	if len(body) > 0 && gjson.ValidBytes(body) {
		tools := gjson.GetBytes(body, "tools")
		if tools.IsArray() {
			tools.ForEach(func(_, item gjson.Result) bool {
				if openAIJSONString(item.Get("type")) != "image_generation" {
					return true
				}
				hasImageTool = true
				imageModel = openAIJSONString(item.Get("model"))
				return false
			})
		}
		if !hasImageTool && openAIJSONInputContainsImageGenTool(gjson.GetBytes(body, "input")) {
			hasImageTool = true
		}
	}
	if imageModel != "" {
		return imageModel
	}
	if isOpenAIImageBillingModelAlias(fallbackModel) {
		return fallbackModel
	}
	if len(body) > 0 && gjson.ValidBytes(body) {
		bodyModel := openAIJSONString(gjson.GetBytes(body, "model"))
		if isOpenAIImageBillingModelAlias(bodyModel) {
			return bodyModel
		}
	}
	if hasImageTool {
		return "gpt-image-2"
	}
	return fallbackModel
}

func isOpenAIImageBillingModelAlias(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	return isOpenAIImageGenerationModel(normalized) || strings.Contains(normalized, "image")
}

func openAIJSONString(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(value.String())
}
