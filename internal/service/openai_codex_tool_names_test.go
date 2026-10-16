package service

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 上游（OpenAI Responses API / Codex 后端）对工具名的硬约束。
// 违反时上游直接 400：Invalid 'input[7].name': string does not match pattern.
var upstreamToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func requireAllToolNamesUpstreamLegal(t *testing.T, body []byte) {
	t.Helper()
	var root map[string]any
	require.NoError(t, json.Unmarshal(body, &root))
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if raw, ok := node["name"].(string); ok {
				if _, isTool := node["parameters"]; isTool || node["type"] == "function" || node["type"] == "custom" {
					require.Regexp(t, upstreamToolNamePattern, raw, "工具声明名必须上游合法")
				}
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(root)
}

// 生产症状复现：请求里的工具名带上游非法字符（MCP 点号 / 中文 / 空格冒号），
// 归一化后必须全部合法，且「声明」与「历史引用」映射到同一个上游名。
func TestNormalizeOpenAIResponsesToolNamesFixesUpstreamIllegalNames(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"tools":[
			{"type":"function","name":"fs.read_file","parameters":{"type":"object"}},
			{"type":"function","name":"文件搜索","parameters":{"type":"object"}},
			{"type":"custom","name":"mcp__github.create_issue"}
		],
		"tool_choice":{"type":"function","name":"fs.read_file"},
		"input":[
			{"type":"message","role":"user","content":"hi"},
			{"type":"function_call","name":"fs.read_file","call_id":"call_1","arguments":"{}"},
			{"type":"custom_tool_call","name":"mcp__github.create_issue","call_id":"call_2","input":"{}"}
		]
	}`)

	normalized, reverse, changed, err := normalizeOpenAIResponsesToolNamesBody(body)
	require.NoError(t, err)
	require.True(t, changed, "带非法字符的工具名必须被归一化")
	requireAllToolNamesUpstreamLegal(t, normalized)

	// 反向映射：上游名 → 原名，响应侧靠它还原。
	require.Equal(t, "fs.read_file", reverse["fs_read_file"])
	require.Equal(t, "mcp__github.create_issue", reverse["mcp__github_create_issue"])
	require.Len(t, reverse, 3)

	var root map[string]any
	require.NoError(t, json.Unmarshal(normalized, &root))
	tools := root["tools"].([]any)
	declared := tools[0].(map[string]any)["name"].(string)
	input := root["input"].([]any)
	referenced := input[1].(map[string]any)["name"].(string)
	require.Equal(t, declared, referenced, "声明与历史引用必须映射到同一个上游名")
	choice := root["tool_choice"].(map[string]any)["name"].(string)
	require.Equal(t, declared, choice, "tool_choice 也要同步改名")
	// 纯中文名不能退化成下划线。
	require.NotEqual(t, "__", tools[1].(map[string]any)["name"].(string))
	require.Equal(t, "文件搜索", reverse[tools[1].(map[string]any)["name"].(string)])
}

// 幂等：已经合法的名字一个字都不改，也不产生映射（避免无谓改写与上游缓存抖动）。
func TestNormalizeOpenAIResponsesToolNamesIsNoOpForLegalNames(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","name":"fs_read_file","parameters":{"type":"object"}}],
		"input":[{"type":"function_call","name":"fs_read_file","call_id":"c1"}]
	}`)

	normalized, reverse, changed, err := normalizeOpenAIResponsesToolNamesBody(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Empty(t, reverse)
	require.Equal(t, string(body), string(normalized), "合法名字不改动 body")
}

