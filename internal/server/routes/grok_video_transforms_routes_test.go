package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// hotSwitchSettingRepo 是只支撑开关读写的内存设置仓库，用于验证“保存即生效”。
type hotSwitchSettingRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func newHotSwitchSettingRepo() *hotSwitchSettingRepo {
	return &hotSwitchSettingRepo{values: map[string]string{}}
}

func (r *hotSwitchSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}

func (r *hotSwitchSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *hotSwitchSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *hotSwitchSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (r *hotSwitchSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}

func (r *hotSwitchSettingRepo) GetAll(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *hotSwitchSettingRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

// newGrokVideoTransformsTestRouter 复刻 gateway_test 的装配方式，但把真实 SettingService
// 注入路由，用于验证热开关不需要重建路由即可改变行为。
func newGrokVideoTransformsTestRouter(cfg *config.Config, settingService *service.SettingService, platform string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			groupID := int64(1)
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
				GroupID: &groupID,
				Group:   &service.Group{Platform: platform},
			})
			c.Next()
		}),
		nil,
		nil,
		nil,
		settingService,
		nil,
		cfg,
	)
	return router
}

var grokVideoTransformPaths = []string{
	"/v1/videos/edits",
	"/videos/edits",
	"/v1/videos/extensions",
	"/videos/extensions",
}

func callGrokVideoTransform(router *gin.Engine, path string) int {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(
		`{"model":"grok-imagine-video","prompt":"change it","video":{"url":"https://example.com/source.mp4"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

// TestGrokVideoTransformsHotToggleWithoutRestart 是本轮改造的核心契约：
// 路由始终注册，开关存数据库并立刻发布内存快照，因此同一个路由实例、不重启进程，
// 关闭→开启→再关闭都能立即改变对客户端的可见行为。
func TestGrokVideoTransformsHotToggleWithoutRestart(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1024 * 1024, TextMaxBodySize: 1024 * 1024}}
	repo := newHotSwitchSettingRepo()
	settingService := service.NewSettingService(repo, cfg) // 启动配置关闭
	router := newGrokVideoTransformsTestRouter(cfg, settingService, service.PlatformGrok)

	for _, path := range grokVideoTransformPaths {
		require.Equal(t, http.StatusNotFound, callGrokVideoTransform(router, path), "关闭时 path=%s", path)
	}

	require.NoError(t, settingService.SetGrokVideoTransformsSettings(context.Background(), &service.GrokVideoTransformsSettings{Enabled: true}))
	for _, path := range grokVideoTransformPaths {
		require.NotEqual(t, http.StatusNotFound, callGrokVideoTransform(router, path), "开启后 path=%s 必须放行到转发逻辑", path)
	}

	require.NoError(t, settingService.SetGrokVideoTransformsSettings(context.Background(), &service.GrokVideoTransformsSettings{Enabled: false}))
	for _, path := range grokVideoTransformPaths {
		require.Equal(t, http.StatusNotFound, callGrokVideoTransform(router, path), "再次关闭后 path=%s", path)
	}

	// 落库值与最后一次保存一致，重启后可恢复。
	require.Equal(t, "false", repo.values[service.SettingKeyGrokVideoTransformsEnabled])
}

// TestGrokVideoTransformsRoutesAlwaysRegistered 钉住路由表：关闭时也必须存在路由
// （否则热生效无从谈起），行为差异只体现在 handler 的 404 上。
func TestGrokVideoTransformsRoutesAlwaysRegistered(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1024 * 1024, TextMaxBodySize: 1024 * 1024}}
	router := newGrokVideoTransformsTestRouter(cfg, service.NewSettingService(newHotSwitchSettingRepo(), cfg), service.PlatformGrok)

	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, path := range grokVideoTransformPaths {
		require.True(t, registered["POST "+path], "路由必须常驻注册：%s", path)
	}
}
