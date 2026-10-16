package service

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
)

func ExtractContentModerationText(protocol string, body []byte) string {
	return ExtractContentModerationInput(protocol, body).Text
}

func ExtractContentModerationInput(protocol string, body []byte) ContentModerationInput {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ContentModerationInput{}
	}
	segments, images := ExtractContentModerationSegments(protocol, body)
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		addModerationText(&parts, segment.Text)
	}
	out := ContentModerationInput{
		Text:     strings.Join(parts, "\n"),
		Images:   normalizeModerationImages(images),
		Segments: segments,
	}
	out.Normalize()
	return out
}

const (
	ContentModerationSourceUser           = "user"
	ContentModerationSourceSkill          = "skill"
	ContentModerationSourceTool           = "tool"
	ContentModerationSourceInstructions   = "instructions"
	contentModerationInstructionTopLevel  = "top_level"
	contentModerationInstructionSystem    = "input_system"
	contentModerationInstructionDeveloper = "input_developer"

	// 512 KiB 按 JSON 解码后的实际指令字节计算。编码后的原始 JSON 最多可能
	// 用 6 个字节表示 1 个 ASCII 字节（例如 \u0061），因此另设 6 倍硬上限，
	// 既不误拒合法转义文本，也不在 String() 解码前接收无界的大字段。
	maxContentModerationInstructionsBytes    = 512 * 1024
	maxContentModerationInstructionsRawBytes = maxContentModerationInstructionsBytes*6 + 2
)

type contentModerationJSONField struct {
	Key   string
	Value gjson.Result
	Count int
}

// contentModerationObjectFieldFold mirrors encoding/json's case-insensitive
// struct-field matching and last-value-wins behavior. Chat/Anthropic adapters
// decode into structs, so an exact-only gjson lookup would let `Instructions`,
// `System`, `Role` or `Content` reach the converter without entering pre-check.
func contentModerationObjectFieldFold(object gjson.Result, name string) contentModerationJSONField {
	var field contentModerationJSONField
	if !object.IsObject() {
		return field
	}
	object.ForEach(func(key, value gjson.Result) bool {
		decodedKey := key.String()
		if !strings.EqualFold(decodedKey, name) {
			return true
		}
		field = contentModerationJSONField{Key: decodedKey, Value: value, Count: field.Count + 1}
		return true
	})
	return field
}

// validateInstructionMessageFields only walks the immediate message envelope
// and, for system/developer messages, its immediate text parts. User history can
// be very large and deeply nested, so unrelated user/tool payloads must not be
// recursively inspected just to defend instruction decoding.
func validateInstructionMessageFields(messages gjson.Result) (statusCode int, message string) {
	if !messages.IsArray() {
		return 0, ""
	}
	for _, item := range messages.Array() {
		if !item.IsObject() {
			continue
		}
		roleField := contentModerationObjectFieldFold(item, "role")
		if roleField.Count > 1 {
			// role itself decides whether content becomes an upstream instruction;
			// accepting two spellings would make that security boundary ambiguous.
			return 400, "duplicate client instruction role fields are not allowed"
		}
		role := strings.ToLower(strings.TrimSpace(roleField.Value.String()))
		if role != "system" && role != "developer" {
			continue
		}

		contentField := contentModerationObjectFieldFold(item, "content")
		if contentField.Count > 1 {
			return 400, "duplicate client instruction content fields are not allowed"
		}
		if !contentField.Value.IsArray() {
			continue
		}
		for _, part := range contentField.Value.Array() {
			if !part.IsObject() {
				continue
			}
			if contentModerationObjectFieldFold(part, "type").Count > 1 {
				return 400, "duplicate client instruction part type fields are not allowed"
			}
			if contentModerationObjectFieldFold(part, "text").Count > 1 {
				return 400, "duplicate client instruction part text fields are not allowed"
			}
		}
	}
	return 0, ""
}

func validateAnthropicSystemBlockFields(system gjson.Result) (statusCode int, message string) {
	if !system.IsArray() {
		return 0, ""
	}
	for _, block := range system.Array() {
		if !block.IsObject() {
			continue
		}
		if contentModerationObjectFieldFold(block, "type").Count > 1 {
			return 400, "duplicate Anthropic system block type fields are not allowed"
		}
		if contentModerationObjectFieldFold(block, "text").Count > 1 {
			return 400, "duplicate Anthropic system block text fields are not allowed"
		}
	}
	return 0, ""
}

func validateGeminiSystemInstructionFields(system gjson.Result) (statusCode int, message string) {
	if !system.IsObject() {
		return 0, ""
	}
	partsField := contentModerationObjectFieldFold(system, "parts")
	if partsField.Count > 1 {
		return 400, "duplicate Gemini systemInstruction parts fields are not allowed"
	}
	if !partsField.Value.IsArray() {
		return 0, ""
	}
	for _, part := range partsField.Value.Array() {
		if !part.IsObject() {
			continue
		}
		if contentModerationObjectFieldFold(part, "text").Count > 1 {
			return 400, "duplicate Gemini systemInstruction part text fields are not allowed"
		}
	}
	return 0, ""
}

