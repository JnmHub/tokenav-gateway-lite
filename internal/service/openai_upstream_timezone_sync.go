package service

import (
	"context"
	"strings"
	"sync"
	"time"
)

// autoUpstreamTimezoneLookup 由自动探测服务在启动时注入：
// 请求热路径按账号 ID 读取探测到的出口时区（纯内存读，无网络请求）。
var autoUpstreamTimezoneLookup func(accountID int64) string

// SetAutoUpstreamTimezoneLookup 注册热路径的自动时区查询函数。
func SetAutoUpstreamTimezoneLookup(fn func(accountID int64) string) {
	autoUpstreamTimezoneLookup = fn
}

// lookupAutoUpstreamTimezone 供热路径调用；未注册或未探测到时返回空串。
func lookupAutoUpstreamTimezone(accountID int64) string {
	if autoUpstreamTimezoneLookup == nil {
		return ""
	}
	return autoUpstreamTimezoneLookup(accountID)
}

// OpenAIUpstreamTimezoneSyncService 后台自动探测 OpenAI 账号出口 IP 的时区，
// 并按账号缓存到进程内存。
//
// 为什么放后台：出口地理定位需要访问外部服务（ip-api.com），绝不能进请求
// 热路径；同一出口（代理 URL 或网关直连）只探测一次，结果在内存里按账号
// 共享，转发路径只读内存 map，零网络请求、微秒级开销。
//
// 手动配置永远优先：credentials.upstream_timezone 非空时，自动值不参与
// 改写判断（见 rewriteUpstreamTimezoneFromAccount）。探测失败的出口保留
// 旧值，避免网络抖动让改写行为来回跳。
type OpenAIUpstreamTimezoneSyncService struct {
	accountRepo AccountRepository
	proxyRepo   ProxyRepository
	prober      ProxyExitInfoProber
	interval    time.Duration
	startDelay  time.Duration

	mu      sync.RWMutex
	byAcct  map[int64]string
	stop    chan struct{}
	stopOnce sync.Once
	started bool
}

func NewOpenAIUpstreamTimezoneSyncService(accountRepo AccountRepository, proxyRepo ProxyRepository, prober ProxyExitInfoProber) *OpenAIUpstreamTimezoneSyncService {
	return &OpenAIUpstreamTimezoneSyncService{
		accountRepo: accountRepo,
		proxyRepo:   proxyRepo,
		prober:      prober,
		interval:    12 * time.Hour,
		startDelay:  30 * time.Second,
		byAcct:      make(map[int64]string),
		stop:        make(chan struct{}),
	}
}

// Start 启动后台探测循环（幂等）。启动延迟 30s 避让进程启动高峰。
func (s *OpenAIUpstreamTimezoneSyncService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	SetAutoUpstreamTimezoneLookup(s.lookup)
	go s.loop()
}

// Stop 停止后台循环。
func (s *OpenAIUpstreamTimezoneSyncService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *OpenAIUpstreamTimezoneSyncService) loop() {
	select {
	case <-s.stop:
		return
	case <-time.After(s.startDelay):
	}
	ctx := context.Background()
	s.refresh(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.refresh(ctx)
		}
	}
}

// lookup 是注入热路径的读函数。
func (s *OpenAIUpstreamTimezoneSyncService) lookup(accountID int64) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byAcct[accountID]
}

// refresh 拉取全部 OpenAI 账号，按出口去重探测，把时区写回按账号的缓存。
func (s *OpenAIUpstreamTimezoneSyncService) refresh(ctx context.Context) {
	if s.prober == nil || s.accountRepo == nil {
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return
	}

	type egress struct {
		proxyURL string
	}
	egresses := make(map[string]*egress)
	acctEgress := make(map[int64]string)
	for i := range accounts {
		acc := &accounts[i]
		if acc == nil || !acc.IsOpenAI() {
			continue
		}
		key, proxyURL := "direct", ""
		if acc.ProxyID != nil && *acc.ProxyID > 0 && s.proxyRepo != nil {
			if proxy, perr := s.proxyRepo.GetByID(ctx, *acc.ProxyID); perr == nil && proxy != nil {
				proxyURL = proxy.URL()
				key = "proxy:" + proxyURL
			}
		}
		if _, exists := egresses[key]; !exists {
			egresses[key] = &egress{proxyURL: proxyURL}
		}
		acctEgress[acc.ID] = key
	}
	if len(acctEgress) == 0 {
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tzByEgress := make(map[string]string, len(egresses))
	for key, eg := range egresses {
		tz := s.probeTimezone(probeCtx, eg.proxyURL)
		if tz != "" {
			tzByEgress[key] = tz
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// 只覆盖成功探测到的出口；失败出口保留旧值。
	for accID, key := range acctEgress {
		if tz, ok := tzByEgress[key]; ok {
			s.byAcct[accID] = tz
		}
	}
}

func (s *OpenAIUpstreamTimezoneSyncService) probeTimezone(ctx context.Context, proxyURL string) string {
	info, _, err := s.prober.ProbeProxy(ctx, proxyURL)
	if err != nil || info == nil {
		return ""
	}
	tz := strings.TrimSpace(info.Timezone)
	if tz == "" {
		return ""
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return ""
	}
	return tz
}

// ProvideOpenAIUpstreamTimezoneSyncService 供 wire 装配：构造即启动后台循环。
func ProvideOpenAIUpstreamTimezoneSyncService(accountRepo AccountRepository, proxyRepo ProxyRepository, prober ProxyExitInfoProber) *OpenAIUpstreamTimezoneSyncService {
	service := NewOpenAIUpstreamTimezoneSyncService(accountRepo, proxyRepo, prober)
	service.Start()
	return service
}
