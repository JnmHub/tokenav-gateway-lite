package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	grokConversationIDHeader         = "X-Grok-Conv-Id"
	claudeCodeSessionHeader          = "X-Claude-Code-Session-Id"
	grokClientToolCacheOptInHeader   = "X-Sub2API-Grok-Client-Tool-Cache"
	grokFreeCacheNativeToolsJSON     = `[{"type":"web_search"},{"type":"x_search"}]`
	grokFreeCacheDisabledToolChoice  = "none"
	grokClientToolCacheOptInExtraKey = "grok_client_tool_cache_enabled"
)

var claudeCodeSessionSuffixPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

func extractClaudeCodeSessionID(c *gin.Context, body []byte) string {
	if c != nil {
		if seed := strings.TrimSpace(c.GetHeader(claudeCodeSessionHeader)); seed != "" {
			return seed
		}
	}
	return extractClaudeCodeSessionIDFromPayload(body)
}

func extractClaudeCodeSessionIDFromPayload(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	userID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String())
	if userID == "" {
		return ""
	}
	if matches := claudeCodeSessionSuffixPattern.FindStringSubmatch(userID); len(matches) >= 2 {
		return matches[1]
	}
	if userID[0] == '{' {
		if sessionID := strings.TrimSpace(gjson.Get(userID, "session_id").String()); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

// resolveGrokCacheIdentity generates one stable upstream cache identity while
// keeping different Tokenav API keys and mapped models isolated. A valid local
// API key is required so internal probes can never share a tenant identity.
func resolveGrokCacheIdentity(c *gin.Context, body []byte, explicitKey, upstreamModel string) string {
	apiKeyID := getAPIKeyIDFromContext(c)
	if apiKeyID <= 0 || isOpenAIResponsesCompactPath(c) {
		return ""
	}

	model := strings.ToLower(strings.TrimSpace(upstreamModel))
	if model == "" {
		return ""
	}

	seed := explicitGrokCacheSeed(c, body, explicitKey)
	if seed == "" {
		seed = deriveOpenAIContentSessionSeed(body)
	}
	if seed == "" {
		return ""
	}

	// The versioned namespace prevents this identity from colliding with other
	// upstream session IDs generated from the same client-provided seed.
	isolatedSeed := fmt.Sprintf("grok-prompt-cache:v1:%d:%s:%s", apiKeyID, model, seed)
	return generateSessionUUID(isolatedSeed)
}

func explicitGrokCacheSeed(c *gin.Context, body []byte, explicitKey string) string {
	seed := extractClaudeCodeSessionID(c, body)
	if seed == "" {
		seed = explicitOpenAIHeaderSessionID(c)
	}
	if seed == "" && c != nil {
		seed = strings.TrimSpace(c.GetHeader(grokConversationIDHeader))
	}
	if seed == "" && len(body) > 0 {
		seed = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	if seed == "" {
		seed = strings.TrimSpace(explicitKey)
	}
	return seed
}

func isGrokRequestContext(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if c.Request != nil {
		if platform, ok := ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
			return platform == PlatformGrok
		}
	}
	value, exists := c.Get("api_key")
	if !exists {
		return false
	}
	apiKey, ok := value.(*APIKey)
	return ok && apiKey != nil && apiKey.Group != nil && apiKey.Group.Platform == PlatformGrok
}

// applyGrokResponsesCacheIdentity replaces any raw client key with the
// tenant-isolated value. For tool-free Grok OAuth requests, the disabled native
// search tools select xAI's cache-capable free route without executing a tool.
func applyGrokResponsesCacheIdentity(body, intentSourceBody []byte, identity string, injectFreeTierTools bool) ([]byte, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		if gjson.GetBytes(body, "prompt_cache_key").Exists() {
			return sjson.DeleteBytes(body, "prompt_cache_key")
		}
		return body, nil
	}

	out, err := sjson.SetBytes(body, "prompt_cache_key", identity)
	if err != nil {
		return nil, err
	}
	if !injectFreeTierTools {
		return out, nil
	}

	// Inspect the pre-sanitization request. Removing an unsupported client tool
	// must not accidentally make the request eligible for native-tool injection.
	if hasGrokResponsesToolIntent(intentSourceBody) {
		return out, nil
	}
	out, err = sjson.SetRawBytes(out, "tools", []byte(grokFreeCacheNativeToolsJSON))
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(out, "tool_choice", grokFreeCacheDisabledToolChoice)
}

