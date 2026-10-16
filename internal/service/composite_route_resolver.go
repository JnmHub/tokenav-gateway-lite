package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const maxCompositeRouteSnapshots = 4096

// compiledCompositeRoutes 是只读快照。精确匹配走哈希表，前缀在保存时预排序，
// 因此正常请求不访问数据库、不排序，也不复制整张规则表。
type compiledCompositeRoutes struct {
	exact  map[string]map[string][]CompositeModelRoute
	prefix map[string][]CompositeModelRoute
}

type CompositeRouteResolver struct {
	repo      CompositeModelRouteRepository
	snapshots sync.Map // map[int64]*compiledCompositeRoutes
	loadMu    sync.Mutex
}

func NewCompositeRouteResolver(repo CompositeModelRouteRepository) *CompositeRouteResolver {
	return &CompositeRouteResolver{repo: repo}
}

func (r *CompositeRouteResolver) Resolve(ctx context.Context, groupID int64, model, endpoint string) (CompositeRouteDecision, error) {
	model = strings.TrimSpace(model)
	endpoint = normalizeCompositeRouteEndpoint(endpoint)
	decision := CompositeRouteDecision{GroupID: groupID, PublicModel: model, Endpoint: endpoint}
	if model == "" {
		decision.Reason = "model is required"
		return decision, nil
	}

	if r != nil && r.repo != nil && groupID > 0 {
		snapshot, err := r.snapshot(ctx, groupID)
		if err != nil {
			return decision, fmt.Errorf("load composite routes: %w", err)
		}
		if route, ok := snapshot.match(model, endpoint); ok {
			upstreamModel := strings.TrimSpace(route.UpstreamModel)
			if upstreamModel == "" {
				upstreamModel = model
			}
			return CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceExplicit, GroupID: groupID,
				PublicModel: model, TargetPlatform: route.TargetPlatform, UpstreamModel: upstreamModel,
				Endpoint: endpoint, Route: &route}, nil
		}
	}
	if platform, ok := DetectModelPlatform(model); ok {
		return CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceDetector, GroupID: groupID,
			PublicModel: model, TargetPlatform: platform, UpstreamModel: model, Endpoint: endpoint}, nil
	}
	decision.Reason = "no explicit route or built-in detector match"
	return decision, nil
}

// RefreshGroup 在管理端修改规则后构建新快照并原子替换；旧请求仍安全使用旧快照。
func (r *CompositeRouteResolver) RefreshGroup(ctx context.Context, groupID int64) error {
	if r == nil || r.repo == nil || groupID <= 0 {
		return nil
	}
	routes, err := r.repo.ListByGroup(ctx, groupID, false)
	if err != nil {
		return err
	}
	r.storeSnapshot(groupID, compileCompositeRoutes(routes))
	return nil
}

func (r *CompositeRouteResolver) InvalidateGroup(groupID int64) {
	if r != nil && groupID > 0 {
		r.snapshots.Delete(groupID)
	}
}

// ListRoutesForPolicy 只在配置快照生成时读取规则，用于把真实路由事实上报给
// control-plane（主站控制面）。它不复用请求快照，避免管理展示反过来影响热路径。
func (r *CompositeRouteResolver) ListRoutesForPolicy(ctx context.Context, groupID int64) ([]CompositeModelRoute, error) {
	if r == nil || r.repo == nil || groupID <= 0 {
		return []CompositeModelRoute{}, nil
	}
	routes, err := r.repo.ListByGroup(ctx, groupID, false)
	if err != nil {
		return nil, err
	}
	return append([]CompositeModelRoute(nil), routes...), nil
}

func (r *CompositeRouteResolver) snapshot(ctx context.Context, groupID int64) (*compiledCompositeRoutes, error) {
	if value, ok := r.snapshots.Load(groupID); ok {
		return value.(*compiledCompositeRoutes), nil
	}
	// 冷启动同一时刻只允许一个加载者访问数据库，避免请求挤压把数据库打满。
	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	if value, ok := r.snapshots.Load(groupID); ok {
		return value.(*compiledCompositeRoutes), nil
	}
	routes, err := r.repo.ListByGroup(ctx, groupID, false)
	if err != nil {
		return nil, err
	}
	snapshot := compileCompositeRoutes(routes)
	r.storeSnapshot(groupID, snapshot)
	return snapshot, nil
}

