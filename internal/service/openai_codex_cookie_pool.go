package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// [2026-09 上游规则变更] 292 turn-state 门票的维持机制改版：
//   - 292 寿命从约 1 小时缩到约 240 秒，且必须携带账号级路由 Cookie 才能维持；
//   - __cfblb / __oailb 是账号级独立路由凭证（Cookie 管"路由"，票管"资格"），
//     两者解耦：同一 session_id 内换票、换 Cookie 均成功，故意错配也成功；
//   - 因此 Cookie 按账号共用一个池即可，不必每张票各存一份。
//
// 本文件实现账号级路由 Cookie 池：从任意上游响应（生产转发、打票探针）的
// Set-Cookie 捕获，内存实时生效 + 节流落库（账号 Extra），出站请求注入。

const (
	// openAICodexRouteCookieExtraKey 路由 Cookie 池在账号 Extra 里的持久化键。
	// 服务端托管：账号编辑时随票据一起保留（Merge）、导出时脱敏（Redact）。
	openAICodexRouteCookieExtraKey = "codex_route_cookies"
	// openAICodexRouteCookiePersistThrottle Cookie 池落库节流间隔。__cfblb 等
	// Cookie 可能每个响应都轮换，内存池每次都更新，落库按最小间隔执行防止
	// 高频请求把 UPDATE 打爆数据库。进程重启最多丢 30 秒的 Cookie 轮换，
	// 而下一次响应就会重新捕获，可接受。
	openAICodexRouteCookiePersistThrottle = 30 * time.Second
)

// openAICodexRouteCookieNames 需要捕获并携带的路由 Cookie 名集合。
var openAICodexRouteCookieNames = map[string]bool{
	"__cfblb": true,
	"__oailb": true,
}