func hasGrokResponsesToolIntent(body []byte) bool {
	if gjson.GetBytes(body, "tools").Exists() || gjson.GetBytes(body, "tool_choice").Exists() {
		return true
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
			continue
		}
		tools := item.Get("tools")
		if !tools.Exists() || !tools.IsArray() || len(tools.Array()) > 0 {
			return true
		}
	}
	return false
}

// applyGrokFreeMessagesFunctionToolCacheRoute keeps the Messages bridge on
// xAI's cache-capable mixed-tools route for positively identified Free OAuth
// accounts. The account switch lets operators opt out without affecting paid,
// API Key or unknown tiers.
func applyGrokFreeMessagesFunctionToolCacheRoute(body, intentSourceBody []byte, account *Account, cacheIdentity string) ([]byte, error) {
	allowPureClientTools, _ := grokClientToolCacheAccountPolicy(account)
	return applyGrokFreeToolCacheRoute(body, intentSourceBody, account, cacheIdentity, allowPureClientTools, true)
}

// Compatibility wrapper for existing callers/tests. New request paths should
// use the protocol-specific helper so request-level opt-out is honored.
func applyGrokFreeFunctionToolCacheRoute(body, intentSourceBody []byte, account *Account, cacheIdentity string) ([]byte, error) {
	// 旧调用只把显式函数形式的 search 转成原生 search，不把 read_file 等
	// 纯客户端工具扩大为联网意图；新请求路径使用上面的账号/请求开关。
	return applyGrokFreeToolCacheRoute(body, intentSourceBody, account, cacheIdentity, false, true)
}

func applyGrokFreeRequestToolCacheRoute(c *gin.Context, body, intentSourceBody []byte, account *Account, cacheIdentity string) ([]byte, error) {
	allowPureClientTools, accountPolicyExplicit := grokClientToolCacheAccountPolicy(account)
	requestOptOut := false
	if c != nil {
		switch strings.ToLower(strings.TrimSpace(c.GetHeader(grokClientToolCacheOptInHeader))) {
		case "1", "true", "yes", "on", "prefer-cache":
			// 管理员显式关闭是账号级硬边界，客户端 Header 不能重新开启。
			if !accountPolicyExplicit || allowPureClientTools {
				allowPureClientTools = true
			}
		case "0", "false", "no", "off":
			allowPureClientTools = false
			requestOptOut = true
		}
	}
	if !allowPureClientTools && !accountPolicyExplicit && !requestOptOut && isGrokClaudeDesktopResponsesCacheRequest(c) {
		allowPureClientTools = true
	}
	return applyGrokFreeToolCacheRoute(body, intentSourceBody, account, cacheIdentity, allowPureClientTools, allowPureClientTools)
}

func grokClientToolCacheAccountPolicy(account *Account) (enabled, explicit bool) {
	if !isKnownGrokFreeAccount(account) {
		return false, false
	}
	if account.Extra == nil {
		return true, false
	}
	value, exists := account.Extra[grokClientToolCacheOptInExtraKey]
	if !exists {
		return true, false
	}
	enabled, valid := value.(bool)
	if !valid {
		return false, true
	}
	return enabled, true
}

func isGrokClaudeDesktopResponsesCacheRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil || isOpenAIResponsesCompactPath(c) {
		return false
	}
	path := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	if !strings.HasSuffix(path, "/responses") || !claudeCodeUAPattern.MatchString(strings.TrimSpace(c.GetHeader("User-Agent"))) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.GetHeader("X-App"))) {
	case "cli", "cli-bg":
	default:
		return false
	}
	return strings.EqualFold(strings.TrimSpace(c.GetHeader("anthropic-client-platform")), "desktop_app") &&
		strings.TrimSpace(c.GetHeader(claudeCodeSessionHeader)) != ""
}

