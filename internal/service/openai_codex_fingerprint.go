package service

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexFingerprintIDsContextKey = "codex_fingerprint_ids"

// stageCodexFingerprintIDs stages one attempt's IDs. Storing nil is required:
// a failover from a converged account to an off account must clear stale IDs.
func stageCodexFingerprintIDs(c *gin.Context, ids *codexFingerprintIDs) {
	if c != nil {
		c.Set(codexFingerprintIDsContextKey, ids)
	}
}

func applyStagedCodexFingerprintHeaders(c *gin.Context, account *Account, headers http.Header) {
	if c == nil || account == nil || account.Type != AccountTypeOAuth {
		return
	}
	value, ok := c.Get(codexFingerprintIDsContextKey)
	if !ok {
		return
	}
	if ids, ok := value.(*codexFingerprintIDs); ok {
		applyCodexFingerprintHeaders(headers, ids)
	}
}

type codexFingerprintMode string

const (
	codexFingerprintOff     codexFingerprintMode = "off"
	codexFingerprintDevice  codexFingerprintMode = "device"
	codexFingerprintSession codexFingerprintMode = "session"
	codexFingerprintFull    codexFingerprintMode = "full"

	codexFingerprintModeExtraKey = "codex_fingerprint_mode"
	codexFingerprintSeedExtraKey = "codex_fingerprint_seed"
)

// canonicalCodexFingerprintSeed 校验账号 extra 里的系统管理随机种子：
// 必须是规范格式的非 nil UUID 字符串。
func canonicalCodexFingerprintSeed(value any) (string, bool) {
	raw, ok := value.(string)
	if !ok {
		return "", false
	}
	trimmed := strings.TrimSpace(raw)
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil || trimmed != parsed.String() {
		return "", false
	}
	return trimmed, true
}

func newCodexFingerprintSeed() string {
	return uuid.NewString()
}

func stripCodexFingerprintSeed(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	stripped := maps.Clone(extra)
	delete(stripped, codexFingerprintSeedExtraKey)
	return stripped
}

func codexFingerprintModeFromExtra(extra map[string]any) codexFingerprintMode {
	if extra == nil {
		return codexFingerprintOff
	}
	raw, _ := extra[codexFingerprintModeExtraKey].(string)
	switch codexFingerprintMode(strings.TrimSpace(raw)) {
	case codexFingerprintOff, codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull:
		return codexFingerprintMode(strings.TrimSpace(raw))
	default:
		return codexFingerprintOff
	}
}

func codexFingerprintModeRequiresSeed(mode codexFingerprintMode) bool {
	switch mode {
	case codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull:
		return true
	default:
		return false
	}
}

func codexFingerprintSeed(extra map[string]any) (string, bool) {
	if extra == nil {
		return "", false
	}
	return canonicalCodexFingerprintSeed(extra[codexFingerprintSeedExtraKey])
}

// prepareCodexFingerprintExtraForCreate 剥离客户端提交的 seed，仅在启用收敛时
// 生成系统管理的随机种子。seed 是内部实现细节，不允许由调用方指定。
func prepareCodexFingerprintExtraForCreate(platform, accountType string, extra map[string]any) map[string]any {
	prepared := stripCodexFingerprintSeed(extra)
	if platform != PlatformOpenAI || accountType != AccountTypeOAuth || !codexFingerprintModeRequiresSeed(codexFingerprintModeFromExtra(prepared)) {
		return prepared
	}
	if prepared == nil {
		prepared = make(map[string]any, 1)
	}
	prepared[codexFingerprintSeedExtraKey] = newCodexFingerprintSeed()
	return prepared
}

// prepareCodexFingerprintExtraForUpdate 在编辑账号时原子保活 seed：
// 已有 seed 原样带回（编辑不能换号），没有 seed 但新配置启用收敛时生成一个。
func prepareCodexFingerprintExtraForUpdate(account *Account, extra map[string]any) map[string]any {
	prepared := stripCodexFingerprintSeed(extra)
	if account == nil || !account.IsOpenAIOAuth() {
		return prepared
	}
	if seed, ok := codexFingerprintSeed(account.Extra); ok {
		if prepared == nil {
			prepared = make(map[string]any, 1)
		}
		prepared[codexFingerprintSeedExtraKey] = seed
		return prepared
	}
	if codexFingerprintModeRequiresSeed(codexFingerprintModeFromExtra(prepared)) {
		if prepared == nil {
			prepared = make(map[string]any, 1)
		}
		prepared[codexFingerprintSeedExtraKey] = newCodexFingerprintSeed()
	}
	return prepared
}

