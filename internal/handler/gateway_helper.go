package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// claudeCodeValidator is a singleton validator for Claude Code client detection
var claudeCodeValidator = service.NewClaudeCodeValidator()

// SetClaudeCodeClientContext 检查请求是否来自 Claude Code 客户端，并设置到 context 中
// 返回更新后的 context
func SetClaudeCodeClientContext(c *gin.Context, body []byte, parsedReq *service.ParsedRequest) {
	if c == nil || c.Request == nil {
		return
	}
	ua := c.GetHeader("User-Agent")
	// Fast path：非 Claude CLI UA 直接判定 false，避免热路径二次 JSON 反序列化。
	if !claudeCodeValidator.ValidateUserAgent(ua) {
		ctx := service.SetClaudeCodeClient(c.Request.Context(), false)
		c.Request = c.Request.WithContext(ctx)
		return
	}

	isClaudeCode := false
	if !strings.Contains(c.Request.URL.Path, "messages") {
		// 与 Validate 行为一致：非 messages 路径 UA 命中即可视为 Claude Code 客户端。
		isClaudeCode = true
	} else {
		// 仅在确认为 Claude CLI 且 messages 路径时再做 body 解析。
		bodyMap := claudeCodeBodyMapFromParsedRequest(parsedReq)
		if bodyMap == nil && len(body) > 0 {
			_ = json.Unmarshal(body, &bodyMap)
		}
		isClaudeCode = claudeCodeValidator.Validate(c.Request, bodyMap)
	}

	// 更新 request context
	ctx := service.SetClaudeCodeClient(c.Request.Context(), isClaudeCode)

	// 仅在确认为 Claude Code 客户端时提取版本号写入 context
	if isClaudeCode {
		if version := claudeCodeValidator.ExtractVersion(ua); version != "" {
			ctx = service.SetClaudeCodeVersion(ctx, version)
		}
	}

	c.Request = c.Request.WithContext(ctx)
}

func claudeCodeBodyMapFromParsedRequest(parsedReq *service.ParsedRequest) map[string]any {
	if parsedReq == nil {
		return nil
	}
	bodyMap := map[string]any{
		"model": parsedReq.Model,
	}
	if parsedReq.HasSystem {
		if system, ok := parsedReq.SystemValue(); ok {
			bodyMap["system"] = system
		} else {
			bodyMap["system"] = nil
		}
	}
	if parsedReq.MetadataUserID != "" {
		bodyMap["metadata"] = map[string]any{"user_id": parsedReq.MetadataUserID}
	}
	return bodyMap
}

// 并发槽位等待相关常量
//
// 性能优化说明：
// 原实现使用固定间隔（100ms）轮询并发槽位，存在以下问题：
// 1. 高并发时频繁轮询增加 Redis 压力
// 2. 固定间隔可能导致多个请求同时重试（惊群效应）
//
// 新实现使用指数退避 + 抖动算法：
// 1. 初始退避 100ms，每次乘以 1.5，最大 2s
// 2. 添加 ±20% 的随机抖动，分散重试时间点
// 3. 减少 Redis 压力，避免惊群效应
const (
	// maxConcurrencyWait 等待并发槽位的最大时间
	maxConcurrencyWait = 30 * time.Second
	// defaultPingInterval 流式响应等待时发送 ping 的默认间隔
	defaultPingInterval = 10 * time.Second
	// initialBackoff 初始退避时间
	initialBackoff = 100 * time.Millisecond
	// backoffMultiplier 退避时间乘数（指数退避）
	backoffMultiplier = 1.5
	// maxBackoff 最大退避时间
	maxBackoff = 2 * time.Second
	// defaultUserSlotMaxWaiting 保护用户套餐并发槽位的等待队列，避免同一用户无限堆积等待请求。
	defaultUserSlotMaxWaiting = 20
)

// SSEPingFormat defines the format of SSE ping events for different platforms
type SSEPingFormat string

const (
	// SSEPingFormatClaude is the Claude/Anthropic SSE ping format
	SSEPingFormatClaude SSEPingFormat = "data: {\"type\": \"ping\"}\n\n"
	// SSEPingFormatNone indicates no ping should be sent (e.g., OpenAI has no ping spec)
	SSEPingFormatNone SSEPingFormat = ""
	// SSEPingFormatComment is an SSE comment ping for OpenAI/Codex CLI clients
	SSEPingFormatComment SSEPingFormat = ":\n\n"
)

