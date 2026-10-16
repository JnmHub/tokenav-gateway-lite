package gatewaylite

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

var ErrGatewayNodeInactive = errors.New("gateway_node_inactive")

type GatewayConfigChangedClient interface {
	ReportGatewayConfigChanged(ctx context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error)
}

type GatewayConfigPushClient interface {
	ReportGatewayConfigPush(ctx context.Context, req GatewayConfigPushRequest) (*GatewayConfigPushResponse, error)
}

type GatewayConfigPushSnapshot struct {
	Accounts          []GatewayAccountSnapshot
	Groups            []GatewayGroupSnapshot
	ModelPrices       []GatewayModelPrice
	EffectivePolicies []GatewayEffectivePolicy
	RuntimeSettings   *GatewayRuntimeSettings
}

type GatewayConfigPushSnapshotProvider interface {
	BuildGatewayConfigPushSnapshot(ctx context.Context, gatewayCode string) (GatewayConfigPushSnapshot, error)
}

type ConfigChangeNotifier struct {
	workers        workerLifetime
	stopped        bool
	client         GatewayConfigChangedClient
	pushClient     GatewayConfigPushClient
	pushProvider   GatewayConfigPushSnapshotProvider
	runtimeRef     *RuntimeConfigRef
	delay          time.Duration
	timeout        time.Duration
	retryBaseDelay time.Duration
	retryMaxDelay  time.Duration
	nextRetryDelay time.Duration

	mu      sync.Mutex
	timer   *time.Timer
	pending GatewayConfigChangedRequest

	lastEventID          string
	lastSuccessAtMillis  int64
	lastFailureAtMillis  int64
	lastFailureError     string
	retryPending         bool
	retryScheduledDelay  time.Duration
	lastSnapshotChecksum string
	forceFullSnapshot    bool
	periodicOnce         sync.Once
}

func NewConfigChangeNotifier(client GatewayConfigChangedClient, runtimeRef *RuntimeConfigRef, delay, timeout time.Duration) *ConfigChangeNotifier {
	if delay <= 0 {
		delay = 2 * time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &ConfigChangeNotifier{
		client:         client,
		runtimeRef:     runtimeRef,
		delay:          delay,
		timeout:        timeout,
		retryBaseDelay: delay,
		retryMaxDelay:  60 * time.Second,
		nextRetryDelay: delay,
	}
}

// WithConfigPush enables authoritative config-push（配置主动推送）. The legacy
// config-changed（配置变更通知）client remains configured as a fallback when a
// snapshot cannot be built or delivered.
func (n *ConfigChangeNotifier) WithConfigPush(client GatewayConfigPushClient, provider GatewayConfigPushSnapshotProvider) *ConfigChangeNotifier {
	if n == nil {
		return nil
	}
	n.pushClient = client
	n.pushProvider = provider
	return n
}

func (n *ConfigChangeNotifier) Notify(resource, action string, resourceID int64) {
	n.notify(resource, action, resourceID, false)
}

// NotifyFullSnapshot 强制下一次上报携带完整配置正文。它只用于主站已经发现
// mirror（镜像）差异的修复请求；普通周期保活仍可继续使用 refresh-only。
func (n *ConfigChangeNotifier) NotifyFullSnapshot(resource, action string, resourceID int64) {
	n.notify(resource, action, resourceID, true)
}

func (n *ConfigChangeNotifier) notify(resource, action string, resourceID int64, forceFullSnapshot bool) {
	if n == nil || (n.client == nil && n.pushClient == nil) {
		return
	}
	cfg := NormalizeRuntimeConfig(RuntimeConfig{})
	if n.runtimeRef != nil {
		cfg = n.runtimeRef.Current()
	}
	req := GatewayConfigChangedRequest{
		GatewayCode:     cfg.GatewayCode,
		Region:          cfg.Region,
		Resource:        strings.TrimSpace(resource),
		Action:          strings.TrimSpace(action),
		ResourceID:      resourceID,
		ChangedAtMillis: time.Now().UnixMilli(),
	}
	if req.Resource == "" {
		req.Resource = "unknown"
	}
	if req.Action == "" {
		req.Action = "update"
	}
	req.EventID = gatewayConfigChangedEventID(req)

	n.mu.Lock()
	if n.stopped { n.mu.Unlock(); return }
	n.pending = mergeGatewayConfigChanged(n.pending, req)
	n.forceFullSnapshot = n.forceFullSnapshot || forceFullSnapshot
	if n.timer == nil {
		n.timer = time.AfterFunc(n.delay, n.flush)
	} else {
		n.timer.Reset(n.delay)
	}
	n.mu.Unlock()
}

// StartPeriodicSnapshot refreshes the signed effective-policy mirror even
// when operators make no configuration changes. The control-plane expires a
// policy after ten minutes; capping refreshes at five minutes avoids turning a
// healthy, stable gateway invisible merely because its configuration is quiet.
func (n *ConfigChangeNotifier) StartPeriodicSnapshot(ctx context.Context, interval time.Duration) {
	if n == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 || interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	n.periodicOnce.Do(func() {
		n.workers.Go(ctx, func(ctx context.Context) {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					n.Notify("mixed", "periodic_snapshot", 0)
				}
			}
		})
	})
}