func sanitizedCodexFingerprintExtraUpdates(updates map[string]any) map[string]any {
	if updates == nil {
		return nil
	}
	sanitized := maps.Clone(updates)
	delete(sanitized, codexFingerprintSeedExtraKey)
	return sanitized
}

// ShouldEnsureCodexFingerprintSeedForExtraUpdates reports whether a JSONB key-level
// extra update is enabling Codex fingerprint convergence and therefore must atomically
// preserve or create the system-managed per-account seed in the repository update.
func ShouldEnsureCodexFingerprintSeedForExtraUpdates(updates map[string]any) bool {
	if updates == nil {
		return false
	}
	return codexFingerprintModeRequiresSeed(codexFingerprintModeFromExtra(updates))
}

// GetCodexFingerprintMode keeps accounts without an explicit setting on the
// original pass-through behavior. Convergence is opt-in so an upgrade cannot
// silently rewrite identities or change upstream quota classification.
func (a *Account) GetCodexFingerprintMode() codexFingerprintMode {
	if a == nil || !a.IsOpenAIOAuth() {
		return codexFingerprintOff
	}
	raw := strings.TrimSpace(a.GetExtraString(codexFingerprintModeExtraKey))
	switch codexFingerprintMode(raw) {
	case codexFingerprintOff, codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull:
		return codexFingerprintMode(raw)
	default:
		return codexFingerprintOff
	}
}

func normalizeCodexFingerprintModeExtra(platform, accountType string, extra map[string]any, create bool) (map[string]any, error) {
	normalized := make(map[string]any, len(extra)+1)
	for key, value := range extra {
		normalized[key] = value
	}
	raw, exists := normalized[codexFingerprintModeExtraKey]
	if !exists {
		return normalized, nil
	}
	mode, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("%s must be off, device, session, or full", codexFingerprintModeExtraKey)
	}
	mode = strings.TrimSpace(mode)
	if platform != PlatformOpenAI || accountType != AccountTypeOAuth {
		return nil, fmt.Errorf("%s is only supported for OpenAI OAuth accounts", codexFingerprintModeExtraKey)
	}
	switch codexFingerprintMode(mode) {
	case codexFingerprintOff, codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull:
		normalized[codexFingerprintModeExtraKey] = mode
		return normalized, nil
	default:
		return nil, fmt.Errorf("%s must be off, device, session, or full", codexFingerprintModeExtraKey)
	}
}

func deriveStableUUIDv4(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(bytes[0:4]), binary.BigEndian.Uint16(bytes[4:6]),
		binary.BigEndian.Uint16(bytes[6:8]), binary.BigEndian.Uint16(bytes[8:10]), bytes[10:16])
}

// codexFingerprintSessionTTL 是收敛 session/thread 的会话生命周期。
// 官方 Codex CLI 每个进程启动时创建新会话；网关以进程内存会话对齐该语义：
// 首次使用时以真实时间生成 UUIDv7，会话内稳定复用，TTL 到期轮换。
// 早期实现把时间戳固定映射到历史窗口，同一账号永远发同一个"过期时间戳"
// 的 session_id，在上游做 session 创建时间分布分析时反而成为新特征。
const codexFingerprintSessionTTL = 24 * time.Hour

// codexFingerprintSessions 是账号维度的收敛会话缓存（进程生命周期）。
// key: int64(accountID) → *codexFingerprintSessionIDs。
// 不落库：官方会话同样不跨进程持久；多实例网关各自持有独立会话，
// 在上游视角中等价于"同一账号多设备登录"，是自然存在的真实分布。
var codexFingerprintSessions sync.Map

type codexFingerprintSessionIDs struct {
	sessionID string
	threadID  string
	expiresAt time.Time
}

