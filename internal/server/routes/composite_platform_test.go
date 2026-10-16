package routes

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCompositeRuntimeGateChangesWithoutRebuildingRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var enabled atomic.Bool
	router := gin.New()
	router.Use(compositeRouteTestAuth(7), compositeTargetPlatformMiddlewareWithGate(nil, enabled.Load))
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := func() int {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		return recorder.Code
	}

	require.Equal(t, http.StatusServiceUnavailable, request())
	enabled.Store(true)
	require.Equal(t, http.StatusNoContent, request())
	enabled.Store(false)
	require.Equal(t, http.StatusServiceUnavailable, request())
}

func TestCompositeGeminiRuntimeGateChangesWithoutRebuildingRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var enabled atomic.Bool
	router := gin.New()
	router.Use(compositeRouteTestAuth(7), compositeGeminiTargetPlatformMiddlewareWithGate(nil, enabled.Load))
	router.POST("/gemini", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := func() int {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/gemini", nil))
		return recorder.Code
	}

	require.Equal(t, http.StatusServiceUnavailable, request())
	enabled.Store(true)
	require.Equal(t, http.StatusNoContent, request())
}

type routeCompositeRepoStub struct {
	routes []service.CompositeModelRoute
}

func (s routeCompositeRepoStub) ListByGroup(_ context.Context, groupID int64, includeDisabled bool) ([]service.CompositeModelRoute, error) {
	result := make([]service.CompositeModelRoute, 0, len(s.routes))
	for _, route := range s.routes {
		if route.GroupID == groupID && (includeDisabled || route.Enabled) {
			result = append(result, route)
		}
	}
	return result, nil
}
func (routeCompositeRepoStub) Create(context.Context, *service.CompositeModelRoute) error { return nil }
func (routeCompositeRepoStub) Update(context.Context, *service.CompositeModelRoute) error { return nil }
func (routeCompositeRepoStub) Delete(context.Context, int64) error                        { return nil }
func (routeCompositeRepoStub) DeleteByGroup(context.Context, int64) error                 { return nil }

func compositeRouteTestAuth(groupID int64) gin.HandlerFunc {
	return gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite},
		})
		c.Next()
	}))
}

func concreteRouteTestAuth(groupID int64) gin.HandlerFunc {
	return gin.HandlerFunc(servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
		})
		c.Next()
	}))
}

func TestCompositeTargetPlatformMiddlewareSkipsConcreteGroupWithoutResolverRead(t *testing.T) {
	repo := &routeCompositeCountingRepo{}
	resolver := service.NewCompositeRouteResolver(repo)
	router := gin.New()
	router.Use(concreteRouteTestAuth(3), compositeTargetPlatformMiddleware(resolver, false))
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Zero(t, repo.listCalls, "普通分组不得读取聚合路由或请求体")
}

func TestCompositeTargetPlatformMiddlewareBindsResolverForWebSocketFirstFrame(t *testing.T) {
	resolver := service.NewCompositeRouteResolver(&routeCompositeRepoStub{})
	router := gin.New()
	router.Use(compositeRouteTestAuth(7), compositeTargetPlatformMiddleware(resolver))
	router.GET("/responses", func(c *gin.Context) {
		value, exists := c.Get(service.CompositeRouteResolverGinContextKey)
		require.True(t, exists)
		require.Same(t, resolver, value)
		_, resolved := service.ResolvedTargetPlatformFromContext(c.Request.Context())
		require.False(t, resolved, "WebSocket 必须等第一帧模型到达后才固化目标平台")
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/responses", nil)
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

type routeCompositeCountingRepo struct{ listCalls int }

func (r *routeCompositeCountingRepo) ListByGroup(context.Context, int64, bool) ([]service.CompositeModelRoute, error) {
	r.listCalls++
	return nil, nil
}
func (*routeCompositeCountingRepo) Create(context.Context, *service.CompositeModelRoute) error {
	return nil
}
func (*routeCompositeCountingRepo) Update(context.Context, *service.CompositeModelRoute) error {
	return nil
}
func (*routeCompositeCountingRepo) Delete(context.Context, int64) error        { return nil }
func (*routeCompositeCountingRepo) DeleteByGroup(context.Context, int64) error { return nil }

func BenchmarkCompositeMiddlewareConcreteGroupFastPath(b *testing.B) {
	gin.SetMode(gin.TestMode)
	for _, withCompositeMiddleware := range []bool{false, true} {
		name := "baseline"
		if withCompositeMiddleware {
			name = "composite_middleware"
		}
		b.Run(name, func(b *testing.B) {
			router := gin.New()
			router.Use(concreteRouteTestAuth(3))
			if withCompositeMiddleware {
				router.Use(compositeTargetPlatformMiddleware(nil, false))
			}
			router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recorder := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
				router.ServeHTTP(recorder, req)
			}
		})
	}
}

func TestCompositeTargetPlatformMiddlewareResolvesAndRestoresJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(compositeRouteTestAuth(1), compositeTargetPlatformMiddleware(nil))
	router.POST("/v1/responses", func(c *gin.Context) {
		platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, service.PlatformOpenAI, platform)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"gpt-5"}`, string(body))
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestCompositeTargetPlatformMiddlewareExplicitAliasRewritesJSON(t *testing.T) {
	resolver := service.NewCompositeRouteResolver(routeCompositeRepoStub{routes: []service.CompositeModelRoute{{
		ID: 1, GroupID: 7, PublicModel: "public-gpt", MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5", Endpoint: service.CompositeRouteEndpointResponses, Enabled: true,
	}}})
	router := gin.New()
	router.Use(compositeRouteTestAuth(7), compositeTargetPlatformMiddleware(resolver))
	router.POST("/v1/responses", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"gpt-5","input":[]}`, string(body))
		publicModel, ok := service.RequestedPublicModelFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, "public-gpt", publicModel)
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"public-gpt","input":[]}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestCompositeTargetPlatformMiddlewareRewritesNestedLiveModel(t *testing.T) {
	resolver := service.NewCompositeRouteResolver(routeCompositeRepoStub{routes: []service.CompositeModelRoute{{
		ID: 1, GroupID: 7, PublicModel: "live-alias", MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-live", Endpoint: service.CompositeRouteEndpointResponses, Enabled: true,
	}}})
	router := gin.New()
	router.Use(compositeRouteTestAuth(7), compositeTargetPlatformMiddleware(resolver))
	router.POST("/backend-api/codex/realtime/calls", func(c *gin.Context) {
		platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, service.PlatformOpenAI, platform)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"session":{"model":"gpt-live"},"sdp":"v=0"}`, string(body))
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/backend-api/codex/realtime/calls",
		strings.NewReader(`{"session":{"model":"live-alias"},"sdp":"v=0"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestCompositeRequestModelFromMultipartLiveSession(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("sdp", "v=0"))
	require.NoError(t, writer.WriteField("session", `{"model":"live-alias"}`))
	require.NoError(t, writer.Close())

	require.Equal(t, "live-alias", compositeRequestModelFromBody(writer.FormDataContentType(), body.Bytes()))
}

func TestCompositeCodexControlPathsUseResponsesRoutes(t *testing.T) {
	for _, path := range []string{
		"/v1/alpha/search",
		"/backend-api/codex/alpha/search",
		"/v1/live",
		"/backend-api/codex/realtime/calls",
	} {
		require.Equal(t, service.CompositeRouteEndpointResponses, compositeRouteEndpointForPath(path), "path=%s", path)
	}
}

func TestCompositeTargetPlatformMiddlewareFailsUnknownModelWith400(t *testing.T) {
	router := gin.New()
	router.Use(compositeRouteTestAuth(1), compositeTargetPlatformMiddleware(nil))
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"unknown-provider-model"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "not supported by composite groups")
}