func (n *ConfigChangeNotifier) flush() {
	n.workers.Go(n.workers.Context(), func(context.Context) { n.flushPending() })
}

func (n *ConfigChangeNotifier) flushPending() {
	n.mu.Lock()
	req := n.pending
	forceFullSnapshot := n.forceFullSnapshot
	n.pending = GatewayConfigChangedRequest{}
	n.forceFullSnapshot = false
	n.timer = nil
	n.mu.Unlock()
	if req.ChangedAtMillis == 0 {
		return
	}

	if n.pushClient != nil && n.pushProvider != nil {
		if err := n.reportConfigPush(req, forceFullSnapshot); err == nil {
			n.markSuccess(req.EventID)
			return
		} else {
			failure := err.Error()
			log.Printf("gateway-lite: report config push failed event_id=%s resource=%s action=%s err=%v", req.EventID, req.Resource, req.Action, err)
			if errors.Is(err, ErrGatewayNodeInactive) {
				n.markTerminalFailure(req.EventID, failure)
				return
			}
			if fallbackErr := n.reportConfigChanged(req); fallbackErr != nil {
				failure += "; config-changed fallback failed: " + fallbackErr.Error()
				log.Printf("gateway-lite: report config changed fallback failed event_id=%s resource=%s action=%s err=%v", req.EventID, req.Resource, req.Action, fallbackErr)
				if errors.Is(fallbackErr, ErrGatewayNodeInactive) {
					n.markTerminalFailure(req.EventID, failure)
					return
				}
			}
			n.scheduleRetry(req, failure, forceFullSnapshot)
			return
		}
	}

	if err := n.reportConfigChanged(req); err != nil {
		log.Printf("gateway-lite: report config changed failed event_id=%s resource=%s action=%s err=%v", req.EventID, req.Resource, req.Action, err)
		if errors.Is(err, ErrGatewayNodeInactive) {
			n.markTerminalFailure(req.EventID, err.Error())
			return
		}
		n.scheduleRetry(req, err.Error(), forceFullSnapshot)
		return
	}
	n.markSuccess(req.EventID)
}

func (n *ConfigChangeNotifier) reportConfigPush(changed GatewayConfigChangedRequest, forceFullSnapshot bool) error {
	ctx, cancel := context.WithTimeout(n.workers.Context(), n.timeout)
	defer cancel()
	snapshot, err := n.pushProvider.BuildGatewayConfigPushSnapshot(ctx, changed.GatewayCode)
	if err != nil {
		return fmt.Errorf("build config push snapshot: %w", err)
	}
	snapshotChecksum, err := GatewayConfigPushSnapshotChecksum(snapshot)
	if err != nil {
		return fmt.Errorf("checksum config push snapshot: %w", err)
	}
	request := GatewayConfigPushRequest{
		EventID:               changed.EventID,
		GatewayCode:           changed.GatewayCode,
		Region:                changed.Region,
		Resource:              changed.Resource,
		Action:                changed.Action,
		ResourceID:            changed.ResourceID,
		ChangedAtMillis:       changed.ChangedAtMillis,
		Accounts:              snapshot.Accounts,
		AccountFull:           true,
		Groups:                snapshot.Groups,
		GroupsFull:            true,
		ModelPrices:           snapshot.ModelPrices,
		ModelPricesFull:       true,
		EffectivePolicies:     snapshot.EffectivePolicies,
		EffectivePoliciesFull: true,
		RuntimeSettings:       snapshot.RuntimeSettings,
		SnapshotChecksum:      snapshotChecksum,
	}
	refreshOnly := !forceFullSnapshot && n.snapshotChecksumConfirmed(snapshotChecksum)
	if refreshOnly {
		// 主站已经确认过完全相同的逻辑快照时，周期心跳只携带校验和。
		// 主站若清库或丢失状态，会在响应里要求本次立即补发完整快照。
		request.Accounts = nil
		request.AccountFull = false
		request.Groups = nil
		request.GroupsFull = false
		request.ModelPrices = nil
		request.ModelPricesFull = false
		request.EffectivePolicies = nil
		request.EffectivePoliciesFull = false
		request.RuntimeSettings = nil
		request.RefreshOnly = true
	}
	resp, err := n.pushClient.ReportGatewayConfigPush(ctx, request)
	if err != nil {
		return err
	}
	if err := configPushResponseError(resp); err != nil {
		return err
	}
	if refreshOnly && resp != nil && resp.FullSnapshotRequired {
		request.Accounts = snapshot.Accounts
		request.AccountFull = true
		request.Groups = snapshot.Groups
		request.GroupsFull = true
		request.ModelPrices = snapshot.ModelPrices
		request.ModelPricesFull = true
		request.EffectivePolicies = snapshot.EffectivePolicies
		request.EffectivePoliciesFull = true
		request.RuntimeSettings = snapshot.RuntimeSettings
		request.RefreshOnly = false
		resp, err = n.pushClient.ReportGatewayConfigPush(ctx, request)
		if err != nil {
			return err
		}
		if err := configPushResponseError(resp); err != nil {
			return err
		}
		refreshOnly = false
	}
	// 旧版主站会忽略新字段并返回 ok=true；只有新版明确确认保存了 checksum，
	// 才允许后续切换到 refresh-only。否则继续发送完整快照，兼容升级顺序。
	if refreshOnly || (resp != nil && resp.SnapshotChecksumAccepted) {
		n.rememberSnapshotChecksum(snapshotChecksum)
	}
	// 只有主站确认接收后才切换请求用量携带的审计版本。否则主站短暂
	// 不可达时，新用量会引用一份主站尚未保存、无法追溯的策略快照。
	if !refreshOnly {
		SetDefaultEffectivePolicyAudit(snapshot.EffectivePolicies)
	}
	return nil
}