// convergedCodexSessionIDs 返回账号的收敛 session/thread ID。
// 语义对齐官方 0.154.0 取证抓包：session_id 是会话创建时刻的真实 UUIDv7，
// thread_id 与 session_id 同值（取证原文 session-id == thread-id ==
// x-client-request-id）。会话内稳定、TTL 轮换，时间戳总是近期的。
//
// 并发正确性：读取-生成-写入必须原子，否则同账号首次并发请求（或过期轮换
// 竞争）会各自生成不同会话——sync.Map 只保证单个操作安全。这里用
// LoadOrStore（首次写入）+ CompareAndSwap（过期轮换）保证任意时刻并发的
// 所有请求拿到同一个会话 ID；CAS 失败意味着并发方刚完成轮换，重读即可。
func convergedCodexSessionIDs(accountID int64, now time.Time) (sessionID, threadID string) {
	newEntry := func() *codexFingerprintSessionIDs {
		session := uuid.Must(uuid.NewV7()).String()
		return &codexFingerprintSessionIDs{
			sessionID: session,
			threadID:  session,
			expiresAt: now.Add(codexFingerprintSessionTTL),
		}
	}
	for {
		cached, ok := codexFingerprintSessions.Load(accountID)
		if !ok {
			candidate := newEntry()
			actual, loaded := codexFingerprintSessions.LoadOrStore(accountID, candidate)
			if !loaded {
				return candidate.sessionID, candidate.threadID
			}
			cached = actual // 并发赢家已写入，走统一读取路径。
		}
		entry, ok := cached.(*codexFingerprintSessionIDs)
		if !ok {
			// 类型异常（理论上不可能）：覆盖为全新会话兜底。
			fallback := newEntry()
			codexFingerprintSessions.Store(accountID, fallback)
			return fallback.sessionID, fallback.threadID
		}
		if now.Before(entry.expiresAt) {
			return entry.sessionID, entry.threadID
		}
		// 过期：CAS 原子轮换；失败表示并发方刚换新，重读。
		candidate := newEntry()
		if codexFingerprintSessions.CompareAndSwap(accountID, entry, candidate) {
			return candidate.sessionID, candidate.threadID
		}
	}
}

// resetCodexFingerprintSessions 清空会话缓存（测试用）。
func resetCodexFingerprintSessions() {
	codexFingerprintSessions.Range(func(k, _ any) bool {
		codexFingerprintSessions.Delete(k)
		return true
	})
}

// resolveConvergedInstallationID 返回设备级持久 ID：官方语义为首次安装时生成的
// 随机 UUIDv4 并长期持久化，因此由账号 seed 确定性派生 v4 是正确的稳定设备标识。
func resolveConvergedInstallationID(account *Account, seed string) string {
	if account == nil {
		return ""
	}
	if deviceID := account.GetOpenAIDeviceID(); deviceID != "" {
		return deviceID
	}
	if seed == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-install-id:v2:" + seed)
}

type codexFingerprintIDs struct {
	mode           codexFingerprintMode
	installationID string
	sessionID      string
	threadID       string
	turnID         string
	windowID       string
	// originalBodySessionID 是请求体 client_metadata.session_id 的原始值。
	// root prompt_cache_key 默认取自该值（同一会话内共享缓存键），收敛
	// session_id 时必须同步改写它，否则后续同一会话的请求缓存键错位。
	// 只捕获一次：后续路径复用同一 ids 实例时不再覆盖。
	originalBodySessionID         string
	originalBodySessionIDCaptured bool
}

// resolveCodexFingerprintIDs 解析账号的收敛身份 ID。
// clientSessionID 参数仅用于触发收敛判断的调用方语义，ID 生成不再依赖它：
// 官方 0.154.0 的 thread_id 与 session_id 同值，会话由网关进程级缓存承载。
func resolveCodexFingerprintIDs(account *Account, clientSessionID string, mode codexFingerprintMode) *codexFingerprintIDs {
	if account == nil || mode == codexFingerprintOff {
		return nil
	}
	seed, ok := codexFingerprintSeed(account.Extra)
	if !ok {
		return nil
	}
	_ = clientSessionID

	ids := &codexFingerprintIDs{mode: mode}
	ids.installationID = resolveConvergedInstallationID(account, seed)
	if ids.installationID == "" {
		return nil
	}

	switch mode {
	case codexFingerprintDevice:
		return ids

	case codexFingerprintSession, codexFingerprintFull:
		// 官方取证（0.154.0）：session-id == thread-id == x-client-request-id，
		// 由真实时间 UUIDv7 生成，会话内稳定、TTL 轮换（见 convergedCodexSessionIDs）。
		ids.sessionID, ids.threadID = convergedCodexSessionIDs(account.ID, time.Now())
		ids.turnID = uuid.Must(uuid.NewV7()).String()
		ids.windowID = ids.threadID + ":0"
		return ids
	}

	return nil
}

func extractClientSessionID(headers http.Header) string {
	if value := strings.TrimSpace(headers.Get("session-id")); value != "" {
		return value
	}
	return strings.TrimSpace(headers.Get("session_id"))
}

func resolveCodexFingerprintIDsFromRequest(account *Account, headers http.Header) *codexFingerprintIDs {
	mode := account.GetCodexFingerprintMode()
	if mode == codexFingerprintOff {
		return nil
	}
	return resolveCodexFingerprintIDs(account, extractClientSessionID(headers), mode)
}