func TestCompositeTargetPlatformMiddlewareRejectsWhenFeatureDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(compositeRouteTestAuth(1), compositeTargetPlatformMiddleware(nil, false))
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "disabled")
}

func TestCompositeTargetPlatformMiddlewareReadsMultipartModel(t *testing.T) {
	resolver := service.NewCompositeRouteResolver(routeCompositeRepoStub{routes: []service.CompositeModelRoute{{
		ID: 1, GroupID: 9, PublicModel: "image-alias", MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-image-1", Endpoint: service.CompositeRouteEndpointImages, Enabled: true,
	}}})
	router := gin.New()
	router.Use(compositeRouteTestAuth(9), compositeTargetPlatformMiddleware(resolver))
	router.POST("/v1/images/edits", func(c *gin.Context) {
		model, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, "gpt-image-1", model)
		c.Status(http.StatusNoContent)
	})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "image-alias"))
	require.NoError(t, writer.WriteField("prompt", "draw"))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestCompositeGeminiMiddlewareResolvesAllNativeActionsAndRewritesPathModelParam(t *testing.T) {
	resolver := service.NewCompositeRouteResolver(routeCompositeRepoStub{routes: []service.CompositeModelRoute{{
		ID: 1, GroupID: 11, PublicModel: "gemini-alias", MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformGemini, UpstreamModel: "gemini-2.5-pro", Endpoint: service.CompositeRouteEndpointGemini, Enabled: true,
	}}})
	for _, action := range []string{"generateContent", "streamGenerateContent", "countTokens"} {
		t.Run(action, func(t *testing.T) {
			router := gin.New()
			router.Use(compositeRouteTestAuth(11), compositeGeminiTargetPlatformMiddleware(resolver))
			router.POST("/v1beta/models/*modelAction", func(c *gin.Context) {
				require.Equal(t, "/gemini-2.5-pro:"+action, c.Param("modelAction"))
				platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
				require.True(t, ok)
				require.Equal(t, service.PlatformGemini, platform)
				publicModel, ok := service.RequestedPublicModelFromContext(c.Request.Context())
				require.True(t, ok)
				require.Equal(t, "gemini-alias", publicModel)
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-alias:"+action, strings.NewReader(`{"contents":[]}`))
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			require.Equal(t, http.StatusNoContent, recorder.Code)
		})
	}
}

func TestCompositeGeminiMiddlewareRejectsCrossPlatformRoute(t *testing.T) {
	resolver := service.NewCompositeRouteResolver(routeCompositeRepoStub{routes: []service.CompositeModelRoute{{
		ID: 1, GroupID: 12, PublicModel: "bad-alias", MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5", Endpoint: service.CompositeRouteEndpointGemini, Enabled: true,
	}}})
	router := gin.New()
	router.Use(compositeRouteTestAuth(12), compositeGeminiTargetPlatformMiddleware(resolver))
	router.POST("/v1beta/models/*modelAction", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/bad-alias:generateContent", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
