package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_gateway_request_body.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	urlvalidator "github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"strings"
	"unicode/utf8"
)


// validateOutboundURL 按 security.url_allowlist 策略校验网关主动连接的出站 URL。
func (s *OpenAIGatewayService) validateOutboundURL(raw string) (string, error) {
	if s.cfg == nil {
		return urlvalidator.ValidateURLFormat(raw, false)
	}
	if !s.cfg.Security.URLAllowlist.Enabled {
		return urlvalidator.ValidateURLFormat(raw, s.cfg.Security.URLAllowlist.AllowInsecureHTTP)
	}
	return urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     s.cfg.Security.URLAllowlist.UpstreamHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
	})
}

func shouldPreserveOpenAIResponsesNoneReasoningEffort(account *Account) bool {
	if account == nil {
		return false
	}
	if account.IsOpenAIPassthroughEnabled() {
		return true
	}
	if account.IsOpenAIOAuthLike() {
		return true
	}
	if !account.IsOpenAIApiKey() {
		return false
	}
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	return baseURL == "" || isOfficialOpenAIModelsBaseURL(baseURL)
}

// Codex 0.149.0 needs a single advertised effort to directly select a visible
// non-reasoning model. Treat that catalog-only "none" value as omission for
// compatible upstreams, while preserving official OpenAI request semantics.
func filterOpenAIResponsesNoneReasoningEffortForAccount(account *Account, body []byte) ([]byte, error) {
	if len(body) == 0 || shouldPreserveOpenAIResponsesNoneReasoningEffort(account) {
		return body, nil
	}

	out := body
	for _, path := range []string{"reasoning.effort", "reasoning_effort"} {
		effort := gjson.GetBytes(out, path)
		if effort.Type != gjson.String || !strings.EqualFold(strings.TrimSpace(effort.String()), "none") {
			continue
		}
		next, err := sjson.DeleteBytes(out, path)
		if err != nil {
			return body, fmt.Errorf("strip %s none placeholder: %w", path, err)
		}
		out = next
	}
	if reasoning := gjson.GetBytes(out, "reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
		next, err := sjson.DeleteBytes(out, "reasoning")
		if err != nil {
			return body, fmt.Errorf("strip empty reasoning object: %w", err)
		}
		out = next
	}
	return out, nil
}

func deleteOpenAIResponsesNoneReasoningEffortFromObject(account *Account, body map[string]any) {
	if body == nil || shouldPreserveOpenAIResponsesNoneReasoningEffort(account) {
		return
	}
	if effort, ok := body["reasoning_effort"].(string); ok && strings.EqualFold(strings.TrimSpace(effort), "none") {
		delete(body, "reasoning_effort")
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		return
	}
	if effort, ok := reasoning["effort"].(string); ok && strings.EqualFold(strings.TrimSpace(effort), "none") {
		delete(reasoning, "effort")
	}
	if len(reasoning) == 0 {
		delete(body, "reasoning")
	}
}

func normalizeOpenAIParallelToolCallsWithoutTools(body []byte, responsesLite bool) ([]byte, bool, error) {
	if responsesLite {
		return body, false, nil
	}
	parallel := gjson.GetBytes(body, "parallel_tool_calls")
	if !parallel.Exists() {
		return body, false, nil
	}
	if openAIRequestBodyHasTools(body) {
		return body, false, nil
	}
	normalized, err := sjson.DeleteBytes(body, "parallel_tool_calls")
	if err != nil {
		return body, false, fmt.Errorf("normalize parallel_tool_calls without tools: %w", err)
	}
	return normalized, true, nil
}

// openAIRequestBodyHasTools 同时识别顶层 tools 和 input[].additional_tools。
func openAIRequestBodyHasTools(body []byte) bool {
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return true
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
			continue
		}
		if tools := item.Get("tools"); tools.IsArray() && len(tools.Array()) > 0 {
			return true
		}
	}
	return false
}

