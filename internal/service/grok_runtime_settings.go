package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

func normalizeGrokDefaultTextModel(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return xai.DefaultTextModel
}

// publishGrokModelMappingSettings 原子替换不可变映射快照。请求热路径只读内存，
// 管理员保存设置时不会让并发请求看到一半新、一半旧的配置。
func publishGrokModelMappingSettings(defaultText string, crossClientEnabled bool) {
	xai.SetRuntimeModelMappingOptions(xai.ModelMappingOptions{
		DefaultText:          normalizeGrokDefaultTextModel(defaultText),
		EnableCrossClientMap: crossClientEnabled,
	})
}

// WarmGrokModelMappingSettings 在启动阶段恢复数据库配置。读取失败时保留安全默认值：
// grok-4.5 且不做跨客户端静默改写。
func (s *SettingService) WarmGrokModelMappingSettings(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyGrokDefaultTextModel,
		SettingKeyGrokCrossClientModelMapEnabled,
	})
	if err != nil {
		return fmt.Errorf("load Grok model mapping settings: %w", err)
	}
	publishGrokModelMappingSettings(
		values[SettingKeyGrokDefaultTextModel],
		strings.EqualFold(strings.TrimSpace(values[SettingKeyGrokCrossClientModelMapEnabled]), "true"),
	)
	return nil
}
