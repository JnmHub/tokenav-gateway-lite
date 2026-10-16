package service

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// [v0.2.5 新增] OpenAI Codex 门票运行时开关与打票代理的 setting 读取层。
// 与官方新版 setting_service.go 等价的缓存语义：60s TTL + stale-on-error，
// Invalidate* 供管理端写路径即时失效。

const (
	// SettingKeyOpenAICodexTicketEnabled 门票功能运行时开关（"true"/"false"）。
	SettingKeyOpenAICodexTicketEnabled = "openai_codex_ticket_enabled"
	// SettingKeyOpenAICodexTicketHarvestProxyURL 打票代理地址。
	SettingKeyOpenAICodexTicketHarvestProxyURL = "openai_codex_ticket_harvest_proxy_url"

	openAICodexTicketSettingTTL     = 60 * time.Second
	openAICodexTicketSettingErrTTL  = 5 * time.Second
	openAICodexTicketSettingTimeout = 3 * time.Second
)

type cachedOpenAICodexTicketEnabled struct {
	value     bool
	expiresAt int64
}

type cachedOpenAICodexTicketHarvestProxy struct {
	value     string
	expiresAt int64
}

// GetOpenAICodexTicketEnabled 读取运行时开关；fallback 为配置文件缺省值。
func (s *SettingService) GetOpenAICodexTicketEnabled(ctx context.Context, fallback bool) bool {
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	if cached, ok := s.openAICodexTicketEnabledCache.Load().(*cachedOpenAICodexTicketEnabled); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.value
	}
	value, err := s.getSettingValueWithTimeout(ctx, SettingKeyOpenAICodexTicketEnabled)
	if err != nil || value == nil {
		// stale-on-error：短暂异常保留旧值，避免后台门控抖动
		if cached, ok := s.openAICodexTicketEnabledCache.Load().(*cachedOpenAICodexTicketEnabled); ok && cached != nil {
			s.openAICodexTicketEnabledCache.Store(&cachedOpenAICodexTicketEnabled{value: cached.value, expiresAt: time.Now().Add(openAICodexTicketSettingErrTTL).UnixNano()})
			return cached.value
		}
		return fallback
	}
	enabled := strings.TrimSpace(*value) == "true"
	s.openAICodexTicketEnabledCache.Store(&cachedOpenAICodexTicketEnabled{value: enabled, expiresAt: time.Now().Add(openAICodexTicketSettingTTL).UnixNano()})
	return enabled
}

// GetOpenAICodexTicketHarvestProxyURL 读取打票代理；空串表示未配置。
func (s *SettingService) GetOpenAICodexTicketHarvestProxyURL(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return ""
	}
	if cached, ok := s.openAICodexTicketHarvestProxyCache.Load().(*cachedOpenAICodexTicketHarvestProxy); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.value
	}
	value, err := s.getSettingValueWithTimeout(ctx, SettingKeyOpenAICodexTicketHarvestProxyURL)
	if err != nil || value == nil {
		if cached, ok := s.openAICodexTicketHarvestProxyCache.Load().(*cachedOpenAICodexTicketHarvestProxy); ok && cached != nil {
			s.openAICodexTicketHarvestProxyCache.Store(&cachedOpenAICodexTicketHarvestProxy{value: cached.value, expiresAt: time.Now().Add(openAICodexTicketSettingErrTTL).UnixNano()})
			return cached.value
		}
		return ""
	}
	proxy := strings.TrimSpace(*value)
	s.openAICodexTicketHarvestProxyCache.Store(&cachedOpenAICodexTicketHarvestProxy{value: proxy, expiresAt: time.Now().Add(openAICodexTicketSettingTTL).UnixNano()})
	return proxy
}

func (s *SettingService) getSettingValueWithTimeout(ctx context.Context, key string) (*string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAICodexTicketSettingTimeout)
	defer cancel()
	value, err := s.settingRepo.GetValue(dbCtx, key)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// InvalidateOpenAICodexTicketEnabledCache 管理端写路径即时失效开关缓存。
func (s *SettingService) InvalidateOpenAICodexTicketEnabledCache() {
	if s == nil {
		return
	}
	s.openAICodexTicketEnabledCache.Store(&cachedOpenAICodexTicketEnabled{expiresAt: 0})
}

// InvalidateOpenAICodexTicketHarvestProxyCache 管理端写路径即时失效代理缓存。
func (s *SettingService) InvalidateOpenAICodexTicketHarvestProxyCache() {
	if s == nil {
		return
	}
	s.openAICodexTicketHarvestProxyCache.Store(&cachedOpenAICodexTicketHarvestProxy{expiresAt: 0})
}

var _ sync.Map // 保持 import 稳定（struct 字段在 setting_service.go）
var _ atomic.Value