// 单射：两个不同原名归一化后撞车时，必须分配不同上游名（否则上游会认为调错工具）。
func TestNormalizeOpenAIResponsesToolNamesDeduplicatesCollisions(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"function","name":"fs.read","parameters":{"type":"object"}},
			{"type":"function","name":"fs_read","parameters":{"type":"object"}}
		]
	}`)

	normalized, reverse, changed, err := normalizeOpenAIResponsesToolNamesBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	requireAllToolNamesUpstreamLegal(t, normalized)

	var root map[string]any
	require.NoError(t, json.Unmarshal(normalized, &root))
	tools := root["tools"].([]any)
	first := tools[0].(map[string]any)["name"].(string)
	second := tools[1].(map[string]any)["name"].(string)
	require.NotEqual(t, first, second, "撞车必须去重")
	// 本来就合法的那一个保持原名。
	require.Equal(t, "fs_read", second)
	require.Equal(t, "fs.read", reverse[first])
	require.Len(t, reverse, 1)
}

// 长度上限：超长名字截断到 64 以内，且仍然可逆。
func TestNormalizeOpenAIResponsesToolNamesCapsLength(t *testing.T) {
	long := strings.Repeat("a", 200)
	body := []byte(`{"tools":[{"type":"function","name":"` + long + `","parameters":{"type":"object"}}]}`)

	normalized, reverse, changed, err := normalizeOpenAIResponsesToolNamesBody(body)
	require.NoError(t, err)
	require.True(t, changed)

	var root map[string]any
	require.NoError(t, json.Unmarshal(normalized, &root))
	name := root["tools"].([]any)[0].(map[string]any)["name"].(string)
	require.LessOrEqual(t, len(name), 64)
	require.Regexp(t, upstreamToolNamePattern, name)
	require.Equal(t, long, reverse[name])
}

// 保留名：ChatGPT Codex 后端占用的 python 仍走既有别名（保持原有行为）。
func TestNormalizeOpenAIResponsesToolNamesKeepsReservedPythonAlias(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"python","parameters":{"type":"object"}}]}`)

	normalized, reverse, changed, err := normalizeOpenAIResponsesToolNamesBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(normalized), codexPythonToolAlias)
	require.Equal(t, codexReservedPythonToolName, reverse[codexPythonToolAlias])
}

// 归一化 + 回程还原：客户端仍应看到自己的原名。
func TestRestoreCodexToolNamesRoundTrip(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"fs.read_file","parameters":{"type":"object"}}]}`)
	_, reverse, changed, err := normalizeOpenAIResponsesToolNamesBody(body)
	require.NoError(t, err)
	require.True(t, changed)

	// 非流式：output 里的 function_call 名应还原。
	responseJSON := []byte(`{"output":[{"type":"function_call","name":"fs_read_file","call_id":"c1"}]}`)
	restored := restoreCodexToolNamesInJSON(responseJSON, reverse)
	require.Contains(t, string(restored), `"fs.read_file"`)
	require.NotContains(t, string(restored), `"fs_read_file"`)

	// 流式：SSE 的 response.output_item.added 事件也应还原。
	frame := []byte(`{"type":"response.output_item.added","item":{"id":"i1","type":"function_call","name":"fs_read_file"}}`)
	restoredFrame := restoreCodexToolNamesInJSON(frame, reverse)
	require.Contains(t, string(restoredFrame), `"fs.read_file"`)
}

// 属性护栏：一批「坏名字」经过归一化后必须全部符合上游约束，且逐个可逆。
func TestNormalizeOpenAIToolNameProducesUpstreamLegalNames(t *testing.T) {
	names := []string{
		"fs.read_file", "mcp__github.create_issue", "文件搜索", "file search",
		"tool:foo", "a/b/c", "a$b", "..", "___", "-", "UPPER.Case",
		"emoji🙂name", "tab\tname", "换 行", strings.Repeat("x", 80), "a.b.c.d",
	}
	for _, name := range names {
		normalized, changed := normalizeOpenAIToolName(name)
		require.Regexp(t, upstreamToolNamePattern, normalized, "名字 %q 归一化后仍需上游合法", name)
		if !changed {
			require.Equal(t, name, normalized)
		}
	}
}

// 上游会把请求里的 tools / tool_choice 原样回显（非流式响应体与
// response.created 都带这一份）。回程必须还原成客户端原名，否则客户端会看到
// 网关生成的上游名，与它自己声明的名字对不上。
func TestRestoreCodexToolNamesRestoresEchoedTools(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","name":"fs.read_file","parameters":{"type":"object"}},{"type":"custom","name":"mcp__github.create_issue"}],
		"tool_choice":{"type":"function","name":"fs.read_file"}
	}`)
	_, reverse, changed, err := normalizeOpenAIResponsesToolNamesBody(body)
	require.NoError(t, err)
	require.True(t, changed)

	// 非流式响应体：顶层 tools / tool_choice 回显。
	echoed := []byte(`{"id":"resp_1","status":"completed","tools":[{"type":"function","name":"fs_read_file","parameters":{"type":"object"}},{"type":"custom","name":"mcp__github_create_issue"}],"tool_choice":{"type":"function","name":"fs_read_file"}}`)
	restored := restoreCodexToolNamesInJSON(echoed, reverse)
	require.Contains(t, string(restored), `"fs.read_file"`)
	require.Contains(t, string(restored), `"mcp__github.create_issue"`)
	require.NotContains(t, string(restored), "fs_read_file")
	require.NotContains(t, string(restored), "mcp__github_create_issue")

	// 流式：response.created 里的 response.tools / response.tool_choice 回显。
	frame := []byte(`{"type":"response.created","response":{"id":"resp_1","tools":[{"type":"function","name":"fs_read_file"}],"tool_choice":{"type":"function","name":"fs_read_file"}}}`)
	restoredFrame := restoreCodexToolNamesInJSON(frame, reverse)
	require.Contains(t, string(restoredFrame), `"fs.read_file"`)
	require.NotContains(t, string(restoredFrame), "fs_read_file")
}

