package service

import (
	"context"
	"fmt"
)

// [v0.2.5 新增] 验证码服务配置（从官方 setting_features.go 提取，避免与本地巨石文件冲突）

type AliyunCaptchaConfig struct {
	Enabled         bool
	AccessKeyID     string
	AccessKeySecret string
	SceneID         string
	Region          string
}

type TencentCaptchaConfig struct {
	Enabled        bool
	AppID          string
	AppSecretKey   string
	CloudSecretID  string
	CloudSecretKey string
	Region         string
}

// [v0.2.5 新增]
func (s *SettingService) GetCaptchaProviderConfig(ctx context.Context) (CaptchaProviderConfig, error) {
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyTurnstileEnabled,
		SettingKeyTurnstileSecretKey,
		SettingKeyTencentCaptchaEnabled,
		SettingKeyTencentCaptchaAppID,
		SettingKeyTencentCaptchaAppSecretKey,
		SettingKeyTencentCaptchaCloudSecretID,
		SettingKeyTencentCaptchaCloudSecretKey,
		SettingKeyTencentCaptchaRegion,
		SettingKeyAliyunCaptchaEnabled,
		SettingKeyAliyunCaptchaAccessKeyID,
		SettingKeyAliyunCaptchaAccessKeySecret,
		SettingKeyAliyunCaptchaSceneID,
		SettingKeyAliyunCaptchaRegion,
	})
	if err != nil {
		return CaptchaProviderConfig{}, fmt.Errorf("read captcha provider settings: %w", err)
	}
	return CaptchaProviderConfig{
		TurnstileEnabled:   values[SettingKeyTurnstileEnabled] == "true",
		TurnstileSecretKey: values[SettingKeyTurnstileSecretKey],
		Tencent: TencentCaptchaConfig{
			Enabled:        values[SettingKeyTencentCaptchaEnabled] == "true",
			AppID:          values[SettingKeyTencentCaptchaAppID],
			AppSecretKey:   values[SettingKeyTencentCaptchaAppSecretKey],
			CloudSecretID:  values[SettingKeyTencentCaptchaCloudSecretID],
			CloudSecretKey: values[SettingKeyTencentCaptchaCloudSecretKey],
			Region:         normalizeTencentCaptchaRegion(values[SettingKeyTencentCaptchaRegion]),
		},
		Aliyun: AliyunCaptchaConfig{
			Enabled:         values[SettingKeyAliyunCaptchaEnabled] == "true",
			AccessKeyID:     values[SettingKeyAliyunCaptchaAccessKeyID],
			AccessKeySecret: values[SettingKeyAliyunCaptchaAccessKeySecret],
			SceneID:         values[SettingKeyAliyunCaptchaSceneID],
			Region:          normalizeAliyunCaptchaRegion(values[SettingKeyAliyunCaptchaRegion]),
		},
	}, nil
}

// [v0.2.5 新增]
type CaptchaProviderConfig struct {
	TurnstileEnabled   bool
	TurnstileSecretKey string
	Tencent            TencentCaptchaConfig
	Aliyun             AliyunCaptchaConfig
}