// ConcurrencyError represents a concurrency limit error with context
type ConcurrencyError struct {
	SlotType    string
	IsTimeout   bool
	IsQueueFull bool
}

func (e *ConcurrencyError) Error() string {
	if e.IsQueueFull {
		return fmt.Sprintf("too many pending requests for %s", e.SlotType)
	}
	if e.IsTimeout {
		return fmt.Sprintf("timeout waiting for %s concurrency slot", e.SlotType)
	}
	return fmt.Sprintf("%s concurrency limit reached", e.SlotType)
}

// ConcurrencyHelper provides common concurrency slot management for gateway handlers
type ConcurrencyHelper struct {
	concurrencyService *service.ConcurrencyService
	pingFormat         SSEPingFormat
	pingInterval       time.Duration
}

// NewConcurrencyHelper creates a new ConcurrencyHelper
func NewConcurrencyHelper(concurrencyService *service.ConcurrencyService, pingFormat SSEPingFormat, pingInterval time.Duration) *ConcurrencyHelper {
	if pingInterval <= 0 {
		pingInterval = defaultPingInterval
	}
	return &ConcurrencyHelper{
		concurrencyService: concurrencyService,
		pingFormat:         pingFormat,
		pingInterval:       pingInterval,
	}
}

// wrapReleaseOnDone ensures release runs at most once and still triggers on context cancellation.
// 用于避免客户端断开或上游超时导致的并发槽位泄漏。
// 优化：基于 context.AfterFunc 注册回调，避免每请求额外守护 goroutine。
func wrapReleaseOnDone(ctx context.Context, releaseFunc func()) func() {
	if releaseFunc == nil {
		return nil
	}
	var once sync.Once
	var stop func() bool

	release := func() {
		once.Do(func() {
			if stop != nil {
				_ = stop()
			}
			releaseFunc()
		})
	}

	stop = context.AfterFunc(ctx, release)

	return release
}

// IncrementWaitCount increments the wait count for a user
func (h *ConcurrencyHelper) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	return h.concurrencyService.IncrementWaitCount(ctx, userID, maxWait)
}

// DecrementWaitCount decrements the wait count for a user
func (h *ConcurrencyHelper) DecrementWaitCount(ctx context.Context, userID int64) {
	h.concurrencyService.DecrementWaitCount(ctx, userID)
}

// IncrementAccountWaitCount increments the wait count for an account
func (h *ConcurrencyHelper) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	return h.concurrencyService.IncrementAccountWaitCount(ctx, accountID, maxWait)
}

// DecrementAccountWaitCount decrements the wait count for an account
func (h *ConcurrencyHelper) DecrementAccountWaitCount(ctx context.Context, accountID int64) {
	h.concurrencyService.DecrementAccountWaitCount(ctx, accountID)
}

// TryAcquireUserSlot 尝试立即获取用户并发槽位。
// 返回值: (releaseFunc, acquired, error)
func (h *ConcurrencyHelper) TryAcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int) (func(), bool, error) {
	result, err := h.concurrencyService.AcquireUserSlot(ctx, userID, maxConcurrency)
	if err != nil {
		return nil, false, err
	}
	if !result.Acquired {
		return nil, false, nil
	}
	return result.ReleaseFunc, true, nil
}

// TryAcquireUserSlotForAPIKey 尝试获取用户并发槽位，并附带 API Key（接口密钥）活跃请求统计。
// API Key 统计是 best-effort（尽力而为），不会改变用户并发限流结果。
func (h *ConcurrencyHelper) TryAcquireUserSlotForAPIKey(ctx context.Context, userID int64, maxConcurrency int, apiKeyID int64) (func(), bool, error) {
	result, err := h.concurrencyService.AcquireUserSlotForAPIKey(ctx, userID, maxConcurrency, apiKeyID)
	if err != nil {
		return nil, false, err
	}
	if result == nil || !result.Acquired {
		return nil, false, nil
	}
	return result.ReleaseFunc, true, nil
}

// TryAcquireUserGroupSlotForAPIKey 尝试一次性获取用户等级、最终分组和 API Key
// 三个维度的槽位。WebSocket/Live 不能使用只检查用户等级的旧入口，否则可以
// 绕过分组的每用户并发硬限制。
func (h *ConcurrencyHelper) TryAcquireUserGroupSlotForAPIKey(
	ctx context.Context,
	userID int64,
	userMaxConcurrency int,
	groupID int64,
	groupMaxConcurrency int,
	apiKeyID int64,
) (func(), bool, error) {
	result, err := h.concurrencyService.AcquireUserGroupSlotForAPIKey(
		ctx,
		userID,
		userMaxConcurrency,
		groupID,
		groupMaxConcurrency,
		apiKeyID,
	)
	if err != nil {
		return nil, false, err
	}
	if result == nil || !result.Acquired {
		return nil, false, nil
	}
	return result.ReleaseFunc, true, nil
}