// toolNameFromToolDeclaration 取出 body 里第 idx 个工具声明的名字（测试用）。
// 兼容两种位置：顶层 tools[]（response.create）与 session.tools[]（session.update）。
func toolNameFromToolDeclaration(t *testing.T, body []byte, idx int) string {
	t.Helper()
	var root map[string]any
	require.NoError(t, json.Unmarshal(body, &root))
	tools, ok := root["tools"].([]any)
	if !ok {
		session, sessionOK := root["session"].(map[string]any)
		require.True(t, sessionOK, "body 里必须有 tools[] 或 session.tools[]")
		tools, ok = session["tools"].([]any)
	}
	require.True(t, ok, "body 里必须有 tools[] 或 session.tools[]")
	require.Greater(t, len(tools), idx)
	return tools[idx].(map[string]any)["name"].(string)
}

// WS（WebSocket）跨帧一致：session.update 声明工具后，后续 response.create
// 帧里对同一个原名的引用必须落到同一个上游名，否则上游会把引用判成未知工具。
// 这是「每帧独立分配」和「带连接级种子」的分水岭。
func TestNormalizeOpenAIWSToolNamesFrameKeepsCrossFrameConsistency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	sessionFrame := []byte(`{"type":"session.update","session":{"tools":[{"type":"function","name":"fs.read_file","parameters":{"type":"object"}}]}}`)
	normalizedSession, sessionReverse, changed, err := normalizeOpenAIWSToolNamesFrame(c, sessionFrame)
	require.NoError(t, err)
	require.True(t, changed, "非法工具名必须被归一化")
	requireAllToolNamesUpstreamLegal(t, normalizedSession)
	upstreamName := toolNameFromToolDeclaration(t, normalizedSession, 0)
	require.Equal(t, "fs.read_file", sessionReverse[upstreamName])

	// 第二帧只带历史引用（不重新声明工具），必须复用会话级映射。
	createFrame := []byte(`{"type":"response.create","model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"hi"},{"type":"function_call","name":"fs.read_file","call_id":"call_1","arguments":"{}"}]}`)
	normalizedCreate, createReverse, changed, err := normalizeOpenAIWSToolNamesFrame(c, createFrame)
	require.NoError(t, err)
	require.True(t, changed)
	var createRoot map[string]any
	require.NoError(t, json.Unmarshal(normalizedCreate, &createRoot))
	referenced := createRoot["input"].([]any)[1].(map[string]any)["name"].(string)
	require.Equal(t, upstreamName, referenced, "同一原名跨帧必须映射到同一个上游名")
	require.Equal(t, "fs.read_file", createReverse[upstreamName])

	// 回程：上游返回的上游名要被还原成客户端原名（function_call 与 custom_tool_call 都要）。
	restored := restoreCodexToolNamesFromContext(c, []byte(`{"type":"response.output_item.added","item":{"type":"function_call","name":"`+upstreamName+`"}}`))
	require.Contains(t, string(restored), `"name":"fs.read_file"`)
	restoredCustom := restoreCodexToolNamesFromContext(c, []byte(`{"type":"response.output_item.done","item":{"type":"custom_tool_call","name":"`+upstreamName+`"}}`))
	require.Contains(t, string(restoredCustom), `"name":"fs.read_file"`)
}