func inspectTopLevelInstructions(body []byte) (count int, maxRawBytes int, maxDecodedBytes int) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0, 0, 0
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return 0, 0, 0
	}
	root.ForEach(func(key, value gjson.Result) bool {
		if !strings.EqualFold(key.String(), "instructions") {
			return true
		}
		count++
		if rawBytes := len(value.Raw); rawBytes > maxRawBytes {
			maxRawBytes = rawBytes
		}
		// 先执行编码硬上限，再解码真正的字符串长度，避免边界内的大量转义
		// 被错误当成超限，同时不给异常超大值制造额外分配的机会。
		if value.Type == gjson.String && len(value.Raw) <= maxContentModerationInstructionsRawBytes {
			if decodedBytes := len(value.String()); decodedBytes > maxDecodedBytes {
				maxDecodedBytes = decodedBytes
			}
		}
		return true
	})
	return count, maxRawBytes, maxDecodedBytes
}

func validateTopLevelInstructions(body []byte) (statusCode int, message string) {
	return validateClientInstructions(ContentModerationProtocolOpenAIResponses, body)
}

// validateClientInstructions applies one fail-closed envelope to every public
// protocol that can turn client-controlled text into an upstream system or
// developer instruction. It runs before normalization/truncation, so an NFKC
// expansion can never hide an unsafe tail that is still forwarded upstream.
func validateClientInstructions(protocol string, body []byte) (statusCode int, message string) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0, ""
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return 0, ""
	}

	texts := make([]string, 0, 4)
	decodedBytes := 0
	decodedPartCount := 0
	appendInstructionText := func(text string) (int, string) {
		if text == "" {
			return 0, ""
		}
		separatorBytes := 0
		if decodedPartCount > 0 {
			separatorBytes = 2
		}
		if len(text) > maxContentModerationInstructionsBytes-decodedBytes-separatorBytes {
			return 413, "combined client instructions exceeds the 512 KiB limit"
		}
		decodedBytes += separatorBytes + len(text)
		decodedPartCount++
		texts = append(texts, text)
		return 0, ""
	}
	appendTopLevelInstructions := func() (int, string) {
		count, maxRawBytes, maxDecodedBytes := inspectTopLevelInstructions(body)
		if count > 1 {
			return 400, "duplicate top-level instructions fields are not allowed"
		}
		if maxRawBytes > maxContentModerationInstructionsRawBytes || maxDecodedBytes > maxContentModerationInstructionsBytes {
			return 413, "top-level instructions exceeds the 512 KiB limit"
		}
		field := contentModerationObjectFieldFold(root, "instructions")
		if field.Value.Type == gjson.String && field.Value.String() != "" {
			return appendInstructionText(field.Value.String())
		}
		return 0, ""
	}
	appendInstructionMessages := func(messages gjson.Result) (int, string) {
		for _, item := range messages.Array() {
			if responsesInputInstructionRawTooLarge(item) {
				return 413, "client instruction field exceeds the encoded size limit"
			}
			separatorBytes := 0
			if decodedPartCount > 0 {
				separatorBytes = 2
			}
			remaining := maxContentModerationInstructionsBytes - decodedBytes - separatorBytes
			text, _, ok, tooLarge := instructionMessageTextWithLimit(item, remaining)
			if tooLarge {
				return 413, "combined client instructions exceeds the 512 KiB limit"
			}
			if ok {
				if code, reason := appendInstructionText(text); code != 0 {
					return code, reason
				}
			}
		}
		return 0, ""
	}

	switch protocol {
	case ContentModerationProtocolOpenAIResponses:
		inputField := contentModerationObjectFieldFold(root, "input")
		if inputField.Count > 1 {
			return 400, "duplicate top-level input fields are not allowed"
		}
		input := inputField.Value
		if code, reason := validateInstructionMessageFields(input); code != 0 {
			return code, reason
		}
		if code, reason := appendInstructionMessages(input); code != 0 {
			return code, reason
		}
		if code, reason := appendTopLevelInstructions(); code != 0 {
			return code, reason
		}
	case ContentModerationProtocolOpenAIChat:
		messagesField := contentModerationObjectFieldFold(root, "messages")
		if messagesField.Count > 1 {
			return 400, "duplicate top-level messages fields are not allowed"
		}
		messages := messagesField.Value
		if code, reason := validateInstructionMessageFields(messages); code != 0 {
			return code, reason
		}
		if code, reason := appendInstructionMessages(messages); code != 0 {
			return code, reason
		}
		if code, reason := appendTopLevelInstructions(); code != 0 {
			return code, reason
		}
	case ContentModerationProtocolAnthropicMessages:
		systemField := contentModerationObjectFieldFold(root, "system")
		if systemField.Count > 1 {
			return 400, "duplicate top-level system fields are not allowed"
		}
		if code, reason := validateAnthropicSystemBlockFields(systemField.Value); code != 0 {
			return code, reason
		}
		if anthropicSystemInstructionRawTooLarge(systemField.Value) {
			return 413, "client instruction field exceeds the encoded size limit"
		}
		text, ok, tooLarge := anthropicSystemInstructionTextWithLimit(systemField.Value, maxContentModerationInstructionsBytes)
		if tooLarge {
			return 413, "combined client instructions exceeds the 512 KiB limit"
		}
		if ok {
			if code, reason := appendInstructionText(text); code != 0 {
				return code, reason
			}
		}
	case ContentModerationProtocolGemini:
		// Gemini OAuth 请求会由 encoding/json 解码为 GeminiRequest。这里使用
		// 相同的大小写不敏感、重复字段最后值生效口径，不能让
		// SystemInstruction/PARTS/TEXT 绕过转换前的本地检查。
		systemField := contentModerationObjectFieldFold(root, "systemInstruction")
		if systemField.Count > 1 {
			return 400, "duplicate top-level systemInstruction fields are not allowed"
		}
		if code, reason := validateGeminiSystemInstructionFields(systemField.Value); code != 0 {
			return code, reason
		}
		if geminiSystemInstructionRawTooLarge(systemField.Value) {
			return 413, "client instruction field exceeds the encoded size limit"
		}
		text, ok, tooLarge := geminiSystemInstructionTextWithLimit(systemField.Value, maxContentModerationInstructionsBytes)
		if tooLarge {
			return 413, "combined client instructions exceeds the 512 KiB limit"
		}
		if ok {
			if code, reason := appendInstructionText(text); code != 0 {
				return code, reason
			}
		}
	default:
		return 0, ""
	}

	normalizedBytes := 0
	partCount := 0
	for _, text := range texts {
		if text == "" {
			continue
		}
		separatorBytes := 0
		if partCount > 0 {
			separatorBytes = 2
		}
		// Use the exact bounded local-policy normalizer with one extra rune of
		// headroom. If compatibility normalization expands past the envelope,
		// reject instead of silently trimming the tail and forwarding it.
		normalized := normalizeContentPolicyTextWithLimit(text, maxContentPolicyInstructionsTextRunes+1)
		if len(normalized) > maxContentModerationInstructionsBytes-normalizedBytes-separatorBytes {
			return 413, "normalized client instructions exceeds the 512 KiB limit"
		}
		normalizedBytes += separatorBytes + len(normalized)
		partCount++
	}
	return 0, ""
}

