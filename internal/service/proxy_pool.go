package service

import (
	"context"
	"strings"
)

// AccountProxyEndpoint 是账号的一个出口代理端点。
// Name 供展示（如 "美国"/"法国"），URL 为代理地址（空串表示直连）。
// Concurrency 是该端点的并发上限：>0 为独立上限（extra.proxy_pool[].concurrency
// 或主代理的 extra.proxy_main_concurrency），<=0 表示跟随账号并发（由
// resolveEndpointConcurrency 折算为最终值）。
type AccountProxyEndpoint struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Concurrency int    `json:"concurrency,omitempty"`
}

// proxyPoolExtraKey 账号代理池在 extra 中的存储键。
// 池内保存代理快照（id/name/url/concurrency）：代理地址变更后需重新保存
// 账号以刷新快照。这样账号加载路径无需关联查询，调度与展示都直接读 extra。
const proxyPoolExtraKey = "proxy_pool"

// proxyMainConcurrencyExtraKey 主代理独立并发上限（>0 生效，缺省跟随账号并发）。
const proxyMainConcurrencyExtraKey = "proxy_main_concurrency"

// resolveEndpointConcurrency 折算端点最终并发上限：
// 端点独立值 >0 用之；否则用主代理覆盖值（仅主代理）；否则账号并发。
func (a *Account) resolveEndpointConcurrency(configured int, isMain bool) int {
	if configured > 0 {
		return configured
	}
	if isMain {
		if a != nil && a.Extra != nil {
			if v, ok := toInt64(a.Extra[proxyMainConcurrencyExtraKey]); ok && v > 0 && v <= 1_000_000 {
				return int(v)
			}
		}
	}
	if a != nil && a.Concurrency > 0 {
		return a.Concurrency
	}
	return 1
}

// ProxyEndpoints 返回账号的全部出口端点：主代理（proxy_id 关联，若配置）+
// extra.proxy_pool 池内代理，按 URL 去重、顺序稳定（主代理在前，池按配置序）。
// Concurrency 已折算为最终生效值（独立上限或账号并发）。
// 返回长度 0 表示账号直连（无任何代理）。
func (a *Account) ProxyEndpoints() []AccountProxyEndpoint {
	if a == nil {
		return nil
	}
	endpoints := make([]AccountProxyEndpoint, 0, 4)
	seen := make(map[string]struct{}, 4)
	if a.Proxy != nil && a.ProxyID != nil {
		ep := AccountProxyEndpoint{ID: a.Proxy.ID, Name: a.Proxy.Name, URL: a.Proxy.URL()}
		ep.Concurrency = a.resolveEndpointConcurrency(0, true)
		if _, dup := seen[ep.URL]; !dup {
			endpoints = append(endpoints, ep)
			seen[ep.URL] = struct{}{}
		}
	}
	for _, ep := range a.ProxyPoolFromExtra() {
		if _, dup := seen[ep.URL]; dup {
			continue
		}
		ep.Concurrency = a.resolveEndpointConcurrency(ep.Concurrency, false)
		endpoints = append(endpoints, ep)
		seen[ep.URL] = struct{}{}
	}
	return endpoints
}

// GetMainProxyConcurrency 返回主代理的独立并发上限（未配置返回 0），
// 供编辑界面回填。
func (a *Account) GetMainProxyConcurrency() int {
	if a == nil || a.Extra == nil {
		return 0
	}
	if v, ok := toInt64(a.Extra[proxyMainConcurrencyExtraKey]); ok && v > 0 && v <= 1_000_000 {
		return int(v)
	}
	return 0
}

// ProxyPoolFromExtra 解析 extra.proxy_pool 快照（宽容解析，畸形条目跳过）。
func (a *Account) ProxyPoolFromExtra() []AccountProxyEndpoint {
	if a == nil || a.Extra == nil {
		return nil
	}
	raw, ok := a.Extra[proxyPoolExtraKey].([]any)
	if !ok {
		return nil
	}
	out := make([]AccountProxyEndpoint, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ep := AccountProxyEndpoint{}
		if v, ok := m["id"].(float64); ok {
			ep.ID = int64(v)
		}
		if v, ok := m["name"].(string); ok {
			ep.Name = strings.TrimSpace(v)
		}
		if v, ok := m["url"].(string); ok {
			ep.URL = strings.TrimSpace(v)
		}
		if v, ok := toInt64(m["concurrency"]); ok && v > 0 && v <= 1_000_000 {
			ep.Concurrency = int(v)
		}
		if ep.URL == "" {
			continue
		}
		out = append(out, ep)
	}
	return out
}

// proxyLister 收窄的代理查询能力：NormalizeProxyPoolExtra 只需要按 ID 批量取
// 代理，小接口让测试桩无需实现整个 ProxyRepository。
type proxyLister interface {
	ListByIDs(ctx context.Context, ids []int64) ([]Proxy, error)
}