// normalizeOpenAIResponsesReasoningContentReplay removes non-portable
// reasoning.content arrays before history is sent to a real OpenAI Responses
// endpoint. Compatible providers may return visible reasoning blocks there,
// while OpenAI accepts only an empty array when the item is replayed.
//
// Keep the reasoning item and its portable fields (summary, encrypted_content,
// ids, and opaque extensions). Callers scope this normalization to OpenAI
// destinations; compatible providers may still consume their own content.
func normalizeOpenAIResponsesReasoningContentReplay(body []byte) ([]byte, bool, error) {
	input := parseRawJSONView(body).Get("input")
	if !input.IsArray() {
		return body, false, nil
	}

	needsNormalization := false
	input.ForEach(func(_, item gjson.Result) bool {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			return true
		}
		content := item.Get("content")
		if content.IsArray() && len(content.Array()) > 0 {
			needsNormalization = true
			return false
		}
		return true
	})
	if !needsNormalization {
		return body, false, nil
	}

	var reqBody map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &reqBody); err != nil {
		return body, false, fmt.Errorf("normalize OpenAI reasoning content replay: %w", err)
	}
	items, ok := reqBody["input"].([]any)
	if !ok {
		return body, false, nil
	}
	changed := false
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "reasoning" {
			continue
		}
		content, ok := item["content"].([]any)
		if !ok || len(content) == 0 {
			continue
		}
		delete(item, "content")
		changed = true
	}
	if !changed {
		return body, false, nil
	}
	normalized, err := marshalOpenAIUpstreamJSON(reqBody)
	if err != nil {
		return body, false, fmt.Errorf("serialize normalized OpenAI reasoning content replay: %w", err)
	}
	return normalized, true, nil
}

func normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body []byte, knownStoreFalse bool) ([]byte, bool, error) {
	if !knownStoreFalse && gjson.GetBytes(body, "store").Type != gjson.False {
		return body, false, nil
	}
	root := parseRawJSONView(body)
	input := root.Get("input")
	if !input.IsArray() {
		return body, false, nil
	}
	if !root.IsObject() || !gjson.ValidBytes(body) || !utf8.Valid(body) || hasDuplicateJSONObjectKeys(root) {
		return normalizeOpenAIAPIKeyStoreFalseReasoningReplayDecoded(body, knownStoreFalse)
	}

	// Only reasoning metadata needs decoding. Keep large image/tool results as
	// slices of the original JSON and copy them once into the final request.
	items := make([]string, 0)
	changed := false
	fallback := false
	var itemErr error
	input.ForEach(func(_, item gjson.Result) bool {
		if !item.IsObject() {
			items = append(items, item.Raw)
			return true
		}
		if hasDuplicateJSONObjectKeys(item) {
			fallback = true
			return false
		}
		typ := strings.TrimSpace(item.Get("type").String())
		id := strings.TrimSpace(item.Get("id").String())
		encrypted := item.Get("encrypted_content")
		if (typ == "reasoning" && (encrypted.Type != gjson.String || strings.TrimSpace(encrypted.Str) == "")) ||
			(typ == "item_reference" && strings.HasPrefix(id, "rs_")) {
			changed = true
			return true
		}
		stripID := typ == "reasoning" && strings.HasPrefix(id, "rs_")
		addSummary := typ == "reasoning" && item.Get("summary").Type == gjson.Null
		stripCallID := shouldStripOpenAIResponsesNonPairCallID(typ) && item.Get("call_id").Exists()
		if !stripID && !addSummary && !stripCallID {
			items = append(items, item.Raw)
			return true
		}
		var decoded map[string]any
		if err := decodeOpenAIJSONUseNumber([]byte(item.Raw), &decoded); err != nil {
			itemErr = err
			return false
		}
		if stripID {
			delete(decoded, "id")
		}
		if addSummary {
			decoded["summary"] = []any{}
		}
		if stripCallID {
			delete(decoded, "call_id")
		}
		encoded, err := marshalOpenAIUpstreamJSON(decoded)
		if err != nil {
			itemErr = err
			return false
		}
		items = append(items, string(encoded))
		changed = true
		return true
	})
	if fallback {
		return normalizeOpenAIAPIKeyStoreFalseReasoningReplayDecoded(body, knownStoreFalse)
	}
	if itemErr != nil {
		return body, false, fmt.Errorf("normalize API-key store=false reasoning replay: %w", itemErr)
	}
	if !changed {
		return body, false, nil
	}
	return replaceOpenAIRawInput(body, input, items), true, nil
}

