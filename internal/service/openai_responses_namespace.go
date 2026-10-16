package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIResponsesNamespaceNamesContextKey = "openai_responses_namespace_names"

// shouldFlattenOpenAIResponsesNamespaces decides whether an OAuth Responses
// request must be adapted for an HTTP upstream that does not understand Codex
// namespace tools. Native WSv2 keeps namespaces intact because its response
// path is passed through without the HTTP restoration step.
func shouldFlattenOpenAIResponsesNamespaces(account *Account, transport OpenAIUpstreamTransport, passthroughEnabled bool, compactPath bool) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return false
	}
	// compact has a narrower schema and does not need tool invocation, so keep
	// the proven legacy adaptation there. Normal Codex requests preserve native
	// namespace declarations unless an administrator explicitly opts in.
	if !compactPath && !account.IsOpenAIResponsesFlattenNamespacesEnabled() {
		return false
	}
	if transport == OpenAIUpstreamTransportResponsesWebsocketV2 && !passthroughEnabled {
		return false
	}
	return true
}

func shouldKeepOpenAIResponsesToolCallNamespaces(account *Account, transport OpenAIUpstreamTransport, passthroughEnabled bool, compactPath bool) bool {
	if account == nil || !account.IsOpenAIOAuth() || compactPath {
		return false
	}
	return !shouldFlattenOpenAIResponsesNamespaces(account, transport, passthroughEnabled, compactPath)
}

var openAIResponsesToolCallItemTypes = map[string]bool{
	"function_call":    true,
	"tool_call":        true,
	"custom_tool_call": true,
	"mcp_tool_call":    true,
}

func isOpenAIResponsesToolCallItemType(itemType string) bool {
	return openAIResponsesToolCallItemTypes[strings.ToLower(strings.TrimSpace(itemType))]
}

// shouldStripOpenAIResponsesInputNamespaces 只清理 HTTP 上游不认识的直接
// input item namespace。原生 WSv2 支持该字段，必须原样保留。
func shouldStripOpenAIResponsesInputNamespaces(account *Account, transport OpenAIUpstreamTransport, passthroughEnabled bool) bool {
	if account == nil || (!account.IsOpenAIOAuth() && !account.IsOpenAIApiKey()) {
		return false
	}
	if transport == OpenAIUpstreamTransportResponsesWebsocketV2 && !passthroughEnabled {
		return false
	}
	return true
}

func flattenOpenAIResponsesNamespaces(c *gin.Context, body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"namespace"`)) {
		return body, nil
	}
	var requestBody map[string]any
	if err := json.Unmarshal(body, &requestBody); err != nil {
		return body, fmt.Errorf("decode OpenAI namespace body: %w", err)
	}
	names, changed, err := apicompat.FlattenResponsesNamespacesExcept(requestBody, map[string]bool{"image_gen": true})
	if err != nil {
		return body, err
	}
	if !changed {
		return body, nil
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI namespace body: %w", err)
	}
	setOpenAIResponsesNamespaceNames(c, names)
	return rebuilt, nil
}

// stripOpenAIResponsesInputNamespaces 只删除 input 数组直接元素的 namespace，
// 保留 tools 和嵌套 content 的同名字段。逐项重建一次，避免长历史产生平方级复制。
func stripOpenAIResponsesInputNamespaces(body []byte, keepToolCallNamespaces bool) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"namespace"`)) {
		return body, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}

	var rebuilt bytes.Buffer
	rebuilt.Grow(len(input.Raw))
	rebuilt.WriteByte('[')
	changed := false
	first := true
	var stripErr error
	input.ForEach(func(_, item gjson.Result) bool {
		if !first {
			rebuilt.WriteByte(',')
		}
		first = false
		itemBody := []byte(item.Raw)
		if item.IsObject() && item.Get("namespace").Exists() &&
			(!keepToolCallNamespaces || !isOpenAIResponsesToolCallItemType(item.Get("type").String())) {
			itemBody, stripErr = sjson.DeleteBytes(itemBody, "namespace")
			if stripErr != nil {
				return false
			}
			changed = true
		}
		rebuilt.Write(itemBody)
		return true
	})
	rebuilt.WriteByte(']')
	if stripErr != nil {
		return body, fmt.Errorf("delete OpenAI input namespace: %w", stripErr)
	}
	if !changed {
		return body, nil
	}
	stripped, err := sjson.SetRawBytes(body, "input", rebuilt.Bytes())
	if err != nil {
		return body, fmt.Errorf("replace OpenAI input after namespace deletion: %w", err)
	}
	return stripped, nil
}

func setOpenAIResponsesNamespaceNames(c *gin.Context, names map[string]apicompat.ResponsesNamespaceName) {
	if c != nil && len(names) > 0 {
		c.Set(openAIResponsesNamespaceNamesContextKey, names)
	}
}

func clearOpenAIResponsesNamespaceNames(c *gin.Context) {
	if c != nil {
		c.Set(openAIResponsesNamespaceNamesContextKey, nil)
	}
}

func openAIResponsesNamespaceNames(c *gin.Context) map[string]apicompat.ResponsesNamespaceName {
	if c == nil {
		return nil
	}
	value, ok := c.Get(openAIResponsesNamespaceNamesContextKey)
	if !ok {
		return nil
	}
	names, _ := value.(map[string]apicompat.ResponsesNamespaceName)
	return names
}

func restoreOpenAIResponsesNamespacePayload(c *gin.Context, payload []byte) ([]byte, error) {
	names := openAIResponsesNamespaceNames(c)
	if len(names) == 0 || !json.Valid(payload) {
		return payload, nil
	}
	restored, changed, err := apicompat.RestoreResponsesNamespaceCalls(payload, names)
	if err != nil {
		return payload, err
	}
	if changed {
		return restored, nil
	}
	return payload, nil
}
