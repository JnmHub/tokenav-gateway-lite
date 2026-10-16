package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexReservedPythonToolName = "python"
	codexPythonToolAlias        = "python__sub2api"
	codexToolNameReverseKey     = "openai_codex_tool_name_reverse"
	codexToolNameSessionKey     = "openai_codex_tool_name_session_reverse"
)

type codexToolNameField struct {
	object map[string]any
	key    string
	name   string
}

// 上游（OpenAI Responses API / Codex 后端）对工具名的约束：只允许 [A-Za-z0-9_-]，
// 且长度不超过 64；违反时上游直接 400（Invalid 'input[7].name': string does not
// match pattern. Expected a string that matches the pattern '^[a-zA-Z0-9_-]+$'）。
// 客户端工具名常不在这个字符集内（MCP 风格 fs.read_file、mcp__srv.tool、中文名、
// 带空格/冒号），所以网关在出站前归一化，并把「上游名 → 原名」存进 gin.Context，
// 响应侧再还原成原名，客户端的工具分发不受影响。
const (
	openAIToolNameMaxLength = 64
	openAIToolNameHashBytes = 4
)

// normalizeOpenAIResponsesToolNames 归一化请求里所有工具名（声明 + 引用），
// 返回「上游名 → 原名」映射（无需改动时返回 nil,false,nil）。
// 契约：幂等（已合法的不动）、一致（同原名处处映射同一个上游名）、
// 单射（不同原名不撞同一个上游名，冲突追加 _2/_3）、合法（匹配 ^[a-zA-Z0-9_-]{1,64}$）。
func normalizeOpenAIResponsesToolNames(reqBody map[string]any) (map[string]string, bool, error) {
	return normalizeOpenAIResponsesToolNamesSeeded(reqBody, nil)
}

// normalizeOpenAIResponsesToolNamesSeeded 与上面相同，但接受一份已有的
// 「上游名 → 原名」映射作为种子：已经分配过的原名继续沿用同一个上游名。
//
// 为什么需要种子：WebSocket（WebSocket 协议）一个连接里会发多帧（session.update 声明工具、
// 后续 response.create 只带 input 历史）。若每帧独立分配，撞车去重（fs.read 与 fs_read 同现）
// 可能在不同帧得到不同后缀，导致「历史里引用的名字与声明不一致」。带上已用映射即可跨帧一致。
func normalizeOpenAIResponsesToolNamesSeeded(reqBody map[string]any, seed map[string]string) (map[string]string, bool, error) {
	if reqBody == nil {
		return nil, false, nil
	}
	fields := collectOpenAIResponsesToolNameFields(reqBody)
	if len(fields) == 0 {
		return nil, false, nil
	}
	present := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if name := strings.TrimSpace(field.name); name != "" {
			present[name] = struct{}{}
		}
	}
	legalNames := make(map[string]struct{}, len(fields))
	taken := make(map[string]struct{}, len(fields)+len(seed))
	for _, field := range fields {
		name := strings.TrimSpace(field.name)
		if name == "" {
			continue
		}
		if _, changed := normalizeOpenAIToolName(field.name); !changed {
			taken[name] = struct{}{}
			legalNames[name] = struct{}{}
		}
	}
	assigned := make(map[string]string, len(fields))
	reverse := make(map[string]string, len(fields))
	// 种子（上一次分配结果）优先：同一个原名在所有帧里必须映射到同一个上游名，
	// 否则「声明帧写的名字」与「引用帧写的名字」对不上，上游会判成未知工具。
	for upstream, original := range seed {
		upstream = strings.TrimSpace(upstream)
		original = strings.TrimSpace(original)
		if upstream == "" || original == "" {
			continue
		}
		if _, reserved := legalNames[upstream]; reserved {
			// 本帧自己就声明了同名合法工具：不能把种子里那个非法原名也映射到
			// 它，否则上游会看到两个同名工具。交给下面的冲突去重回分配。
			continue
		}
		// 历史分配过的上游名一律视为已占用，避免本帧的新名字撞上它。
		taken[upstream] = struct{}{}
		if _, ok := present[original]; !ok {
			continue
		}
		if _, exists := assigned[original]; exists {
			continue
		}
		if _, changed := normalizeOpenAIToolName(original); !changed {
			continue
		}
		assigned[original] = upstream
		reverse[upstream] = original
	}
	for _, field := range fields {
		original := strings.TrimSpace(field.name)
		if original == "" {
			continue
		}
		candidate, changed := normalizeOpenAIToolName(field.name)
		if !changed {
			continue
		}
		if _, ok := assigned[original]; ok {
			continue
		}
		candidate = uniquifyOpenAIToolName(candidate, original, taken)
		assigned[original] = candidate
		reverse[candidate] = original
		taken[candidate] = struct{}{}
	}
	if len(reverse) == 0 {
		return nil, false, nil
	}
	for _, field := range fields {
		if candidate, ok := assigned[strings.TrimSpace(field.name)]; ok && candidate != field.name {
			field.object[field.key] = candidate
		}
	}
	return reverse, true, nil
}