func applyCodexFingerprintHeaders(headers http.Header, ids *codexFingerprintIDs) {
	if headers == nil || ids == nil {
		return
	}
	headers.Set("x-codex-installation-id", ids.installationID)
	fields := map[string]any{"installation_id": ids.installationID}
	if ids.mode != codexFingerprintDevice {
		headers.Set("x-codex-window-id", ids.windowID)
		headers.Set("x-client-request-id", ids.threadID)
		headers.Set("session-id", ids.sessionID)
		headers.Set("session_id", ids.sessionID)
		headers.Set("thread-id", ids.threadID)
		fields["session_id"] = ids.sessionID
		fields["thread_id"] = ids.threadID
		fields["turn_id"] = ids.turnID
		fields["window_id"] = ids.windowID
		fields["turn_started_at_unix_ms"] = time.Now().UnixMilli()
	}
	rewriteCodexTurnMetadataFields(headers, fields)
}

func rewriteCodexTurnMetadataFields(headers http.Header, fields map[string]any) {
	raw := strings.TrimSpace(headers.Get("x-codex-turn-metadata"))
	if raw == "" {
		return
	}
	var metadata map[string]any
	if json.Unmarshal([]byte(raw), &metadata) != nil {
		return
	}
	for key, value := range fields {
		metadata[key] = value
	}
	if rebuilt, err := json.Marshal(metadata); err == nil {
		headers.Set("x-codex-turn-metadata", string(rebuilt))
	}
}

func applyCodexFingerprintClientMetadata(body map[string]any, ids *codexFingerprintIDs) bool {
	if body == nil || ids == nil {
		return false
	}
	captureCodexFingerprintOriginalBodySessionID(ids, body["client_metadata"])
	metadata, _ := body["client_metadata"].(map[string]any)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	modified := false
	if applyCodexFingerprintToClientMetadataMap(metadata, ids) {
		body["client_metadata"] = metadata
		modified = true
	}
	if applyCodexFingerprintPromptCacheKey(body, ids) {
		modified = true
	}
	return modified
}

// captureCodexFingerprintOriginalBodySessionID 记录请求体 client_metadata 的原始
// session_id（只捕获一次），用于识别 root prompt_cache_key 是否取该默认值。
func captureCodexFingerprintOriginalBodySessionID(ids *codexFingerprintIDs, clientMetadata any) {
	if ids == nil || ids.originalBodySessionIDCaptured {
		return
	}
	ids.originalBodySessionIDCaptured = true
	if clientMetadata == nil {
		return
	}
	switch metadata := clientMetadata.(type) {
	case map[string]any:
		if sessionID, ok := metadata["session_id"].(string); ok {
			ids.originalBodySessionID = strings.TrimSpace(sessionID)
		}
	case map[string]string:
		ids.originalBodySessionID = strings.TrimSpace(metadata["session_id"])
	}
}

func captureCodexFingerprintOriginalBodySessionIDRaw(ids *codexFingerprintIDs, value gjson.Result) {
	if ids == nil || ids.originalBodySessionIDCaptured {
		return
	}
	ids.originalBodySessionIDCaptured = true
	if value.Exists() && value.Type == gjson.String {
		ids.originalBodySessionID = strings.TrimSpace(value.String())
	}
}

// shouldRewriteCodexFingerprintPromptCacheKey 判定 root prompt_cache_key 是否
// 需要随收敛 session_id 一起改写：仅 session/full 模式，且该键确实是客户端
// body 里的默认 session_id 值（不是用户显式指定的自定义缓存键）。
func shouldRewriteCodexFingerprintPromptCacheKey(ids *codexFingerprintIDs, promptCacheKey string) bool {
	if ids == nil || !ids.originalBodySessionIDCaptured || ids.originalBodySessionID == "" || ids.sessionID == "" {
		return false
	}
	if ids.mode != codexFingerprintSession && ids.mode != codexFingerprintFull {
		return false
	}
	return promptCacheKey == ids.originalBodySessionID
}

// applyCodexFingerprintPromptCacheKey 将 root prompt_cache_key 重写为收敛后的
// session_id。缓存键命中的是同一会话的请求，收敛会话后必须跟随新身份，
// 否则缓存空间被旧身份键占住且无法命中。
func applyCodexFingerprintPromptCacheKey(reqBody map[string]any, ids *codexFingerprintIDs) bool {
	if reqBody == nil {
		return false
	}
	promptCacheKey, ok := reqBody["prompt_cache_key"].(string)
	if !ok || strings.TrimSpace(promptCacheKey) == "" || !shouldRewriteCodexFingerprintPromptCacheKey(ids, promptCacheKey) {
		return false
	}
	if promptCacheKey == ids.sessionID {
		return false
	}
	reqBody["prompt_cache_key"] = ids.sessionID
	return true
}

