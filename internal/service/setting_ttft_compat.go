package service

import (
	"context"
	"strings"
	"sync/atomic"
	"time"
)

// [v0.2.5 新增] OpenAI TTFT 统计口径 setting 读取层（官方 setting_gateway_runtime.go 等价实现，
// 适配我们的 SettingService：直接读 setting，60s 进程内缓存）。


type cachedOpenAITTFTMode struct {
	value     string
	expiresAt int64
}

var openAITTFTModeCache atomic.Value

// GetOpenAITTFTMode 返回 Responses first_token_ms 的统计口径。
func (s *SettingService) GetOpenAITTFTMode(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return OpenAITTFTModeSemantic
	}
	if cached, ok := openAITTFTModeCache.Load().(*cachedOpenAITTFTMode); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.value
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	mode, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAITTFTMode)
	if err != nil {
		return OpenAITTFTModeSemantic
	}
	value := normalizeOpenAITTFTMode(mode)
	openAITTFTModeCache.Store(&cachedOpenAITTFTMode{value: value, expiresAt: time.Now().Add(60 * time.Second).UnixNano()})
	return value
}

// normalizeOpenAITTFTMode 归一化 TTFT 口径，缺省 semantic。
func normalizeOpenAITTFTMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), OpenAITTFTModeVisible) {
		return OpenAITTFTModeVisible
	}
	return OpenAITTFTModeSemantic
}