// normalizeOpenAIToolName 返回名字的上游合法形式与「是否发生变化」；已合法时原样返回。
func normalizeOpenAIToolName(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return name, false
	}
	candidate := trimmed
	// ChatGPT Codex 后端占用的名字保持既有别名行为。
	if strings.EqualFold(candidate, codexReservedPythonToolName) {
		candidate = codexPythonToolAlias
	}
	var b strings.Builder
	b.Grow(len(candidate))
	for _, r := range candidate {
		if isUpstreamLegalToolNameRune(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	normalized := b.String()
	if strings.Trim(normalized, "_") == "" {
		normalized = "tool_" + shortToolNameHash(trimmed)
	}
	if len(normalized) > openAIToolNameMaxLength {
		keep := openAIToolNameMaxLength - openAIToolNameHashBytes*2 - 1
		normalized = normalized[:keep] + "_" + shortToolNameHash(trimmed)
	}
	if normalized == name {
		return name, false
	}
	return normalized, true
}

// uniquifyOpenAIToolName 在候选名已被占用时按确定顺序追加 _2、_3…。
func uniquifyOpenAIToolName(candidate, original string, taken map[string]struct{}) string {
	if _, exists := taken[candidate]; !exists {
		return candidate
	}
	for i := 2; i < 1000; i++ {
		suffix := "_" + strconv.Itoa(i)
		base := candidate
		if len(base)+len(suffix) > openAIToolNameMaxLength {
			base = base[:openAIToolNameMaxLength-len(suffix)]
		}
		if next := base + suffix; next != candidate {
			if _, exists := taken[next]; !exists {
				return next
			}
		}
	}
	return "tool_" + shortToolNameHash(original)
}

func isUpstreamLegalToolNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-':
		return true
	default:
		return false
	}
}

func shortToolNameHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:openAIToolNameHashBytes])
}

