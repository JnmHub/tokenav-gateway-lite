package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OpenAI 账号上游 WebSocket（WS，双向长连接）支持探测。
//
// 设计目标：账号是否真的能以 WS 连上上游，只能通过真实握手验证——
// OAuth 上游 chatgpt.com 与 API Key 中转站的 WS 支持度差异很大，
// 静态配置无法判断。探测只做一次握手（不发送 response.create，不产生
// 推理消费），成功即代表该账号可走 WS 转发路径；结果落在账号 extra，
// 供管理端列表筛选与批量开启，探测本身不改变任何转发行为。
const (
	// OpenAIWSProbeStatusExtraKey 存 supported / unsupported / unknown。
	OpenAIWSProbeStatusExtraKey = "openai_ws_probe_status"
	// OpenAIWSProbeCheckedAtExtraKey 存 unix 秒，用于后台复检节流与前端展示。
	OpenAIWSProbeCheckedAtExtraKey = "openai_ws_probe_checked_at"
	// OpenAIWSProbeDetailExtraKey 存简短原因（如握手 HTTP 状态），便于诊断。
	OpenAIWSProbeDetailExtraKey = "openai_ws_probe_detail"
)

const (
	OpenAIWSProbeStatusSupported   = "supported"
	OpenAIWSProbeStatusUnsupported = "unsupported"
	OpenAIWSProbeStatusUnknown     = "unknown"
	// OpenAIWSProbeUnsetFilter 列表筛选专用：还没做过任何探测的账号。
	OpenAIWSProbeUnsetFilter = "__unset__"
)

// wsProbeUnsupportedStatuses 这些握手状态码说明上游明确没有可用的 WS
// 端点（不存在 / 不允许 / 要求升级但协议不符），判定为不支持。
// 401/403 是凭据问题、429 是限流、5xx 是上游故障——它们都无法证明
// WS 支持与否，一律归 unknown，等下一次复检。
var wsProbeUnsupportedStatuses = map[int]struct{}{
	http.StatusBadRequest:       {},
	http.StatusNotFound:         {},
	http.StatusMethodNotAllowed: {},
	http.StatusGone:             {},
	http.StatusUpgradeRequired:  {},
	http.StatusNotImplemented:   {},
}

// OpenAIWSSupportProbeResult 一次探测的结果快照。
type OpenAIWSSupportProbeResult struct {
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	CheckedAt int64  `json:"checked_at"`
}

// openAIWSProbeDialTimeout 握手超时。WS 握手只有 TCP+TLS+HTTP 升级，
// 正常 1-2 秒内完成；超时多为网络或代理问题，归 unknown。
const openAIWSProbeDialTimeout = 10 * time.Second

// openAIWSProbeLoopInterval 后台复检周期。新账号最迟一个周期内出结果；
// 已有结果的账号按 openAIWSProbeResultTTL 节流，避免反复打扰上游。
const (
	openAIWSProbeLoopInterval = 15 * time.Minute
	openAIWSProbeResultTTL    = 12 * time.Hour
	openAIWSProbeAccountGap   = 2 * time.Second
)

// probeOpenAIWSTargetURL 计算账号的上游 WS 探测地址。
// 与生产转发 buildOpenAIResponsesWSURL 的选路保持一致：OAuth 走
// chatgpt.com Codex 后端；API Key 走账号 base_url（校验后拼 /v1/responses，
// 缺省 api.openai.com）；其余类型按平台默认地址。
func (s *AccountTestService) probeOpenAIWSTargetURL(account *Account) (string, error) {
	var targetURL string
	switch account.Type {
	case AccountTypeOAuth:
		targetURL = chatgptCodexURL
	case AccountTypeAPIKey:
		baseURL := account.GetOpenAIBaseURL()
		if baseURL == "" {
			targetURL = openaiPlatformAPIURL
		} else {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return "", err
			}
			targetURL = buildOpenAIResponsesURL(validatedURL)
		}
	default:
		targetURL = openaiPlatformAPIURL
	}
	parsed := strings.Replace(strings.TrimSpace(targetURL), "https://", "wss://", 1)
	if !strings.HasPrefix(parsed, "wss://") && !strings.HasPrefix(parsed, "ws://") {
		if strings.HasPrefix(parsed, "http://") {
			parsed = "ws://" + strings.TrimPrefix(parsed, "http://")
		} else {
			return "", fmt.Errorf("unsupported probe target url: %s", targetURL)
		}
	}
	return parsed, nil
}