// Preserve the decoder's handling of unusual or duplicate-key input objects.
func normalizeOpenAIAPIKeyStoreFalseReasoningReplayDecoded(body []byte, knownStoreFalse bool) ([]byte, bool, error) {
	if !knownStoreFalse && gjson.GetBytes(body, "store").Type != gjson.False {
		return body, false, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false, nil
	}

	var reqBody map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &reqBody); err != nil {
		return body, false, fmt.Errorf("normalize API-key store=false reasoning replay: %w", err)
	}
	items, ok := reqBody["input"].([]any)
	if !ok {
		return body, false, nil
	}
	filtered := make([]any, 0, len(items))
	changed := false
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			filtered = append(filtered, rawItem)
			continue
		}
		typ := strings.TrimSpace(firstNonEmptyString(item["type"]))
		id := strings.TrimSpace(firstNonEmptyString(item["id"]))
		switch typ {
		case "reasoning":
			encryptedContent, hasEncryptedContent := item["encrypted_content"].(string)
			if !hasEncryptedContent || strings.TrimSpace(encryptedContent) == "" {
				changed = true
				continue
			}
			if strings.HasPrefix(id, "rs_") {
				delete(item, "id")
				changed = true
			}
			if summary, ok := item["summary"]; !ok || summary == nil {
				item["summary"] = []any{}
				changed = true
			}
		case "item_reference":
			if strings.HasPrefix(id, "rs_") {
				changed = true
				continue
			}
		}
		if shouldStripOpenAIResponsesNonPairCallID(typ) {
			if _, hasCallID := item["call_id"]; hasCallID {
				delete(item, "call_id")
				changed = true
			}
		}
		filtered = append(filtered, item)
	}
	if !changed {
		return body, false, nil
	}
	reqBody["input"] = filtered
	normalized, err := marshalOpenAIUpstreamJSON(reqBody)
	if err != nil {
		return body, false, fmt.Errorf("serialize API-key store=false reasoning replay: %w", err)
	}
	return normalized, true, nil
}

func normalizeOpenAICodexCompactReasoningEffortForAccount(c *gin.Context, account *Account, body []byte) ([]byte, bool, error) {
	if account == nil || !account.IsOpenAIOAuthLike() || !isOpenAIResponsesCompactPath(c) {
		return body, false, nil
	}

	requestedModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	effectiveModel := account.GetMappedModel(requestedModel)
	return normalizeOpenAICodexCompactReasoningEffort(body, effectiveModel)
}

func normalizeOpenAICodexCompactReasoningEffort(body []byte, effectiveModel string) ([]byte, bool, error) {
	if !isOpenAIGPT56Model(effectiveModel) ||
		!strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String()), "max") {
		return body, false, nil
	}

	// Codex Ultra 在客户端编排层会下发 max；ChatGPT compact 端点目前只接受到
	// xhigh。这里只降级 OpenAI OAuth 的 GPT-5.6 compact 子请求，普通 Responses、
	// API Key 请求和其他平台的 OAuth 请求保留 max。
	normalized, err := sjson.SetBytes(body, "reasoning.effort", "xhigh")
	if err != nil {
		return body, false, fmt.Errorf("normalize codex compact reasoning effort: %w", err)
	}
	return normalized, true, nil
}