func collectOpenAIResponsesToolNameFields(reqBody map[string]any) []codexToolNameField {
	fields := make([]codexToolNameField, 0, 8)
	appendName := func(object map[string]any, key string) {
		if object == nil {
			return
		}
		name, ok := object[key].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return
		}
		fields = append(fields, codexToolNameField{object: object, key: key, name: name})
	}
	var collectTools func(any)
	collectTools = func(rawTools any) {
		tools, ok := rawTools.([]any)
		if !ok {
			return
		}
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			toolType := strings.ToLower(strings.TrimSpace(firstNonEmptyString(tool["type"])))
			// 只处理「工具声明」语义的 type：function（含 CC 的 tools[].function.name）
			// 与 custom。type 为空的是 Anthropic 形态的透传工具，由 Claude 路径
			// （gateway_tool_rewrite.go）负责，这里不碰，避免两套映射互相覆盖。
			if toolType == "function" || toolType == "custom" {
				appendName(tool, "name")
				if function, ok := tool["function"].(map[string]any); ok {
					appendName(function, "name")
				}
			}
			if toolType == "namespace" {
				collectTools(tool["tools"])
			}
		}
	}
	collectTools(reqBody["tools"])
	if functions, ok := reqBody["functions"].([]any); ok {
		for _, raw := range functions {
			function, _ := raw.(map[string]any)
			appendName(function, "name")
		}
	}
	if strings.EqualFold(strings.TrimSpace(firstNonEmptyString(reqBody["type"])), "session.update") {
		if session, ok := reqBody["session"].(map[string]any); ok {
			collectTools(session["tools"])
		}
	}
	if choice, ok := reqBody["tool_choice"].(map[string]any); ok {
		if strings.EqualFold(strings.TrimSpace(firstNonEmptyString(choice["type"])), "allowed_tools") {
			collectTools(choice["tools"])
		}
		if strings.EqualFold(strings.TrimSpace(firstNonEmptyString(choice["type"])), "function") {
			appendName(choice, "name")
			if function, ok := choice["function"].(map[string]any); ok {
				appendName(function, "name")
			}
		}
	}
	if input, ok := reqBody["input"].([]any); ok {
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			typ := strings.ToLower(strings.TrimSpace(firstNonEmptyString(item["type"])))
			if typ == "additional_tools" {
				collectTools(item["tools"])
			}
			// function_call / custom_tool_call 都是历史里对工具名的引用
			// （生产报错 input[7].name 就是这里命中的），必须与 tools[] 声明同步改名。
			if typ == "function_call" || typ == "custom_tool_call" {
				appendName(item, "name")
				if function, ok := item["function"].(map[string]any); ok {
					appendName(function, "name")
				}
			}
		}
	}
	// Chat Completions 形态的历史：assistant 消息里的 tool_calls[].function.name
	// 同样是对工具名的引用，必须与 tools[] 声明保持一致。
	if messages, ok := reqBody["messages"].([]any); ok {
		for _, raw := range messages {
			message, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			toolCalls, _ := message["tool_calls"].([]any)
			for _, rawCall := range toolCalls {
				call, ok := rawCall.(map[string]any)
				if !ok {
					continue
				}
				if callType := strings.ToLower(strings.TrimSpace(firstNonEmptyString(call["type"]))); callType != "" && callType != "function" {
					continue
				}
				if function, ok := call["function"].(map[string]any); ok {
					appendName(function, "name")
				}
			}
		}
	}
	return fields
}

// bodyMayCarryToolNames 是廉价预筛：只有出现「可能携带工具名」的键时才解码 body，
// 避免给每个请求都加一次完整 JSON 解码（这条路径在用户请求的热路径上）。
func bodyMayCarryToolNames(body []byte) bool {
	for _, marker := range [...][]byte{
		[]byte(`"tools"`), []byte(`"functions"`), []byte(`"tool_choice"`),
		[]byte(`"function_call"`), []byte(`"custom_tool_call"`), []byte(`"tool_calls"`),
		[]byte(`"additional_tools"`),
	} {
		if bytes.Contains(body, marker) {
			return true
		}
	}
	return false
}

// normalizeOpenAIResponsesToolNamesBody 是请求侧入口：把 body 里的工具名归一化成
// 上游合法形式，返回改后的 body 与「上游名 → 原名」映射（无需改动时原样返回）。
func normalizeOpenAIResponsesToolNamesBody(body []byte) ([]byte, map[string]string, bool, error) {
	return normalizeOpenAIResponsesToolNamesBodyWithSeed(body, nil)
}

// normalizeOpenAIResponsesToolNamesBodyWithSeed 同上，但接受一份已有的
// 「上游名 → 原名」映射作为种子（WS 多帧场景由 codexToolNameSeedForContext 提供）。
func normalizeOpenAIResponsesToolNamesBodyWithSeed(body []byte, seed map[string]string) ([]byte, map[string]string, bool, error) {
	if len(body) == 0 || !bodyMayCarryToolNames(body) {
		return body, nil, false, nil
	}
	var reqBody map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &reqBody); err != nil {
		return body, nil, false, fmt.Errorf("decode OpenAI tool names: %w", err)
	}
	reverse, changed, err := normalizeOpenAIResponsesToolNamesSeeded(reqBody, seed)
	if err != nil || !changed {
		return body, reverse, false, err
	}
	normalized, err := json.Marshal(reqBody)
	if err != nil {
		return body, nil, false, fmt.Errorf("encode OpenAI tool names: %w", err)
	}
	return normalized, reverse, true, nil
}