// probeOpenAIWSHandshakeHeaders 构造与真实 Codex CLI 对齐的最小握手头集。
// 指纹跟随账号配置：合成模式（device/session/full）用账号种子生成，off 则
// 不带指纹头——与该账号真实流量的指纹形态一致，避免探测结论与实跑不符。
func (s *AccountTestService) probeOpenAIWSHandshakeHeaders(ctx context.Context, account *Account, token string) http.Header {
	headers := make(http.Header)
	if !account.IsOpenAIAgentIdentity() {
		headers.Set("authorization", "Bearer "+token)
	}
	headers.Set("originator", "codex_cli_rs")
	headers.Set("OpenAI-Beta", openAIWSBetaV2Value)
	headers.Set("user-agent", codexCLIUserAgent)
	if account.Type == AccountTypeOAuth {
		if accountID := account.GetChatGPTAccountID(); accountID != "" {
			headers.Set("chatgpt-account-id", accountID)
		}
		// 探测没有客户端可透传，直接从账号种子合成；off 模式返回 nil 不写头。
		if ids := resolveCodexFingerprintIDsFromRequest(account, http.Header{}); ids != nil {
			applyCodexFingerprintHeaders(headers, ids)
		}
	}
	account.ApplyHeaderOverrides(headers)
	return headers
}

// probeOpenAIWSSupport 对单个 OpenAI 账号做一次 WS 握手探测。
// 只判定握手是否成功，不发请求体、不消费推理额度。
func (s *AccountTestService) probeOpenAIWSSupport(ctx context.Context, account *Account) OpenAIWSSupportProbeResult {
	result := OpenAIWSSupportProbeResult{
		Status:    OpenAIWSProbeStatusUnknown,
		CheckedAt: time.Now().Unix(),
	}
	if account == nil || !account.IsOpenAI() {
		result.Detail = "not an OpenAI account"
		return result
	}
	if account.IsCredentialShadow() {
		// 凭影子账号没有独立凭据，探测结论应落在运行账号上，由调用方处理。
		result.Detail = "credential shadow account"
		return result
	}

	var token string
	switch account.Type {
	case AccountTypeOAuth, AccountTypeSetupToken:
		if s.openAITokenProvider != nil {
			if accessToken, err := s.openAITokenProvider.GetAccessToken(ctx, account); err == nil && strings.TrimSpace(accessToken) != "" {
				token = strings.TrimSpace(accessToken)
			}
		}
		if token == "" {
			token = strings.TrimSpace(account.GetOpenAIAccessToken())
		}
		if token == "" && !account.IsOpenAIAgentIdentity() {
			result.Detail = "no access token available"
			return result
		}
	case AccountTypeAPIKey:
		token = strings.TrimSpace(account.GetOpenAIApiKey())
		if token == "" {
			result.Detail = "no api key available"
			return result
		}
	default:
		result.Detail = "unsupported account type: " + account.Type
		return result
	}

	wsURL, err := s.probeOpenAIWSTargetURL(account)
	if err != nil {
		result.Detail = "build ws url: " + err.Error()
		return result
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	dialCtx, cancel := context.WithTimeout(ctx, openAIWSProbeDialTimeout)
	defer cancel()
	conn, status, _, dialErr := newDefaultOpenAIWSClientDialer().Dial(dialCtx, wsURL, s.probeOpenAIWSHandshakeHeaders(dialCtx, account, token), proxyURL)
	if dialErr == nil {
		_ = conn.Close()
		result.Status = OpenAIWSProbeStatusSupported
		result.Detail = fmt.Sprintf("handshake ok (%s)", wsHostOf(wsURL))
		return result
	}
	var handshakeErr *openAIWSHandshakeError
	if errors.As(dialErr, &handshakeErr) {
		if _, ok := wsProbeUnsupportedStatuses[status]; ok {
			result.Status = OpenAIWSProbeStatusUnsupported
		}
		result.Detail = fmt.Sprintf("handshake http %d", status)
		if body := strings.TrimSpace(string(handshakeErr.Body)); body != "" && len(body) <= 128 {
			result.Detail += ": " + body
		}
		return result
	}
	result.Detail = "dial: " + dialErr.Error()
	return result
}

func wsHostOf(rawURL string) string {
	if idx := strings.Index(rawURL, "://"); idx >= 0 {
		rest := rawURL[idx+3:]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			return rest[:slash]
		}
		return rest
	}
	return rawURL
}