// 连接级种子不能让两个不同工具撞成同一个上游名：本帧自己声明了同名合法工具时，
// 旧映射必须让位并按冲突重新分配（否则上游看到两个同名工具）。
func TestNormalizeOpenAIWSToolNamesFrameResolvesSeedCollisionWithLegalName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	sessionFrame := []byte(`{"type":"session.update","session":{"tools":[{"type":"function","name":"fs.read_file","parameters":{"type":"object"}}]}}`)
	normalizedSession, _, changed, err := normalizeOpenAIWSToolNamesFrame(c, sessionFrame)
	require.NoError(t, err)
	require.True(t, changed)
	firstUpstreamName := toolNameFromToolDeclaration(t, normalizedSession, 0)
	require.Equal(t, "fs_read_file", firstUpstreamName)

	// 下一帧把合法的 fs_read_file 也声明进来：两者必须区分开。
	secondFrame := []byte(`{"type":"response.create","model":"gpt-5.6-sol","tools":[{"type":"function","name":"fs.read_file","parameters":{"type":"object"}},{"type":"function","name":"fs_read_file","parameters":{"type":"object"}}]}`)
	normalizedSecond, reverse, changed, err := normalizeOpenAIWSToolNamesFrame(c, secondFrame)
	require.NoError(t, err)
	require.True(t, changed)
	requireAllToolNamesUpstreamLegal(t, normalizedSecond)
	first := toolNameFromToolDeclaration(t, normalizedSecond, 0)
	second := toolNameFromToolDeclaration(t, normalizedSecond, 1)
	require.NotEqual(t, first, second, "两个不同工具不能撞同一个上游名")
	require.Equal(t, "fs_read_file", second, "本来就合法的名字保持不变")
	require.Equal(t, "fs.read_file", reverse[first])
}

// 会话级映射只写在 session.update 帧上，response.create 帧合并保留，避免
// 「上一轮的工具名在下一轮被丢掉」导致回程还原失效。
func TestNormalizeOpenAIWSToolNamesFrameKeepsSessionMappingAlive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	sessionFrame := []byte(`{"type":"session.update","session":{"tools":[{"type":"function","name":"mcp__github.create_issue","parameters":{"type":"object"}}]}}`)
	_, _, changed, err := normalizeOpenAIWSToolNamesFrame(c, sessionFrame)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotEmpty(t, codexToolNameReverseForKey(c, codexToolNameSessionKey), "session.update 帧要写会话级映射")

	// 一个不含任何工具字段的 response.create 帧不应清空连接级映射。
	_, _, _, err = normalizeOpenAIWSToolNamesFrame(c, []byte(`{"type":"response.create","model":"gpt-5.6-sol"}`))
	require.NoError(t, err)
	require.NotEmpty(t, codexToolNameReverseForKey(c, codexToolNameSessionKey))
	sessionMap := codexToolNameReverseForKey(c, codexToolNameSessionKey)
	var upstreamName string
	for name := range sessionMap {
		upstreamName = name
	}
	restored := restoreCodexToolNamesFromContext(c, []byte(`{"type":"response.output_item.done","item":{"type":"function_call","name":"`+upstreamName+`"}}`))
	require.Contains(t, string(restored), `"name":"mcp__github.create_issue"`)
}

// 合法名字完全不触发任何改写，也不写连接级状态（热路径零副作用）。
func TestNormalizeOpenAIWSToolNamesFrameNoOpForLegalNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	frame := []byte(`{"type":"session.update","session":{"tools":[{"type":"function","name":"fs_read_file","parameters":{"type":"object"}}]}}`)
	normalized, reverse, changed, err := normalizeOpenAIWSToolNamesFrame(c, frame)
	require.NoError(t, err)
	require.False(t, changed)
	require.Empty(t, reverse)
	require.Equal(t, string(frame), string(normalized))
	require.Empty(t, codexToolNameReverseFromContext(c))
}

// 同名引用与声明必须一起改：Chat Completions 形态的 messages[].tool_calls[].function.name
// 与 tools[] 声明同步，回程同样还原。
func TestNormalizeOpenAIResponsesToolNamesSyncsChatToolCalls(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","name":"fs.read_file","parameters":{"type":"object"}}],
		"messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"fs.read_file","arguments":"{}"}}]}]
	}`)
	normalized, reverse, changed, err := normalizeOpenAIResponsesToolNamesBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	requireAllToolNamesUpstreamLegal(t, normalized)

	var root map[string]any
	require.NoError(t, json.Unmarshal(normalized, &root))
	declared := root["tools"].([]any)[0].(map[string]any)["name"].(string)
	call := root["messages"].([]any)[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"].(string)
	require.Equal(t, declared, call)

	restored := restoreCodexToolNamesInJSON([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"`+declared+`","arguments":"{}"}}]}}]}`), reverse)
	require.Contains(t, string(restored), `"fs.read_file"`)
}
