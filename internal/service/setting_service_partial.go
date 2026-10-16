package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 单独修订行与设置字段同事务提交，不能用先读版本再普通写入代替 CAS。
const SettingKeySystemSettingsVersion = "system_settings_version"

// 全局运行时缓存也被多个 SettingService 使用。回读与发布必须串行，
// 不能让较早回读的请求晚于新请求发布旧值；数据库并发裁决仍由 CAS 完成。
var systemSettingsCacheRefreshMu sync.Mutex

var (
	ErrSettingsVersionRequired = infraerrors.New(http.StatusPreconditionRequired, "SETTINGS_VERSION_REQUIRED", "请刷新设置后再保存")
	ErrSettingsVersionConflict = infraerrors.Conflict("SETTINGS_VERSION_CONFLICT", "设置已被其他管理员修改，请刷新后确认并重新保存")
)

func systemSettingsVersion(values map[string]string) string {
	if version, ok := values[SettingKeySystemSettingsVersion]; ok {
		return version
	}
	return "0"
}

// ValidateSettingsVersion 限制为规范十进制字符串，避免浮点数丢失修订精度。
func ValidateSettingsVersion(version string) error {
	if version == "" {
		return ErrSettingsVersionRequired
	}
	revision, err := strconv.ParseUint(version, 10, 64)
	if err != nil || strconv.FormatUint(revision, 10) != version || revision == ^uint64(0) {
		return infraerrors.BadRequest("SETTINGS_VERSION_INVALID", "version 必须是有效的设置修订字符串")
	}
	return nil
}

// GetSettingsSnapshot 将普通设置、默认授予与版本从同一仓库查询结果解析。
func (s *SettingService) GetSettingsSnapshot(ctx context.Context) (*SystemSettings, *AuthSourceDefaultSettings, error) {
	values, err := s.settingRepo.GetAll(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("get settings snapshot: %w", err)
	}
	settings := s.parseSettings(values)
	// 同一快照补齐回显平台键，不重新读数据库或使用热路径缓存。
	quotas := make(map[string]*DefaultPlatformQuotaSetting, len(AllowedQuotaPlatforms))
	for _, platform := range AllowedQuotaPlatforms {
		quota := settings.DefaultPlatformQuotas[platform]
		if quota == nil {
			quota = &DefaultPlatformQuotaSetting{}
		}
		quotas[platform] = quota
	}
	settings.DefaultPlatformQuotas = quotas
	policy := DefaultOpenAIFastPolicySettings()
	if raw := values[SettingKeyOpenAIFastPolicySettings]; raw != "" {
		var parsed OpenAIFastPolicySettings
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			normalizeOpenAIFastPolicySettingsForRead(&parsed)
			policy = &parsed
		}
	}
	settings.OpenAIFastPolicySettings = policy
	return settings, s.parseAuthSourceDefaultSettings(values), nil
}

// UpdateSettingsPartial 使用已校验的完整内存值生成存储值，只写请求出现的字段。
// 省略值不能固化无关默认值；secret 空串仍由原生成器跳过，保留只写语义。
func (s *SettingService) UpdateSettingsPartial(ctx context.Context, settings *SystemSettings, authDefaults *AuthSourceDefaultSettings, version string, fields map[string]bool, fastPolicy *OpenAIFastPolicySettings) error {
	if err := ValidateSettingsVersion(version); err != nil {
		return err
	}
	casRepo, ok := s.settingRepo.(OpenAISchedulerPolicyCASRepository)
	if !ok {
		return fmt.Errorf("setting repository does not support settings CAS")
	}
	updates, err := s.buildSystemSettingsUpdates(ctx, settings)
	if err != nil {
		return err
	}
	authUpdates, err := s.buildAuthSourceDefaultUpdates(ctx, authDefaults)
	if err != nil {
		return err
	}
	for key, value := range authUpdates {
		updates[key] = value
	}
	// Gateway 标签包含 Fast/Flex 策略；必须先校验并与版本一同写入，
	// 不能设置已提交后再执行普通 Set，否则冲突请求也可能留下部分保存。
	if fastPolicy != nil {
		if err := validateOpenAIFastPolicySettingsForWrite(fastPolicy); err != nil {
			return infraerrors.BadRequest("OPENAI_FAST_POLICY_INVALID", err.Error())
		}
		raw, err := json.Marshal(fastPolicy)
		if err != nil {
			return err
		}
		updates[SettingKeyOpenAIFastPolicySettings] = string(raw)
	}

	allowed := make(map[string]bool, len(fields))
	for field := range fields {
		key := field
		if field == "smtp_from_email" {
			key = SettingKeySMTPFrom
		}
		allowed[key] = true
	}
	// 这些关联规则已有明确语义：显式切换钉钉策略必须清除失效的身份同步开关；
	// 显式启用旧 OIDC 配置使用兼容写默认，不能改变未编辑 OIDC 时的原配置。
	if fields["dingtalk_connect_corp_restriction_policy"] || fields["dingtalk_connect_enabled"] {
		for _, key := range []string{SettingKeyDingTalkConnectBypassRegistration, SettingKeyDingTalkConnectSyncCorpEmail, SettingKeyDingTalkConnectSyncDisplayName, SettingKeyDingTalkConnectSyncDept} {
			allowed[key] = true
		}
	}
	if fields["oidc_connect_enabled"] {
		allowed[SettingKeyOIDCConnectUsePKCE] = true
		allowed[SettingKeyOIDCConnectValidateIDToken] = true
	}
	for key := range updates {
		if !allowed[key] {
			delete(updates, key)
		}
	}
	revision, _ := strconv.ParseUint(version, 10, 64)
	nextVersion := strconv.FormatUint(revision+1, 10)
	swapped, err := casRepo.CompareAndSwapMultiple(ctx, SettingKeySystemSettingsVersion, version, nextVersion, updates)
	if err != nil {
		return err
	}
	if !swapped {
		return ErrSettingsVersionConflict
	}
	// 提交成功后串行回读和发布：同进程较早的请求不能迟到刷新旧缓存。
	systemSettingsCacheRefreshMu.Lock()
	defer systemSettingsCacheRefreshMu.Unlock()
	current, _, err := s.GetSettingsSnapshot(ctx)
	if err != nil {
		return err
	}
	s.refreshCachedSettings(current)
	if fastPolicy != nil {
		s.storeOpenAIFastPolicySettingsCache(current.OpenAIFastPolicySettings, openAIFastPolicySettingsCacheTTL)
	}
	settings.Version = nextVersion
	return nil
}
