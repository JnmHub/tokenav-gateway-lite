package service

import (
	"strconv"
	"strings"

	"context"
)
// [v0.2.5 新增] 审计日志保留配置（从官方 setting_features.go 提取，避免与本地巨石 setting_service.go 冲突）

const defaultAuditLogRetentionDays = 180

// GetAuditLogRetentionDays 审计日志保留天数（<=0 表示永久保留，仅支持手动清空）。
func (s *SettingService) GetAuditLogRetentionDays(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAuditLogRetentionDays)
	if err != nil {
		return defaultAuditLogRetentionDays
	}
	return parseAuditLogRetentionDays(value)
}

func parseAuditLogRetentionDays(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultAuditLogRetentionDays
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return defaultAuditLogRetentionDays
	}
	if n < 0 {
		return 0
	}
	return n
}