func (n *ConfigChangeNotifier) snapshotChecksumConfirmed(checksum string) bool {
	if n == nil || strings.TrimSpace(checksum) == "" {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastSnapshotChecksum == checksum
}

func (n *ConfigChangeNotifier) rememberSnapshotChecksum(checksum string) {
	if n == nil || strings.TrimSpace(checksum) == "" {
		return
	}
	n.mu.Lock()
	n.lastSnapshotChecksum = checksum
	n.mu.Unlock()
}

func (n *ConfigChangeNotifier) reportConfigChanged(req GatewayConfigChangedRequest) error {
	if n.client == nil {
		return fmt.Errorf("config changed fallback client is not configured")
	}
	ctx, cancel := context.WithTimeout(n.workers.Context(), n.timeout)
	defer cancel()
	resp, err := n.client.ReportGatewayConfigChanged(ctx, req)
	if err != nil {
		return err
	}
	if resp != nil && !resp.OK {
		responseError := strings.TrimSpace(resp.Error)
		if isGatewayNodeInactiveResponse(responseError) {
			return fmt.Errorf("config changed report rejected: %w", ErrGatewayNodeInactive)
		}
		if responseError == "" {
			return fmt.Errorf("config changed report rejected")
		}
		return fmt.Errorf("config changed report rejected: %s", responseError)
	}
	return nil
}

func configPushResponseError(resp *GatewayConfigPushResponse) error {
	if resp == nil || resp.OK {
		return nil
	}
	responseError := strings.TrimSpace(resp.Error)
	if isGatewayNodeInactiveResponse(responseError) {
		return fmt.Errorf("config push rejected: %w", ErrGatewayNodeInactive)
	}
	if responseError == "" {
		return fmt.Errorf("config push rejected")
	}
	return fmt.Errorf("config push rejected: %s", responseError)
}

func isGatewayNodeInactiveResponse(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), ErrGatewayNodeInactive.Error())
}

func (n *ConfigChangeNotifier) markSuccess(eventID string) {
	n.mu.Lock()
	n.nextRetryDelay = n.retryBaseDelay
	n.retryPending = false
	n.retryScheduledDelay = 0
	n.lastEventID = eventID
	n.lastSuccessAtMillis = time.Now().UnixMilli()
	n.lastFailureError = ""
	n.mu.Unlock()
}

func (n *ConfigChangeNotifier) markTerminalFailure(eventID, failure string) {
	n.mu.Lock()
	// 节点已停用是主站的最终管理决定，不是临时故障。保留诊断信息，但不能
	// 回填 pending 或确认 checksum，否则归档节点会持续重试并伪装成已同步。
	n.nextRetryDelay = n.retryBaseDelay
	n.retryPending = false
	n.retryScheduledDelay = 0
	n.lastEventID = eventID
	n.lastFailureAtMillis = time.Now().UnixMilli()
	n.lastFailureError = strings.TrimSpace(failure)
	if n.lastFailureError == "" {
		n.lastFailureError = ErrGatewayNodeInactive.Error()
	}
	n.mu.Unlock()
}