// ExtractContentModerationSegments 只取本轮尾部新增内容。请求虽然携带完整历史，
// 但这里绝不回扫旧用户消息或旧工具结果，避免一次命中后整个会话被永久拦截。
func ExtractContentModerationSegments(protocol string, body []byte) ([]ContentModerationSegment, []string) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, nil
	}
	root := gjson.ParseBytes(body)
	switch protocol {
	case ContentModerationProtocolOpenAIResponses:
		return extractResponsesRequestSegments(root, protocol)
	case ContentModerationProtocolOpenAIChat:
		return extractChatRequestSegments(root, protocol)
	case ContentModerationProtocolAnthropicMessages:
		return extractAnthropicRequestSegments(root, protocol)
	case ContentModerationProtocolGemini:
		return extractGeminiRequestSegments(root, protocol)
	case ContentModerationProtocolOpenAIImages:
		var parts, images []string
		addModerationText(&parts, root.Get("prompt").String())
		collectContentValue(root.Get("images"), &parts, &images)
		return moderationSegmentsFromParts(parts, ContentModerationSourceUser, protocol, ""), images
	default:
		if segments, images := extractResponsesTailSegments(root.Get("input"), protocol); len(segments) > 0 || len(images) > 0 {
			return segments, images
		}
		if segments, images := extractChatTailSegments(root.Get("messages"), protocol); len(segments) > 0 || len(images) > 0 {
			return segments, images
		}
		return extractGeminiTailSegments(root.Get("contents"), protocol)
	}
}

func extractResponsesRequestSegments(root gjson.Result, protocol string) ([]ContentModerationSegment, []string) {
	input := contentModerationObjectFieldFold(root, "input").Value
	segments, images := extractResponsesTailSegments(input, protocol)
	instructionSegments := contentModerationTopLevelInstructionSegments(root, protocol)
	// OpenAI OAuth 转换会把 input 中 role=system 的文本提升到顶层
	// instructions；role=developer 本身也会作为高优先级指令发送给上游。
	// 真实 Responses 转换固定先放顶层 instructions，再按 input 顺序追加
	// system/developer。审查副本必须保持相同顺序，否则跨边界短语会漏检，
	// 反向短语还可能误报。普通用户/工具 segment 仍排在前面使用独立预算。
	instructionSegments = append(instructionSegments, extractResponsesInputInstructionSegments(input, protocol)...)
	return append(segments, instructionSegments...), images
}

func contentModerationTopLevelInstructionSegments(root gjson.Result, protocol string) []ContentModerationSegment {
	instructions := contentModerationObjectFieldFold(root, "instructions").Value
	if instructions.Type != gjson.String {
		return nil
	}
	segments := moderationSegmentsFromParts(
		[]string{instructions.String()}, ContentModerationSourceInstructions, protocol, "",
	)
	for index := range segments {
		segments[index].InstructionOrigin = contentModerationInstructionTopLevel
	}
	return segments
}

func extractResponsesInputInstructionSegments(input gjson.Result, protocol string) []ContentModerationSegment {
	if !input.IsArray() {
		return nil
	}
	var segments []ContentModerationSegment
	for _, item := range input.Array() {
		text, ok := responsesInputInstructionText(item)
		if !ok || text == "" {
			continue
		}
		itemSegments := moderationSegmentsFromParts(
			[]string{text}, ContentModerationSourceInstructions, protocol, "",
		)
		origin := contentModerationInstructionSystem
		if contentModerationObjectFieldFold(item, "role").Value.String() == "developer" {
			origin = contentModerationInstructionDeveloper
		}
		for index := range itemSegments {
			itemSegments[index].InstructionOrigin = origin
		}
		segments = append(segments, itemSegments...)
	}
	return segments
}

// responsesInputInstructionText 镜像 OAuth 转换器的文本提取口径：只认精确
// 小写 system/developer 角色，字符串原样取值，文本 content parts 按原顺序
// 无分隔拼接。这样危险短语拆在两个 part 中也不会在转换后绕过预审。
func responsesInputInstructionText(item gjson.Result) (string, bool) {
	text, _, ok, _ := instructionMessageTextWithLimit(item, -1)
	return text, ok
}

