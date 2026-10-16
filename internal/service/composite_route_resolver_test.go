package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type compositeRouteRepoStub struct {
	routes []CompositeModelRoute
	lists  atomic.Int64
}

func (s *compositeRouteRepoStub) ListByGroup(_ context.Context, groupID int64, includeDisabled bool) ([]CompositeModelRoute, error) {
	s.lists.Add(1)
	routes := make([]CompositeModelRoute, 0, len(s.routes))
	for _, route := range s.routes {
		if route.GroupID == groupID && (includeDisabled || route.Enabled) {
			routes = append(routes, route)
		}
	}
	return routes, nil
}
func (*compositeRouteRepoStub) Create(context.Context, *CompositeModelRoute) error { return nil }
func (*compositeRouteRepoStub) Update(context.Context, *CompositeModelRoute) error { return nil }
func (*compositeRouteRepoStub) Delete(context.Context, int64) error                { return nil }
func (*compositeRouteRepoStub) DeleteByGroup(context.Context, int64) error         { return nil }

func TestCompositeRouteResolverDeterministicPriority(t *testing.T) {
	repo := &compositeRouteRepoStub{routes: []CompositeModelRoute{
		{ID: 1, GroupID: 7, PublicModel: "router/", MatchType: CompositeRouteMatchPrefix, TargetPlatform: PlatformAnthropic, Endpoint: CompositeRouteEndpointAny, Priority: 1, Enabled: true},
		{ID: 2, GroupID: 7, PublicModel: "router/gpt-", MatchType: CompositeRouteMatchPrefix, TargetPlatform: PlatformOpenAI, UpstreamModel: "gpt-family", Endpoint: CompositeRouteEndpointResponses, Priority: 100, Enabled: true},
		{ID: 3, GroupID: 7, PublicModel: "router/gpt-5", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformGrok, UpstreamModel: "grok-wrong-endpoint", Endpoint: CompositeRouteEndpointMessages, Priority: 1, Enabled: true},
		{ID: 4, GroupID: 7, PublicModel: "router/gpt-5", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAI, UpstreamModel: "gpt-5", Endpoint: CompositeRouteEndpointResponses, Priority: 50, Enabled: true},
	}}
	resolver := NewCompositeRouteResolver(repo)
	decision, err := resolver.Resolve(context.Background(), 7, "router/gpt-5", CompositeRouteEndpointResponses)
	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Equal(t, int64(4), decision.Route.ID, "精确规则优先于前缀，接口专用优先于 any")
	require.Equal(t, PlatformOpenAI, decision.TargetPlatform)
	require.Equal(t, "gpt-5", decision.UpstreamModel)
}

func TestCompositeRouteResolverFallsBackAndFailsClosed(t *testing.T) {
	resolver := NewCompositeRouteResolver(&compositeRouteRepoStub{})
	known, err := resolver.Resolve(context.Background(), 7, "grok-4", CompositeRouteEndpointResponses)
	require.NoError(t, err)
	require.True(t, known.Matched)
	require.Equal(t, CompositeRouteSourceDetector, known.Source)
	require.Equal(t, PlatformGrok, known.TargetPlatform)

	unknown, err := resolver.Resolve(context.Background(), 7, "llama-mystery", CompositeRouteEndpointResponses)
	require.NoError(t, err)
	require.False(t, unknown.Matched)
	require.Contains(t, unknown.Reason, "no explicit route")
}

func TestCompositeRouteResolverSnapshotAvoidsHotPathDatabaseQueries(t *testing.T) {
	repo := &compositeRouteRepoStub{routes: []CompositeModelRoute{{ID: 1, GroupID: 7, PublicModel: "gpt-", MatchType: CompositeRouteMatchPrefix, TargetPlatform: PlatformOpenAI, Endpoint: CompositeRouteEndpointAny, Enabled: true}}}
	resolver := NewCompositeRouteResolver(repo)
	for i := 0; i < 1000; i++ {
		decision, err := resolver.Resolve(context.Background(), 7, fmt.Sprintf("gpt-%d", i), CompositeRouteEndpointResponses)
		require.NoError(t, err)
		require.True(t, decision.Matched)
	}
	require.Equal(t, int64(1), repo.lists.Load(), "同一分组仅冷加载一次，正常请求不得反复查数据库")
}

