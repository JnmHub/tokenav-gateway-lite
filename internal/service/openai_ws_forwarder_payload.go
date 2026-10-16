package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_ws_forwarder_payload.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。

import (
	"encoding/json"
	"github.com/tidwall/gjson"
	"strings"
	"unsafe"
)


// combineOpenAIWSReplayItems 合并历史与增量为新头数组，正文共享不复制。
func combineOpenAIWSReplayItems(history, delta []json.RawMessage) []json.RawMessage {
	if len(delta) == 0 {
		return history
	}
	combined := make([]json.RawMessage, 0, len(history)+len(delta))
	combined = append(combined, history...)
	return append(combined, delta...)
}

// openAIWSPayloadStringView 返回与 payload 共享底层数组的零拷贝 string 视图，
// 供 gjson.Get 使用（gjson.GetBytes 会整段复制结果 Raw，对 input 这类占
// payload 主体的字段是每次 O(payload) 分配）。调用方必须保证 payload 在结果
// 存活期间不可变（replay 所有权不变式）。
func openAIWSPayloadStringView(payload []byte) string {
	return unsafe.String(unsafe.SliceData(payload), len(payload))
}

// openAIWSRawMessageFromResult 优先返回 parent 的子切片（gjson 值零拷贝共享），
// Index 不可用时回退为复制。共享要求 parent 遵守上面的不可变约定。
func openAIWSRawMessageFromResult(parent []byte, value gjson.Result) json.RawMessage {
	idx := value.Index
	if idx > 0 && idx+len(value.Raw) <= len(parent) && string(parent[idx:idx+len(value.Raw)]) == value.Raw {
		return json.RawMessage(parent[idx : idx+len(value.Raw)])
	}
	return json.RawMessage(value.Raw)
}

// sanitizeOpenAIWSHistoricalReplayToolCalls 返回的新头数组与 previousItems 共享正文。
func sanitizeOpenAIWSHistoricalReplayToolCalls(
	previousItems []json.RawMessage,
	currentItems []json.RawMessage,
) []json.RawMessage {
	if len(previousItems) == 0 {
		return previousItems
	}
	outputCallIDs := make(map[string]struct{})
	collectOutputCallIDs := func(items []json.RawMessage) {
		for _, item := range items {
			if !isCodexToolCallOutputItemType(gjson.GetBytes(item, "type").String()) {
				continue
			}
			if callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String()); callID != "" {
				outputCallIDs[callID] = struct{}{}
			}
		}
	}
	collectOutputCallIDs(previousItems)
	collectOutputCallIDs(currentItems)

	sanitized := make([]json.RawMessage, 0, len(previousItems))
	for _, item := range previousItems {
		if isCodexToolCallContextItemType(gjson.GetBytes(item, "type").String()) {
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if _, paired := outputCallIDs[callID]; !paired {
				continue
			}
		}
		sanitized = append(sanitized, item)
	}
	return sanitized
}

// buildOpenAIWSReplayInputSequenceFromItems 基于已解析的当前 turn input 构建
// replay 序列。返回序列的正文与 previousFullInput/currentItems 共享所有权
// （见 combineOpenAIWSReplayItems 上方的所有权不变式），头数组可能直接转移自
// currentItems。
func buildOpenAIWSReplayInputSequenceFromItems(
	previousFullInput []json.RawMessage,
	previousFullInputExists bool,
	currentItems []json.RawMessage,
	currentExists bool,
	hasPreviousResponseID bool,
) ([]json.RawMessage, bool) {
	if !hasPreviousResponseID || !previousFullInputExists {
		return currentItems, currentExists
	}
	previousFullInput = sanitizeOpenAIWSHistoricalReplayToolCalls(previousFullInput, currentItems)
	if !currentExists || len(currentItems) == 0 {
		return previousFullInput, true
	}
	if openAIWSRawItemsHasPrefix(currentItems, previousFullInput) {
		return currentItems, true
	}
	merged := make([]json.RawMessage, 0, len(previousFullInput)+len(currentItems))
	merged = append(merged, previousFullInput...)
	merged = append(merged, currentItems...)
	return merged, true
}