func (h *ConcurrencyHelper) tryAcquireFinalUserSlotForAPIKey(
	ctx context.Context,
	user *service.User,
	userID int64,
	userMaxConcurrency int,
	groupID int64,
	groupMaxConcurrency int,
	apiKeyID int64,
	apiKeyMaxConcurrency int,
) (*service.AcquireResult, error) {
	if user != nil && user.OrganizationID > 0 {
		return h.concurrencyService.AcquireOrganizationMemberGroupSlotForAPIKey(
			ctx,
			user.OrganizationID,
			user.OrganizationConcurrency,
			userID,
			userMaxConcurrency,
			groupID,
			groupMaxConcurrency,
			apiKeyID,
			apiKeyMaxConcurrency,
		)
	}
	return h.concurrencyService.AcquireUserGroupSlotForAPIKey(
		ctx,
		userID,
		userMaxConcurrency,
		groupID,
		groupMaxConcurrency,
		apiKeyID,
	)
}

func finalConcurrencyUser(c *gin.Context) *service.User {
	if apiKey, ok := middleware2.GetAPIKeyFromContext(c); ok && apiKey != nil {
		return apiKey.User
	}
	return nil
}

// AcquireOpenAIWSIngressLease 限制同一 API Key 跨实例存活的客户端 WebSocket 连接数。
// 该租约覆盖整条连接生命周期，不与每一轮请求临时占用的并发槽位混用。
func (h *ConcurrencyHelper) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int) (*service.OpenAIWSIngressLease, bool, error) {
	if h == nil || h.concurrencyService == nil {
		if maxConnections <= 0 {
			return nil, true, nil
		}
		return nil, false, errors.New("concurrency service is unavailable")
	}
	return h.concurrencyService.AcquireOpenAIWSIngressLease(ctx, apiKeyID, maxConnections)
}

// TryAcquireAccountSlot 尝试立即获取账号并发槽位。
// 返回值: (releaseFunc, acquired, error)
func (h *ConcurrencyHelper) TryAcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int) (func(), bool, error) {
	result, err := h.concurrencyService.AcquireAccountSlot(ctx, accountID, maxConcurrency)
	if err != nil {
		return nil, false, err
	}
	if !result.Acquired {
		return nil, false, nil
	}
	return result.ReleaseFunc, true, nil
}

// TryAcquireFailoverGroupSlot 仅占用目标 B 分组的每用户并发槽位；入口 A 的
// 用户、企业和 API Key 槽位继续由外层请求持有，避免重复占槽。
func (h *ConcurrencyHelper) TryAcquireFailoverGroupSlot(ctx context.Context, user *service.User, userID int64, group *service.Group) (func(), bool, error) {
	if h == nil || h.concurrencyService == nil || group == nil {
		return nil, false, errors.New("failover group concurrency service is unavailable")
	}
	result, err := h.concurrencyService.AcquireFailoverGroupSlot(ctx, user, userID, group.ID, group.PerUserConcurrency)
	if err != nil {
		return nil, false, err
	}
	if result == nil || !result.Acquired {
		return nil, false, nil
	}
	return result.ReleaseFunc, true, nil
}

// AcquireUserSlotWithWait acquires a user concurrency slot, waiting if necessary.
// For streaming requests, sends ping events during the wait.
// streamStarted is updated if streaming response has begun.
func (h *ConcurrencyHelper) AcquireUserSlotWithWait(c *gin.Context, userID int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	groupID, groupMaxConcurrency := finalGroupConcurrencyLimit(c)
	release, err := h.acquireUserGroupSlotWithWaitTimeout(c, finalConcurrencyUser(c), userID, maxConcurrency, groupID, groupMaxConcurrency, maxConcurrencyWait, isStream, streamStarted)
	if err != nil {
		return nil, err
	}
	return h.withGroupIngressSlotFromContext(c, release), nil
}

