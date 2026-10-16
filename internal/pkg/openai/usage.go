package openai

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// ResponseUsage is the protocol-level usage shape shared by HTTP, SSE and
// WebSocket response handlers. Keeping the parser outside service prevents the
// image-tool accounting rules from drifting between transports.
type ResponseUsage struct {
	InputTokens              int
	ImageInputTokens         int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	ImageOutputTokens        int
}

// ExtractResponseUsage parses the final usage evidence carried by an OpenAI
// response. For image generation, aggregate tool usage has atomic precedence
// over response.usage because the latter only describes the orchestration
// response on Codex OAuth traffic. Per-call image_generation_call.usage is the
// fallback when the aggregate tool_usage block is absent.
func ExtractResponseUsage(payload []byte) (ResponseUsage, bool) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ResponseUsage{}, false
	}

	root := gjson.ParseBytes(payload)
	eventType := strings.TrimSpace(root.Get("type").String())
	if eventType != "" && !usageTerminalEvent(eventType) {
		return ResponseUsage{}, false
	}

	base, baseOK := responseUsageFromRoot(root)
	if usageFailureEvent(eventType) || usageFailureStatus(root) {
		// Failed/incomplete/cancelled responses may report real text usage, but
		// incomplete image calls must never become billable image-tool usage.
		return base, baseOK
	}

	for _, path := range []string{"response.tool_usage.image_gen", "tool_usage.image_gen"} {
		if toolUsage, ok := ParseImageToolUsage(root.Get(path)); ok {
			return mergeImageToolUsage(base, baseOK, toolUsage), true
		}
	}

	if callUsage, ok := imageCallUsageFromRoot(root); ok {
		return mergeImageToolUsage(base, baseOK, callUsage), true
	}
	return base, baseOK
}

// ParseUsageObject parses a standard Responses or Chat Completions usage
// object, including image input/output token details.
func ParseUsageObject(value gjson.Result) (ResponseUsage, bool) {
	if !value.Exists() || !value.IsObject() {
		return ResponseUsage{}, false
	}

	inputTokens, inputOK := firstOptionalUsageInt(value, "input_tokens", "prompt_tokens")
	outputTokens, outputOK := firstOptionalUsageInt(value, "output_tokens", "completion_tokens")
	cacheReadTokens, cacheReadOK := firstOptionalUsageInt(value,
		"input_tokens_details.cached_tokens",
		"prompt_tokens_details.cached_tokens",
		"cache_read_input_tokens",
		"cache_read_tokens",
		"cached_tokens",
	)
	cacheWriteTokens, cacheWriteOK := firstOptionalUsageInt(value,
		"input_tokens_details.cache_write_tokens",
		"prompt_tokens_details.cache_write_tokens",
		"cache_write_tokens",
		"cache_creation_input_tokens",
	)
	imageInputTokens, imageInputOK := firstOptionalUsageInt(value,
		"input_tokens_details.image_tokens",
		"prompt_tokens_details.image_tokens",
		"image_input_tokens",
	)
	imageOutputTokens, imageOutputOK := firstOptionalUsageInt(value,
		"output_tokens_details.image_tokens",
		"completion_tokens_details.image_tokens",
		"image_output_tokens",
	)
	if !inputOK || !outputOK || !cacheReadOK || !cacheWriteOK || !imageInputOK || !imageOutputOK {
		return ResponseUsage{}, false
	}
	return ResponseUsage{
		InputTokens:              inputTokens,
		ImageInputTokens:         imageInputTokens,
		OutputTokens:             outputTokens,
		CacheCreationInputTokens: cacheWriteTokens,
		CacheReadInputTokens:     cacheReadTokens,
		ImageOutputTokens:        imageOutputTokens,
	}, true
}

// ParseImageToolUsage validates aggregate image-tool usage atomically. A
// malformed field rejects the whole block so a partial token set cannot
// replace otherwise valid response-level usage.
func ParseImageToolUsage(value gjson.Result) (ResponseUsage, bool) {
	if !value.Exists() || !value.IsObject() {
		return ResponseUsage{}, false
	}
	inputTokens, inputOK := requiredUsageInt(value, "input_tokens", "prompt_tokens")
	outputTokens, outputOK := requiredUsageInt(value, "output_tokens", "completion_tokens")
	imageInputTokens, imageInputOK := firstOptionalUsageInt(value,
		"input_tokens_details.image_tokens",
		"prompt_tokens_details.image_tokens",
		"image_input_tokens",
	)
	imageOutputTokens, imageOutputOK := requiredUsageInt(value,
		"output_tokens_details.image_tokens",
		"completion_tokens_details.image_tokens",
		"image_output_tokens",
	)
	if !inputOK || !outputOK || !imageInputOK || !imageOutputOK {
		return ResponseUsage{}, false
	}
	// OpenAI Responses 会在普通文本请求里也返回一个完整但全为 0 的
	// tool_usage.image_gen 占位块。它只表示“图片工具可用但本次未调用”，
	// 不能覆盖 response.usage 中真实的文本 Token；否则长上下文和普通文本
	// 请求都会被错误记录为 0 用量。真实图片生成必须产生图片输出 Token。
	if outputTokens == 0 || imageOutputTokens == 0 {
		return ResponseUsage{}, false
	}
	if imageInputTokens > inputTokens || imageOutputTokens > outputTokens {
		return ResponseUsage{}, false
	}
	return ResponseUsage{
		InputTokens:       inputTokens,
		ImageInputTokens:  imageInputTokens,
		OutputTokens:      outputTokens,
		ImageOutputTokens: imageOutputTokens,
	}, true
}

