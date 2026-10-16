package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldRecordGrokMediaUsage(t *testing.T) {
	tests := []struct {
		name     string
		endpoint service.GrokMediaEndpoint
		model    string
		want     bool
	}{
		{name: "image generation records usage", endpoint: service.GrokMediaEndpointImagesGenerations, model: "grok-imagine", want: true},
		{name: "image edit records usage", endpoint: service.GrokMediaEndpointImagesEdits, model: "grok-imagine-edit", want: true},
		// 视频创建（Grok 生成/编辑/扩展与 Seedance 创建）都走异步结算，不在这里同步入账：
		// Grok 由数据库任务在终态结算，Seedance 由状态轮询按 completion_tokens 结算。
		// 对齐官方 shouldRecordGrokMediaUsage：create 与 lookup 一律 false；
		// 正常创建带 ResponseID 时由 videoUsageDeferred 保证不入账，这里把兜底口径也收紧为「不入账」。
		{name: "video generation defers usage to task settlement", endpoint: service.GrokMediaEndpointVideosGenerations, model: "grok-imagine-video-1.5", want: false},
		{name: "video edit defers usage to task settlement", endpoint: service.GrokMediaEndpointVideosEdits, model: "grok-imagine-video", want: false},
		{name: "video extension defers usage to task settlement", endpoint: service.GrokMediaEndpointVideosExtensions, model: "grok-imagine-video", want: false},
		{name: "seedance create defers usage to status polling", endpoint: service.SeedanceEndpointCreate, model: "seedance-video", want: false},
		{name: "seedance status never records usage", endpoint: service.SeedanceEndpointStatus, model: "seedance-video", want: false},
		{name: "seedance delete never records usage", endpoint: service.SeedanceEndpointDelete, model: "seedance-video", want: false},
		{name: "video status skips empty model usage", endpoint: service.GrokMediaEndpointVideoStatus, model: "", want: false},
		{name: "generation skips usage without model", endpoint: service.GrokMediaEndpointImagesGenerations, model: " ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldRecordGrokMediaUsage(tt.endpoint, tt.model))
		})
	}
}

func TestGrokMediaRequiredCapabilityKeepsStatusLookupAvailable(t *testing.T) {
	tests := []struct {
		endpoint service.GrokMediaEndpoint
		want     service.OpenAIEndpointCapability
	}{
		{endpoint: service.GrokMediaEndpointImagesGenerations, want: service.OpenAIEndpointCapabilityGrokMediaGeneration},
		{endpoint: service.GrokMediaEndpointImagesEdits, want: service.OpenAIEndpointCapabilityGrokMediaGeneration},
		{endpoint: service.GrokMediaEndpointVideosGenerations, want: service.OpenAIEndpointCapabilityGrokMediaGeneration},
		{endpoint: service.GrokMediaEndpointVideosEdits, want: service.OpenAIEndpointCapabilityGrokMediaGeneration},
		{endpoint: service.GrokMediaEndpointVideosExtensions, want: service.OpenAIEndpointCapabilityGrokMediaGeneration},
		{endpoint: service.GrokMediaEndpointVideoStatus, want: ""},
		{endpoint: service.GrokMediaEndpointVideoContent, want: ""},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, grokMediaRequiredCapability(tt.endpoint))
	}
}

func TestGrokVideoTaskOwnershipIsBoundToUserAndCreatingKey(t *testing.T) {
	apiKey := &service.APIKey{ID: 10}
	localTask := &service.GrokVideoTask{APIKeyID: 10, UserID: 42}
	require.True(t, grokVideoTaskBelongsToCaller(context.Background(), localTask, apiKey, 42))
	require.False(t, grokVideoTaskBelongsToCaller(context.Background(), localTask, &service.APIKey{ID: 11}, 42))
	require.False(t, grokVideoTaskBelongsToCaller(context.Background(), localTask, apiKey, 43))

	regionalTask := &service.GrokVideoTask{APIKeyID: 7001, GatewayKeyID: "remote-key-1", UserID: 42}
	matchingCtx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "request-1", KeyID: "remote-key-1", UserID: 42})
	otherCtx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "request-2", KeyID: "remote-key-2", UserID: 42})
	require.True(t, grokVideoTaskBelongsToCaller(matchingCtx, regionalTask, apiKey, 42))
	require.False(t, grokVideoTaskBelongsToCaller(otherCtx, regionalTask, apiKey, 42))
	require.False(t, grokVideoTaskBelongsToCaller(context.Background(), regionalTask, apiKey, 42), "gateway task IDs cannot fall back to the local API key ID")
}

func TestGrokMediaContentModerationBlocksBeforeRoutingAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)

	moderationConfig := &service.ContentModerationConfig{
		Enabled:             true,
		Mode:                service.ContentModerationModePreBlock,
		SampleRate:          100,
		AllGroups:           true,
		BlockMessage:        "Grok 媒体内容审计测试阻断",
		BlockedKeywords:     []string{"bad prompt"},
		KeywordBlockingMode: service.ContentModerationKeywordModeKeywordOnly,
	}
	rawConfig, err := json.Marshal(moderationConfig)
	require.NoError(t, err)

	moderationRepo := &contentModerationHandlerTestRepo{}
	settingRepo := &contentModerationHandlerSettingRepo{values: map[string]string{
		service.SettingKeyRiskControlEnabled:      "true",
		service.SettingKeyContentModerationConfig: string(rawConfig),
	}}
	moderationService := service.NewContentModerationService(
		settingRepo,
		moderationRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	handler := &OpenAIGatewayHandler{
		gatewayService:           &service.OpenAIGatewayService{},
		billingCacheService:      &service.BillingCacheService{},
		apiKeyService:            &service.APIKeyService{},
		contentModerationService: moderationService,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&concurrencyCacheMock{}), SSEPingFormatNone, time.Second),
		imageLimiter:             &imageConcurrencyLimiter{},
	}

	groupID := int64(9)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{
		"model":"grok-imagine-image",
		"prompt":"bad prompt"
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      901,
		GroupID: &groupID,
		User:    &service.User{ID: 902, Email: "blocked-grok@example.com"},
		Group: &service.Group{
			ID:                   groupID,
			Name:                 "Grok",
			Platform:             service.PlatformGrok,
			Status:               service.StatusActive,
			Hydrated:             true,
			AllowImageGeneration: true,
		},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 902, Concurrency: 1})

	handler.GrokImages(c)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "keyword_allowed_group_unavailable")
	require.Contains(t, recorder.Body.String(), "Grok 媒体内容审计测试阻断")
	require.Eventually(t, func() bool {
		return len(moderationRepo.logSnapshot()) == 1
	}, time.Second, 10*time.Millisecond)
	logs := moderationRepo.logSnapshot()
	require.Equal(t, service.ContentModerationActionKeywordBlock, logs[0].Action)
	require.Equal(t, "/v1/images/generations", logs[0].Endpoint)
	require.True(t, logs[0].Flagged)
}