// instructionMessageTextWithLimit mirrors the struct-based protocol adapters
// while bounding the aggregate allocation before strings.Builder grows. A
// single escaped JSON string may still need decoding, but a 64 MiB frame split
// across many legal parts can no longer be concatenated before the 512 KiB
// envelope is enforced.
func instructionMessageTextWithLimit(item gjson.Result, maxBytes int) (text string, role string, ok bool, tooLarge bool) {
	role = contentModerationObjectFieldFold(item, "role").Value.String()
	if role != "system" && role != "developer" {
		return "", "", false, false
	}
	content := contentModerationObjectFieldFold(item, "content").Value
	if content.Type == gjson.String {
		value := content.String()
		if maxBytes >= 0 && len(value) > maxBytes {
			return "", role, true, true
		}
		return value, role, true, false
	}
	if !content.IsArray() {
		return "", role, true, false
	}
	var b strings.Builder
	for _, part := range content.Array() {
		switch contentModerationObjectFieldFold(part, "type").Value.String() {
		case "text", "input_text", "output_text":
			if text := contentModerationObjectFieldFold(part, "text").Value; text.Type == gjson.String {
				value := text.String()
				if maxBytes >= 0 && len(value) > maxBytes-b.Len() {
					return "", role, true, true
				}
				_, _ = b.WriteString(value)
			}
		}
	}
	return b.String(), role, true, false
}

func responsesInputInstructionRawTooLarge(item gjson.Result) bool {
	role := contentModerationObjectFieldFold(item, "role").Value.String()
	if role != "system" && role != "developer" {
		return false
	}
	content := contentModerationObjectFieldFold(item, "content").Value
	if content.Type == gjson.String {
		return len(content.Raw) > maxContentModerationInstructionsRawBytes
	}
	for _, part := range content.Array() {
		switch contentModerationObjectFieldFold(part, "type").Value.String() {
		case "text", "input_text", "output_text":
			if text := contentModerationObjectFieldFold(part, "text").Value; text.Type == gjson.String && len(text.Raw) > maxContentModerationInstructionsRawBytes {
				return true
			}
		}
	}
	return false
}

func extractChatRequestSegments(root gjson.Result, protocol string) ([]ContentModerationSegment, []string) {
	messages := contentModerationObjectFieldFold(root, "messages").Value
	if !messages.Exists() {
		// Chat's compatibility adapter accepts Responses input-only bodies too.
		// Inspect that same shape rather than silently treating it as empty.
		return extractResponsesRequestSegments(root, protocol)
	}
	segments, images := extractChatTailSegments(messages, protocol)
	// Chat → Responses 与 Responses fallback 都先保留顶层 instructions，
	// 再转换 messages 中的 system/developer，顺序与实际上游请求一致。
	instructionSegments := contentModerationTopLevelInstructionSegments(root, protocol)
	for _, item := range messages.Array() {
		text, role, ok := chatInstructionMessageText(item)
		if !ok || text == "" {
			continue
		}
		itemSegments := moderationSegmentsFromParts(
			[]string{text}, ContentModerationSourceInstructions, protocol, "",
		)
		origin := contentModerationInstructionSystem
		if role == "developer" {
			origin = contentModerationInstructionDeveloper
		}
		for index := range itemSegments {
			itemSegments[index].InstructionOrigin = origin
		}
		instructionSegments = append(instructionSegments, itemSegments...)
	}
	return append(segments, instructionSegments...), images
}

// chatInstructionMessageText 同时服务首次预审和后续历史隔离。控制消息的
// 多个文本 part 按顺序无分隔拼接，避免把危险短语拆进 output_text 等 part
// 后绕过首审，也避免首审记录的指纹与下一轮隔离口径不同。
func chatInstructionMessageText(item gjson.Result) (text string, role string, ok bool) {
	text, role, ok, _ = instructionMessageTextWithLimit(item, -1)
	return text, role, ok
}

func extractAnthropicRequestSegments(root gjson.Result, protocol string) ([]ContentModerationSegment, []string) {
	segments, images := extractAnthropicTailSegments(contentModerationObjectFieldFold(root, "messages").Value, protocol)
	parts := anthropicSystemInstructionParts(contentModerationObjectFieldFold(root, "system").Value)
	systemSegments := moderationSegmentsFromParts(
		parts, ContentModerationSourceInstructions, protocol, "",
	)
	for index := range systemSegments {
		// Anthropic 顶层 system 在协议转换后会成为 developer 消息，按
		// system 控制来源处理，避免把客户系统提示词外发给第三方审查 API。
		systemSegments[index].InstructionOrigin = contentModerationInstructionSystem
	}
	return append(segments, systemSegments...), images
}

// anthropicSystemInstructionParts 保留每个 text block 的独立边界。首次
// 拒绝时才能只记录真正贡献命中的 block 指纹，下一轮也只删除这些 block，
// 而不是把计费归因和其他安全规则连同整个 system 字段一起清空。
func anthropicSystemInstructionParts(system gjson.Result) []string {
	if system.Type == gjson.String {
		text := system.String()
		if text == "" || isStrictAnthropicBillingAttributionText(text) {
			return nil
		}
		return []string{text}
	}
	if !system.IsArray() {
		return nil
	}
	parts := make([]string, 0, len(system.Array()))
	for _, block := range system.Array() {
		if contentModerationObjectFieldFold(block, "type").Value.String() != "text" {
			continue
		}
		text := contentModerationObjectFieldFold(block, "text").Value
		if text.Type != gjson.String || text.String() == "" || isStrictAnthropicBillingAttributionText(text.String()) {
			continue
		}
		parts = append(parts, text.String())
	}
	return parts
}