// withGroupIngressSlotFromContext adds a stats-only slot for the request's
// original entry group. The slot deliberately remains attached to that group
// even if a later failover changes the effective group. Missing metadata and
// Redis errors are no-ops so observability cannot affect request admission.
func (h *ConcurrencyHelper) withGroupIngressSlotFromContext(c *gin.Context, releaseFunc func()) func() {
	ingressRelease := h.trackGroupIngressSlotFromContext(c)
	return func() {
		if releaseFunc != nil {
			releaseFunc()
		}
		if ingressRelease != nil {
			ingressRelease()
		}
	}
}

// trackGroupIngressSlotFromContext starts a stats-only lease for the request's
// original group. It is separate from user-slot acquisition so direct paths
// such as Live/WebSocket can account for their own turn lifecycle.
func (h *ConcurrencyHelper) trackGroupIngressSlotFromContext(c *gin.Context) func() {
	if h == nil || h.concurrencyService == nil || c == nil || c.Request == nil {
		return nil
	}
	groupID, ok := gatewaylite.IngressGroupIDFromContext(c.Request.Context())
	if !ok {
		// Keep compatibility with contexts created before the dedicated ingress
		// marker existed. New RequestMeta contexts mirror this value automatically.
		meta, hasMeta := gatewaylite.RequestMetaFromContext(c.Request.Context())
		if !hasMeta || meta.OriginalGroupID <= 0 {
			return nil
		}
		groupID = meta.OriginalGroupID
	}
	return h.concurrencyService.TrackGroupIngressSlot(c.Request.Context(), groupID)
}

func finalGroupConcurrencyLimit(c *gin.Context) (int64, int) {
	if c == nil || c.Request == nil {
		return 0, 0
	}
	if group, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group); ok && group != nil {
		return group.ID, group.PerUserConcurrency
	}
	if apiKey, ok := middleware2.GetAPIKeyFromContext(c); ok && apiKey != nil && apiKey.Group != nil {
		return apiKey.Group.ID, apiKey.Group.PerUserConcurrency
	}
	return 0, 0
}

func finalAPIKeyConcurrencyID(c *gin.Context) int64 {
	if c == nil || c.Request == nil {
		return 0
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		return 0
	}
	// 主站快照 Key 在轻量网关没有本地数据库 ID。复用稳定的非秘密 KeyID
	// 命名空间，既可原子释放三维槽位，也不会把原始 API Key 写入 Redis。
	return apiKey.WSIngressConcurrencyID(c.Request.Context())
}

func finalAPIKeyConcurrencyLimit(c *gin.Context) int {
	if c == nil {
		return 0
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		return 0
	}
	return apiKey.KeyConcurrencyLimit
}

func (h *ConcurrencyHelper) acquireUserGroupSlotWithWaitTimeout(c *gin.Context, user *service.User, userID int64, userMaxConcurrency int, groupID int64, groupMaxConcurrency int, timeout time.Duration, isStream bool, streamStarted *bool) (func(), error) {
	if (user == nil || user.OrganizationID <= 0) && (groupID <= 0 || groupMaxConcurrency <= 0) {
		return h.acquireUserSlotWithWaitTimeout(c, userID, userMaxConcurrency, timeout, isStream, streamStarted)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	apiKeyID := finalAPIKeyConcurrencyID(c)
	apiKeyMaxConcurrency := finalAPIKeyConcurrencyLimit(c)
	acquire := func() (*service.AcquireResult, error) {
		return h.tryAcquireFinalUserSlotForAPIKey(ctx, user, userID, userMaxConcurrency, groupID, groupMaxConcurrency, apiKeyID, apiKeyMaxConcurrency)
	}
	var pingCh <-chan time.Time
	var pingTicker *time.Ticker
	var flusher http.Flusher
	if isStream && h.pingFormat != "" {
		var ok bool
		flusher, ok = c.Writer.(http.Flusher)
		if !ok {
			return nil, fmt.Errorf("streaming not supported")
		}
		pingTicker = time.NewTicker(h.pingInterval)
		defer pingTicker.Stop()
		pingCh = pingTicker.C
	}
	for backoff := time.Duration(0); ; {
		if backoff > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, &ConcurrencyError{SlotType: "group_user", IsTimeout: true}
			case <-timer.C:
			case <-pingCh:
				timer.Stop()
				if streamStarted != nil && !*streamStarted {
					c.Header("Content-Type", "text/event-stream")
					c.Header("Cache-Control", "no-cache")
					c.Header("X-Accel-Buffering", "no")
					*streamStarted = true
				}
				if _, err := fmt.Fprint(c.Writer, string(h.pingFormat)); err != nil {
					return nil, err
				}
				flusher.Flush()
				continue
			}
		}
		result, err := acquire()
		if err != nil {
			return nil, err
		}
		if result != nil && result.Acquired {
			return result.ReleaseFunc, nil
		}
		if backoff == 0 {
			backoff = initialBackoff
		} else {
			backoff = nextBackoff(backoff)
		}
	}
}