func applyCodexFingerprintToClientMetadataMap(metadata map[string]any, ids *codexFingerprintIDs) bool {
	if metadata == nil || ids == nil {
		return false
	}
	metadata["x-codex-installation-id"] = ids.installationID
	fields := map[string]any{"installation_id": ids.installationID}
	if ids.mode != codexFingerprintDevice {
		metadata["session_id"] = ids.sessionID
		metadata["thread_id"] = ids.threadID
		metadata["turn_id"] = ids.turnID
		metadata["x-codex-window-id"] = ids.windowID
		fields["session_id"] = ids.sessionID
		fields["thread_id"] = ids.threadID
		fields["turn_id"] = ids.turnID
		fields["window_id"] = ids.windowID
		fields["turn_started_at_unix_ms"] = time.Now().UnixMilli()
	}
	rewriteClientMetadataTurnMetadata(metadata, fields)
	return true
}

// applyCodexFingerprintClientMetadataRaw changes only the small
// client_metadata object and splices it back into the original JSON. This
// keeps the passthrough hot path from fully decoding multi-megabyte requests.
func applyCodexFingerprintClientMetadataRaw(body []byte, ids *codexFingerprintIDs) ([]byte, bool, error) {
	if len(body) == 0 || ids == nil {
		return body, false, nil
	}
	// 非 JSON 对象的 body（数组/标量/畸形）没有 client_metadata 语义，
	// sjson 在这类根上写字段会改写整体结构，直接放行保持原样。
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		captureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.Result{})
		return body, false, nil
	}

	existing := make(map[string]any)
	if cm := gjson.GetBytes(body, "client_metadata"); cm.IsObject() {
		captureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.GetBytes(body, "client_metadata.session_id"))
		if err := json.Unmarshal([]byte(cm.Raw), &existing); err != nil {
			return body, false, fmt.Errorf("decode client_metadata for fingerprint: %w", err)
		}
	} else {
		captureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.Result{})
	}

	next := body
	modified := false
	if applyCodexFingerprintToClientMetadataMap(existing, ids) {
		raw, err := json.Marshal(existing)
		if err != nil {
			return body, false, fmt.Errorf("encode converged client_metadata: %w", err)
		}
		set, setErr := sjson.SetRawBytes(body, "client_metadata", raw)
		if setErr != nil {
			return body, false, fmt.Errorf("splice converged client_metadata: %w", setErr)
		}
		next = set
		modified = true
	}
	promptCacheKey := gjson.GetBytes(body, "prompt_cache_key")
	if promptCacheKey.Exists() && promptCacheKey.Type == gjson.String && strings.TrimSpace(promptCacheKey.String()) != "" && shouldRewriteCodexFingerprintPromptCacheKey(ids, promptCacheKey.String()) {
		rewritten, err := sjson.SetBytes(next, "prompt_cache_key", ids.sessionID)
		if err != nil {
			return body, false, fmt.Errorf("splice converged prompt_cache_key: %w", err)
		}
		next = rewritten
		modified = true
	}
	return next, modified, nil
}

func applyCodexFingerprintClientMetadataBytes(body []byte, ids *codexFingerprintIDs) ([]byte, bool, error) {
	if len(body) == 0 || ids == nil {
		return body, false, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, false, err
	}
	if !applyCodexFingerprintClientMetadata(decoded, ids) {
		return body, false, nil
	}
	rebuilt, err := json.Marshal(decoded)
	if err != nil {
		return nil, false, err
	}
	return rebuilt, true, nil
}

// copyCodexFingerprintHeaders only carries identity headers into the WS
// handshake. Session selection and continuation still use the original
// request/body values, so convergence cannot change connection affinity.
func copyCodexFingerprintHeaders(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	for _, key := range []string{
		"x-codex-installation-id",
		"x-codex-window-id",
		"x-client-request-id",
		"session-id",
		"thread-id",
	} {
		if value := strings.TrimSpace(src.Get(key)); value != "" {
			dst.Set(key, value)
		}
	}
}

func rewriteClientMetadataTurnMetadata(metadata map[string]any, fields map[string]any) {
	raw, _ := metadata["x-codex-turn-metadata"].(string)
	if raw == "" {
		return
	}
	var embedded map[string]any
	if json.Unmarshal([]byte(raw), &embedded) != nil {
		return
	}
	for key, value := range fields {
		embedded[key] = value
	}
	if rebuilt, err := json.Marshal(embedded); err == nil {
		metadata["x-codex-turn-metadata"] = string(rebuilt)
	}
}
