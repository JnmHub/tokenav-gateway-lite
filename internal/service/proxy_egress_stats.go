package service

import (
	"sort"
	"sync"
	"time"
)

// ProxyEgressStat 是单个（账号 × 出口代理）的实时在途与最近请求状态。
// in_flight 来自上游连接池条目（每 account:proxy 一个条目，天然分代理）；
// recent 是最近 N 次请求成败（true=成功绿 / false=失败红），按时间升序。
type ProxyEgressStat struct {
	AccountID int64  `json:"account_id"`
	ProxyKey  string `json:"proxy_key"` // 规范化代理地址；直连为 "direct"
	InFlight  int64  `json:"in_flight"`
	Recent    []bool `json:"recent"`
	UpdatedAt int64  `json:"updated_at"` // 最近一次状态记录的 Unix 毫秒
}

// ProxyEgressObserver 提供账号出口代理的运行时观测。
// 独立小接口 + 类型断言获取，避免给 HTTPUpstream 主接口加方法
// 而破坏大量测试 stub（与 TLSFPProfileService 的 setter 注入同思路）。
type ProxyEgressObserver interface {
	// EgressInFlight 返回（账号, 代理）维度的当前在途请求数。
	// proxyURL 为调用方使用的原始代理地址（空串=直连），实现内部做键规范化。
	EgressInFlight(accountID int64, proxyURL string) int64
	// EgressStats 返回全部出口观测快照（按账号、代理键排序）。
	EgressStats() []ProxyEgressStat
	// RecordEgressResult 记录一次出口请求的成败（供最近状态展示）。
	RecordEgressResult(accountID int64, proxyKey string, ok bool)
	// EgressTripped 判定（账号, 代理）是否处于失败熔断：最近 consecutive 次
	// 请求全部失败即熔断，调度应暂时避开该出口。proxyURL 为原始地址。
	EgressTripped(accountID int64, proxyURL string, consecutive int) bool
}

// proxyEgressRecentSize 最近状态条数（展示为绿/红状态点）。
const proxyEgressRecentSize = 10

// ProxyEgressHistory 单个（账号×代理）的最近请求状态环形缓冲。
type ProxyEgressHistory struct {
	mu      sync.Mutex
	ring    []bool
	next    int
	updated int64
}

// Record 记录一次结果（nowMS 为 Unix 毫秒）。
func (h *ProxyEgressHistory) Record(ok bool, nowMS int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ring) == 0 {
		h.ring = make([]bool, 0, proxyEgressRecentSize)
	}
	if len(h.ring) < proxyEgressRecentSize {
		h.ring = append(h.ring, ok)
	} else {
		h.ring[h.next] = ok
		h.next = (h.next + 1) % proxyEgressRecentSize
	}
	h.updated = nowMS
}

// Snapshot 返回按时间升序的最近状态副本及最后更新时间。
func (h *ProxyEgressHistory) Snapshot() ([]bool, int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ring) < proxyEgressRecentSize {
		out := make([]bool, len(h.ring))
		copy(out, h.ring)
		return out, h.updated
	}
	out := make([]bool, proxyEgressRecentSize)
	copy(out, h.ring[h.next:])
	copy(out[proxyEgressRecentSize-h.next:], h.ring[:h.next])
	return out, h.updated
}

// ProxyEgressRegistry 网关进程级的出口请求状态注册表。
// 在途数以连接池条目为单一事实源（由 observer 实现从条目读取），
// 这里只维护最近成败历史；repository 层写入，调度与 admin API 读取。
type ProxyEgressRegistry struct {
	mu      sync.Mutex
	history map[int64]map[string]*ProxyEgressHistory
}

// NewProxyEgressRegistry 创建空注册表。
func NewProxyEgressRegistry() *ProxyEgressRegistry {
	return &ProxyEgressRegistry{history: make(map[int64]map[string]*ProxyEgressHistory)}
}

// RecordEgressResult 记录一次（账号, 代理）请求成败。
func (r *ProxyEgressRegistry) RecordEgressResult(accountID int64, proxyKey string, ok bool) {
	now := time.Now().UnixMilli()
	r.mu.Lock()
	perProxy, exists := r.history[accountID]
	if !exists {
		perProxy = make(map[string]*ProxyEgressHistory)
		r.history[accountID] = perProxy
	}
	h, exists := perProxy[proxyKey]
	if !exists {
		h = &ProxyEgressHistory{}
		perProxy[proxyKey] = h
	}
	r.mu.Unlock()
	h.Record(ok, now)
}

// History 返回指定（账号, 代理）的最近状态快照；不存在时返回空。
func (r *ProxyEgressRegistry) History(accountID int64, proxyKey string) ([]bool, int64) {
	r.mu.Lock()
	perProxy, exists := r.history[accountID]
	if !exists {
		r.mu.Unlock()
		return []bool{}, 0
	}
	h := perProxy[proxyKey]
	r.mu.Unlock()
	if h == nil {
		return []bool{}, 0
	}
	return h.Snapshot()
}

// AccountKeys 返回有历史记录的（账号, 代理键）集合（用于与在途快照合并）。
func (r *ProxyEgressRegistry) AccountKeys() [][2]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][2]any, 0, 16)
	for id, perProxy := range r.history {
		for key := range perProxy {
			out = append(out, [2]any{id, key})
		}
	}
	return out
}

// SortProxyEgressStats 按账号、代理键排序（导出快照的稳定顺序）。
func SortProxyEgressStats(stats []ProxyEgressStat) {
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].AccountID != stats[j].AccountID {
			return stats[i].AccountID < stats[j].AccountID
		}
		return stats[i].ProxyKey < stats[j].ProxyKey
	})
}