func (h *ConcurrencyHelper) acquireUserSlotWithWaitTimeout(c *gin.Context, userID int64, maxConcurrency int, timeout time.Duration, isStream bool, streamStarted *bool) (func(), error) {
	ctx := c.Request.Context()

	// Try to acquire immediately
	apiKeyID := finalAPIKeyConcurrencyID(c)
	releaseFunc, acquired, err := h.TryAcquireUserSlotForAPIKey(ctx, userID, maxConcurrency, apiKeyID)
	if err != nil {
		return nil, err
	}

	if acquired {
		return releaseFunc, nil
	}

	releaseFunc, err = h.waitForSlotWithPingTimeout(c, "user", userID, maxConcurrency, timeout, defaultUserSlotMaxWaiting, isStream, streamStarted, false)
	if err != nil {
		return nil, err
	}
	return h.withAPIKeySlotFromGin(c, releaseFunc), nil
}

func (h *ConcurrencyHelper) withAPIKeySlotFromGin(c *gin.Context, releaseFunc func()) func() {
	if c == nil {
		return releaseFunc
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		return releaseFunc
	}
	return h.withAPIKeySlot(c.Request.Context(), apiKey.WSIngressConcurrencyID(c.Request.Context()), releaseFunc)
}

func (h *ConcurrencyHelper) withAPIKeySlot(ctx context.Context, apiKeyID int64, releaseFunc func()) func() {
	if h == nil || h.concurrencyService == nil || apiKeyID == 0 {
		return releaseFunc
	}
	apiKeyReleaseFunc := h.concurrencyService.TrackAPIKeySlot(ctx, apiKeyID)
	return func() {
		if releaseFunc != nil {
			releaseFunc()
		}
		if apiKeyReleaseFunc != nil {
			apiKeyReleaseFunc()
		}
	}
}

// AcquireAccountSlotWithWait acquires an account concurrency slot, waiting if necessary.
// For streaming requests, sends ping events during the wait.
// streamStarted is updated if streaming response has begun.
func (h *ConcurrencyHelper) AcquireAccountSlotWithWait(c *gin.Context, accountID int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	ctx := c.Request.Context()

	// Try to acquire immediately
	releaseFunc, acquired, err := h.TryAcquireAccountSlot(ctx, accountID, maxConcurrency)
	if err != nil {
		return nil, err
	}

	if acquired {
		return releaseFunc, nil
	}

	// Need to wait - handle streaming ping if needed
	return h.waitForSlotWithPing(c, "account", accountID, maxConcurrency, isStream, streamStarted)
}

// waitForSlotWithPing waits for a concurrency slot, sending ping events for streaming requests.
// streamStarted pointer is updated when streaming begins (for proper error handling by caller).
func (h *ConcurrencyHelper) waitForSlotWithPing(c *gin.Context, slotType string, id int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	return h.waitForSlotWithPingTimeout(c, slotType, id, maxConcurrency, maxConcurrencyWait, 0, isStream, streamStarted, false)
}

