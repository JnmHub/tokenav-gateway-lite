package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 验证用量日志 API 响应包含伪造首字字段：用户侧与 Admin 侧都必须输出，
// 否则前端 UsageTable 的 fake_first_token_ms 判空永远不成立。
func TestUsageLogDTOPersistsFakeFirstTokenMs(t *testing.T) {
	fake := 205
	first := 9822
	log := &service.UsageLog{FirstTokenMs: &first, FakeFirstTokenMs: &fake}

	userJSON, err := json.Marshal(UsageLogFromService(log))
	if err != nil {
		t.Fatalf("marshal user dto: %v", err)
	}
	if !jsonContains(userJSON, "fake_first_token_ms", 205) {
		t.Fatalf("user dto missing fake_first_token_ms=205: %s", userJSON)
	}

	adminJSON, err := json.Marshal(UsageLogFromServiceAdmin(log))
	if err != nil {
		t.Fatalf("marshal admin dto: %v", err)
	}
	if !jsonContains(adminJSON, "fake_first_token_ms", 205) {
		t.Fatalf("admin dto missing fake_first_token_ms=205: %s", adminJSON)
	}
}

func jsonContains(data []byte, key string, value int) bool {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return false
	}
	v, ok := m[key].(float64)
	return ok && int(v) == value
}