func (r *CompositeRouteResolver) storeSnapshot(groupID int64, snapshot *compiledCompositeRoutes) {
	count := 0
	r.snapshots.Range(func(_, _ any) bool { count++; return count < maxCompositeRouteSnapshots })
	if count >= maxCompositeRouteSnapshots {
		// 极端配置量下整体清空比无界增长更安全；后续按分组冷加载，不创建后台协程或定时器。
		r.snapshots.Clear()
	}
	r.snapshots.Store(groupID, snapshot)
}

func compileCompositeRoutes(routes []CompositeModelRoute) *compiledCompositeRoutes {
	snapshot := &compiledCompositeRoutes{exact: make(map[string]map[string][]CompositeModelRoute), prefix: make(map[string][]CompositeModelRoute)}
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		route.PublicModel = strings.TrimSpace(route.PublicModel)
		if route.PublicModel == "" || !isConcreteRequestPlatform(route.TargetPlatform) {
			continue
		}
		route.Endpoint = normalizeCompositeRouteEndpoint(route.Endpoint)
		route.MatchType = normalizeCompositeRouteMatchType(route.MatchType)
		if route.MatchType == CompositeRouteMatchExact {
			byModel := snapshot.exact[route.Endpoint]
			if byModel == nil {
				byModel = make(map[string][]CompositeModelRoute)
				snapshot.exact[route.Endpoint] = byModel
			}
			byModel[route.PublicModel] = append(byModel[route.PublicModel], route)
		} else {
			snapshot.prefix[route.Endpoint] = append(snapshot.prefix[route.Endpoint], route)
		}
	}
	less := func(a, b CompositeModelRoute) bool {
		if len(a.PublicModel) != len(b.PublicModel) {
			return len(a.PublicModel) > len(b.PublicModel)
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.ID < b.ID
	}
	for _, byModel := range snapshot.exact {
		for model := range byModel {
			sort.SliceStable(byModel[model], func(i, j int) bool { return less(byModel[model][i], byModel[model][j]) })
		}
	}
	for endpoint := range snapshot.prefix {
		routes := snapshot.prefix[endpoint]
		sort.SliceStable(routes, func(i, j int) bool { return less(routes[i], routes[j]) })
		snapshot.prefix[endpoint] = routes
	}
	return snapshot
}

func (s *compiledCompositeRoutes) match(model, endpoint string) (CompositeModelRoute, bool) {
	if s == nil {
		return CompositeModelRoute{}, false
	}
	for _, scope := range []string{endpoint, CompositeRouteEndpointAny} {
		if byModel := s.exact[scope]; len(byModel[model]) > 0 {
			return byModel[model][0], true
		}
	}
	for _, scope := range []string{endpoint, CompositeRouteEndpointAny} {
		for _, route := range s.prefix[scope] {
			if strings.HasPrefix(model, route.PublicModel) {
				return route, true
			}
		}
	}
	return CompositeModelRoute{}, false
}

func matchCompositeRoute(routes []CompositeModelRoute, model, endpoint string) (CompositeModelRoute, bool) {
	return compileCompositeRoutes(routes).match(strings.TrimSpace(model), normalizeCompositeRouteEndpoint(endpoint))
}

// ResolveCompositeRouteFromRoutes 用于配置快照生成；它和请求解析器共用完全相同
// 的排序与内置检测规则，防止主站展示与真实请求得出两个答案。
func ResolveCompositeRouteFromRoutes(groupID int64, routes []CompositeModelRoute, model, endpoint string) CompositeRouteDecision {
	model = strings.TrimSpace(model)
	endpoint = normalizeCompositeRouteEndpoint(endpoint)
	if route, ok := matchCompositeRoute(routes, model, endpoint); ok {
		upstream := strings.TrimSpace(route.UpstreamModel)
		if upstream == "" {
			upstream = model
		}
		return CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceExplicit, GroupID: groupID,
			PublicModel: model, TargetPlatform: route.TargetPlatform, UpstreamModel: upstream, Endpoint: endpoint, Route: &route}
	}
	if platform, ok := DetectModelPlatform(model); ok {
		return CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceDetector, GroupID: groupID,
			PublicModel: model, TargetPlatform: platform, UpstreamModel: model, Endpoint: endpoint}
	}
	return CompositeRouteDecision{GroupID: groupID, PublicModel: model, Endpoint: endpoint, Reason: "no explicit route or built-in detector match"}
}
