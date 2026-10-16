package service

import "strings"

// 固定的生成约束也可能含有描述和示例，作为客户数据整体送审；这不代表
// 这些配置或引用到的远程资源已经执行、展开或被模型独立判定。
func isContentModerationAIConstraintField(key string) bool {
	switch strings.ToLower(key) {
	case "response_format", "text", "output_config", "output_format", "generationconfig", "generation_config",
		"tool_choice", "function_call", "toolconfig", "tool_config", "stop", "stop_sequences", "prediction",
		"temperature", "top_p", "top_k", "min_p", "seed", "frequency_penalty", "presence_penalty", "repetition_penalty", "logit_bias",
		"max_tokens", "max_completion_tokens", "max_output_tokens", "n", "reasoning", "reasoning_effort", "thinking",
		"parallel_tool_calls", "verbosity", "enable_thinking", "include", "metadata", "safetysettings", "safety_settings",
		"size", "quality", "style", "background", "output_compression", "moderation":
		return true
	}
	return false
}

func validateContentModerationAIProtocolInput(protocol string, root map[string]any) error {
	keys := []string{"messages", "input"}
	if protocol == ContentModerationProtocolGemini {
		keys = []string{"contents"}
	} else if protocol == ContentModerationProtocolAnthropicMessages {
		keys = []string{"messages"}
	} else if protocol == ContentModerationProtocolOpenAIImages {
		keys = []string{"prompt"}
	}
	for _, key := range keys {
		value := aiField(root, key)
		// 空字符串/空数组仍是合法的协议输入形状；是否存在可送内容由运行时
		// 决定，系统说明或工具定义也可以单独送审，不因主文本为空整单拒绝。
		if _, ok := value.(string); ok && (key == "input" || key == "prompt") {
			return nil
		}
		if _, ok := value.([]any); ok && protocol != ContentModerationProtocolOpenAIImages {
			return nil
		}
	}
	return aiError("ai_empty_or_uncovered_input", 503)
}

// 只校验协议必要形状。未知扩展、输出媒体或远程状态不会成为额外准入门槛。
func validateContentModerationAIInputFields(root map[string]any, recordUncovered func(string)) error {
	for key, value := range root {
		if isContentModerationAIConstraintField(key) {
			continue
		}
		switch strings.ToLower(key) {
		case "model", "messages", "input", "contents", "instructions", "system", "systeminstruction", "prompt", "tools", "functions",
			"previous_response_id", "conversation", "conversation_id", "cachedcontent", "cached_content", "file_id", "attachments",
			"stream", "stream_options", "store", "service_tier", "user", "safety_identifier", "prompt_cache_key", "prompt_cache_retention",
			"background", "truncation", "logprobs", "top_logprobs", "response_include", "response_modalities", "modalities",
			"response_format", "encoding_format", "output_format":
		default:
			recordUncovered("field:" + key)
		}
		if value == nil {
			continue
		}
		switch strings.ToLower(key) {
		case "modalities", "response_modalities":
			if aiJSON(value) != `["text"]` && aiJSON(value) != `["TEXT"]` {
				recordUncovered("media:" + key)
			}
		case "truncation":
			if aiString(value) != "disabled" {
				recordUncovered("generation_constraint:" + key)
			}
		}
	}
	return nil
}

func validateContentModerationAIContentFields(obj map[string]any, recordUncovered func(string)) error {
	for key, value := range obj {
		switch strings.ToLower(key) {
		case "role", "type", "content", "text", "output", "input", "arguments", "refusal", "name", "description", "parts", "tool_calls", "function", "function_call", "functionresponse", "function_response", "functioncall", "reasoning_content", "reasoning", "thinking", "summary", "namespace", "annotations", "output_config",
			"id", "call_id", "tool_call_id", "tool_use_id", "status", "is_error", "cache_control", "index", "prefix",
			"image_url", "inline_data", "inlinedata", "source", "media_type", "mime_type", "mimetype", "data", "url", "detail":
		case "signature", "encrypted_content", "encryptedcontent":
			if value != nil && value != "" {
				recordUncovered("history:" + key)
			}
		default:
			recordUncovered("content_field:" + key)
		}
	}
	return nil
}