// anthropicSystemInstructionTextWithLimit 镜像协议转换器的总解码大小，
// 但只豁免严格、整块均为机器归因字段的文本；“合法头 + 自然语言尾文”
// 必须计入预算并进入后续本地审查。
func anthropicSystemInstructionTextWithLimit(system gjson.Result, maxBytes int) (string, bool, bool) {
	if system.Type == gjson.String {
		text := system.String()
		if text == "" || isStrictAnthropicBillingAttributionText(text) {
			return "", true, false
		}
		if maxBytes >= 0 && len(text) > maxBytes {
			return "", true, true
		}
		return text, true, false
	}
	if !system.IsArray() {
		return "", false, false
	}
	var b strings.Builder
	for _, block := range system.Array() {
		if contentModerationObjectFieldFold(block, "type").Value.String() != "text" {
			continue
		}
		text := contentModerationObjectFieldFold(block, "text").Value
		if text.Type != gjson.String || text.String() == "" || isStrictAnthropicBillingAttributionText(text.String()) {
			continue
		}
		value := text.String()
		if maxBytes >= 0 && len(value) > maxBytes-b.Len() {
			return "", true, true
		}
		_, _ = b.WriteString(value)
	}
	return b.String(), true, false
}

func anthropicSystemInstructionRawTooLarge(system gjson.Result) bool {
	if system.Type == gjson.String {
		return len(system.Raw) > maxContentModerationInstructionsRawBytes
	}
	for _, block := range system.Array() {
		if contentModerationObjectFieldFold(block, "type").Value.String() != "text" {
			continue
		}
		text := contentModerationObjectFieldFold(block, "text").Value
		if text.Type == gjson.String && len(text.Raw) > maxContentModerationInstructionsRawBytes {
			return true
		}
	}
	return false
}

func extractGeminiRequestSegments(root gjson.Result, protocol string) ([]ContentModerationSegment, []string) {
	segments, images := extractGeminiTailSegments(contentModerationObjectFieldFold(root, "contents").Value, protocol)
	parts := geminiSystemInstructionParts(contentModerationObjectFieldFold(root, "systemInstruction").Value)
	systemSegments := moderationSegmentsFromParts(
		parts, ContentModerationSourceInstructions, protocol, "",
	)
	for index := range systemSegments {
		// 系统指令只参加本网关语种和关键词检查；标成 system 后，可选的
		// 第三方 moderation API（内容审查接口）不会收到客户系统提示词。
		systemSegments[index].InstructionOrigin = contentModerationInstructionSystem
	}
	return append(segments, systemSegments...), images
}

func geminiSystemInstructionParts(system gjson.Result) []string {
	if !system.IsObject() {
		return nil
	}
	partsField := contentModerationObjectFieldFold(system, "parts").Value
	if !partsField.IsArray() {
		return nil
	}
	parts := make([]string, 0, len(partsField.Array()))
	for _, part := range partsField.Array() {
		text := contentModerationObjectFieldFold(part, "text").Value
		if text.Type == gjson.String && text.String() != "" {
			parts = append(parts, text.String())
		}
	}
	return parts
}

// geminiSystemInstructionTextWithLimit 镜像 GeminiRequest.SystemInstruction
// 的 struct（结构体）解码：parts 中每个 GeminiPart.Text 按原顺序无分隔
// 拼接。限制在 Builder（字符串构建器）扩容前执行，防止大量小 part 先造成
// 大内存分配再被 512 KiB 门禁拒绝。
func geminiSystemInstructionTextWithLimit(system gjson.Result, maxBytes int) (string, bool, bool) {
	if !system.IsObject() {
		return "", false, false
	}
	parts := contentModerationObjectFieldFold(system, "parts").Value
	if !parts.IsArray() {
		return "", false, false
	}
	var b strings.Builder
	for _, part := range parts.Array() {
		text := contentModerationObjectFieldFold(part, "text").Value
		if text.Type != gjson.String {
			continue
		}
		value := text.String()
		if maxBytes >= 0 && len(value) > maxBytes-b.Len() {
			return "", true, true
		}
		_, _ = b.WriteString(value)
	}
	return b.String(), true, false
}

func geminiSystemInstructionRawTooLarge(system gjson.Result) bool {
	if !system.IsObject() {
		return false
	}
	parts := contentModerationObjectFieldFold(system, "parts").Value
	for _, part := range parts.Array() {
		text := contentModerationObjectFieldFold(part, "text").Value
		if text.Type == gjson.String && len(text.Raw) > maxContentModerationInstructionsRawBytes {
			return true
		}
	}
	return false
}

func contentModerationRemoteInput(input ContentModerationInput) ContentModerationInput {
	segments := make([]ContentModerationSegment, 0, len(input.Segments))
	parts := make([]string, 0, len(input.Segments))
	for _, segment := range input.Segments {
		// 客户端控制指令可能含私有提示词、密钥或内部路径。本轮新增的
		// instructions 覆盖只参加本网关本地语种与关键词策略，不能顺带
		// 扩大兼容保留的第三方 moderation API（内容审查接口）外发范围。
		if segment.Source == ContentModerationSourceInstructions {
			continue
		}
		segments = append(segments, segment)
		addModerationText(&parts, segment.Text)
	}
	out := ContentModerationInput{
		Text:     strings.Join(parts, "\n"),
		Images:   append([]string(nil), input.Images...),
		Segments: segments,
	}
	out.Normalize()
	return out
}