func setCodexToolNameReverse(c *gin.Context, reverse map[string]string) {
	if c == nil {
		return
	}
	storeCodexToolNameReverse(c, codexToolNameReverseKey, reverse)
	storeCodexToolNameReverse(c, codexToolNameSessionKey, nil)
}

func storeCodexToolNameReverse(c *gin.Context, key string, reverse map[string]string) {
	if c == nil {
		return
	}
	copyMap := make(map[string]string, len(reverse))
	for aliased, original := range reverse {
		copyMap[aliased] = original
	}
	c.Set(key, copyMap)
}

func mergeCodexToolNameReverse(c *gin.Context, reverse map[string]string) {
	if c == nil || len(reverse) == 0 {
		return
	}
	merged := make(map[string]string, len(reverse)+len(codexToolNameReverseFromContext(c)))
	for aliased, original := range codexToolNameReverseFromContext(c) {
		merged[aliased] = original
	}
	for aliased, original := range reverse {
		merged[aliased] = original
	}
	storeCodexToolNameReverse(c, codexToolNameReverseKey, merged)
}

func codexToolNameReverseFromContext(c *gin.Context) map[string]string {
	return codexToolNameReverseForKey(c, codexToolNameReverseKey)
}

func codexToolNameReverseForKey(c *gin.Context, key string) map[string]string {
	if c == nil {
		return nil
	}
	raw, ok := c.Get(key)
	if !ok {
		return nil
	}
	reverse, _ := raw.(map[string]string)
	return reverse
}

// updateCodexToolNameReverseForWSFrame keeps the active turn isolated from
// session updates that may arrive while that turn is still streaming.
func updateCodexToolNameReverseForWSFrame(c *gin.Context, frame []byte, reverse map[string]string) {
	if c == nil {
		return
	}
	eventType := strings.TrimSpace(gjson.GetBytes(frame, "type").String())
	switch eventType {
	case "session.update":
		if gjson.GetBytes(frame, "session.tools").Exists() {
			storeCodexToolNameReverse(c, codexToolNameSessionKey, reverse)
		}
	case "response.create", "":
		active := reverse
		if !openAIWSFrameHasExplicitToolDeclarations(frame) {
			active = mergeCodexToolNameReverseMaps(
				codexToolNameReverseForKey(c, codexToolNameSessionKey),
				reverse,
			)
		}
		storeCodexToolNameReverse(c, codexToolNameReverseKey, active)
	}
}

func openAIWSFrameHasExplicitToolDeclarations(frame []byte) bool {
	if gjson.GetBytes(frame, "tools").Exists() {
		return true
	}
	for _, item := range gjson.GetBytes(frame, "input").Array() {
		if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "additional_tools") && item.Get("tools").Exists() {
			return true
		}
	}
	return false
}

func mergeCodexToolNameReverseMaps(base, overlay map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(overlay))
	for aliased, original := range base {
		merged[aliased] = original
	}
	for aliased, original := range overlay {
		merged[aliased] = original
	}
	return merged
}

// codexToolNameSeedForContext 汇总当前连接已经分配过的映射（会话级 + 当前轮），
// 作为下一帧归一化的种子，保证同一原名在整条 WS（WebSocket）连接里映射稳定。
func codexToolNameSeedForContext(c *gin.Context) map[string]string {
	sessionReverse := codexToolNameReverseForKey(c, codexToolNameSessionKey)
	activeReverse := codexToolNameReverseFromContext(c)
	if len(sessionReverse) == 0 {
		return activeReverse
	}
	if len(activeReverse) == 0 {
		return sessionReverse
	}
	return mergeCodexToolNameReverseMaps(sessionReverse, activeReverse)
}