// NormalizeProxyPoolExtra 规范化账号 extra.proxy_pool（管理端编辑入口调用）：
// 前端只提交 {id,name}（凭据不明文下发），这里按 id 从代理仓库补全完整 URL
// 快照；id 查不到的条目剔除，空池删除键。幂等：已含合法 URL 的条目按 id
// 重新对账（代理地址变更后重新保存账号即刷新快照）。
func NormalizeProxyPoolExtra(ctx context.Context, repo proxyLister, extra map[string]any) map[string]any {
	if extra == nil {
		return extra
	}
	raw, ok := extra[proxyPoolExtraKey]
	if !ok {
		return extra
	}
	items, _ := raw.([]any)
	if len(items) == 0 {
		delete(extra, proxyPoolExtraKey)
		return extra
	}
	// 收集待补全的代理 ID。
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := toInt64(m["id"]); ok && id > 0 {
			ids = append(ids, id)
		}
	}
	byID := make(map[int64]*Proxy, len(ids))
	if repo != nil && len(ids) > 0 {
		if proxies, err := repo.ListByIDs(ctx, ids); err == nil {
			for i := range proxies {
				byID[proxies[i].ID] = &proxies[i]
			}
		}
	}
	out := make([]any, 0, len(items))
	seen := make(map[int64]struct{}, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, ok := toInt64(m["id"])
		if !ok || id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		proxy, found := byID[id]
		if !found || proxy == nil {
			continue // 代理已删除：从池中剔除，避免悬挂引用。
		}
		seen[id] = struct{}{}
		entry := map[string]any{
			"id":   proxy.ID,
			"name": proxy.Name,
			"url":  proxy.URL(),
		}
		// 保留管理员设置的每代理独立并发上限（合法范围 1..1e6，非法值丢弃）。
		if v, ok := toInt64(m["concurrency"]); ok && v > 0 && v <= 1_000_000 {
			entry["concurrency"] = v
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		delete(extra, proxyPoolExtraKey)
		return extra
	}
	extra[proxyPoolExtraKey] = out
	return extra
}

// SameEgressProxyKey 粗匹配出口端点 URL 与观测代理键：观测键是规范化地址
//（scheme://host[:port]，"direct" 表示直连），端点 URL 可能携带认证信息；
// 这里剥掉 scheme 与 userinfo 按 host[:port] 比较，供展示层把观测行与
// 代理池端点关联（去重与命名）。
func SameEgressProxyKey(endpointURL, proxyKey string) bool {
	if proxyKey == "direct" || proxyKey == "" {
		return endpointURL == ""
	}
	stripSchemes := func(s string) string {
		for _, scheme := range []string{"https://", "http://", "socks5://", "socks5h://"} {
			if strings.HasPrefix(s, scheme) {
				s = s[len(scheme):]
				break
			}
		}
		// 去掉 userinfo（user:pass@host）。
		if i := strings.LastIndex(s, "@"); i >= 0 {
			s = s[i+1:]
		}
		return strings.Split(s, "/")[0]
	}
	return strings.EqualFold(stripSchemes(endpointURL), stripSchemes(proxyKey))
}

// SelectEgressProxy 从账号出口池中选择本次请求使用的代理 URL（无熔断回调的
// 兼容入口，语义与 SelectEgressProxyHealthy(tripped=nil) 一致）。
func SelectEgressProxy(account *Account, inFlight func(proxyURL string) int64) string {
	return SelectEgressProxyHealthy(account, inFlight, nil)
}

// SelectEgressProxyHealthy 在 SelectEgressProxy 基础上增加出口熔断过滤：
//  0. tripped 回调返回 true 的端点（最近连续失败，代理已死）本轮跳过；
//     全部熔断时不跳过（总得发出去，且观测继续累积）。
func SelectEgressProxyHealthy(account *Account, inFlight func(proxyURL string) int64, tripped func(proxyURL string) bool) string {
	if account == nil {
		return ""
	}
	endpoints := account.ProxyEndpoints()
	if len(endpoints) == 0 {
		return ""
	}
	if len(endpoints) == 1 {
		return endpoints[0].URL
	}
	if inFlight == nil {
		return endpoints[0].URL
	}
	// 第一轮：熔断过滤 + 满载过滤 + 最小在途。
	bestIndex := -1
	var bestLoad int64
	for i, ep := range endpoints {
		if tripped != nil && tripped(ep.URL) {
			continue // 熔断：连续失败出口本轮避开。
		}
		load := inFlight(ep.URL)
		if load >= int64(ep.Concurrency) {
			continue // 满载：跳过。
		}
		if bestIndex < 0 || load < bestLoad {
			bestIndex, bestLoad = i, load
		}
	}
	if bestIndex >= 0 {
		return endpoints[bestIndex].URL
	}
	// 全部满载或熔断：忽略熔断重新按最小在途选（流量仍尽量分散，
	// 且失败观测继续累积，下一轮熔断判定样本更新）。
	bestIndex = 0
	bestLoad = inFlight(endpoints[0].URL)
	for i, ep := range endpoints[1:] {
		if load := inFlight(ep.URL); load < bestLoad {
			bestIndex, bestLoad = i+1, load
		}
	}
	return endpoints[bestIndex].URL
}
