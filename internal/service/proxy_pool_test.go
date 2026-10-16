package service

import (
	"context"
	"testing"
)

// stubProxyRepo 最小代理查询桩：NormalizeProxyPoolExtra 只依赖 ListByIDs。
type stubProxyRepo struct {
	byID map[int64]*Proxy
}

func (r *stubProxyRepo) ListByIDs(ctx context.Context, ids []int64) ([]Proxy, error) {
	out := make([]Proxy, 0, len(ids))
	for _, id := range ids {
		if p, ok := r.byID[id]; ok {
			out = append(out, *p)
		}
	}
	return out, nil
}

// NormalizeProxyPoolExtra：前端提交 {id,name}，后端补全 URL、剔除已删代理、空池删键。
func TestNormalizeProxyPoolExtra(t *testing.T) {
	repo := &stubProxyRepo{byID: map[int64]*Proxy{
		1: {ID: 1, Name: "法国", Protocol: "socks5", Host: "1.1.1.1", Port: 1080, Username: "u", Password: "p"},
		2: {ID: 2, Name: "美国", Protocol: "http", Host: "2.2.2.2", Port: 8080},
	}}

	// 正常补全：含凭据的完整 URL + 畸形条目剔除。
	extra := map[string]any{
		"proxy_pool": []any{
			map[string]any{"id": float64(1), "name": "法国"},
			map[string]any{"id": float64(2), "name": "美国"},
			"garbage",
			map[string]any{"id": float64(999), "name": "已删除"},
		},
	}
	out := NormalizeProxyPoolExtra(context.Background(), repo, extra)
	pool, ok := out["proxy_pool"].([]any)
	if !ok || len(pool) != 2 {
		t.Fatalf("池应剩 2 个有效条目: %+v", out["proxy_pool"])
	}
	first := pool[0].(map[string]any)
	if first["url"] != "socks5://u:p@1.1.1.1:1080" {
		t.Fatalf("应补全含凭据的完整 URL: %v", first["url"])
	}
	if first["name"] != "法国" {
		t.Fatalf("名称应保留: %v", first["name"])
	}

	// 空池：键删除。
	out = NormalizeProxyPoolExtra(context.Background(), repo, map[string]any{"proxy_pool": []any{}})
	if _, exists := out["proxy_pool"]; exists {
		t.Fatal("空池应删除 proxy_pool 键")
	}

	// 无池键：原样返回。
	untouched := map[string]any{"other": 1}
	out = NormalizeProxyPoolExtra(context.Background(), repo, untouched)
	if len(out) != 1 || out["other"] != 1 {
		t.Fatalf("无池键时不应改动 extra: %+v", out)
	}
}

func int64PtrOf(v int64) *int64 { return &v }

func TestProxyEndpoints_MergesMainAndPoolWithDedup(t *testing.T) {
	mainName := "主代理"
	account := &Account{
		ID:      1,
		ProxyID: int64PtrOf(10),
		Proxy:   &Proxy{ID: 10, Name: mainName, Protocol: "http", Host: "1.1.1.1", Port: 8080},
		Extra: map[string]any{
			"proxy_pool": []any{
				map[string]any{"id": float64(11), "name": "美国", "url": "http://2.2.2.2:8080"},
				map[string]any{"id": float64(10), "name": "主代理重复", "url": "http://1.1.1.1:8080"}, // URL 同主代理，应去重
				map[string]any{"id": float64(12), "name": "空URL", "url": ""},                        // 空 URL 跳过
				"畸形条目",
			},
		},
	}
	endpoints := account.ProxyEndpoints()
	if len(endpoints) != 2 {
		t.Fatalf("端点数 = %d, want 2（主代理+池1个有效项，重复与空项剔除）: %+v", len(endpoints), endpoints)
	}
	if endpoints[0].Name != mainName || endpoints[1].Name != "美国" {
		t.Fatalf("端点顺序/名称不符: %+v", endpoints)
	}
}

func TestSelectEgressProxy_PrefersLeastInFlight(t *testing.T) {
	account := &Account{
		ID: 7,
		Extra: map[string]any{
			"proxy_pool": []any{
				map[string]any{"id": float64(1), "name": "法国", "url": "http://fr:8080"},
				map[string]any{"id": float64(2), "name": "美国", "url": "http://us:8080"},
				map[string]any{"id": float64(3), "name": "新加坡", "url": "http://sg:8080"},
			},
		},
	}
	load := map[string]int64{"http://fr:8080": 3, "http://us:8080": 1, "http://sg:8080": 4}
	got := SelectEgressProxy(account, func(proxyURL string) int64 { return load[proxyURL] })
	if got != "http://us:8080" {
		t.Fatalf("应选最小在途代理, got %s", got)
	}
	// 并列时取顺序靠前者（fr 与 us 同载 → fr）。
	load["http://us:8080"] = 3
	if got := SelectEgressProxy(account, func(proxyURL string) int64 { return load[proxyURL] }); got != "http://fr:8080" {
		t.Fatalf("并列时应取顺序靠前者, got %s", got)
	}
	// 无代理账号返回直连空串。
	if got := SelectEgressProxy(&Account{ID: 8}, nil); got != "" {
		t.Fatalf("无代理账号应直连, got %s", got)
	}
}