func TestCompositeRouteResolverRefreshReplacesSnapshot(t *testing.T) {
	repo := &compositeRouteRepoStub{routes: []CompositeModelRoute{{ID: 1, GroupID: 7, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAI, UpstreamModel: "gpt-5", Endpoint: CompositeRouteEndpointAny, Enabled: true}}}
	resolver := NewCompositeRouteResolver(repo)
	first, err := resolver.Resolve(context.Background(), 7, "alias", CompositeRouteEndpointAny)
	require.NoError(t, err)
	require.Equal(t, "gpt-5", first.UpstreamModel)
	repo.routes[0].UpstreamModel = "gpt-5.6-sol"
	require.NoError(t, resolver.RefreshGroup(context.Background(), 7))
	second, err := resolver.Resolve(context.Background(), 7, "alias", CompositeRouteEndpointAny)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", second.UpstreamModel)
}

func TestCompositeRouteResolverSnapshotCountIsBounded(t *testing.T) {
	resolver := NewCompositeRouteResolver(nil)
	empty := compileCompositeRoutes(nil)
	for groupID := int64(1); groupID <= maxCompositeRouteSnapshots+1; groupID++ {
		resolver.storeSnapshot(groupID, empty)
	}
	count := 0
	resolver.snapshots.Range(func(_, _ any) bool {
		count++
		return true
	})
	require.LessOrEqual(t, count, maxCompositeRouteSnapshots)
}

func TestDetectModelPlatform(t *testing.T) {
	tests := map[string]string{
		"claude-sonnet-4-6": PlatformAnthropic, "anthropic/claude-opus-4-6": PlatformAnthropic,
		"gpt-5.6-sol": PlatformOpenAI, "o3-mini": PlatformOpenAI, "text-embedding-3-large": PlatformOpenAI,
		"gemini-3-pro": PlatformGemini, "models/gemini-2.5-flash": PlatformGemini,
		"grok-4": PlatformGrok, "xai/grok-4": PlatformGrok,
		"k3": PlatformKimi, "k3-256k": PlatformKimi, "kimi-code/k3": PlatformKimi,
	}
	for model, want := range tests {
		got, ok := DetectModelPlatform(model)
		require.True(t, ok, model)
		require.Equal(t, want, got, model)
	}
	_, ok := DetectModelPlatform("llama-4-maverick")
	require.False(t, ok)
	_, ok = DetectModelPlatform("k3-preview")
	require.False(t, ok)
}

func BenchmarkCompositeRouteResolver1000Rules(b *testing.B) {
	routes := make([]CompositeModelRoute, 0, 1000)
	for i := 0; i < 1000; i++ {
		routes = append(routes, CompositeModelRoute{ID: int64(i + 1), GroupID: 7, PublicModel: fmt.Sprintf("alias/%04d/", i), MatchType: CompositeRouteMatchPrefix, TargetPlatform: PlatformOpenAI, Endpoint: CompositeRouteEndpointAny, Enabled: true})
	}
	resolver := NewCompositeRouteResolver(&compositeRouteRepoStub{routes: routes})
	_, _ = resolver.Resolve(context.Background(), 7, "alias/0999/model", CompositeRouteEndpointResponses)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = resolver.Resolve(context.Background(), 7, "alias/0999/model", CompositeRouteEndpointResponses)
	}
}

func TestCompositeRouteDecisionContextRoundTrip(t *testing.T) {
	decision := CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceExplicit, PublicModel: "public", UpstreamModel: "gpt-5", TargetPlatform: PlatformOpenAI}
	ctx := WithCompositeRouteDecision(context.Background(), decision)
	platform, ok := ResolvedTargetPlatformFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, PlatformOpenAI, platform)
	upstream, ok := ResolvedUpstreamModelFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "gpt-5", upstream)
	requested, ok := RequestedPublicModelFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "public", requested)
}