// ProbeAccountWSSupport 管理端手动触发：取号、探测、落库并返回最新结果。
// 探测结论写进 extra（key 级合并），与隐私模式等运行标记的写法一致。
func (s *AccountTestService) ProbeAccountWSSupport(ctx context.Context, accountID int64) (*Account, OpenAIWSSupportProbeResult, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return nil, OpenAIWSSupportProbeResult{}, fmt.Errorf("account %d not found: %w", accountID, err)
	}
	probeAccount := account
	if account.IsCredentialShadow() {
		// 影子账号要借母账号的实时代理与凭据做握手，结果仍记在影子自己的 extra 上。
		runtimeAccount, _, err := resolveShadowRuntimeAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, OpenAIWSSupportProbeResult{}, err
		}
		if runtimeAccount != nil {
			probeAccount = runtimeAccount
		}
	}
	result := s.probeOpenAIWSSupport(ctx, probeAccount)
	if result.Status == OpenAIWSProbeStatusUnknown && strings.Contains(result.Detail, "credential shadow account") {
		return account, result, nil
	}
	if err := s.persistWSProbeResult(ctx, probeAccount.ID, result); err != nil {
		return probeAccount, result, fmt.Errorf("persist probe result: %w", err)
	}
	return probeAccount, result, nil
}

func (s *AccountTestService) persistWSProbeResult(ctx context.Context, accountID int64, result OpenAIWSSupportProbeResult) error {
	if s.accountRepo == nil {
		return nil
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{
		OpenAIWSProbeStatusExtraKey:    result.Status,
		OpenAIWSProbeCheckedAtExtraKey: result.CheckedAt,
		OpenAIWSProbeDetailExtraKey:    result.Detail,
	})
}

// wsProbeResultFresh 判断账号现有探测结论是否仍在有效期内。
func wsProbeResultFresh(account *Account, now time.Time) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	status, _ := account.Extra[OpenAIWSProbeStatusExtraKey].(string)
	if status != OpenAIWSProbeStatusSupported && status != OpenAIWSProbeStatusUnsupported {
		return false
	}
	checkedAt, _ := account.Extra[OpenAIWSProbeCheckedAtExtraKey].(float64)
	if checkedAt <= 0 {
		// 旧数据可能是 int64/json.Number，做一次宽松回退。
		if v, ok := account.Extra[OpenAIWSProbeCheckedAtExtraKey].(int64); ok {
			checkedAt = float64(v)
		} else {
			return false
		}
	}
	return now.Unix()-int64(checkedAt) < int64(openAIWSProbeResultTTL/time.Second)
}

// StartOpenAIWSSupportProbeLoop 启动后台复检循环：周期性扫描 OpenAI 平台
// 账号，对结论缺失或过期的账号补一次握手探测。手动触发不受此节流限制。
// 探测串行且账号间留间隔，避免集中握手被上游风控注意。
// 生命周期遵循项目惯例：Start 启动、Stop 等待退出，由 wire cleanup 收口。
func (s *AccountTestService) StartOpenAIWSSupportProbeLoop() {
	s.wsProbeLoopOnce.Do(func() {
		s.wsProbeLoopCancel = make(chan struct{})
		wsProbeLoopDone := make(chan struct{})
		s.wsProbeLoopDone = wsProbeLoopDone
		go func() {
			defer close(wsProbeLoopDone)
			// 启动后先等一小段时间，让配置快照、数据库等依赖就绪。
			select {
			case <-s.wsProbeLoopCancel:
				return
			case <-time.After(30 * time.Second):
			}
			s.runOpenAIWSSupportProbeSweep()
			ticker := time.NewTicker(openAIWSProbeLoopInterval)
			defer ticker.Stop()
			for {
				select {
				case <-s.wsProbeLoopCancel:
					return
				case <-ticker.C:
					s.runOpenAIWSSupportProbeSweep()
				}
			}
		}()
	})
}

// StopOpenAIWSSupportProbeLoop 停止后台复检循环并等待当前一轮退出。
func (s *AccountTestService) StopOpenAIWSSupportProbeLoop() {
	s.wsProbeLoopOnce.Do(func() {
		// Start 未被调用过时，Once 兜底初始化通道，避免 nil channel 阻塞。
		s.wsProbeLoopCancel = make(chan struct{})
		close(s.wsProbeLoopCancel)
		s.wsProbeLoopDone = make(chan struct{})
	})
	select {
	case <-s.wsProbeLoopCancel:
	default:
		close(s.wsProbeLoopCancel)
	}
	if s.wsProbeLoopDone != nil {
		select {
		case <-s.wsProbeLoopDone:
		case <-time.After(3 * time.Second):
		}
	}
}

func (s *AccountTestService) runOpenAIWSSupportProbeSweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return
	}
	now := time.Now()
	for i := range accounts {
		if ctx.Err() != nil {
			return
		}
		account := &accounts[i]
		if account.Status != StatusActive {
			continue
		}
		if wsProbeResultFresh(account, now) {
			continue
		}
		result := s.probeOpenAIWSSupport(ctx, account)
		_ = s.persistWSProbeResult(ctx, account.ID, result)
		select {
		case <-ctx.Done():
			return
		case <-time.After(openAIWSProbeAccountGap):
		}
	}
}
