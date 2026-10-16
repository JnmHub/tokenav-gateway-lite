package gatewaylite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	controlPlaneMaxIdleConns        = 64
	controlPlaneMaxIdleConnsPerHost = 32
	controlPlaneMaxConnsPerHost     = 64
	controlPlaneIdleConnTimeout     = 90 * time.Second
	controlPlaneUsageTimeout        = 10 * time.Second
	controlPlaneErrorBodyLimit      = 4 << 10

	// controlPlaneHotPathMaxTimeout 是「用户请求路径上的控制面调用」（API Key 解析、
	// 额度租约获取/补充/再平衡）的超时上限。
	//
	// 为什么单独设上限：这类调用一旦慢，会直接占住用户的请求（Redis 未命中时同步回源）。
	// 控制面整体不可用时，把它们的等待时间放大到配置值（例如 10s）会让用户请求一起挂 10s，
	// 而不是快速失败后走既有的降级/重试。因此热路径取 min(配置值, 本上限)。
	// 后台调用（健康上报、配置快照/推送、缓存失效同步、控制面探活）不受此上限影响。
	controlPlaneHotPathMaxTimeout = 3 * time.Second

	// controlPlaneBackgroundTimeoutFloor / Ceiling 界定后台控制面调用的超时范围：
	// 下限保证后台调用不会因为配置值过小而反复超时（生产实测：跨境链路单次往返
	// 0.23~0.69s，且会出现成簇的数秒停顿），上限避免配置写错时无限等待。
	controlPlaneBackgroundTimeoutFloor   = 10 * time.Second
	controlPlaneBackgroundTimeoutCeiling = 30 * time.Second
)

// ControlPlaneBackgroundTimeout 把配置里的 control_plane_timeout_ms 折算成
// 「后台控制面调用」应使用的超时：不低于 floor、不高于 ceiling。
// 调用点自带 context 期限时（配置快照、缓存失效同步）也应使用本函数，
// 否则更短的 context 期限会覆盖客户端超时，调大配置也不会生效。
func ControlPlaneBackgroundTimeout(configured time.Duration) time.Duration {
	if configured < controlPlaneBackgroundTimeoutFloor {
		return controlPlaneBackgroundTimeoutFloor
	}
	if configured > controlPlaneBackgroundTimeoutCeiling {
		return controlPlaneBackgroundTimeoutCeiling
	}
	return configured
}

// ControlPlaneHTTPError 保留 control-plane（主站控制面）的 HTTP 状态和
// 业务错误码，让可靠投递层能区分“稍后可恢复”和“参数永远不会成功”。
type ControlPlaneHTTPError struct {
	Path       string
	StatusCode int
	ErrorCode  string
}

func (e *ControlPlaneHTTPError) Error() string {
	if e == nil {
		return "control plane request failed"
	}
	message := fmt.Sprintf("control plane %s returned %d", e.Path, e.StatusCode)
	if code := strings.TrimSpace(e.ErrorCode); code != "" {
		message += ": " + code
	}
	return message
}

type ControlPlaneClient struct {
	baseURL    string
	authToken  string
	httpClient *http.Client
	// backgroundHTTPClient 与热路径共享连接池，但用于后台控制面调用
	// （健康上报、配置快照/推送、缓存失效同步、控制面探活）。这类调用不在用户
	// 请求路径上，允许更长的响应时间，否则跨境链路出现成簇数秒停顿时会持续报错。
	backgroundHTTPClient *http.Client
	// usageHTTPClient 与普通控制请求共享连接池，但允许批量结算使用更长的响应时间。
	// 不能直接放大普通控制请求超时，否则冷缓存鉴权故障会长时间占住用户请求。
	usageHTTPClient *http.Client
}

func NewControlPlaneClient(baseURL, authToken string, timeout time.Duration) (*ControlPlaneClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("control plane base URL is required")
	}
	if timeout <= 0 {
		timeout = 300 * time.Millisecond
	}
	// 热路径取上限，后台调用按下限抬高：见两个常量的注释。
	hotPathTimeout := timeout
	if hotPathTimeout > controlPlaneHotPathMaxTimeout {
		hotPathTimeout = controlPlaneHotPathMaxTimeout
	}
	backgroundTimeout := ControlPlaneBackgroundTimeout(timeout)
	transport := newControlPlaneTransport()
	return &ControlPlaneClient{
		baseURL:   baseURL,
		authToken: authToken,
		httpClient: &http.Client{
			Timeout:   hotPathTimeout,
			Transport: transport,
		},
		backgroundHTTPClient: &http.Client{
			Timeout:   backgroundTimeout,
			Transport: transport,
		},
		usageHTTPClient: &http.Client{
			Timeout:   controlPlaneUsageTimeout,
			Transport: transport,
		},
	}, nil
}

func newControlPlaneTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// gateway-lite 会高频访问 control-plane 做 key snapshot（接口密钥快照）
	// 和 lease（额度租约）兜底刷新；显式放大连接池可避免冷缓存时重复 TCP/TLS 建连。
	transport.MaxIdleConns = controlPlaneMaxIdleConns
	transport.MaxIdleConnsPerHost = controlPlaneMaxIdleConnsPerHost
	transport.MaxConnsPerHost = controlPlaneMaxConnsPerHost
	transport.IdleConnTimeout = controlPlaneIdleConnTimeout
	return transport
}

