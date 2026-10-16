package middleware

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// isAsyncImageTaskRead 自官方 0.2.7 backend/internal/server/middleware/api_key_auth.go
// 移植的纯函数：判断是否为异步生图任务查询（GET，只读不计费）。
// 注意：本仓库生产代码尚未接线该判定（api_key_auth.go 未移植），为不改动生产代码，
// 暂放测试文件供单测覆盖；后续生产移植时请删除此处定义，避免重复声明。
func isAsyncImageTaskRead(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	return strings.HasPrefix(path, "/v1/images/tasks/") || strings.HasPrefix(path, "/images/tasks/")
}

func TestIsAsyncImageTaskRead(t *testing.T) {
	require.True(t, isAsyncImageTaskRead(http.MethodGet, "/v1/images/tasks/imgtask_123"))
	require.True(t, isAsyncImageTaskRead(http.MethodGet, "/images/tasks/imgtask_123"))
	require.False(t, isAsyncImageTaskRead(http.MethodPost, "/v1/images/tasks/imgtask_123"))
	require.False(t, isAsyncImageTaskRead(http.MethodGet, "/v1/images/generations"))
}