func normalizeOpenAIOAuthResponsesCompatibilityFields(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	changed := false
	if prompt, exists := reqBody["prompt"]; exists {
		if input, hasInput := reqBody["input"]; !hasInput || input == nil {
			if prompt != nil {
				reqBody["input"] = prompt
			}
		}
		delete(reqBody, "prompt")
		changed = true
	}
	if _, exists := reqBody["commands"]; exists {
		delete(reqBody, "commands")
		changed = true
	}
	// Codex can attach internal message metadata when a custom provider is
	// named OpenAI. ChatGPT rejects this field on input items (#7066).
	input, _ := reqBody["input"].([]any)
	for _, value := range input {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := item["internal_chat_message_metadata_passthrough"]; exists {
			delete(item, "internal_chat_message_metadata_passthrough")
			changed = true
		}
	}
	return changed
}

func normalizeOpenAIOAuthResponsesCompatibilityBody(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	normalized := body
	changed := false
	prompt := gjson.GetBytes(normalized, "prompt")
	if prompt.Exists() {
		input := gjson.GetBytes(normalized, "input")
		if prompt.Type != gjson.Null && (!input.Exists() || input.Type == gjson.Null) {
			next, err := sjson.SetRawBytes(normalized, "input", []byte(prompt.Raw))
			if err != nil {
				return body, false, fmt.Errorf("normalize oauth responses prompt: %w", err)
			}
			normalized = next
		}
		next, err := sjson.DeleteBytes(normalized, "prompt")
		if err != nil {
			return body, false, fmt.Errorf("normalize oauth responses delete prompt: %w", err)
		}
		normalized = next
		changed = true
	}
	if gjson.GetBytes(normalized, "commands").Exists() {
		next, err := sjson.DeleteBytes(normalized, "commands")
		if err != nil {
			return body, false, fmt.Errorf("normalize oauth responses delete commands: %w", err)
		}
		normalized = next
		changed = true
	}
	// Only remove the input-item field, never same-named user content.
	input := gjson.GetBytes(normalized, "input")
	if !input.IsArray() {
		return normalized, changed, nil
	}
	for i, item := range input.Array() {
		if !item.IsObject() || !item.Get("internal_chat_message_metadata_passthrough").Exists() {
			continue
		}
		next, err := sjson.DeleteBytes(normalized, fmt.Sprintf("input.%d.internal_chat_message_metadata_passthrough", i))
		if err != nil {
			return body, false, fmt.Errorf("normalize oauth input metadata: %w", err)
		}
		normalized = next
		changed = true
	}
	return normalized, changed, nil
}

func normalizeOpenAIResponsesReasoningMode(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	// Astra 的 reasoning.mode 与 reasoning.effort 是独立参数，不做兼容替换；非 Astra 维持旧 strip-mode/pro->max 行为。
	if isOpenAIGPT6AstraModel(gjson.GetBytes(body, "model").String()) {
		return body, false, nil
	}
	mode := gjson.GetBytes(body, "reasoning.mode")
	if !mode.Exists() || mode.Type != gjson.String {
		return body, false, nil
	}
	updated := body
	effort := gjson.GetBytes(body, "reasoning.effort")
	if (!effort.Exists() || effort.Type == gjson.Null || strings.TrimSpace(effort.String()) == "") &&
		strings.EqualFold(strings.TrimSpace(mode.String()), "pro") {
		var err error
		updated, err = sjson.SetBytes(updated, "reasoning.effort", "max")
		if err != nil {
			return body, false, fmt.Errorf("set reasoning effort for mode=pro: %w", err)
		}
	}
	updated, err := sjson.DeleteBytes(updated, "reasoning.mode")
	if err != nil {
		return body, false, fmt.Errorf("delete unsupported reasoning.mode: %w", err)
	}
	if reasoning := gjson.GetBytes(updated, "reasoning"); reasoning.Exists() && reasoning.IsObject() && len(reasoning.Map()) == 0 {
		updated, err = sjson.DeleteBytes(updated, "reasoning")
		if err != nil {
			return body, false, fmt.Errorf("delete empty reasoning object: %w", err)
		}
	}
	return updated, true, nil
}

