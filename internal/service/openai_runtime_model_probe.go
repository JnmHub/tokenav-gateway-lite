package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
)

const runtimeModelProbeMaxBodyBytes = 64 * 1024
const runtimeModelProbeMaxAccountAttempts = 16
const runtimeModelProbeAccountReloadTimeout = 2 * time.Second

// 单账号上限与健康页 degraded（波动）阈值一致；4~6 秒的成功响应应标黄，
// 不能在阈值之前被内部子超时误报成 error（错误）。
const runtimeModelProbeDefaultAttemptTimeout = monitorDegradedThreshold
const runtimeModelProbeDeadlineReserve = 500 * time.Millisecond
const runtimeModelProbeMaxSlowBudgetSlots = 3

// ProbeRuntimeModel 使用真实 OpenAI-compatible（OpenAI 兼容）账号池发起极小模型请求。
// 该探测不走用户 API Key（接口密钥）和计费链路，只验证本地账号池是否真的能调度并请求上游。
func (s *OpenAIGatewayService) ProbeRuntimeModel(ctx context.Context, preferredModel string) gatewaylite.RuntimeModelProbeResult {
	started := time.Now()
	if s == nil {
		return gatewaylite.RuntimeModelProbeResult{
			OK:      false,
			Status:  MonitorStatusError,
			Target:  "model-chat-completions",
			Error:   "openai gateway service not ready",
			Latency: time.Since(started),
		}
	}
	if s.httpUpstream == nil {
		return gatewaylite.RuntimeModelProbeResult{
			OK:      false,
			Status:  MonitorStatusError,
			Target:  "model-chat-completions",
			Error:   "http upstream client not ready",
			Latency: time.Since(started),
		}
	}

	var lastErr error
	var lastResult gatewaylite.RuntimeModelProbeResult
	var hasLastResult bool
	for _, model := range s.runtimeModelProbeCandidates(ctx, preferredModel) {
		accounts, selectErr := s.runtimeModelProbeAccounts(ctx, model)
		if len(accounts) == 0 {
			if selectErr != nil {
				lastErr = selectErr
			}
			continue
		}

		// 一个慢账号不能独占整次健康探针的 deadline（截止时间）。预算最多按 3 个
		// 慢候选切分，而不是按全部 16 个候选平均：否则账号池越大，正常的 1~2 秒
		// 上游也会被压成不足 1 秒并误判。快速失败仍会继续遍历后续候选；连续慢
		// 账号最多消耗外层总 deadline，不能无限延长整轮探针。
		for index, account := range accounts {
			attemptTimeout := runtimeModelProbeAttemptTimeout(ctx, len(accounts)-index)
			attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
			result := s.probeOpenAIAccountRuntimeModel(attemptCtx, account, model, started)
			cancel()
			if result.OK {
				return result
			}
			lastResult = result
			hasLastResult = true
		}
		if hasLastResult {
			return lastResult
		}
	}

	msg := "no available OpenAI-compatible accounts"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	return gatewaylite.RuntimeModelProbeResult{
		OK:       false,
		Status:   MonitorStatusError,
		Target:   "model-chat-completions",
		Platform: PlatformOpenAI,
		Model:    strings.TrimSpace(preferredModel),
		Error:    sanitizeRuntimeProbeErrorMessage(msg),
		Latency:  time.Since(started),
	}
}

// runtimeModelProbeAccounts 按正式调度器的顺序收集本轮账号候选。
// 预先收集候选是为了在发请求前知道账号数，从而在总 deadline 内公平切分预算；
// 该过程只读取调度快照，不占用账号并发槽，也不改变账号健康或优先级。
func (s *OpenAIGatewayService) runtimeModelProbeAccounts(ctx context.Context, model string) ([]*Account, error) {
	excluded := make(map[int64]struct{})
	accounts := make([]*Account, 0, 4)
	var lastErr error
	for len(accounts) < runtimeModelProbeMaxAccountAttempts {
		account, err := s.SelectAccountForModelWithExclusions(ctx, nil, "", model, excluded)
		if err != nil {
			lastErr = err
			break
		}
		if account == nil {
			lastErr = fmt.Errorf("selected account is nil")
			break
		}
		if _, exists := excluded[account.ID]; exists {
			lastErr = fmt.Errorf("account selector returned duplicate account %d", account.ID)
			break
		}
		accounts = append(accounts, account)
		excluded[account.ID] = struct{}{}
	}
	return accounts, lastErr
}

