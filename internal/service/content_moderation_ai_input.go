package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

func parseContentModerationAIStrictJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, aiError("ai_invalid_utf8", http.StatusServiceUnavailable)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func() (any, error)
	read = func() (any, error) {
		tok, err := d.Token()
		if err != nil {
			return nil, aiError("ai_invalid_json", 503)
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return tok, nil
		}
		switch delim {
		case '{':
			obj := map[string]any{}
			seen := map[string]bool{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return nil, aiError("ai_invalid_json", 503)
				}
				key, ok := keyToken.(string)
				if !ok || seen[strings.ToLower(key)] {
					return nil, aiError("ai_duplicate_json_key", 503)
				}
				seen[strings.ToLower(key)] = true
				value, err := read()
				if err != nil {
					return nil, err
				}
				obj[key] = value
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, aiError("ai_invalid_json", 503)
			}
			return obj, nil
		case '[':
			arr := []any{}
			for d.More() {
				value, err := read()
				if err != nil {
					return nil, err
				}
				arr = append(arr, value)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, aiError("ai_invalid_json", 503)
			}
			return arr, nil
		default:
			return nil, aiError("ai_invalid_json", 503)
		}
	}
	value, err := read()
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, aiError("ai_json_trailing_data", 503)
	}
	return value, nil
}