// 出口熔断：tripped 回调标记的端点被避开；全部熔断时回退最小在途。
func TestSelectEgressProxyHealthy_SkipsTripped(t *testing.T) {
	account := &Account{
		ID:          11,
		Concurrency: 100,
		Extra: map[string]any{
			"proxy_pool": []any{
				map[string]any{"id": float64(1), "name": "死代理", "url": "http://dead:8080"},
				map[string]any{"id": float64(2), "name": "活代理", "url": "http://alive:8080"},
			},
		},
	}
	load := map[string]int64{"http://dead:8080": 0, "http://alive:8080": 5}
	tripped := func(url string) bool { return url == "http://dead:8080" }
	// 死代理在途更低（0），但熔断应被避开 → 选活代理。
	if got := SelectEgressProxyHealthy(account, func(u string) int64 { return load[u] }, tripped); got != "http://alive:8080" {
		t.Fatalf("熔断端点应被避开, got %s", got)
	}
	// 全部熔断：回退最小在途（dead 0）。
	allTripped := func(string) bool { return true }
	if got := SelectEgressProxyHealthy(account, func(u string) int64 { return load[u] }, allTripped); got != "http://dead:8080" {
		t.Fatalf("全部熔断应回退最小在途, got %s", got)
	}
	// 无熔断回调时与旧行为一致（最小在途=dead）。
	if got := SelectEgressProxy(account, func(u string) int64 { return load[u] }); got != "http://dead:8080" {
		t.Fatalf("无熔断回调应保持旧行为, got %s", got)
	}
}

// 每代理独立并发上限：满载端点被避开；全部满载时回退最小在途；
// 未设置上限的端点跟随账号并发。
func TestSelectEgressProxy_RespectsPerProxyConcurrency(t *testing.T) {
	account := &Account{
		ID:          9,
		Concurrency: 100, // 账号并发（默认上限）
		Extra: map[string]any{
			"proxy_pool": []any{
				map[string]any{"id": float64(1), "name": "法国", "url": "http://fr:8080", "concurrency": float64(2)},
				map[string]any{"id": float64(2), "name": "美国", "url": "http://us:8080"}, // 跟随账号 100
			},
		},
	}
	load := map[string]int64{"http://fr:8080": 2, "http://us:8080": 50}
	got := SelectEgressProxy(account, func(proxyURL string) int64 { return load[proxyURL] })
	if got != "http://us:8080" {
		t.Fatalf("法国已满载(2/2)应避开选美国, got %s", got)
	}
	// 全部满载：回退最小在途（法国 2 < 美国 100）。
	load["http://us:8080"] = 100
	if got := SelectEgressProxy(account, func(proxyURL string) int64 { return load[proxyURL] }); got != "http://fr:8080" {
		t.Fatalf("全部满载应回退最小在途, got %s", got)
	}
	// 端点折算：独立上限生效，未设项跟随账号并发。
	endpoints := account.ProxyEndpoints()
	if len(endpoints) != 2 || endpoints[0].Concurrency != 2 || endpoints[1].Concurrency != 100 {
		t.Fatalf("端点并发折算不符: %+v", endpoints)
	}
}

// 主代理独立并发上限（extra.proxy_main_concurrency）覆盖账号并发。
func TestMainProxyConcurrencyOverride(t *testing.T) {
	account := &Account{
		ID:          10,
		ProxyID:     int64PtrOf(20),
		Proxy:       &Proxy{ID: 20, Name: "主", Protocol: "http", Host: "1.1.1.1", Port: 8080},
		Concurrency: 100,
		Extra:       map[string]any{"proxy_main_concurrency": float64(5)},
	}
	if got := account.GetMainProxyConcurrency(); got != 5 {
		t.Fatalf("主代理覆盖并发应为 5, got %d", got)
	}
	endpoints := account.ProxyEndpoints()
	if len(endpoints) != 1 || endpoints[0].Concurrency != 5 {
		t.Fatalf("主代理端点并发应取覆盖值: %+v", endpoints)
	}
	// 未配置覆盖时跟随账号并发。
	account.Extra = nil
	if got := account.GetMainProxyConcurrency(); got != 0 {
		t.Fatalf("未配置覆盖应返回 0, got %d", got)
	}
	if endpoints := account.ProxyEndpoints(); len(endpoints) != 1 || endpoints[0].Concurrency != 100 {
		t.Fatalf("无覆盖时主代理并发应跟随账号: %+v", endpoints)
	}
}