func runtimeModelProbeAttemptTimeout(ctx context.Context, accountCount int) time.Duration {
	if accountCount <= 0 {
		accountCount = 1
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return runtimeModelProbeDefaultAttemptTimeout
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	reserve := runtimeModelProbeDeadlineReserve
	if maxReserve := remaining / 10; reserve > maxReserve {
		reserve = maxReserve
	}
	usable := remaining - reserve
	if usable <= 0 {
		usable = remaining
	}
	budgetSlots := accountCount
	if budgetSlots > runtimeModelProbeMaxSlowBudgetSlots {
		budgetSlots = runtimeModelProbeMaxSlowBudgetSlots
	}
	timeout := usable / time.Duration(budgetSlots)
	if timeout <= 0 {
		return time.Nanosecond
	}
	if timeout > runtimeModelProbeDefaultAttemptTimeout {
		return runtimeModelProbeDefaultAttemptTimeout
	}
	return timeout
}

func (s *OpenAIGatewayService) runtimeModelProbeCandidates(ctx context.Context, preferredModel string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		if _, ok := seen[model]; ok {
			return
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}

	add(preferredModel)

	if accounts, err := s.listSchedulableAccounts(ctx, nil); err == nil {
		mapped := make([]string, 0)
		for i := range accounts {
			account := &accounts[i]
			if !isOpenAIAccountEligibleForRequest(ctx, account, "", false, "") {
				continue
			}
			for model := range account.GetModelMapping() {
				if strings.TrimSpace(model) != "" {
					mapped = append(mapped, model)
				}
			}
		}
		sort.Strings(mapped)
		for _, model := range mapped {
			add(model)
		}
	}

	add(openai.DefaultTestModel)
	return out
}

func (s *OpenAIGatewayService) probeOpenAIAccountRuntimeModel(ctx context.Context, account *Account, requestedModel string, started time.Time) gatewaylite.RuntimeModelProbeResult {
	selectedAccount := account
	account, reloadErr := s.reloadRuntimeProbeAccountCredentials(ctx, account)
	if reloadErr != nil {
		result := gatewaylite.RuntimeModelProbeResult{
			Platform: PlatformOpenAI,
			Target:   "model-chat-completions",
			Status:   MonitorStatusError,
			Error:    sanitizeRuntimeProbeAccountErrorMessage("reload selected account: "+reloadErr.Error(), selectedAccount),
			Latency:  time.Since(started),
		}
		if selectedAccount != nil {
			result.AccountID = selectedAccount.ID
			result.AccountName = selectedAccount.Name
		}
		return result
	}
	billingModel := resolveOpenAIForwardModel(account, requestedModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	result := gatewaylite.RuntimeModelProbeResult{
		Model:         billingModel,
		UpstreamModel: upstreamModel,
		Platform:      PlatformOpenAI,
		AccountID:     account.ID,
		AccountName:   account.Name,
		Target:        "model-chat-completions",
	}
	challenge := generateChallenge()

	// Agent Identity（代理身份）没有 bearer token（承载令牌）和刷新生命周期，
	// 每次请求由 buildOpenAIAuthenticationHeaders 使用私钥动态签名。这里显式绕过
	// TokenProvider，避免健康探针把合法的无 token 身份误判成损坏 OAuth 账号。
	token := ""
	var err error
	if !account.IsOpenAIAgentIdentity() {
		token, _, err = s.GetAccessToken(ctx, account)
		if err != nil {
			result.Status = MonitorStatusError
			result.Error = sanitizeRuntimeProbeAccountErrorMessage("get access token: "+err.Error(), account)
			result.Latency = time.Since(started)
			return result
		}
	}

	var req *http.Request
	switch account.Type {
	case AccountTypeOAuth:
		result.Target = "model-responses"
		req, err = s.buildOpenAIRuntimeResponsesProbeRequest(ctx, account, token, upstreamModel, challenge.Prompt)
	case AccountTypeAPIKey:
		req, err = s.buildOpenAIRuntimeChatProbeRequest(ctx, account, token, upstreamModel, challenge.Prompt)
	default:
		err = fmt.Errorf("unsupported OpenAI account type: %s", account.Type)
	}
	if err != nil {
		result.Status = MonitorStatusError
		result.Error = sanitizeRuntimeProbeAccountErrorMessage("build upstream probe request: "+err.Error(), account)
		result.Latency = time.Since(started)
		return result
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	result.Latency = time.Since(started)
	if err != nil {
		result.Status = MonitorStatusError
		result.Error = sanitizeRuntimeProbeAccountErrorMessage("upstream request failed: "+err.Error(), account)
		return result
	}
	if resp == nil {
		result.Status = MonitorStatusError
		result.Error = "upstream response is nil"
		return result
	}
	defer func() { _ = resp.Body.Close() }()

	result.StatusCode = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && account.Type == AccountTypeOAuth {
		respText, streamErr := readOpenAIRuntimeResponsesProbeStream(resp.Body)
		// 流式请求在 Do 返回时通常只收到响应头。健康页需要展示一次完整探针的
		// 实际耗时，因此必须等到 SSE 完成或失败后再记录，不能把首包时间误当总耗时。
		result.Latency = time.Since(started)
		if streamErr != nil {
			result.Status = MonitorStatusError
			result.Error = sanitizeRuntimeProbeAccountErrorMessage(streamErr.Error(), account)
			return result
		}
		if !validateChallenge(respText, challenge.Expected) {
			result.Status = MonitorStatusFailed
			result.Error = sanitizeRuntimeProbeAccountErrorMessage(fmt.Sprintf("challenge mismatch (expected %s, got %q)", challenge.Expected, respText), account)
			return result
		}
		result.OK = true
		if result.Latency >= monitorDegradedThreshold {
			result.Status = MonitorStatusDegraded
		} else {
			result.Status = MonitorStatusOperational
		}
		return result
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, runtimeModelProbeMaxBodyBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, runtimeModelProbeMaxBodyBytes))
	result.Latency = time.Since(started)
	if account.IsOpenAIAgentIdentity() && !agentIdentityTaskRecoveryWasTried(ctx) && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, body) {
		expectedTaskID := strings.TrimSpace(account.GetCredential("task_id"))
		if recoverErr := s.recoverAgentIdentityTask(ctx, account, expectedTaskID); recoverErr != nil {
			result.Status = MonitorStatusError
			result.Error = sanitizeRuntimeProbeAccountErrorMessage("agent identity task recovery failed: "+recoverErr.Error(), account)
			return result
		}
		return s.probeOpenAIAccountRuntimeModel(markAgentIdentityTaskRecoveryTried(ctx), account, requestedModel, started)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		respText := extractOpenAIRuntimeProbeText(account.Type, body)
		if !validateChallenge(respText, challenge.Expected) {
			result.Status = MonitorStatusFailed
			result.Error = sanitizeRuntimeProbeAccountErrorMessage(fmt.Sprintf("challenge mismatch (expected %s, got %q)", challenge.Expected, respText), account)
			return result
		}
		result.OK = true
		if result.Latency >= monitorDegradedThreshold {
			result.Status = MonitorStatusDegraded
		} else {
			result.Status = MonitorStatusOperational
		}
		return result
	}
	msg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	result.Status = MonitorStatusError
	result.Error = sanitizeRuntimeProbeAccountErrorMessage(msg, account)
	return result
}

// sanitizeRuntimeProbeErrorMessage 是所有会上报到 control-plane（主站控制面）的
// 探针错误出口。上游错误内容不可信，必须同时擦除 URL 参数和常见密钥字面量，并限制长度，
// 避免兼容上游把凭据或大段响应写进健康心跳、数据库和告警邮件。
func sanitizeRuntimeProbeErrorMessage(msg string) string {
	return truncateMessage(sanitizeErrorMessage(sanitizeUpstreamErrorMessage(msg)))
}

var runtimeProbeSecretCredentialKeys = [...]string{
	"api_key",
	"access_token",
	"refresh_token",
	"id_token",
	"client_secret",
	"agent_private_key",
}

// sanitizeRuntimeProbeAccountErrorMessage 先按当前账号的真实认证材料做精确擦除。
// 通用正则无法识别 `jnm` 这类短自定义密钥，也无法覆盖所有非 JWT Token；精确替换后
// 再执行通用脱敏，才能保证异常上游回显 Authorization 时不会把凭据带进心跳。
func sanitizeRuntimeProbeAccountErrorMessage(msg string, account *Account) string {
	if account != nil {
		for _, key := range runtimeProbeSecretCredentialKeys {
			secret := strings.TrimSpace(account.GetCredential(key))
			if len(secret) < 3 {
				continue
			}
			msg = strings.ReplaceAll(msg, secret, "***REDACTED***")
		}
	}
	return sanitizeRuntimeProbeErrorMessage(msg)
}

// reloadRuntimeProbeAccountCredentials 对调度元数据做一次有界的完整账号补全。
// 正常请求的 selector（选择器）已经会从 sched:acc 读取完整账号；这里仍保留低频兜底，
// 防止滚动升级期间旧 hot cache（热缓存）只带 sched:meta，导致 Agent Identity 被误走 token。
func (s *OpenAIGatewayService) reloadRuntimeProbeAccountCredentials(ctx context.Context, account *Account) (*Account, error) {
	if account == nil || !runtimeProbeAccountNeedsCredentialReload(account) || s == nil || s.accountRepo == nil || account.ID <= 0 {
		return account, nil
	}
	reloadCtx, cancel := context.WithTimeout(ctx, runtimeModelProbeAccountReloadTimeout)
	defer cancel()
	reloaded, err := s.accountRepo.GetByID(reloadCtx, account.ID)
	if err != nil {
		return nil, err
	}
	if reloaded == nil {
		return nil, fmt.Errorf("account %d not found", account.ID)
	}
	return reloaded, nil
}

func runtimeProbeAccountNeedsCredentialReload(account *Account) bool {
	if account == nil || !account.IsOpenAI() {
		return false
	}
	switch account.Type {
	case AccountTypeOAuth:
		if account.IsOpenAIAgentIdentity() {
			return strings.TrimSpace(account.GetCredential("agent_private_key")) == "" ||
				strings.TrimSpace(account.GetCredential("agent_runtime_id")) == ""
		}
		return strings.TrimSpace(account.GetOpenAIAccessToken()) == "" &&
			strings.TrimSpace(account.GetOpenAIRefreshToken()) == ""
	case AccountTypeAPIKey:
		return strings.TrimSpace(account.GetOpenAIApiKey()) == ""
	default:
		return false
	}
}

func (s *OpenAIGatewayService) buildOpenAIRuntimeChatProbeRequest(ctx context.Context, account *Account, token string, upstreamModel string, prompt string) (*http.Request, error) {
	baseURL := account.GetOpenAIBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	validatedURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"model": upstreamModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": monitorChallengeMaxTokens,
		"stream":     false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildOpenAIChatCompletionsURL(validatedURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("authorization", "Bearer "+token)
	if ua := account.GetOpenAIUserAgent(); strings.TrimSpace(ua) != "" {
		req.Header.Set("user-agent", ua)
	}
	return req, nil
}

func (s *OpenAIGatewayService) buildOpenAIRuntimeResponsesProbeRequest(ctx context.Context, account *Account, token string, upstreamModel string, prompt string) (*http.Request, error) {
	payload, _ := json.Marshal(map[string]any{
		"model":        upstreamModel,
		"instructions": "You are a channel health-check endpoint. Answer the arithmetic challenge exactly and briefly.",
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]string{
					{"type": "input_text", "text": prompt},
				},
			},
		},
		// ChatGPT Codex backend（后端）要求流式请求，但部分当前版本明确拒绝
		// public Responses API（公开响应接口）的 max_output_tokens 字段。
		// 算术挑战本身很短，不依赖输出上限；省略该字段可兼容官方和代理上游。
		"stream": true,
		"store":  false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"
	authHeaders, authErr := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if authErr != nil {
		return nil, authErr
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("originator", "codex_cli_rs")
	if chatgptAccountID := account.GetChatGPTAccountID(); chatgptAccountID != "" {
		req.Header.Set("chatgpt-account-id", chatgptAccountID)
	}
	if ua := account.GetOpenAIUserAgent(); strings.TrimSpace(ua) != "" {
		req.Header.Set("user-agent", ua)
	} else {
		req.Header.Set("user-agent", codexCLIUserAgent)
	}
	enforceCodexIdentityHeadersWithUA(req.Header, account.GetOpenAIUserAgent())
	stripOpenAILegacyResponsesBeta(req.Header)
	return req, nil
}

// readOpenAIRuntimeResponsesProbeStream 只读取 OAuth Responses 探针的最小 SSE（服务端事件流）。
// 探针在终止事件到达后立即返回，避免等待上游长连接自然断开；总读取量受 64 KiB 限制，
// 防止异常上游用无界流占用健康检查 goroutine（协程）和内存。
func readOpenAIRuntimeResponsesProbeStream(body io.Reader) (string, error) {
	if body == nil {
		return "", fmt.Errorf("upstream response body is nil")
	}

	limited := &io.LimitedReader{R: body, N: runtimeModelProbeMaxBodyBytes + 1}
	scanner := bufio.NewScanner(limited)
	buf := getSSEScannerBuf64K()
	defer putSSEScannerBuf64K(buf)
	scanner.Buffer(buf[:], runtimeModelProbeMaxBodyBytes)

	var dataLines []string
	var deltas strings.Builder
	flush := func() (string, bool, error) {
		if len(dataLines) == 0 {
			return "", false, nil
		}
		lines := dataLines
		dataLines = nil
		if strings.TrimSpace(strings.Join(lines, "\n")) == "[DONE]" {
			return "", false, fmt.Errorf("upstream stream ended before response.completed")
		}

		var terminalText string
		var terminal bool
		var eventErr error
		emitOpenAISSEDataPayloads(lines, func(payload []byte) {
			if terminal || eventErr != nil {
				return
			}
			if !gjson.ValidBytes(payload) {
				eventErr = fmt.Errorf("upstream returned an invalid SSE event")
				return
			}

			eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
			if eventType == "error" || gjson.GetBytes(payload, "error").Exists() && eventType == "" {
				message := firstNonEmptyRuntimeProbeError(
					gjson.GetBytes(payload, "error.message").String(),
					gjson.GetBytes(payload, "message").String(),
					"OpenAI response stream returned an error",
				)
				eventErr = fmt.Errorf("%s", message)
				return
			}

			switch eventType {
			case "response.output_text.delta":
				deltas.WriteString(gjson.GetBytes(payload, "delta").String())
			case "response.completed", "response.done":
				response := gjson.GetBytes(payload, "response")
				if response.Exists() {
					terminalText = extractOpenAIResponsesText([]byte(response.Raw))
				}
				if strings.TrimSpace(terminalText) == "" {
					terminalText = deltas.String()
				}
				terminal = true
			case "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
				message := firstNonEmptyRuntimeProbeError(
					gjson.GetBytes(payload, "response.error.message").String(),
					gjson.GetBytes(payload, "error.message").String(),
					gjson.GetBytes(payload, "message").String(),
					"OpenAI response stream failed",
				)
				eventErr = fmt.Errorf("%s", message)
			}
		})
		return terminalText, terminal, eventErr
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if data, ok := extractOpenAISSEDataLine(line); ok {
			dataLines = append(dataLines, data)
			continue
		}
		if strings.TrimSpace(line) != "" {
			continue
		}
		text, terminal, err := flush()
		if err != nil {
			return "", err
		}
		if terminal {
			return text, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("upstream stream read failed: %w", err)
	}
	text, terminal, err := flush()
	if err != nil {
		return "", err
	}
	if terminal {
		return text, nil
	}
	if limited.N <= 0 {
		return "", fmt.Errorf("upstream stream exceeded %d bytes", runtimeModelProbeMaxBodyBytes)
	}
	return "", fmt.Errorf("upstream stream ended before response.completed")
}

func firstNonEmptyRuntimeProbeError(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "OpenAI response stream failed"
}

func extractOpenAIRuntimeProbeText(accountType string, body []byte) string {
	if accountType == AccountTypeOAuth {
		return extractOpenAIResponsesText(body)
	}
	return gjson.GetBytes(body, "choices.0.message.content").String()
}