func responseUsageFromRoot(root gjson.Result) (ResponseUsage, bool) {
	if usage, ok := ParseUsageObject(root.Get("usage")); ok {
		return usage, true
	}
	return ParseUsageObject(root.Get("response.usage"))
}

func imageCallUsageFromRoot(root gjson.Result) (ResponseUsage, bool) {
	var output gjson.Result
	if responseOutput := root.Get("response.output"); responseOutput.Exists() {
		output = responseOutput
	} else {
		output = root.Get("output")
	}
	if !output.Exists() || !output.IsArray() {
		return ResponseUsage{}, false
	}

	seen := make(map[string]struct{})
	var aggregate ResponseUsage
	found := false
	for index, item := range output.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "image_generation_call" || !successfulImageCall(item) {
			continue
		}
		key := strings.TrimSpace(item.Get("id").String())
		if key == "" {
			// A terminal response array is stable; the index prevents two distinct
			// anonymous images with identical usage from being collapsed.
			key = "anonymous:" + strconv.Itoa(index)
		}
		if _, duplicated := seen[key]; duplicated {
			continue
		}
		seen[key] = struct{}{}

		usage, ok := parseImageCallUsage(item.Get("usage"))
		if !ok {
			continue
		}
		aggregate.InputTokens += usage.InputTokens
		aggregate.ImageInputTokens += usage.ImageInputTokens
		aggregate.OutputTokens += usage.OutputTokens
		aggregate.ImageOutputTokens += usage.ImageOutputTokens
		found = true
	}
	return aggregate, found
}

func parseImageCallUsage(value gjson.Result) (ResponseUsage, bool) {
	if !value.Exists() || !value.IsObject() {
		return ResponseUsage{}, false
	}
	inputTokens, inputOK := requiredUsageInt(value, "input_tokens", "prompt_tokens")
	outputTokens, outputOK := requiredUsageInt(value, "output_tokens", "completion_tokens")
	imageInputTokens, imageInputOK := firstOptionalUsageInt(value,
		"input_tokens_details.image_tokens",
		"prompt_tokens_details.image_tokens",
		"image_input_tokens",
	)
	imageOutputTokens, imageOutputOK := firstOptionalUsageInt(value,
		"output_tokens_details.image_tokens",
		"completion_tokens_details.image_tokens",
		"image_output_tokens",
	)
	if !inputOK || !outputOK || !imageInputOK || !imageOutputOK {
		return ResponseUsage{}, false
	}
	if imageOutputTokens == 0 {
		// The call itself can only emit image tokens, so old OAuth payloads that
		// omit output_tokens_details still have an unambiguous image output total.
		imageOutputTokens = outputTokens
	}
	if imageInputTokens > inputTokens || imageOutputTokens > outputTokens {
		return ResponseUsage{}, false
	}
	return ResponseUsage{
		InputTokens:       inputTokens,
		ImageInputTokens:  imageInputTokens,
		OutputTokens:      outputTokens,
		ImageOutputTokens: imageOutputTokens,
	}, true
}

func successfulImageCall(item gjson.Result) bool {
	switch strings.TrimSpace(item.Get("status").String()) {
	case "failed", "incomplete", "cancelled", "canceled":
		return false
	}
	return strings.TrimSpace(item.Get("result").String()) != "" ||
		strings.TrimSpace(item.Get("b64_json").String()) != "" ||
		strings.TrimSpace(item.Get("url").String()) != "" ||
		strings.TrimSpace(item.Get("status").String()) == "completed"
}

