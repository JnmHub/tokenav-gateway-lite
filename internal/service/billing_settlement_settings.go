package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	BillingSettlementModeLegacy       = "legacy"
	BillingSettlementModeIdempotentV2 = "idempotent_v2"
)

// BillingSettlementSettings 是轻量网关结算方式的独立配置。
// Mode 和 LegacyWarning 都由开关派生，避免客户端提交互相矛盾的状态；
// 警告文案由前端本地化，接口只返回是否需要展示旧逻辑风险提示。
type BillingSettlementSettings struct {
	IdempotentEnabled bool   `json:"idempotent_enabled"`
	Mode              string `json:"mode"`
	LegacyWarning     bool   `json:"legacy_warning"`
}

func newBillingSettlementSettings(enabled bool) *BillingSettlementSettings {
	settings := &BillingSettlementSettings{
		IdempotentEnabled: enabled,
		Mode:              BillingSettlementModeIdempotentV2,
	}
	if !enabled {
		settings.Mode = BillingSettlementModeLegacy
		settings.LegacyWarning = true
	}
	return settings
}

// GetBillingSettlementSettings 读取持久化配置。缺省值严格为关闭，保持旧结算逻辑。
func (s *SettingService) GetBillingSettlementSettings(ctx context.Context) (*BillingSettlementSettings, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	raw, err := s.settingRepo.GetValue(ctx, SettingKeyGatewayLiteIdempotentSettlementEnabled)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, fmt.Errorf("get billing settlement settings: %w", err)
	}
	enabled := err == nil && strings.EqualFold(strings.TrimSpace(raw), "true")
	// A successful read is also a runtime reconciliation point. The admin page
	// must never display persisted=true while requests keep using a stale false
	// snapshot after a transient startup database failure.
	s.gatewayLiteIdempotentSettlementEnabled.Store(enabled)
	return newBillingSettlementSettings(enabled), nil
}

// SetBillingSettlementSettings 持久化配置，并仅在写入成功后发布新的运行时快照。
func (s *SettingService) SetBillingSettlementSettings(ctx context.Context, settings *BillingSettlementSettings) error {
	if s == nil || s.settingRepo == nil {
		return errors.New("setting repository is unavailable")
	}
	if settings == nil {
		return errors.New("billing settlement settings are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := s.settingRepo.Set(
		ctx,
		SettingKeyGatewayLiteIdempotentSettlementEnabled,
		strconv.FormatBool(settings.IdempotentEnabled),
	); err != nil {
		return fmt.Errorf("set billing settlement settings: %w", err)
	}
	s.gatewayLiteIdempotentSettlementEnabled.Store(settings.IdempotentEnabled)
	return nil
}

// WarmBillingSettlementSettings 在启动时把数据库值加载到无锁运行时快照。
// 读取失败时保留最后一次已确认状态；进程刚启动时 atomic 零值本来就是
// legacy，而运行中短暂抖动不能把已开启的金额规则静默切回旧版。
func (s *SettingService) WarmBillingSettlementSettings(ctx context.Context) error {
	if s == nil {
		return errors.New("setting service is unavailable")
	}
	settings, err := s.GetBillingSettlementSettings(ctx)
	if err != nil {
		return err
	}
	s.gatewayLiteIdempotentSettlementEnabled.Store(settings.IdempotentEnabled)
	return nil
}

// WarmBillingSettlementSettingsUntilReady retries only the failed startup
// reconciliation. Once a database value is loaded, normal PUT operations and
// admin GET reconciliation keep the in-memory snapshot current.
func (s *SettingService) WarmBillingSettlementSettingsUntilReady(ctx context.Context, interval time.Duration) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := s.WarmBillingSettlementSettings(readCtx)
			cancel()
			if err == nil {
				return
			}
			timer.Reset(interval)
		}
	}
}

// GatewayLiteIdempotentSettlementEnabled 返回请求进入轻量网关时应拍下的配置快照。
// 该方法只读 atomic（原子内存值），不会访问数据库或 Redis。
func (s *SettingService) GatewayLiteIdempotentSettlementEnabled() bool {
	return s != nil && s.gatewayLiteIdempotentSettlementEnabled.Load()
}