func applyGrokFreeToolCacheRoute(body, intentSourceBody []byte, account *Account, cacheIdentity string, allowPureClientTools, allowFunctionSearch bool) ([]byte, error) {
	if strings.TrimSpace(cacheIdentity) == "" || !isKnownGrokFreeAccount(account) {
		return body, nil
	}
	intentTools := gjson.GetBytes(intentSourceBody, "tools")
	intentToolChoice := gjson.GetBytes(intentSourceBody, "tool_choice")
	if !isGrokFreeCacheFunctionToolIntent(intentTools, intentToolChoice) {
		return body, nil
	}
	if intentToolChoice.Type == gjson.String && strings.TrimSpace(intentToolChoice.String()) == grokFreeCacheDisabledToolChoice {
		return appendGrokFreeCacheNativeToolsWithPolicy(body, true, false)
	}
	return appendGrokFreeCacheNativeToolsWithPolicy(body, allowPureClientTools, allowFunctionSearch)
}

func isKnownGrokFreeAccount(account *Account) bool {
	if account == nil || !account.IsGrokOAuth() {
		return false
	}
	// JWT 是刷新后的实时档位，必须覆盖旧 billing/credential 快照。
	if tier := xai.SubscriptionTierFromJWT(account.GetCredential("access_token")); tier != "" {
		return isGrokFreeSubscriptionTier(tier)
	}
	freeSignal := false
	paidSignal := false
	inferredFreeSignal := false

	if billing, err := grokBillingSnapshotFromExtra(account.Extra); err == nil && billing != nil {
		if tier := strings.TrimSpace(billing.Plan); tier != "" {
			if isGrokFreeSubscriptionTier(tier) {
				freeSignal = true
			} else if !isGrokUnknownSubscriptionTier(tier) {
				paidSignal = true
			}
		}
		if billing.UsagePercent != nil || billing.UsedPercent != nil ||
			(billing.MonthlyLimitCents != nil && *billing.MonthlyLimitCents > 0) {
			paidSignal = true
		}
		// xAI monthly billing can return a successful blank plan for Free. Keep
		// partial probes fail-closed so a failed paid window cannot look Free.
		if strings.TrimSpace(billing.MonthlyUpdatedAt) != "" ||
			(billing.StatusCode >= http.StatusOK && billing.StatusCode < http.StatusMultipleChoices &&
				!billing.Partial && len(billing.FailedWindows) == 0) {
			inferredFreeSignal = true
		}
	}
	if snapshot, err := grokQuotaSnapshotFromExtra(account.Extra); err == nil && snapshot != nil {
		if tier := strings.TrimSpace(snapshot.SubscriptionTier); tier != "" {
			if isGrokFreeSubscriptionTier(tier) {
				freeSignal = true
			} else if !isGrokUnknownSubscriptionTier(tier) {
				paidSignal = true
			}
		}
		if snapshot.Tokens != nil && snapshot.Tokens.Limit != nil &&
			xai.IsGrokFreeRolling24hTokenLimit(*snapshot.Tokens.Limit) {
			inferredFreeSignal = true
		}
	}
	if tier := strings.TrimSpace(account.GetCredential("subscription_tier")); tier != "" {
		if isGrokFreeSubscriptionTier(tier) {
			freeSignal = true
		} else if !isGrokUnknownSubscriptionTier(tier) {
			paidSignal = true
		}
	}

	// Explicit paid evidence wins over inferred Free evidence. This prevents an
	// upgraded account with a stale 2M-token Free snapshot from taking the Free route.
	return !paidSignal && (freeSignal || inferredFreeSignal)
}

func isGrokFreeSubscriptionTier(tier string) bool {
	switch xai.NormalizeSubscriptionTier(tier) {
	case "free", "x_basic":
		return true
	default:
		return false
	}
}

func isGrokUnknownSubscriptionTier(tier string) bool {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "unknown", "n/a", "none":
		return true
	default:
		return false
	}
}