// openAICodexRouteCookie 单个路由 Cookie 的池内条目。
type openAICodexRouteCookie struct {
	Value      string    `json:"value"`
	CapturedAt time.Time `json:"captured_at"`
	// ExpiresAt 取上游 Set-Cookie 的 Expires 属性；会话型 Cookie（无 Expires）
	// 保持零值，视作"未知过期时间"，只按捕获时间参与状态展示。
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// openAICodexRouteCookiePool 账号级 Cookie 池：名 -> 条目。即 Extra 持久化结构。
type openAICodexRouteCookiePool map[string]openAICodexRouteCookie

// OpenAICodexRouteCookieStatus 管理端 Cookie 池摘要，不含 Cookie 值。
type OpenAICodexRouteCookieStatus struct {
	Name             string     `json:"name"`
	Ready            bool       `json:"ready"`
	CapturedAt       *time.Time `json:"captured_at,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	// Stale 表示已捕获但疑似过期（带 Expires 且已过，或捕获超过 10 分钟）。
	// 上游可能随时轮换路由 Cookie，旧值只是"上次已知值"，出站注入仍会带上
	// （fail-open），由上游响应刷新。
	Stale bool `json:"stale"`
}

// parseOpenAICodexRouteCookiePool 把 Extra 里的任意 JSON 值解析成池结构。
func parseOpenAICodexRouteCookiePool(raw any) openAICodexRouteCookiePool {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var pool openAICodexRouteCookiePool
	if err := json.Unmarshal(b, &pool); err != nil {
		return nil
	}
	if len(pool) == 0 {
		return nil
	}
	for name, entry := range pool {
		entry.Value = strings.TrimSpace(entry.Value)
		pool[name] = entry
	}
	return pool
}

// lookupOpenAICodexRouteCookiePool 取账号当前 Cookie 池：内存优先，回填 Extra。
func (s *OpenAIGatewayService) lookupOpenAICodexRouteCookiePool(account *Account) openAICodexRouteCookiePool {
	if s == nil || account == nil || account.ID <= 0 {
		return nil
	}
	if raw, ok := s.openaiCodexRouteCookies.Load(account.ID); ok {
		if pool, ok := raw.(openAICodexRouteCookiePool); ok && len(pool) > 0 {
			return pool
		}
	}
	if account.Extra == nil {
		return nil
	}
	pool := parseOpenAICodexRouteCookiePool(account.Extra[openAICodexRouteCookieExtraKey])
	if pool != nil {
		s.openaiCodexRouteCookies.Store(account.ID, pool)
	}
	return pool
}

// openAICodexRouteCookieHeader 把池内未过期的路由 Cookie 拼成 Cookie 头值。
// 池空或全部过期返回 ""。注入是 fail-open 的：没有 Cookie 不拦请求，
// 只靠打票探针与后续响应继续补池（避免 Cookie 缺失反而放大 fail_closed 面积）。
func (s *OpenAIGatewayService) openAICodexRouteCookieHeader(account *Account) string {
	pool := s.lookupOpenAICodexRouteCookiePool(account)
	if len(pool) == 0 {
		return ""
	}
	now := time.Now()
	parts := make([]string, 0, len(pool))
	for name, entry := range pool {
		if entry.Value == "" {
			continue
		}
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			continue
		}
		parts = append(parts, name+"="+entry.Value)
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// captureOpenAICodexRouteCookies 从上游响应 Set-Cookie 捕获路由 Cookie 入池。
// 任何上游响应（生产转发、打票探针）都可喂池；池是账号级的，与票、模型解耦。
func (s *OpenAIGatewayService) captureOpenAICodexRouteCookies(account *Account, header http.Header) {
	if s == nil || account == nil || account.ID <= 0 || header == nil || !isOpenAICodexTicketAccount(account) {
		return
	}
	now := time.Now()
	fresh := make(openAICodexRouteCookiePool, 2)
	for _, ck := range readSetCookies(header) {
		name := strings.TrimSpace(ck.Name)
		if !openAICodexRouteCookieNames[name] {
			continue
		}
		value := strings.TrimSpace(ck.Value)
		if value == "" {
			continue
		}
		entry := openAICodexRouteCookie{Value: value, CapturedAt: now}
		if ck.Expires.After(now) {
			entry.ExpiresAt = ck.Expires
		}
		fresh[name] = entry
	}
	if len(fresh) == 0 {
		return
	}
	// 克隆后再合并：池可能被转发 goroutine 与探针并发读写，禁止原地改共享 map。
	var pool openAICodexRouteCookiePool
	if raw, ok := s.openaiCodexRouteCookies.Load(account.ID); ok {
		if p, ok := raw.(openAICodexRouteCookiePool); ok {
			pool = make(openAICodexRouteCookiePool, len(p)+len(fresh))
			for k, v := range p {
				pool[k] = v
			}
		}
	}
	if pool == nil {
		pool = make(openAICodexRouteCookiePool, len(fresh))
	}
	changed := false
	for name, entry := range fresh {
		if old, ok := pool[name]; !ok || old.Value != entry.Value {
			pool[name] = entry
			changed = true
		}
	}
	if !changed {
		return
	}
	s.openaiCodexRouteCookies.Store(account.ID, pool)
	s.persistOpenAICodexRouteCookies(account, pool)
}

// persistOpenAICodexRouteCookies 把池写入账号 Extra（节流）。内存池已实时生效，
// 落库只为进程重启后仍有"上次已知"的路由 Cookie 可用。
func (s *OpenAIGatewayService) persistOpenAICodexRouteCookies(account *Account, pool openAICodexRouteCookiePool) {
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 || len(pool) == 0 {
		return
	}
	now := time.Now()
	if raw, ok := s.openaiCodexRouteCookiePersistAt.Load(account.ID); ok {
		if last, ok := raw.(time.Time); ok && now.Sub(last) < openAICodexRouteCookiePersistThrottle {
			return
		}
	}
	s.openaiCodexRouteCookiePersistAt.Store(account.ID, now)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		openAICodexRouteCookieExtraKey: pool,
	}); err != nil {
		logger.L().Warn("openai_codex_route_cookie persist failed",
			zap.Int64("account_id", account.ID),
			zap.Error(err),
		)
	}
}

// OpenAICodexRouteCookieStatuses 管理端徽章数据源：账号 Cookie 池摘要。
func OpenAICodexRouteCookieStatuses(account *Account) []OpenAICodexRouteCookieStatus {
	if account == nil || !isOpenAICodexTicketAccount(account) {
		return nil
	}
	var pool openAICodexRouteCookiePool
	if account.Extra != nil {
		pool = parseOpenAICodexRouteCookiePool(account.Extra[openAICodexRouteCookieExtraKey])
	}
	if len(pool) == 0 {
		return nil
	}
	now := time.Now()
	names := make([]string, 0, len(pool))
	for name := range pool {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]OpenAICodexRouteCookieStatus, 0, len(names))
	for _, name := range names {
		entry := pool[name]
		status := OpenAICodexRouteCookieStatus{Name: name}
		if entry.Value == "" {
			out = append(out, status)
			continue
		}
		status.CapturedAt = timePtrUTC(entry.CapturedAt)
		if !entry.ExpiresAt.IsZero() {
			status.ExpiresAt = timePtrUTC(entry.ExpiresAt)
		}
		switch {
		case !entry.ExpiresAt.IsZero():
			remaining := int64(entry.ExpiresAt.Sub(now) / time.Second)
			status.Ready = remaining > 0
			if remaining < 0 {
				remaining = 0
			}
			status.RemainingSeconds = remaining
		case !entry.CapturedAt.IsZero():
			// 会话型 Cookie 无过期时间：捕获后 10 分钟内视作可用。
			status.Ready = now.Sub(entry.CapturedAt) < 10*time.Minute
		}
		status.Stale = !status.Ready
		out = append(out, status)
	}
	return out
}

// timePtrUTC 把时间规整为 UTC（秒精度）后取指针；零值时间返回 nil。
func timePtrUTC(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	v := t.UTC().Truncate(time.Second)
	return &v
}

// readSetCookies 轻量解析响应 Set-Cookie 值：只取名字、值和 Expires 属性
// （net/http 的 readSetCookies 未导出，此处手写等价的最小子集）。
func readSetCookies(header http.Header) []*http.Cookie {
	if header == nil {
		return nil
	}
	raws := header.Values("Set-Cookie")
	out := make([]*http.Cookie, 0, len(raws))
	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.Split(raw, ";")
		first := strings.TrimSpace(parts[0])
		eq := strings.Index(first, "=")
		if eq <= 0 {
			continue
		}
		ck := &http.Cookie{
			Name:  strings.TrimSpace(first[:eq]),
			Value: strings.TrimSpace(first[eq+1:]),
		}
		for _, attr := range parts[1:] {
			attr = strings.TrimSpace(attr)
			if len(attr) > 8 && strings.EqualFold(attr[:8], "expires=") {
				if t, err := http.ParseTime(attr[8:]); err == nil {
					ck.Expires = t
				}
			}
		}
		out = append(out, ck)
	}
	return out
}