func moderationSegmentsFromParts(parts []string, source, protocol, callID string) []ContentModerationSegment {
	out := make([]ContentModerationSegment, 0, len(parts))
	for _, part := range parts {
		text := normalizeContentModerationText(part)
		if text == "" {
			continue
		}
		out = append(out, ContentModerationSegment{Source: source, Protocol: protocol, CallID: callID, Text: text})
	}
	return out
}

func extractResponsesTailSegments(input gjson.Result, protocol string) ([]ContentModerationSegment, []string) {
	if input.Type == gjson.String {
		return []ContentModerationSegment{{Source: ContentModerationSourceUser, Protocol: protocol, Text: input.String()}}, nil
	}
	if !input.IsArray() {
		return nil, nil
	}
	items := input.Array()
	if len(items) == 0 {
		return nil, nil
	}
	if isResponsesToolOutput(items[len(items)-1]) {
		start := len(items) - 1
		for start > 0 && isResponsesToolOutput(items[start-1]) {
			start--
		}
		segments := make([]ContentModerationSegment, 0, len(items)-start)
		var images []string
		for _, item := range items[start:] {
			callID := moderationFirstNonEmpty(contentModerationObjectFieldFold(item, "call_id").Value.String(), contentModerationObjectFieldFold(item, "id").Value.String())
			var parts []string
			collectContentValue(contentModerationObjectFieldFold(item, "output").Value, &parts, &images)
			collectContentValue(contentModerationObjectFieldFold(item, "content").Value, &parts, &images)
			source := ContentModerationSourceTool
			if responsesCallLooksLikeSkill(items, start, callID) || contentLooksLikeSkill(strings.Join(parts, "\n")) {
				source = ContentModerationSourceSkill
			}
			segments = append(segments, moderationSegmentsFromParts(parts, source, protocol, callID)...)
		}
		return segments, images
	}
	last := items[len(items)-1]
	if !isResponsesUserTextItem(last) {
		return nil, nil
	}
	var parts, images []string
	collectContentValue(contentModerationObjectFieldFold(last, "content").Value, &parts, &images)
	if contentModerationObjectFieldFold(last, "type").Value.String() == "input_text" || contentModerationObjectFieldFold(last, "text").Value.Exists() {
		collectContentValue(last, &parts, &images)
	}
	return moderationSegmentsFromParts(parts, ContentModerationSourceUser, protocol, ""), images
}

func isResponsesToolOutput(item gjson.Result) bool {
	switch strings.ToLower(strings.TrimSpace(contentModerationObjectFieldFold(item, "type").Value.String())) {
	case "function_call_output", "custom_tool_call_output", "computer_call_output", "tool_result":
		return true
	default:
		return false
	}
}

func responsesCallLooksLikeSkill(items []gjson.Result, outputStart int, callID string) bool {
	if callID == "" {
		return false
	}
	checked := 0
	for index := outputStart - 1; index >= 0 && checked < 64; index-- {
		checked++
		item := items[index]
		candidateID := moderationFirstNonEmpty(item.Get("call_id").String(), item.Get("id").String())
		if candidateID != callID {
			continue
		}
		return contentLooksLikeSkill(item.Get("name").String() + " " + item.Get("arguments").String())
	}
	return false
}

func extractChatTailSegments(messages gjson.Result, protocol string) ([]ContentModerationSegment, []string) {
	if !messages.IsArray() {
		return nil, nil
	}
	items := messages.Array()
	if len(items) == 0 {
		return nil, nil
	}
	if strings.EqualFold(strings.TrimSpace(contentModerationObjectFieldFold(items[len(items)-1], "role").Value.String()), "tool") {
		start := len(items) - 1
		for start > 0 && strings.EqualFold(strings.TrimSpace(contentModerationObjectFieldFold(items[start-1], "role").Value.String()), "tool") {
			start--
		}
		var segments []ContentModerationSegment
		var images []string
		for _, item := range items[start:] {
			callID := contentModerationObjectFieldFold(item, "tool_call_id").Value.String()
			var parts []string
			collectContentValue(contentModerationObjectFieldFold(item, "content").Value, &parts, &images)
			source := ContentModerationSourceTool
			if chatCallLooksLikeSkill(items, start, callID) || contentLooksLikeSkill(strings.Join(parts, "\n")) {
				source = ContentModerationSourceSkill
			}
			segments = append(segments, moderationSegmentsFromParts(parts, source, protocol, callID)...)
		}
		return segments, images
	}
	last := items[len(items)-1]
	if !strings.EqualFold(strings.TrimSpace(contentModerationObjectFieldFold(last, "role").Value.String()), "user") {
		return nil, nil
	}
	var parts, images []string
	collectContentValue(contentModerationObjectFieldFold(last, "content").Value, &parts, &images)
	return moderationSegmentsFromParts(parts, ContentModerationSourceUser, protocol, ""), images
}

func chatCallLooksLikeSkill(items []gjson.Result, outputStart int, callID string) bool {
	if callID == "" {
		return false
	}
	checked := 0
	for index := outputStart - 1; index >= 0 && checked < 64; index-- {
		checked++
		calls := items[index].Get("tool_calls")
		if !calls.IsArray() {
			continue
		}
		for _, call := range calls.Array() {
			if call.Get("id").String() == callID {
				return contentLooksLikeSkill(call.Get("function.name").String() + " " + call.Get("function.arguments").String())
			}
		}
	}
	return false
}