func mergeImageToolUsage(base ResponseUsage, baseOK bool, tool ResponseUsage) ResponseUsage {
	// Tool totals replace, rather than add to, response totals. This matches the
	// OAuth protocol where response.usage measures orchestration and tool usage
	// measures the actual image generation work.
	if tool.ImageInputTokens == 0 && baseOK && base.ImageInputTokens <= tool.InputTokens {
		tool.ImageInputTokens = base.ImageInputTokens
	}
	return tool
}

func usageTerminalEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.done", "response.failed", "response.incomplete",
		"response.cancelled", "response.canceled", "image_generation.completed":
		return true
	default:
		return false
	}
}

func usageFailureEvent(eventType string) bool {
	switch eventType {
	case "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func usageFailureStatus(root gjson.Result) bool {
	status := strings.TrimSpace(root.Get("response.status").String())
	if status == "" {
		status = strings.TrimSpace(root.Get("status").String())
	}
	switch status {
	case "failed", "incomplete", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func requiredUsageInt(value gjson.Result, paths ...string) (int, bool) {
	for _, path := range paths {
		field := value.Get(path)
		if !field.Exists() {
			continue
		}
		return BoundedJSONNonNegativeInt(field)
	}
	return 0, false
}

func firstOptionalUsageInt(value gjson.Result, paths ...string) (int, bool) {
	for _, path := range paths {
		field := value.Get(path)
		if !field.Exists() {
			continue
		}
		return BoundedJSONNonNegativeInt(field)
	}
	return 0, true
}

// BoundedJSONNonNegativeInt parses integral JSON exponent notation without
// invoking an arbitrary-precision parser on an upstream-controlled exponent.
func BoundedJSONNonNegativeInt(value gjson.Result) (int, bool) {
	if !value.Exists() || value.Type != gjson.Number {
		return 0, false
	}
	raw := value.Raw
	if len(raw) == 0 || len(raw) > 64 || raw[0] == '-' {
		return 0, false
	}

	mantissaEnd := len(raw)
	for i, c := range raw {
		if c == 'e' || c == 'E' {
			mantissaEnd = i
			break
		}
	}

	digits := raw[:mantissaEnd]
	fractionDigits := 0
	digitCount := 0
	dotSeen := false
	mantissaIsZero := true
	for _, c := range digits {
		switch {
		case c == '.' && !dotSeen:
			dotSeen = true
		case c >= '0' && c <= '9':
			digitCount++
			mantissaIsZero = mantissaIsZero && c == '0'
			if dotSeen {
				fractionDigits++
			}
		default:
			return 0, false
		}
	}
	if digitCount == 0 {
		return 0, false
	}

	exponent := 0
	if mantissaEnd < len(raw) {
		exponentRaw := raw[mantissaEnd+1:]
		negative := false
		if len(exponentRaw) > 0 && (exponentRaw[0] == '+' || exponentRaw[0] == '-') {
			negative = exponentRaw[0] == '-'
			exponentRaw = exponentRaw[1:]
		}
		if len(exponentRaw) == 0 {
			return 0, false
		}
		for len(exponentRaw) > 1 && exponentRaw[0] == '0' {
			exponentRaw = exponentRaw[1:]
		}
		for _, digit := range exponentRaw {
			if digit < '0' || digit > '9' {
				return 0, false
			}
		}
		if mantissaIsZero {
			return 0, true
		}
		if len(exponentRaw) > 3 {
			return 0, false
		}
		for _, digit := range exponentRaw {
			exponent = exponent*10 + int(digit-'0')
		}
		if exponent > 100 {
			return 0, false
		}
		if negative {
			exponent = -exponent
		}
	}

	trailingZeros := exponent - fractionDigits
	scaleReduction := 0
	if trailingZeros < 0 {
		scaleReduction = -trailingZeros
		remaining := scaleReduction
		allZeros := true
		for i := len(digits) - 1; i >= 0; i-- {
			if digits[i] == '.' {
				continue
			}
			if digits[i] != '0' {
				allZeros = false
				if remaining > 0 {
					return 0, false
				}
			}
			if remaining > 0 {
				remaining--
			}
		}
		if remaining > 0 {
			if allZeros {
				return 0, true
			}
			return 0, false
		}
	}

	maxInt := int(^uint(0) >> 1)
	parsed := 0
	digitsToAccumulate := digitCount - scaleReduction
	for _, c := range digits {
		if c == '.' {
			continue
		}
		if digitsToAccumulate <= 0 {
			break
		}
		if parsed > (maxInt-int(c-'0'))/10 {
			return 0, false
		}
		parsed = parsed*10 + int(c-'0')
		digitsToAccumulate--
	}
	if trailingZeros < 0 {
		return parsed, true
	}
	for ; trailingZeros > 0; trailingZeros-- {
		if parsed > maxInt/10 {
			return 0, false
		}
		parsed *= 10
	}
	return parsed, true
}