func normalizeOpenAIResponseFormatSchemasBody(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	textFormat := strings.TrimSpace(gjson.GetBytes(body, "text.format.type").String())
	responseFormat := strings.TrimSpace(gjson.GetBytes(body, "response_format.type").String())
	if textFormat != "json_schema" && responseFormat != "json_schema" {
		return body, false, nil
	}
	var reqBody map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &reqBody); err != nil {
		return body, false, fmt.Errorf("normalize responses schema body: %w", err)
	}
	if !normalizeOpenAIResponseFormatSchemas(reqBody) {
		return body, false, nil
	}
	normalized, err := json.Marshal(reqBody)
	if err != nil {
		return body, false, fmt.Errorf("serialize normalized responses schema body: %w", err)
	}
	return normalized, true, nil
}

func normalizeOpenAIResponsesWebSocketCompatibilityBody(body []byte, account *Account, responsesLite bool) ([]byte, bool, error) {
	if account == nil || !account.IsOpenAI() {
		return body, false, nil
	}
	normalized := body
	changed := false
	if account.IsOpenAIOAuthLike() {
		var err error
		normalized, changed, err = normalizeOpenAIResponsesLegacyIngress(body)
		if err != nil {
			return body, false, err
		}
	}
	if next, normalizedReasoningContent, err := normalizeOpenAIResponsesReasoningContentReplay(normalized); err != nil {
		return body, false, err
	} else if normalizedReasoningContent {
		normalized = next
		changed = true
	}
	if account.IsOpenAIApiKey() {
		if next, normalizedParallel, err := normalizeOpenAIParallelToolCallsWithoutTools(normalized, responsesLite); err != nil {
			return body, false, err
		} else if normalizedParallel {
			normalized = next
			changed = true
		}
		if next, normalizedReasoning, err := normalizeOpenAIAPIKeyStoreFalseReasoningReplay(normalized, false); err != nil {
			return body, false, err
		} else if normalizedReasoning {
			normalized = next
			changed = true
		}
	}
	if sanitized, idsChanged, err := sanitizeOpenAIResponsesInputItemIDs(normalized); err != nil {
		return body, false, fmt.Errorf("sanitize websocket Responses input item IDs: %w", err)
	} else if idsChanged {
		normalized = sanitized
		changed = true
	}
	if account != nil && account.IsOpenAI() && account.IsOAuth() {
		if reasoningBody, reasoningChanged, err := normalizeOpenAIResponsesReasoningMode(normalized); err != nil {
			return body, false, err
		} else if reasoningChanged {
			normalized = reasoningBody
			changed = true
		}
	}
	if account != nil && account.IsOpenAIOAuthLike() {
		oauthBody, oauthChanged, err := normalizeOpenAIOAuthResponsesCompatibilityBody(normalized)
		if err != nil {
			return body, false, err
		}
		normalized = oauthBody
		changed = changed || oauthChanged
		for _, field := range openAIChatGPTInternalUnsupportedFields {
			if !gjson.GetBytes(normalized, field).Exists() {
				continue
			}
			next, deleteErr := sjson.DeleteBytes(normalized, field)
			if deleteErr != nil {
				return body, false, fmt.Errorf("normalize websocket body delete %s: %w", field, deleteErr)
			}
			normalized = next
			changed = true
		}
	}
	needsOrphanCleanup := account != nil && account.IsOpenAIOAuthLike() &&
		gjson.GetBytes(normalized, "input").IsArray()
	if needsOrphanCleanup || openAIResponsesInputMayNeedTruncation(normalized) {
		var reqBody map[string]any
		if err := decodeOpenAIJSONUseNumber(normalized, &reqBody); err != nil {
			return body, false, fmt.Errorf("normalize websocket Responses body: %w", err)
		}
		mapChanged := false
		if needsOrphanCleanup {
			if input, ok := reqBody["input"].([]any); ok && sanitizeOpenAIResponsesOrphanToolOutputs(
				reqBody,
				input,
				strings.TrimSpace(firstNonEmptyString(reqBody["previous_response_id"])) != "",
			) {
				mapChanged = true
			}
		}
		if truncateOpenAIResponsesInputText(reqBody) {
			mapChanged = true
		}
		if mapChanged {
			next, err := marshalOpenAIUpstreamJSON(reqBody)
			if err != nil {
				return body, false, fmt.Errorf("serialize normalized websocket Responses body: %w", err)
			}
			normalized = next
			changed = true
		}
	}
	if schemaBody, schemaChanged, err := normalizeOpenAIResponseFormatSchemasBody(normalized); err != nil {
		return body, false, err
	} else if schemaChanged {
		normalized = schemaBody
		changed = true
	}
	if openAIRequestBodyImageGenerationToolNeedsNormalization(normalized) {
		var reqBody map[string]any
		if err := json.Unmarshal(normalized, &reqBody); err != nil {
			return body, false, fmt.Errorf("normalize websocket image tool body: %w", err)
		}
		if normalizeOpenAIResponsesImageGenerationTools(reqBody) {
			next, err := json.Marshal(reqBody)
			if err != nil {
				return body, false, fmt.Errorf("serialize normalized websocket image tool body: %w", err)
			}
			normalized = next
			changed = true
		}
	}
	if account != nil {
		if schemaBody, schemaChanged, err := sanitizeOpenAIResponsesToolSchemasForPlatform(normalized, account.Platform); err != nil {
			return body, false, fmt.Errorf("normalize websocket tool schemas: %w", err)
		} else if schemaChanged {
			normalized = schemaBody
			changed = true
		}
	}
	// Keep this last: earlier compatibility passes may filter or rebuild input.
	// Remote compaction v2 requires one trigger as the final input item.
	if triggerBody, triggerChanged, err := NormalizeCompactionTriggerInputOrder(normalized); err != nil {
		return body, false, fmt.Errorf("normalize websocket compaction trigger order: %w", err)
	} else if triggerChanged {
		normalized = triggerBody
		changed = true
	}
	return normalized, changed, nil
}

func isOpenAICodexModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "codex")
}

func explicitRequestedReasoningEffortFromBody(body []byte) string {
	raw := strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	}
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(body, "output_config.effort").String())
	}
	return raw
}

// CanonicalRequestedReasoningEffort extracts the client-requested effort before
// group policy rewriting and before model-family remapping (max -> xhigh).
// Empty or unknown values return nil. "max" is preserved even for models that
// later persist "xhigh".
func CanonicalRequestedReasoningEffort(body []byte, modelCandidates ...string) *string {
	if raw := explicitRequestedReasoningEffortFromBody(body); raw != "" {
		canonical := NormalizeMaxReasoningEffort(raw)
		if canonical == "" {
			return nil
		}
		return &canonical
	}
	for _, model := range modelCandidates {
		if value := canonicalReasoningEffortFromModelSuffix(model); value != "" {
			return &value
		}
	}
	if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); model != "" {
		if value := canonicalReasoningEffortFromModelSuffix(model); value != "" {
			return &value
		}
	}
	return nil
}

func canonicalReasoningEffortFromModelSuffix(model string) string {
	if strings.TrimSpace(model) == "" {
		return ""
	}
	modelID := strings.TrimSpace(model)
	if strings.Contains(modelID, "/") {
		parts := strings.Split(modelID, "/")
		modelID = parts[len(parts)-1]
	}
	parts := strings.FieldsFunc(strings.ToLower(modelID), func(r rune) bool {
		switch r {
		case '-', '_', ' ':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return ""
	}
	return NormalizeMaxReasoningEffort(parts[len(parts)-1])
}

// ErrInvalidOpenAIServiceTier indicates a request carried a service_tier value
// that is not a known OpenAI tier. HTTP handlers translate it into a 400
// invalid_request_error so malformed values are rejected up front instead of
// being silently stripped (which would mask the user's intent to use fast
// mode).
type ErrInvalidOpenAIServiceTier struct {
	Value string
}

func (e *ErrInvalidOpenAIServiceTier) Error() string {
	return fmt.Sprintf("invalid service_tier %q: must be one of auto, default, fast, flex, priority, scale, ultrafast", e.Value)
}

const invalidOpenAIServiceTierValueMaxLen = 64

func boundInvalidOpenAIServiceTierValue(raw string) string {
	if len(raw) <= invalidOpenAIServiceTierValueMaxLen {
		return raw
	}
	return raw[:invalidOpenAIServiceTierValueMaxLen] + "..."
}

// ValidateOpenAIServiceTierField validates the service_tier field of a raw
// OpenAI-compatible request body (/v1/responses and /v1/chat/completions).
//
//   - absent / null → valid, returns "" (field omitted keeps current behavior)
//   - "fast" → normalized to "priority" (the two are equivalent; the canonical
//     value is what reaches the OpenAI upstream)
//   - "priority" / "flex" / "auto" / "default" / "scale" / "ultrafast" → valid, returned as-is
//   - an explicitly present non-string value, an empty string, or any other
//     unknown value → *ErrInvalidOpenAIServiceTier (handler maps to HTTP 400),
//     matching OpenAI's enum validation semantics
func ValidateOpenAIServiceTierField(body []byte) (string, error) {
	tierResult := gjson.GetBytes(body, "service_tier")
	if !tierResult.Exists() || tierResult.Type == gjson.Null {
		return "", nil
	}
	if tierResult.Type != gjson.String {
		return "", &ErrInvalidOpenAIServiceTier{Value: "<non-string>"}
	}
	raw := strings.TrimSpace(tierResult.String())
	if raw == "" {
		return "", &ErrInvalidOpenAIServiceTier{Value: raw}
	}
	norm := normalizedOpenAIServiceTierValue(raw)
	if norm == "" {
		return "", &ErrInvalidOpenAIServiceTier{Value: boundInvalidOpenAIServiceTierValue(raw)}
	}
	return norm, nil
}

// shouldForceOpenAIFastPriorityForMissingTier reports whether a request that
// omitted service_tier should be upgraded to priority. This is opt-in through
// the dedicated "missing" tier matcher; legacy "all" rules continue to apply
// only to requests that explicitly selected a recognized tier.
func (s *OpenAIGatewayService) shouldForceOpenAIFastPriorityForMissingTier(ctx context.Context, account *Account, model string) bool {
	if account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	action, _ := s.evaluateOpenAIFastPolicy(ctx, account, model, OpenAIFastTierMissing)
	return action == OpenAIFastPolicyActionForcePriority
}

func openAIGroupForcesFast(ctx context.Context, account *Account) bool {
	if ctx == nil || account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	return IsGroupContextValid(group) && groupSupportsOpenAIFast(group.Platform) && group.ForceOpenAIFast
}

func CanonicalRequestedReasoningEffortFromReqBody(reqBody map[string]any, modelCandidates ...string) *string {
	if reqBody == nil {
		return CanonicalRequestedReasoningEffort(nil, modelCandidates...)
	}
	raw := ""
	if reasoning, ok := reqBody["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok {
			raw = strings.TrimSpace(effort)
		}
	}
	if raw == "" {
		if effort, ok := reqBody["reasoning_effort"].(string); ok {
			raw = strings.TrimSpace(effort)
		}
	}
	if raw != "" {
		canonical := NormalizeMaxReasoningEffort(raw)
		if canonical == "" {
			return nil
		}
		return &canonical
	}
	return CanonicalRequestedReasoningEffort(nil, modelCandidates...)
}

// supportsOpenAIReasoningEffortMax reports model families whose upstream scale
// has a distinct max level. Other models keep the legacy max -> xhigh behavior.
func supportsOpenAIReasoningEffortMax(model string) bool {
	if isOpenAIGPT6AstraModel(model) || isOpenAIGPT56Model(model) {
		return true
	}

	normalized := strings.ToLower(lastOpenAIModelSegment(model))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch {
	case strings.HasPrefix(normalized, "deepseek-v4"), strings.HasPrefix(normalized, "deepseek-flash"):
		// deepseek-flash（= DeepSeek-V4.1-Flash）与 v4 系同为 low/high/max 档位。
		return true
	case strings.HasPrefix(normalized, "glm-"):
		return true
	case strings.HasPrefix(normalized, "kimi-"), strings.HasPrefix(normalized, "moonshot-"):
		return true
	case normalized == "k3" || strings.HasPrefix(normalized, "k3-"):
		return true
	default:
		return false
	}
}


// [v0.2.5 新增]
func normalizeOpenAIResponseFormatSchemas(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	modified := false
	normalizeFormat := func(format map[string]any) {
		if format == nil || strings.TrimSpace(firstNonEmptyString(format["type"])) != "json_schema" {
			return
		}
		if schema, ok := format["schema"].(map[string]any); ok && normalizeOpenAIResponseJSONSchema(schema) {
			modified = true
		}
		if jsonSchema, ok := format["json_schema"].(map[string]any); ok {
			if schema, ok := jsonSchema["schema"].(map[string]any); ok && normalizeOpenAIResponseJSONSchema(schema) {
				modified = true
			}
		}
	}
	if text, ok := reqBody["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok {
			normalizeFormat(format)
		}
	}
	if responseFormat, ok := reqBody["response_format"].(map[string]any); ok {
		normalizeFormat(responseFormat)
	}
	return modified
}