func extractAnthropicTailSegments(messages gjson.Result, protocol string) ([]ContentModerationSegment, []string) {
	if !messages.IsArray() {
		return nil, nil
	}
	items := messages.Array()
	if len(items) == 0 {
		return nil, nil
	}
	last := items[len(items)-1]
	if !strings.EqualFold(strings.TrimSpace(last.Get("role").String()), "user") {
		return nil, nil
	}
	content := last.Get("content")
	if !content.IsArray() {
		var parts, images []string
		collectAnthropicUserContentValue(content, &parts, &images)
		return moderationSegmentsFromParts(parts, ContentModerationSourceUser, protocol, ""), images
	}
	blocks := content.Array()
	if len(blocks) > 0 && strings.EqualFold(blocks[len(blocks)-1].Get("type").String(), "tool_result") {
		start := len(blocks) - 1
		for start > 0 && strings.EqualFold(blocks[start-1].Get("type").String(), "tool_result") {
			start--
		}
		var segments []ContentModerationSegment
		var images []string
		for _, block := range blocks[start:] {
			callID := block.Get("tool_use_id").String()
			var parts []string
			collectContentValue(block.Get("content"), &parts, &images)
			source := ContentModerationSourceTool
			if anthropicCallLooksLikeSkill(items, callID) || contentLooksLikeSkill(strings.Join(parts, "\n")) {
				source = ContentModerationSourceSkill
			}
			segments = append(segments, moderationSegmentsFromParts(parts, source, protocol, callID)...)
		}
		return segments, images
	}
	var parts, images []string
	collectAnthropicUserContentValue(content, &parts, &images)
	return moderationSegmentsFromParts(parts, ContentModerationSourceUser, protocol, ""), images
}

func anthropicCallLooksLikeSkill(messages []gjson.Result, callID string) bool {
	if callID == "" {
		return false
	}
	checked := 0
	for index := len(messages) - 2; index >= 0 && checked < 32; index-- {
		checked++
		for _, block := range messages[index].Get("content").Array() {
			if block.Get("type").String() == "tool_use" && block.Get("id").String() == callID {
				return contentLooksLikeSkill(block.Get("name").String() + " " + block.Get("input").Raw)
			}
		}
	}
	return false
}

func extractGeminiTailSegments(contents gjson.Result, protocol string) ([]ContentModerationSegment, []string) {
	if !contents.IsArray() {
		return nil, nil
	}
	items := contents.Array()
	if len(items) == 0 {
		return nil, nil
	}
	last := items[len(items)-1]
	partsResult := last.Get("parts")
	partsArray := partsResult.Array()
	if len(partsArray) > 0 && (partsArray[len(partsArray)-1].Get("functionResponse").Exists() || partsArray[len(partsArray)-1].Get("function_response").Exists()) {
		var segments []ContentModerationSegment
		var images []string
		for _, part := range partsArray {
			response := part.Get("functionResponse")
			if !response.Exists() {
				response = part.Get("function_response")
			}
			if !response.Exists() {
				continue
			}
			name := response.Get("name").String()
			text := moderationFirstNonEmpty(response.Get("response.output").String(), response.Get("response.content").String(), response.Get("response").Raw)
			source := ContentModerationSourceTool
			if contentLooksLikeSkill(name + " " + text) {
				source = ContentModerationSourceSkill
			}
			segments = append(segments, moderationSegmentsFromParts([]string{text}, source, protocol, name)...)
		}
		return segments, images
	}
	role := strings.ToLower(strings.TrimSpace(last.Get("role").String()))
	if role != "" && role != "user" {
		return nil, nil
	}
	var parts, images []string
	for _, part := range partsArray {
		addModerationText(&parts, part.Get("text").String())
		addGeminiModerationImage(&images, part)
	}
	return moderationSegmentsFromParts(parts, ContentModerationSourceUser, protocol, ""), images
}

func contentLooksLikeSkill(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "skill.md") || strings.Contains(lower, "/skills/") || strings.Contains(lower, "\\skills\\") || strings.Contains(lower, "read_skill")
}

func moderationFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func collectLastRoleMessage(messages gjson.Result, role string, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	if strings.ToLower(strings.TrimSpace(last.Get("role").String())) != role {
		return
	}
	var candidate []string
	var candidateImages []string
	collectContentValue(last.Get("content"), &candidate, &candidateImages)
	if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func collectLastAnthropicUserMessage(messages gjson.Result, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	if strings.ToLower(strings.TrimSpace(last.Get("role").String())) != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	collectAnthropicUserContentValue(last.Get("content"), &candidate, &candidateImages)
	if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func collectAnthropicUserContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		addModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collectAnthropicUserContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(contentModerationObjectFieldFold(value, "type").Value.String()))
		switch typ {
		case "", "text", "input_text", "output_text", "message":
			if text := contentModerationObjectFieldFold(value, "text").Value; text.Exists() {
				addModerationText(parts, text.String())
			}
			if content := contentModerationObjectFieldFold(value, "content").Value; content.Exists() {
				collectAnthropicUserContentValue(content, parts, images)
			}
		case "image_url", "input_image", "image":
			collectContentValue(value, parts, images)
		}
	}
}

func isAnthropicSystemReminderText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<system-reminder>")
}

func collectLastResponsesInput(input gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !input.Exists():
		return
	case input.Type == gjson.String:
		addModerationText(parts, input.String())
	case input.IsArray():
		array := input.Array()
		if len(array) == 0 {
			return
		}
		last := array[len(array)-1]
		if !isResponsesUserTextItem(last) {
			return
		}
		collectContentValue(last.Get("content"), parts, images)
		if last.Get("type").String() == "input_text" || last.Get("text").Exists() {
			collectContentValue(last, parts, images)
		}
	case input.IsObject():
		if isResponsesUserTextItem(input) {
			collectContentValue(input.Get("content"), parts, images)
			if input.Get("type").String() == "input_text" || input.Get("text").Exists() {
				collectContentValue(input, parts, images)
			}
		}
	}
}