// normalizeOpenAIWSToolNamesFrame 是 WS 入站帧出站前的工具名归一化入口：
// 改名后把「上游名 → 原名」映射并回连接级状态，供同连接后续帧与回程还原复用。
// 返回原帧表示无需改动；归一化失败时也返回原帧（fail-open，绝不因为工具名
// 处理失败把用户请求挡在网关侧），由调用方决定是否记日志。
func normalizeOpenAIWSToolNamesFrame(c *gin.Context, frame []byte) ([]byte, map[string]string, bool, error) {
	if c == nil || len(frame) == 0 {
		return frame, nil, false, nil
	}
	normalized, reverse, changed, err := normalizeOpenAIResponsesToolNamesBodyWithSeed(frame, codexToolNameSeedForContext(c))
	if err != nil {
		return frame, nil, false, err
	}
	// 即使这一帧没有任何名字需要改，也要刷新一次「当前轮」映射：session.update
	// 帧只写会话级映射，若后续 response.create 帧完全不带工具字段就不刷新，
	// 回程（restoreCodexToolNamesFromContext）会拿不到映射，上游名就还原不回原名。
	updateCodexToolNameReverseForWSFrame(c, frame, reverse)
	if !changed {
		return frame, reverse, false, nil
	}
	return normalized, reverse, true, nil
}

func restoreCodexToolNamesInJSON(data []byte, reverse map[string]string) []byte {
	if len(data) == 0 || len(reverse) == 0 || !json.Valid(data) {
		return data
	}
	// 廉价预筛：所有还原点都以 "name" 键为载体（工具声明 / function_call /
	// tool_use / tool_calls[].function），没有这个键就不可能需要还原，跳过
	// 一次完整 JSON 解码。WS 与 SSE 的每个事件都会走这条路径。
	if !bytes.Contains(data, []byte(`"name"`)) {
		return data
	}
	var decoded any
	if err := decodeOpenAIJSONUseNumber(data, &decoded); err != nil {
		return data
	}
	if !restoreCodexToolNameFields(decoded, reverse) {
		return data
	}
	restored, err := json.Marshal(decoded)
	if err != nil {
		return data
	}
	return restored
}

func restoreCodexToolNamesFromContext(c *gin.Context, data []byte) []byte {
	reverse := codexToolNameReverseFromContext(c)
	switch strings.TrimSpace(gjson.GetBytes(data, "type").String()) {
	case "session.created", "session.updated":
		reverse = codexToolNameReverseForKey(c, codexToolNameSessionKey)
	}
	return restoreCodexToolNamesInJSON(data, reverse)
}

func restoreCodexToolNamesFromSSEContext(c *gin.Context, data []byte, eventType string) []byte {
	if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "" || strings.TrimSpace(eventType) == "" {
		return restoreCodexToolNamesFromContext(c, data)
	}
	compat := []byte(openAICompatPayloadWithEventType(string(data), eventType))
	restored := restoreCodexToolNamesFromContext(c, compat)
	if string(restored) == string(compat) {
		return data
	}
	withoutSyntheticType, err := sjson.DeleteBytes(restored, "type")
	if err != nil {
		return restored
	}
	return withoutSyntheticType
}

