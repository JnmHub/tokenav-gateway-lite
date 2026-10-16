package gatewaylite

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// control_plane_timeout_ms 折算规则：热路径有上限、后台调用有下限/上限。
//
// 背景：生产实测 43→116 跨境链路单次往返 0.23~0.69s，且会出现成簇的数秒停顿，
// 原来的 3s 配置导致控制面后台调用持续超时。直接把配置调大会同时放大
// 「用户请求路径上的鉴权/租约等待」，因此拆成两类客户端。
func TestControlPlaneClientTimeoutSplit(t *testing.T) {
	cases := []struct {
		name           string
		configured     time.Duration
		wantHotPath    time.Duration
		wantBackground time.Duration
	}{
		{"配置 300ms 时热路径照旧、后台抬到下限", 300 * time.Millisecond, 300 * time.Millisecond, 10 * time.Second},
		{"配置 3s（原线上值）时热路径 3s、后台抬到下限", 3 * time.Second, 3 * time.Second, 10 * time.Second},
		{"配置 10s（本次上调值）时热路径仍被限到 3s、后台 10s", 10 * time.Second, 3 * time.Second, 10 * time.Second},
		{"配置 60s 时后台封顶 30s", 60 * time.Second, 3 * time.Second, 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewControlPlaneClient("https://control.example.test", "token", tc.configured)
			require.NoError(t, err)
			require.Equal(t, tc.wantHotPath, client.httpClient.Timeout,
				"热路径（API Key 解析/额度租约）超时必须被上限约束")
			require.Equal(t, tc.wantBackground, client.backgroundHTTPClient.Timeout,
				"后台调用（健康上报/配置快照推送/缓存失效/探活）应使用后台超时")
			require.Equal(t, controlPlaneUsageTimeout, client.usageHTTPClient.Timeout,
				"用量结算超时保持独立，不受本次改动影响")
		})
	}
}

func TestControlPlaneBackgroundTimeoutClamp(t *testing.T) {
	require.Equal(t, 10*time.Second, ControlPlaneBackgroundTimeout(0))
	require.Equal(t, 10*time.Second, ControlPlaneBackgroundTimeout(time.Second))
	require.Equal(t, 10*time.Second, ControlPlaneBackgroundTimeout(10*time.Second))
	require.Equal(t, 20*time.Second, ControlPlaneBackgroundTimeout(20*time.Second))
	require.Equal(t, 30*time.Second, ControlPlaneBackgroundTimeout(time.Minute))
	require.Equal(t, 30*time.Second, ControlPlaneBackgroundTimeout(time.Hour))
}

// 行为护栏：后台调用走的是「更长超时」的客户端，热路径仍被上限卡住。
// 用一个比热路径上限（3s）慢、但比后台超时快的响应来区分两者 ——
// 若以后有人把后台调用接回 httpClient，本用例会立刻失败。
func TestControlPlaneBackgroundCallsUseLongerTimeout(t *testing.T) {
	const slowResponse = controlPlaneHotPathMaxTimeout + 700*time.Millisecond

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(slowResponse)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/key/resolve":
			_ = json.NewEncoder(w).Encode(ResolveKeyResponse{OK: true})
		case "/internal/gateway/health-report":
			_ = json.NewEncoder(w).Encode(GatewayHealthReportResponse{OK: true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewControlPlaneClient(server.URL, "token", 10*time.Second)
	require.NoError(t, err)

	// 后台调用：应容忍慢响应。
	health, err := client.ReportGatewayHealth(context.Background(), GatewayHealthReportRequest{})
	require.NoError(t, err, "后台控制面调用不应被热路径上限卡死")
	require.NotNil(t, health)

	// 热路径调用：同一个慢响应下必须被上限截断（保护用户请求等待时间）。
	_, err = client.ResolveKey(context.Background(), ResolveKeyRequest{KeyID: "k"})
	require.Error(t, err, "热路径必须在上限内失败，而不是跟着后台一起等 10s")
}