// waitForSlotWithPingTimeout waits for a concurrency slot with a custom timeout.
func (h *ConcurrencyHelper) waitForSlotWithPingTimeout(c *gin.Context, slotType string, id int64, maxConcurrency int, timeout time.Duration, maxWaiting int, isStream bool, streamStarted *bool, tryImmediate bool) (func(), error) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	acquireSlot := func() (*service.AcquireResult, error) {
		if slotType == "user" {
			return h.concurrencyService.AcquireUserSlot(ctx, id, maxConcurrency)
		}
		return h.concurrencyService.AcquireAccountSlot(ctx, id, maxConcurrency)
	}

	if tryImmediate {
		result, err := acquireSlot()
		if err != nil {
			return nil, err
		}
		if result.Acquired {
			return result.ReleaseFunc, nil
		}
	}

	if maxWaiting > 0 {
		var allowed bool
		var err error
		if slotType == "user" {
			allowed, err = h.IncrementWaitCount(ctx, id, maxWaiting)
		} else {
			allowed, err = h.IncrementAccountWaitCount(ctx, id, maxWaiting)
		}
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, &ConcurrencyError{SlotType: slotType, IsQueueFull: true}
		}
		defer func() {
			if slotType == "user" {
				h.DecrementWaitCount(ctx, id)
				return
			}
			h.DecrementAccountWaitCount(ctx, id)
		}()
	}

	// Determine if ping is needed (streaming + ping format defined)
	needPing := isStream && h.pingFormat != ""

	var flusher http.Flusher
	if needPing {
		var ok bool
		flusher, ok = c.Writer.(http.Flusher)
		if !ok {
			return nil, fmt.Errorf("streaming not supported")
		}
	}

	// Only create ping ticker if ping is needed
	var pingCh <-chan time.Time
	if needPing {
		pingTicker := time.NewTicker(h.pingInterval)
		defer pingTicker.Stop()
		pingCh = pingTicker.C
	}

	// 释放即通知：本进程里账号/用户槽被真正释放时（service 包的槽位释放路径会广播，
	// 见 service/slot_release_notify.go），等待者立刻重试一次，不必睡满退避周期 ——
	// 否则刚腾出来的容量会被空置到某个等待者的退避到期，把排队延迟放大到秒级。
	// 信号丢失、或槽位是在另一个网关实例释放的，只是退回下面的轮询退避，不影响正确性。
	slotReleaseCh := service.SlotReleaseChannel(slotType, id)

	backoff := initialBackoff
	timer := time.NewTimer(backoff)
	defer timer.Stop()

	tryAcquire := func() (func(), bool, error) {
		result, err := acquireSlot()
		if err != nil {
			return nil, false, err
		}
		if result.Acquired {
			return result.ReleaseFunc, true, nil
		}
		return nil, false, nil
	}

	for {
		select {
		case <-ctx.Done():
			if parentErr := c.Request.Context().Err(); parentErr != nil {
				return nil, parentErr
			}
			return nil, &ConcurrencyError{
				SlotType:  slotType,
				IsTimeout: true,
			}

		case <-pingCh:
			// Send ping to keep connection alive
			if !*streamStarted {
				c.Header("Content-Type", "text/event-stream")
				c.Header("Cache-Control", "no-cache")
				c.Header("Connection", "keep-alive")
				c.Header("X-Accel-Buffering", "no")
				*streamStarted = true
			}
			if _, err := fmt.Fprint(c.Writer, string(h.pingFormat)); err != nil {
				return nil, err
			}
			flusher.Flush()

		case <-slotReleaseCh:
			// 重新取「当前代」channel（通知会关闭并替换当前代），退避重置回最小值后立即重试。
			slotReleaseCh = service.SlotReleaseChannel(slotType, id)
			backoff = initialBackoff
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(backoff)
			release, acquired, err := tryAcquire()
			if err != nil {
				return nil, err
			}
			if acquired {
				return release, nil
			}

		case <-timer.C:
			// Try to acquire slot
			release, acquired, err := tryAcquire()
			if err != nil {
				return nil, err
			}
			if acquired {
				return release, nil
			}
			backoff = nextBackoff(backoff)
			timer.Reset(backoff)
		}
	}
}

// AcquireAccountSlotWithWaitTimeout acquires an account slot with a custom timeout (keeps SSE ping).
func (h *ConcurrencyHelper) AcquireAccountSlotWithWaitTimeout(c *gin.Context, accountID int64, maxConcurrency int, timeout time.Duration, maxWaiting int, isStream bool, streamStarted *bool) (func(), error) {
	return h.waitForSlotWithPingTimeout(c, "account", accountID, maxConcurrency, timeout, maxWaiting, isStream, streamStarted, true)
}

// nextBackoff 计算下一次退避时间
// 性能优化：使用指数退避 + 随机抖动，避免惊群效应
// current: 当前退避时间
// 返回值：下一次退避时间（100ms ~ 2s 之间）
func nextBackoff(current time.Duration) time.Duration {
	// 指数退避：当前时间 * 1.5
	next := time.Duration(float64(current) * backoffMultiplier)
	if next > maxBackoff {
		next = maxBackoff
	}
	// 添加 ±20% 的随机抖动（jitter 范围 0.8 ~ 1.2）
	// 抖动可以分散多个请求的重试时间点，避免同时冲击 Redis
	jitter := 0.8 + rand.Float64()*0.4
	jittered := time.Duration(float64(next) * jitter)
	if jittered < initialBackoff {
		return initialBackoff
	}
	if jittered > maxBackoff {
		return maxBackoff
	}
	return jittered
}