func (n *ConfigChangeNotifier) scheduleRetry(req GatewayConfigChangedRequest, failure string, forceFullSnapshot bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stopped { return }
	n.pending = mergeGatewayConfigChanged(req, n.pending)
	n.forceFullSnapshot = n.forceFullSnapshot || forceFullSnapshot
	n.retryPending = true
	n.retryScheduledDelay = 0
	n.lastEventID = req.EventID
	n.lastFailureAtMillis = time.Now().UnixMilli()
	n.lastFailureError = strings.TrimSpace(failure)
	if n.lastFailureError == "" {
		n.lastFailureError = "config changed report rejected"
	}
	delay := n.nextRetryDelay
	if delay <= 0 {
		delay = n.retryBaseDelay
	}
	if delay <= 0 {
		delay = n.delay
	}
	if delay <= 0 {
		delay = 2 * time.Second
	}
	n.retryScheduledDelay = delay
	maxDelay := n.retryMaxDelay
	if maxDelay <= 0 {
		maxDelay = 60 * time.Second
	}
	nextDelay := delay * 2
	if nextDelay > maxDelay {
		nextDelay = maxDelay
	}
	n.nextRetryDelay = nextDelay
	if n.timer == nil {
		n.timer = time.AfterFunc(delay, n.flush)
	} else {
		n.timer.Reset(delay)
	}
}

func (n *ConfigChangeNotifier) RuntimeMetrics(ctx context.Context) map[string]any {
	if n == nil || (ctx != nil && ctx.Err() != nil) {
		return nil
	}
	// 健康指标是可选快照，不能排在配置变更热路径的锁后面等待。
	// 锁正忙时本轮省略即可，下个心跳会重新采集。
	if !n.mu.TryLock() {
		return nil
	}
	defer n.mu.Unlock()
	out := map[string]any{
		"config_changed_pending_retry": n.retryPending,
	}
	if n.lastEventID != "" {
		out["config_changed_last_event_id"] = n.lastEventID
	}
	if n.lastSuccessAtMillis > 0 {
		out["config_changed_last_success_at_millis"] = n.lastSuccessAtMillis
	}
	if n.lastFailureAtMillis > 0 {
		out["config_changed_last_failure_at_millis"] = n.lastFailureAtMillis
	}
	if n.lastFailureError != "" {
		out["config_changed_last_failure_error"] = n.lastFailureError
	}
	if n.retryPending && n.retryScheduledDelay > 0 {
		out["config_changed_next_retry_delay_seconds"] = int64(n.retryScheduledDelay.Seconds())
	}
	return out
}

func mergeGatewayConfigChanged(current, next GatewayConfigChangedRequest) GatewayConfigChangedRequest {
	if current.ChangedAtMillis == 0 {
		if next.EventID == "" {
			next.EventID = gatewayConfigChangedEventID(next)
		}
		return next
	}
	if next.ChangedAtMillis == 0 {
		if current.EventID == "" {
			current.EventID = gatewayConfigChangedEventID(current)
		}
		return current
	}
	if !strings.EqualFold(strings.TrimSpace(current.Resource), strings.TrimSpace(next.Resource)) {
		current.Resource = "mixed"
	} else {
		current.Resource = strings.TrimSpace(next.Resource)
	}
	if !strings.EqualFold(strings.TrimSpace(current.Action), strings.TrimSpace(next.Action)) {
		current.Action = "bulk_update"
	} else {
		current.Action = strings.TrimSpace(next.Action)
	}
	if current.ResourceID != next.ResourceID {
		current.ResourceID = 0
	}
	current.GatewayCode = next.GatewayCode
	current.Region = next.Region
	current.ChangedAtMillis = next.ChangedAtMillis
	current.EventID = gatewayConfigChangedEventID(current)
	return current
}

func gatewayConfigChangedEventID(req GatewayConfigChangedRequest) string {
	gatewayCode := strings.TrimSpace(req.GatewayCode)
	resource := strings.TrimSpace(req.Resource)
	action := strings.TrimSpace(req.Action)
	if resource == "" {
		resource = "unknown"
	}
	if action == "" {
		action = "update"
	}
	return fmt.Sprintf("%s:%s:%s:%d:%d", gatewayCode, resource, action, req.ResourceID, req.ChangedAtMillis)
}