func aiField(obj map[string]any, key string) any {
	for k, v := range obj {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return nil
}
func aiString(value any) string { s, _ := value.(string); return s }

// 输入版本描述证据格式；不用于缓存或复用旧判定。
const contentModerationAICanonicalVersion = "tokenav-ai-input-v8"

// 所有已提交的角色文本都视为客户数据，不因 system 标签、工具角色或历史
// 判定而获得信任。ForwardBody 原字节保留，抽取不会修改实际业务请求。
func CanonicalContentModerationAIInput(protocol string, body []byte, cfg ContentModerationAIConfig) (*ContentModerationAICanonicalInput, error) {
	return canonicalContentModerationAIInput(protocol, body, cfg, false)
}

func canonicalContentModerationAIInput(protocol string, body []byte, cfg ContentModerationAIConfig, responsesWebSocket bool) (*ContentModerationAICanonicalInput, error) {
	out := &ContentModerationAICanonicalInput{Protocol: protocol, ForwardBody: append([]byte(nil), body...)}
	hashDomain := "http"
	if responsesWebSocket {
		hashDomain = contentModerationAIResponsesWSDomain
	}
	// 失败时也保留已提取文本、图片元信息、未覆盖字段及原文身份，日志才能
	// 区分“抽取失败”和“没有内容”。JSON 解析失败时只能保留原字节身份。
	var parsed any
	defer func() {
		// 未覆盖标记来自 map 遍历；稳定排序后同一请求才能得到可复核的证据身份。
		sort.Strings(out.UncoveredFields)
		identity := string(body)
		if parsed != nil {
			identity = aiJSON(parsed)
		}
		h := sha256.Sum256([]byte(contentModerationAICanonicalVersion + "\x00" + hashDomain + "\x00" + protocol + "\x00" + identity + "\x00" + aiJSON(out.Fragments) + "\x00" + aiJSON(out.Images) + "\x00" + aiJSON(out.UncoveredFields)))
		out.InputHash = hex.EncodeToString(h[:])
	}()
	var err error
	parsed, err = strictJSON(body)
	if err != nil {
		return out, err
	}
	root, ok := parsed.(map[string]any)
	if !ok {
		return out, aiError("ai_input_shape", 503)
	}
	switch protocol {
	case ContentModerationProtocolOpenAIChat, ContentModerationProtocolOpenAIResponses,
		ContentModerationProtocolAnthropicMessages, ContentModerationProtocolGemini, ContentModerationProtocolOpenAIImages:
	default:
		return out, aiError("ai_unsupported_protocol", 503)
	}
	fieldRoot := root
	if responsesWebSocket {
		// WS 传输信封仍经过原有授权边界，不能让未知字段放行规则绕过它。
		fieldRoot, err = contentModerationAIWSBusinessFields(protocol, root)
		if err != nil {
			return out, err
		}
	}
	recordUncovered := func(name string) {
		for _, seen := range out.UncoveredFields {
			if seen == name {
				return
			}
		}
		out.UncoveredFields = append(out.UncoveredFields, name)
	}
	if err := validateContentModerationAIInputFields(fieldRoot, recordUncovered); err != nil {
		return out, err
	}
	for _, key := range []string{"previous_response_id", "conversation", "conversation_id", "cachedContent", "cached_content", "file_id", "attachments"} {
		if value := aiField(root, key); value != nil && aiJSON(value) != `""` && aiJSON(value) != "[]" {
			recordUncovered("history:" + key)
		}
	}
	promptReference := false
	if prompt, ok := aiField(root, "prompt").(map[string]any); ok {
		promptReference = aiField(prompt, "id") != nil || aiField(prompt, "version") != nil || aiField(prompt, "variables") != nil
		if promptReference {
			recordUncovered("history:prompt")
		}
	}
	appendText := func(path, role, text string) error {
		if text == "" {
			return nil
		}
		if !utf8.ValidString(text) {
			return aiError("ai_invalid_utf8", 503)
		}
		runes := utf8.RuneCountInString(text)
		out.TextRunes += runes
		out.Fragments = append(out.Fragments, ContentModerationAIFragment{
			ID: fmt.Sprintf("f%04d", len(out.Fragments)+1), ParentID: path, Role: role,
			Source: "customer_data", StartRune: 0, EndRune: runes, Text: text,
		})
		return nil
	}
	if err := appendContentModerationAIConstraints(protocol, root, appendText, recordUncovered); err != nil {
		return out, err
	}
	var imageErr error
	appendImage := func(block map[string]any, path, role string) error {
		invalidImage := func() error {
			// 错误仍使本轮审查失败，但继续提取后续已知文本，完整保留未送审证据。
			if imageErr == nil {
				imageErr = aiError("ai_invalid_image_encoding", 503)
			}
			recordUncovered("image_invalid:" + path)
			return nil
		}
		if !cfg.ImageReviewEnabled {
			// 开关关闭时先返回，连无效 base64 都不解码，也不影响正常业务。
			recordUncovered("image_skipped:" + path)
			return nil
		}
		imageURL := ""
		if value := aiString(aiField(block, "image_url")); value != "" {
			imageURL = value
		} else if object, ok := aiField(block, "image_url").(map[string]any); ok {
			imageURL = aiString(aiField(object, "url"))
		} else {
			inline := aiField(block, "inline_data")
			if inline == nil {
				inline = aiField(block, "inlineData")
			}
			if object, ok := inline.(map[string]any); ok {
				mediaType := aiString(aiField(object, "mime_type"))
				if mediaType == "" {
					mediaType = aiString(aiField(object, "mimeType"))
				}
				imageURL = "data:" + mediaType + ";base64," + aiString(aiField(object, "data"))
			} else if object, ok := aiField(block, "source").(map[string]any); ok && aiString(aiField(object, "type")) == "base64" {
				imageURL = "data:" + aiString(aiField(object, "media_type")) + ";base64," + aiString(aiField(object, "data"))
			}
		}
		if !strings.HasPrefix(strings.ToLower(imageURL), "data:image/") {
			// 不下载远程图片或文件，也不能把链接文本当作已经审过的图片。
			recordUncovered("image_not_inline:" + path)
			return nil
		}
		header, payload, hasComma := strings.Cut(imageURL, ",")
		mediaType, encoding, hasSemicolon := strings.Cut(header[len("data:"):], ";")
		if !hasComma || !hasSemicolon || !strings.EqualFold(encoding, "base64") || strings.EqualFold(mediaType, "image/") {
			return invalidImage()
		}
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return invalidImage()
		}
		out.Images = append(out.Images, ContentModerationAIImage{
			ID: fmt.Sprintf("i%04d", len(out.Images)+1), ParentID: path, Role: role,
			MediaType: mediaType, DataURL: imageURL, Bytes: len(decoded),
		})
		return nil
	}
	var content func(any, string, string) error
	content = func(value any, path, role string) error {
		switch v := value.(type) {
		case nil:
			return nil
		case string:
			return appendText(path, role, v)
		case []any:
			for i, item := range v {
				if err := content(item, fmt.Sprintf("%s/%d", path, i), role); err != nil {
					return err
				}
			}
		case map[string]any:
			if r := aiString(aiField(v, "role")); r != "" {
				role = r
			}
			if err := validateContentModerationAIContentFields(v, recordUncovered); err != nil {
				return err
			}
			typ := strings.ToLower(aiString(aiField(v, "type")))
			found, imageBlock := false, false
			switch typ {
			case "input_image", "image", "image_url", "output_image":
				if err := appendImage(v, path, role); err != nil {
					return err
				}
				found, imageBlock = true, true
			case "", "message", "text", "input_text", "output_text", "refusal", "tool_result", "function_call_output", "custom_tool_call_output", "function_call", "custom_tool_call", "tool_use", "function", "thinking", "reasoning", "summary_text":
			default:
				recordUncovered("content_type:" + typ)
				if strings.Contains(typ, "audio") || strings.Contains(typ, "video") || strings.Contains(typ, "file") || typ == "document" || typ == "computer_call_output" {
					recordUncovered("media:" + typ)
				}
			}
			if err := appendContentModerationAIConstraints(protocol, map[string]any{"output_config": aiField(v, "output_config")}, func(key, kind, text string) error {
				return appendText(path+"/"+key, kind, text)
			}, recordUncovered); err != nil {
				return err
			}
			if prefix := aiField(v, "prefix"); prefix != nil {
				if err := appendText(path+"/prefix", "generation_constraint", aiJSON(prefix)); err != nil {
					return err
				}
			}
			for _, key := range []string{"content", "text", "output", "input", "arguments", "refusal", "name", "description", "parts", "tool_calls", "function", "function_call", "functionResponse", "function_response", "functionCall", "reasoning_content", "reasoning", "thinking", "summary", "namespace", "annotations", "inline_data", "inlineData"} {
				item := aiField(v, key)
				if item == nil {
					continue
				}
				found = true
				switch {
				case key == "inline_data" || key == "inlineData":
					if !imageBlock {
						if err := appendImage(map[string]any{key: item}, path+"/"+key, role); err != nil {
							return err
						}
					}
				case key == "annotations" || key == "arguments" || key == "tool_calls" || key == "function_call" || key == "functionResponse" || key == "function_response" || key == "functionCall" || (key == "input" && typ == "tool_use") || (key == "output" && (typ == "function_call_output" || typ == "custom_tool_call_output")):
					// 工具载荷是当前请求已提交的数据；完整保留对象，未知键不丢失。
					text, ok := item.(string)
					if !ok {
						text = aiJSON(item)
					}
					if err := appendText(path+"/"+key, role, text); err != nil {
						return err
					}
				default:
					if err := content(item, path+"/"+key, role); err != nil {
						return err
					}
				}
			}
			if !found {
				recordUncovered("content_shape:" + path)
			}
		default:
			recordUncovered("content_shape:" + path)
		}
		return nil
	}
	if promptReference {
		// 托管提示本体不可见，但本次提交的变量值可见；分别记录，不能整块跳过。
		prompt := aiField(root, "prompt").(map[string]any)
		if variables, ok := aiField(prompt, "variables").(map[string]any); ok {
			names := make([]string, 0, len(variables))
			for name := range variables {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if err := content(variables[name], "prompt/variables/"+name, "customer_instruction"); err != nil {
					return out, err
				}
			}
		} else if aiField(prompt, "variables") != nil {
			recordUncovered("content_shape:prompt/variables")
		}
	}
	for _, key := range []string{"instructions", "system", "systemInstruction", "prompt", "tools", "functions"} {
		value := aiField(root, key)
		if value == nil || (key == "prompt" && promptReference) {
			continue
		}
		if key == "tools" || key == "functions" {
			// 仅当前固定定义参与文本审查，工具执行和引用到的资源不在覆盖范围。
			if contentModerationAIHasRemoteSchema(value) {
				recordUncovered("schema_reference:" + key)
			}
			if tools, ok := value.([]any); ok {
				for i, tool := range tools {
					object, ok := tool.(map[string]any)
					typ := strings.ToLower(aiString(aiField(object, "type")))
					if !ok || (typ != "" && typ != "function" && typ != "custom") {
						recordUncovered(fmt.Sprintf("tool_definition:%s/%d", key, i))
					}
					for _, name := range []string{"googleSearch", "google_search", "googleSearchRetrieval", "codeExecution", "code_execution", "urlContext", "url_context"} {
						if aiField(object, name) != nil {
							recordUncovered(fmt.Sprintf("tool_runtime:%s/%d/%s", key, i, name))
						}
					}
				}
			} else {
				recordUncovered("tool_definition:" + key)
			}
			if err := appendText(key, "tool_definition", aiJSON(value)); err != nil {
				return out, err
			}
		} else if err := content(value, key, "customer_instruction"); err != nil {
			return out, err
		}
	}
	for _, key := range []string{"messages", "input", "contents"} {
		if value := aiField(root, key); value != nil {
			if err := content(value, key, "user"); err != nil {
				return out, err
			}
		}
	}
	// 协议形状仍必须成立；不再按“可审文本数量”拒绝纯图片、未知块或空段落。
	// 没有选中内容时由运行时明确跳过，不能伪造模型已审查的结论。
	if err := validateContentModerationAIProtocolInput(protocol, root); err != nil {
		return out, err
	}
	return out, imageErr
}

func ContentModerationAICapabilities() []ContentModerationAICapability {
	return []ContentModerationAICapability{
		{ContentModerationProtocolOpenAIChat, true, "submitted_text_and_inline_images", "按原字段整体审查角色文本、工具参数/输出和固定定义；可选内联图片"},
		{ContentModerationProtocolOpenAIResponses, true, "submitted_text_and_inline_images", "每轮审查本次提交文本和可选内联图片；服务端历史只记未覆盖"},
		{ContentModerationProtocolAnthropicMessages, true, "submitted_text_and_inline_images", "含 system、消息和工具数据；可选 base64 图片"},
		{ContentModerationProtocolGemini, true, "submitted_text_and_inline_images", "含 systemInstruction、消息和工具数据；可选 inline_data 图片"},
		{ContentModerationProtocolOpenAIImages, true, "generation_prompt_only", "审查已提交生成提示；编辑、参考图及未知媒体只记未覆盖"},
		{"image_audio_video_file_realtime_embeddings", false, "independent_endpoints_not_integrated", "独立音视频、文件、实时和嵌入端点未接入 AI 审查；已支持对话协议内的未知媒体只记未覆盖"},
	}
}
