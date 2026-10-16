package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCompositeTargetPlatformAllowedKnownAndUnknown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite}}
	for _, tc := range []struct {
		model   string
		allowed bool
	}{
		{model: "text-embedding-3-large", allowed: true},
		{model: "claude-sonnet-4-5", allowed: false},
		{model: "unknown-provider-model", allowed: false},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/embeddings", nil)
		require.Equal(t, tc.allowed, compositeTargetPlatformAllowed(c, apiKey, tc.model, service.PlatformOpenAI), tc.model)
	}
}

func TestGeminiNativeEndpointAllowedUsesResolvedCompositeTarget(t *testing.T) {
	tests := []struct {
		name     string
		group    *service.Group
		resolved string
		model    string
		allowed  bool
	}{
		{name: "普通Gemini分组", group: &service.Group{Platform: service.PlatformGemini}, allowed: true},
		{name: "普通OpenAI分组", group: &service.Group{Platform: service.PlatformOpenAI}, allowed: false},
		{name: "聚合目标为Gemini", group: &service.Group{Platform: service.PlatformComposite}, resolved: service.PlatformGemini, model: "gemini-alias", allowed: true},
		{name: "聚合目标为OpenAI", group: &service.Group{Platform: service.PlatformComposite}, resolved: service.PlatformOpenAI, model: "gemini-alias", allowed: false},
		{name: "聚合未知模型失败关闭", group: &service.Group{Platform: service.PlatformComposite}, model: "unknown-provider-model", allowed: false},
		{name: "缺少分组", allowed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/"+tt.model+":generateContent", nil)
			if tt.resolved != "" {
				c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), tt.resolved))
			}
			require.Equal(t, tt.allowed, geminiNativeEndpointAllowed(c, &service.APIKey{Group: tt.group}, tt.model))
		})
	}
}

func TestCompositeWebSocketExplicitAliasUsesBoundResolver(t *testing.T) {
	repo := &handlerCompositeRepoStub{routes: []service.CompositeModelRoute{{
		ID: 1, GroupID: 4, PublicModel: "ws-alias", MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5", Endpoint: service.CompositeRouteEndpointResponses, Enabled: true,
	}}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
	c.Set(service.CompositeRouteResolverGinContextKey, service.NewCompositeRouteResolver(repo))
	apiKey := &service.APIKey{Group: &service.Group{ID: 4, Platform: service.PlatformComposite}}
	require.True(t, compositeTargetPlatformAllowed(c, apiKey, "ws-alias", service.PlatformOpenAI))
	upstream, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context())
	require.True(t, ok)
	require.Equal(t, "gpt-5", upstream)
}

type handlerCompositeRepoStub struct{ routes []service.CompositeModelRoute }

func (s *handlerCompositeRepoStub) ListByGroup(context.Context, int64, bool) ([]service.CompositeModelRoute, error) {
	return s.routes, nil
}
func (*handlerCompositeRepoStub) Create(context.Context, *service.CompositeModelRoute) error {
	return nil
}
func (*handlerCompositeRepoStub) Update(context.Context, *service.CompositeModelRoute) error {
	return nil
}
func (*handlerCompositeRepoStub) Delete(context.Context, int64) error        { return nil }
func (*handlerCompositeRepoStub) DeleteByGroup(context.Context, int64) error { return nil }

func TestClientRequestedUsageFieldsPreservesCompositeAlias(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), service.CompositeRouteDecision{
		Matched: true, PublicModel: "public-alias", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5",
	}))
	fields := clientRequestedUsageFields(c, service.ChannelMappingResult{MappedModel: "gpt-5"}, "gpt-5", "gpt-5")
	require.Equal(t, "public-alias", fields.OriginalModel)
	require.Equal(t, "public-alias", fields.ChannelMappedModel)
	require.Equal(t, "public-alias→gpt-5", fields.ModelMappingChain)
}

func TestOpenAIReasoningEffortPolicyForCompositeTarget(t *testing.T) {
	group := &service.Group{
		Platform:           service.PlatformComposite,
		MaxReasoningEffort: "medium",
		ReasoningEffortMappings: []service.ReasoningEffortMapping{
			{From: "max", To: "xhigh"},
		},
	}
	apiKey := &service.APIKey{Group: group}
	body := []byte(`{"reasoning":{"effort":"max"}}`)

	openAICtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	openAICtx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	openAICtx.Request = openAICtx.Request.WithContext(service.WithResolvedTargetPlatform(openAICtx.Request.Context(), service.PlatformOpenAI))
	got, changed := applyOpenAIReasoningEffortPolicyForRequest(openAICtx, apiKey, body)
	require.True(t, changed)
	require.JSONEq(t, `{"reasoning":{"effort":"medium"}}`, string(got))

	bindOpenAIReasoningEffortPolicyForMessagesRequest(openAICtx, apiKey, []byte(`{"output_config":{"effort":"max"}}`))
	got, changed = service.ApplyOpenAIReasoningEffortPolicyFromContext(openAICtx.Request.Context(), body)
	require.True(t, changed)
	require.JSONEq(t, `{"reasoning":{"effort":"medium"}}`, string(got))

	omittedCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	omittedCtx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	omittedCtx.Request = omittedCtx.Request.WithContext(service.WithResolvedTargetPlatform(omittedCtx.Request.Context(), service.PlatformOpenAI))
	bindOpenAIReasoningEffortPolicyForMessagesRequest(omittedCtx, apiKey, []byte(`{"model":"gpt-5"}`))
	got, changed = service.ApplyOpenAIReasoningEffortPolicyFromContext(omittedCtx.Request.Context(), body)
	require.False(t, changed)
	require.Equal(t, body, got)

	grokCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	grokCtx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	grokCtx.Request = grokCtx.Request.WithContext(service.WithResolvedTargetPlatform(grokCtx.Request.Context(), service.PlatformGrok))
	got, changed = applyOpenAIReasoningEffortPolicyForRequest(grokCtx, apiKey, body)
	require.False(t, changed)
	require.Equal(t, body, got)
}