func restoreCodexToolNameFields(value any, reverse map[string]string) bool {
	root, ok := value.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	restoreItem := func(raw any) {
		item, ok := raw.(map[string]any)
		if !ok {
			return
		}
		// function_call / custom_tool_call 两种历史项都以 name 引用工具名，
		// 出站时被改过名，回程必须一起还原。
		switch strings.ToLower(strings.TrimSpace(firstNonEmptyString(item["type"]))) {
		case "function_call", "custom_tool_call":
		default:
			return
		}
		name, _ := item["name"].(string)
		if original, exists := reverse[name]; exists {
			item["name"] = original
			changed = true
		}
	}
	restoreOutput := func(raw any) {
		output, _ := raw.([]any)
		for _, item := range output {
			restoreItem(item)
		}
	}
	// restoreTools / restoreToolChoiceName 需要先声明后赋值：下面的
	// restoreResponse 在它们之前定义，但运行时（闭包执行时）会用到它们。
	var restoreTools func(any)
	var restoreToolChoiceName func(any)
	restoreResponse := func(raw any) {
		response, ok := raw.(map[string]any)
		if ok {
			restoreOutput(response["output"])
			// Responses 对象会把请求里的 tools / tool_choice 原样回显；出站
			// 改过名的话这里必须一起还原，否则客户端会看到网关生成的上游名
			// （与它自己声明的名字对不上）。
			restoreTools(response["tools"])
			restoreToolChoiceName(response["tool_choice"])
		}
	}
	restoreFunction := func(raw any) {
		function, ok := raw.(map[string]any)
		if !ok {
			return
		}
		name, _ := function["name"].(string)
		if original, exists := reverse[name]; exists {
			function["name"] = original
			changed = true
		}
	}
	restoreChatToolCalls := func(raw any) {
		toolCalls, _ := raw.([]any)
		for _, rawCall := range toolCalls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			callType := strings.ToLower(strings.TrimSpace(firstNonEmptyString(call["type"])))
			if callType == "" || callType == "function" {
				restoreFunction(call["function"])
			}
		}
	}
	restoreMessageContent := func(raw any) {
		content, _ := raw.([]any)
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok || !strings.EqualFold(strings.TrimSpace(firstNonEmptyString(block["type"])), "tool_use") {
				continue
			}
			name, _ := block["name"].(string)
			if original, exists := reverse[name]; exists {
				block["name"] = original
				changed = true
			}
		}
	}
	restoreTools = func(raw any) {
		tools, _ := raw.([]any)
		for _, rawTool := range tools {
			tool, ok := rawTool.(map[string]any)
			if !ok {
				continue
			}
			toolType := strings.ToLower(strings.TrimSpace(firstNonEmptyString(tool["type"])))
			if toolType == "function" || toolType == "custom" {
				name, _ := tool["name"].(string)
				if original, exists := reverse[name]; exists {
					tool["name"] = original
					changed = true
				}
			}
			if toolType == "namespace" {
				restoreTools(tool["tools"])
			}
		}
	}
	// tool_choice 也可能带着被改过名的工具（{"type":"function","name":...}）。
	restoreToolChoiceName = func(raw any) {
		choice, ok := raw.(map[string]any)
		if !ok {
			return
		}
		if strings.EqualFold(strings.TrimSpace(firstNonEmptyString(choice["type"])), "function") {
			name, _ := choice["name"].(string)
			if original, exists := reverse[name]; exists {
				choice["name"] = original
				changed = true
			}
		}
		restoreFunction(choice["function"])
	}

	eventType := strings.ToLower(strings.TrimSpace(firstNonEmptyString(root["type"])))
	switch eventType {
	case "response.output_item.added", "response.output_item.done":
		restoreItem(root["item"])
	case "response.created", "response.in_progress", "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		restoreResponse(root["response"])
	case "session.created", "session.updated":
		if session, ok := root["session"].(map[string]any); ok {
			restoreTools(session["tools"])
		}
	}
	if _, hasOutput := root["output"]; hasOutput {
		restoreOutput(root["output"])
	}
	if choices, ok := root["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"message", "delta"} {
				if message, ok := choice[key].(map[string]any); ok {
					restoreChatToolCalls(message["tool_calls"])
				}
			}
		}
	}
	restoreMessageContent(root["content"])
	// 请求里的 tools / tool_choice 会被上游原样回显（非流式响应体、
	// response.created/completed 都有这一份），必须按映射还原。
	if _, hasTools := root["tools"]; hasTools {
		restoreTools(root["tools"])
	}
	if choice, hasChoice := root["tool_choice"]; hasChoice {
		restoreToolChoiceName(choice)
	}
	if block, ok := root["content_block"].(map[string]any); ok && strings.EqualFold(strings.TrimSpace(firstNonEmptyString(block["type"])), "tool_use") {
		name, _ := block["name"].(string)
		if original, exists := reverse[name]; exists {
			block["name"] = original
			changed = true
		}
	}
	return changed
}