func (c *ControlPlaneClient) ResolveKey(ctx context.Context, req ResolveKeyRequest) (*ResolveKeyResponse, error) {
	var out ResolveKeyResponse
	if err := c.postJSON(ctx, "/internal/key/resolve", req, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return &out, nil
	}
	return &out, nil
}

func (c *ControlPlaneClient) AcquireLease(ctx context.Context, req AcquireLeaseRequest) (*AcquireLeaseResponse, error) {
	var out AcquireLeaseResponse
	if err := c.postJSON(ctx, "/internal/quota/acquire-lease", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *ControlPlaneClient) RefillLease(ctx context.Context, req AcquireLeaseRequest) (*AcquireLeaseResponse, error) {
	var out AcquireLeaseResponse
	if err := c.postJSON(ctx, "/internal/quota/refill-lease", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *ControlPlaneClient) RebalanceLease(ctx context.Context, req RebalanceLeaseRequest) (*RebalanceLeaseResponse, error) {
	var out RebalanceLeaseResponse
	if err := c.postJSON(ctx, "/internal/quota/rebalance-lease", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *ControlPlaneClient) ReportUsage(ctx context.Context, event UsageEvent) error {
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if event.ReportLatencyMillis < 0 {
		event.ReportLatencyMillis = 0
	}
	if err := c.postJSONWithClient(ctx, c.usageHTTPClient, "/internal/usage/report", event, &out); err != nil {
		return err
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "usage report rejected"
		}
		return errors.New(out.Error)
	}
	return nil
}

func (c *ControlPlaneClient) ReportUsageBatch(ctx context.Context, events []UsageEvent) error {
	if len(events) == 0 {
		return nil
	}
	var out UsageBatchReportResponse
	if err := c.postJSONWithClient(ctx, c.usageHTTPClient, "/internal/usage/report-batch", UsageBatchReportRequest{Events: events}, &out); err != nil {
		return err
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "usage batch report rejected"
		}
		return errors.New(out.Error)
	}
	return nil
}

func (c *ControlPlaneClient) ReportContentModerationPenalty(ctx context.Context, req ContentModerationPenaltyRequest) (*ContentModerationPenaltyResponse, error) {
	var out ContentModerationPenaltyResponse
	if err := c.postJSON(ctx, "/internal/content-moderation/penalty", req, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "content moderation penalty rejected"
		}
		return &out, &ControlPlaneHTTPError{
			Path:       "/internal/content-moderation/penalty",
			StatusCode: http.StatusOK,
			ErrorCode:  out.Error,
		}
	}
	return &out, nil
}

func (c *ControlPlaneClient) ReportGatewayHealth(ctx context.Context, req GatewayHealthReportRequest) (*GatewayHealthReportResponse, error) {
	var out GatewayHealthReportResponse
	if err := c.postJSONBackground(ctx, "/internal/gateway/health-report", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *ControlPlaneClient) ReportGatewayConfigChanged(ctx context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
	var out GatewayConfigChangedResponse
	if err := c.postJSONBackground(ctx, "/internal/gateway/config-changed", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *ControlPlaneClient) ReportGatewayConfigPush(ctx context.Context, req GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
	var out GatewayConfigPushResponse
	if err := c.postJSONBackground(ctx, "/internal/gateway/config-push", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *ControlPlaneClient) ProbeControlPlaneHealth(ctx context.Context) (time.Duration, error) {
	startedAt := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return 0, err
	}
	// 探活属于后台调用：用后台客户端，避免被热路径上限压到 3s 而误报控制面不可用。
	resp, err := c.backgroundHTTPClient.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("control plane /health returned %d", resp.StatusCode)
	}
	return time.Since(startedAt), nil
}

func (c *ControlPlaneClient) FetchGatewayConfigSnapshot(ctx context.Context, req GatewayConfigSnapshotRequest) (*GatewayConfigSnapshotResponse, error) {
	var out GatewayConfigSnapshotResponse
	if err := c.postJSONBackground(ctx, "/internal/config/snapshot", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *ControlPlaneClient) FetchCacheInvalidations(ctx context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
	var out CacheInvalidationResponse
	if err := c.postJSONBackground(ctx, "/internal/cache/invalidate", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *ControlPlaneClient) postJSON(ctx context.Context, path string, in any, out any) error {
	return c.postJSONWithClient(ctx, c.httpClient, path, in, out)
}

// postJSONBackground 是后台控制面调用的入口：走后台客户端（更长超时）。
// 与 postJSON 分开，是为了让「调大 control_plane_timeout_ms」只放宽后台调用，
// 不把用户请求路径上的鉴权/租约等待一起放大。
func (c *ControlPlaneClient) postJSONBackground(ctx context.Context, path string, in any, out any) error {
	return c.postJSONWithClient(ctx, c.backgroundHTTPClient, path, in, out)
}

func (c *ControlPlaneClient) postJSONWithClient(ctx context.Context, client *http.Client, path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, controlPlaneErrorBodyLimit))
		var responseError struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &responseError)
		return &ControlPlaneHTTPError{
			Path:       path,
			StatusCode: resp.StatusCode,
			ErrorCode:  strings.TrimSpace(responseError.Error),
		}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