func isResponsesUserTextItem(item gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(contentModerationObjectFieldFold(item, "role").Value.String()))
	if role == "user" {
		return responseItemHasModerationText(item)
	}
	if role != "" {
		return false
	}
	return responseItemHasModerationText(item)
}

func responseItemHasModerationText(item gjson.Result) bool {
	var parts []string
	var images []string
	collectContentValue(contentModerationObjectFieldFold(item, "content").Value, &parts, &images)
	if contentModerationObjectFieldFold(item, "type").Value.String() == "input_text" || contentModerationObjectFieldFold(item, "text").Value.Exists() {
		collectContentValue(item, &parts, &images)
	}
	return normalizeContentModerationText(strings.Join(parts, "\n")) != "" || len(images) > 0
}

func collectLastGeminiContent(contents gjson.Result, parts *[]string, images *[]string) {
	if !contents.IsArray() {
		return
	}
	array := contents.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	role := strings.ToLower(strings.TrimSpace(last.Get("role").String()))
	if role != "" && role != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	if arr := last.Get("parts"); arr.IsArray() {
		arr.ForEach(func(_, part gjson.Result) bool {
			addModerationText(&candidate, part.Get("text").String())
			addGeminiModerationImage(&candidateImages, part)
			return true
		})
	}
	if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func collectContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		addModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collectContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(contentModerationObjectFieldFold(value, "type").Value.String()))
		addModerationImage(images, value.Get("image_url.url").String())
		addModerationImage(images, value.Get("image_url").String())
		addModerationImage(images, value.Get("url").String())
		addModerationImageData(images, value.Get("source.media_type").String(), value.Get("source.data").String())
		addModerationImageData(images, value.Get("source.mediaType").String(), value.Get("source.data").String())
		addModerationImageData(images, value.Get("media_type").String(), value.Get("data").String())
		addModerationImageData(images, value.Get("mime_type").String(), value.Get("data").String())
		addModerationImageData(images, value.Get("mimeType").String(), value.Get("data").String())
		addModerationImage(images, value.Get("source.data").String())
		addModerationImage(images, value.Get("data").String())
		addModerationImage(images, value.Get("base64").String())
		switch typ {
		case "", "text", "input_text", "output_text", "message":
			if text := contentModerationObjectFieldFold(value, "text").Value; text.Exists() {
				addModerationText(parts, text.String())
			}
			if content := contentModerationObjectFieldFold(value, "content").Value; content.Exists() {
				collectContentValue(content, parts, images)
			}
		case "image_url", "input_image", "image":
		}
	}
}

func addGeminiModerationImage(images *[]string, part gjson.Result) {
	if inlineData := part.Get("inline_data"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mime_type").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	if inlineData := part.Get("inlineData"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mimeType").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	addModerationImage(images, part.Get("file_data.file_uri").String())
	addModerationImage(images, part.Get("fileData.fileUri").String())
}

func addModerationImageData(images *[]string, mimeType string, data string) {
	mimeType = strings.TrimSpace(mimeType)
	data = strings.TrimSpace(data)
	if mimeType == "" || data == "" {
		return
	}
	addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
}

func addModerationImage(images *[]string, image string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}
	if strings.HasPrefix(image, "data:") || strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		*images = append(*images, image)
	}
}

func normalizeModerationImages(images []string) []string {
	out := make([]string, 0, len(images))
	seen := make(map[string]struct{}, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		out = append(out, image)
	}
	return out
}

func limitContentModerationImages(images []string) []string {
	if len(images) <= maxContentModerationInputImages {
		return images
	}
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(images))))
	if err != nil {
		return images[:maxContentModerationInputImages]
	}
	return []string{images[int(idx.Int64())]}
}

func addModerationText(parts *[]string, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	*parts = append(*parts, text)
}

func normalizeContentModerationText(text string) string {
	return collapseContentModerationWhitespace(text)
}

// collapseContentModerationWhitespace 与 strings.Join(strings.Fields(text), " ")
// 保持同一语义，但不会为 256 KiB 的普通 Skill/系统指令创建上万个字段切片。
// 常见的单空格文本直接返回原字符串；确有首尾、连续或 Unicode 空白时才复制。
func collapseContentModerationWhitespace(text string) string {
	if text == "" {
		return ""
	}
	needsCollapse := false
	previousSpace := true // 开头空白需要清理。
	for _, r := range text {
		space := unicode.IsSpace(r)
		if space && (r != ' ' || previousSpace) {
			needsCollapse = true
			break
		}
		previousSpace = space
	}
	if !needsCollapse && !previousSpace {
		return text
	}

	var out strings.Builder
	// 规范化只会缩短文本；按原长度预分配一次即可。
	out.Grow(len(text))
	fieldStart := -1
	for byteIndex, r := range text {
		if !unicode.IsSpace(r) {
			if fieldStart < 0 {
				fieldStart = byteIndex
			}
			continue
		}
		if fieldStart < 0 {
			continue
		}
		if out.Len() > 0 {
			_ = out.WriteByte(' ')
		}
		_, _ = out.WriteString(text[fieldStart:byteIndex])
		fieldStart = -1
	}
	if fieldStart >= 0 {
		if out.Len() > 0 {
			_ = out.WriteByte(' ')
		}
		_, _ = out.WriteString(text[fieldStart:])
	}
	return out.String()
}