// [v0.2.5 新增]
func groupSupportsOpenAIFast(platform string) bool {
	return platform == PlatformOpenAI || platform == PlatformComposite
}

// [v0.2.5 新增]
func normalizeOpenAIResponseJSONSchema(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	modified := false
	for _, key := range []string{"uniqueItems", "minProperties"} {
		if _, exists := schema[key]; exists {
			delete(schema, key)
			modified = true
		}
	}
	if rawType, exists := schema["type"]; !exists || rawType == nil {
		switch {
		case schema["properties"] != nil:
			schema["type"] = "object"
			modified = true
		case schema["items"] != nil:
			schema["type"] = "array"
			modified = true
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for _, raw := range properties {
			if child, ok := raw.(map[string]any); ok && normalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	switch items := schema["items"].(type) {
	case map[string]any:
		if normalizeOpenAIResponseJSONSchema(items) {
			modified = true
		}
	case []any:
		for _, raw := range items {
			if child, ok := raw.(map[string]any); ok && normalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	for _, key := range []string{
		"additionalProperties",
		"additionalItems",
		"contains",
		"not",
		"if",
		"then",
		"else",
		"propertyNames",
		"unevaluatedProperties",
		"unevaluatedItems",
	} {
		if child, ok := schema[key].(map[string]any); ok && normalizeOpenAIResponseJSONSchema(child) {
			modified = true
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		children, _ := schema[key].([]any)
		for _, raw := range children {
			if child, ok := raw.(map[string]any); ok && normalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	for _, key := range []string{"$defs", "definitions", "patternProperties", "dependentSchemas"} {
		children, _ := schema[key].(map[string]any)
		for _, raw := range children {
			if child, ok := raw.(map[string]any); ok && normalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	if dependencies, ok := schema["dependencies"].(map[string]any); ok {
		for _, raw := range dependencies {
			if child, ok := raw.(map[string]any); ok && normalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	return modified
}

// [v0.2.5 新增]
const OpenAIFastTierMissing = "missing"
