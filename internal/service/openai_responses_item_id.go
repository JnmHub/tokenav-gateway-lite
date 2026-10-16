package service

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 非法回放 ID 只能删除，不能伪造 msg_/fc_ 前缀；伪造值可能指向另一上游对象。
func shouldStripOpenAIResponsesInputItemID(itemType, id string) bool {
	if id == "" {
		return false
	}
	if itemType == "message" {
		return !strings.HasPrefix(id, "msg")
	}
	if itemType == "reasoning" {
		return !strings.HasPrefix(id, "rs")
	}
	if isCodexToolCallInputType(itemType) {
		return !strings.HasPrefix(id, "fc")
	}
	return false
}

func sanitizeOpenAIResponsesInputItemIDs(body []byte) ([]byte, bool, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false, nil
	}

	var rebuilt bytes.Buffer
	rebuilt.Grow(len(input.Raw))
	rebuilt.WriteByte('[')
	changed := false
	first := true
	index := 0
	var sanitizeErr error
	input.ForEach(func(_, item gjson.Result) bool {
		currentIndex := index
		index++
		if !first {
			rebuilt.WriteByte(',')
		}
		first = false
		itemBody := []byte(item.Raw)
		if item.IsObject() {
			itemType := item.Get("type")
			id := item.Get("id")
			if itemType.Type == gjson.String && id.Type == gjson.String &&
				shouldStripOpenAIResponsesInputItemID(itemType.String(), id.String()) {
				itemBody, sanitizeErr = sjson.DeleteBytes(itemBody, "id")
				if sanitizeErr != nil {
					sanitizeErr = fmt.Errorf("delete input.%d.id: %w", currentIndex, sanitizeErr)
					return false
				}
				changed = true
			}
		}
		rebuilt.Write(itemBody)
		return true
	})
	if sanitizeErr != nil {
		return nil, false, sanitizeErr
	}
	if !changed {
		return body, false, nil
	}
	rebuilt.WriteByte(']')
	sanitized, err := sjson.SetRawBytes(body, "input", rebuilt.Bytes())
	if err != nil {
		return nil, false, fmt.Errorf("replace sanitized input: %w", err)
	}
	return sanitized, true, nil
}

// [v0.2.5 新增]
func shouldStripOpenAIResponsesNonPairCallID(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "message", "reasoning", "image_generation_call":
		return true
	default:
		return false
	}
}