// 外部 schema（结构定义）只能看到引用地址，不能声称引用内容已被审查。
func contentModerationAIHasRemoteSchema(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "$ref" || key == "$dynamicRef" || key == "$recursiveRef" {
				ref, ok := item.(string)
				if !ok || !strings.HasPrefix(ref, "#") {
					return true
				}
			}
			if contentModerationAIHasRemoteSchema(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if contentModerationAIHasRemoteSchema(item) {
				return true
			}
		}
	}
	return false
}

// 这里只识别已知格式形状以标记未覆盖，不把供应商扩展变成业务拒绝条件。
func contentModerationAIOutputFormatKnown(value any, chat bool) bool {
	format, ok := value.(map[string]any)
	if !ok {
		return false
	}
	switch aiString(aiField(format, "type")) {
	case "text", "json_object":
		return true
	case "json_schema":
		if chat {
			format, ok = aiField(format, "json_schema").(map[string]any)
			if !ok {
				return false
			}
		}
		switch aiField(format, "schema").(type) {
		case map[string]any, bool:
			return true
		}
	}
	return false
}

func contentModerationAIConstraintShapeKnown(protocol, key string, value any) bool {
	switch key {
	case "response_format":
		if protocol == ContentModerationProtocolOpenAIImages {
			return aiString(value) == "url" || aiString(value) == "b64_json"
		}
		return protocol == ContentModerationProtocolOpenAIChat && contentModerationAIOutputFormatKnown(value, true)
	case "text", "output_config":
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for name := range object {
			if !strings.EqualFold(name, "format") && !strings.EqualFold(name, "verbosity") && !strings.EqualFold(name, "effort") {
				return false
			}
		}
		if format := aiField(object, "format"); format != nil {
			return contentModerationAIOutputFormatKnown(format, false)
		}
	case "output_format":
		if protocol == ContentModerationProtocolOpenAIImages {
			return aiString(value) == "png" || aiString(value) == "jpeg" || aiString(value) == "webp"
		}
		return contentModerationAIOutputFormatKnown(value, false)
	}
	return true
}

func appendContentModerationAIConstraints(protocol string, root map[string]any, appendText func(string, string, string) error, recordUncovered func(string)) error {
	// 字段顺序稳定，原字段只形成一段完整 JSON，不按字符切片。
	keys := []string{"response_format", "text", "output_config", "output_format", "generationConfig", "generation_config",
		"tool_choice", "function_call", "toolConfig", "tool_config", "stop", "stop_sequences", "prediction",
		"temperature", "top_p", "top_k", "min_p", "seed", "frequency_penalty", "presence_penalty", "repetition_penalty", "logit_bias",
		"max_tokens", "max_completion_tokens", "max_output_tokens", "n", "reasoning", "reasoning_effort", "thinking",
		"parallel_tool_calls", "verbosity", "enable_thinking", "include", "metadata", "safetySettings", "safety_settings",
		"size", "quality", "style", "background", "output_compression", "moderation"}
	for _, key := range keys {
		value := aiField(root, key)
		if value == nil {
			continue
		}
		if contentModerationAIHasRemoteSchema(value) {
			recordUncovered("schema_reference:" + key)
		}
		if !contentModerationAIConstraintShapeKnown(protocol, key, value) {
			recordUncovered("generation_constraint:" + key)
		}
		if protocol == ContentModerationProtocolGemini && (key == "generationConfig" || key == "generation_config") {
			if config, ok := value.(map[string]any); ok {
				for _, name := range []string{"responseModalities", "response_modalities"} {
					if modes := aiField(config, name); modes != nil && aiJSON(modes) != `["TEXT"]` && aiJSON(modes) != `["text"]` {
						recordUncovered("media:" + key + "/" + name)
					}
				}
			}
		}
		if err := appendText(key, "generation_constraint", aiJSON(value)); err != nil {
			return err
		}
	}
	return nil
}