func isGrokFreeCacheFunctionToolIntent(tools, toolChoice gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	items := tools.Array()
	if len(items) == 0 {
		return false
	}
	for _, tool := range items {
		if !tool.IsObject() {
			return false
		}
		toolType := strings.TrimSpace(tool.Get("type").String())
		if _, supported := grokResponsesSupportedToolTypes[toolType]; !supported {
			return false
		}
		if toolType == "function" && (strings.TrimSpace(tool.Get("name").String()) == "" || tool.Get("function").Exists()) {
			return false
		}
	}
	if !toolChoice.Exists() {
		return true
	}
	if toolChoice.Type != gjson.String {
		return false
	}
	switch strings.TrimSpace(toolChoice.String()) {
	case "auto", grokFreeCacheDisabledToolChoice:
		return true
	default:
		return false
	}
}

func appendMissingGrokFreeCacheNativeTools(body []byte) ([]byte, error) {
	return appendGrokFreeCacheNativeTools(body, false)
}

func appendGrokFreeCacheNativeTools(body []byte, allowPureClientTools bool) ([]byte, error) {
	return appendGrokFreeCacheNativeToolsWithPolicy(body, allowPureClientTools, true)
}

func appendGrokFreeCacheNativeToolsWithPolicy(body []byte, allowPureClientTools, allowFunctionSearch bool) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body, nil
	}

	items := tools.Array()
	if len(items) == 0 {
		return body, nil
	}
	hasNativeSearch := false
	for _, tool := range items {
		switch strings.TrimSpace(tool.Get("type").String()) {
		case "web_search", "x_search":
			hasNativeSearch = true
		}
	}
	if !allowPureClientTools && !allowFunctionSearch && !hasNativeSearch {
		return body, nil
	}
	merged := make([]json.RawMessage, 0, len(items)+2)
	present := make(map[string]bool, 2)
	hasCompanionTool := false
	for _, tool := range items {
		toolType := strings.TrimSpace(tool.Get("type").String())
		switch toolType {
		case "function":
			name := strings.TrimSpace(tool.Get("name").String())
			if !tool.IsObject() || name == "" || tool.Get("function").Exists() {
				return body, nil
			}
			// Grok Build may expose search as a function declaration. Convert it
			// to the native tool form and deduplicate it instead of sending two
			// tools with the same name to xAI.
			if (name == "web_search" || name == "x_search") && allowFunctionSearch {
				if present[name] {
					continue
				}
				raw, err := json.Marshal(map[string]string{"type": name})
				if err != nil {
					return nil, err
				}
				merged = append(merged, raw)
				present[name] = true
				if allowPureClientTools {
					hasCompanionTool = true
				}
				continue
			}
			if name == "web_search" || name == "x_search" {
				present[name] = true
			}
			hasCompanionTool = true
			merged = append(merged, json.RawMessage(tool.Raw))
		case "web_search", "x_search":
			if present[toolType] {
				continue
			}
			merged = append(merged, json.RawMessage(tool.Raw))
			present[toolType] = true
		default:
			if _, supported := grokResponsesSupportedToolTypes[toolType]; !supported {
				return body, nil
			}
			hasCompanionTool = true
			merged = append(merged, json.RawMessage(tool.Raw))
		}
	}
	if !hasCompanionTool {
		return body, nil
	}
	// 纯客户端函数工具（例如 view_image/read_file）不代表用户想联网搜索。
	// 只有请求本身已经带搜索工具时，才补齐缺少的原生搜索工具。
	if !allowPureClientTools && !present["web_search"] && !present["x_search"] {
		return body, nil
	}
	for _, toolType := range []string{"web_search", "x_search"} {
		if present[toolType] {
			continue
		}
		raw, err := json.Marshal(map[string]string{"type": toolType})
		if err != nil {
			return nil, err
		}
		merged = append(merged, raw)
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encoded)
}

func applyGrokCacheHeaders(headers http.Header, identity string) {
	if headers == nil {
		return
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		headers.Del(grokConversationIDHeader)
		return
	}
	headers.Set(grokConversationIDHeader, identity)
}

func stripGrokChatPromptCacheKey(body []byte) ([]byte, error) {
	if !gjson.GetBytes(body, "prompt_cache_key").Exists() {
		return body, nil
	}
	return sjson.DeleteBytes(body, "prompt_cache_key")
}
