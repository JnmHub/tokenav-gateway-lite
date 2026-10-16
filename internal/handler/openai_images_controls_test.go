package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResolveOpenAIImagesRoutingModelDoesNotLeakCompositeTargetAfterKeywordReroute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalGroupID := int64(110)
	effectiveGroupID := int64(111)
	requestModel := "gpt-image-public"
	ctx := service.WithCompositeRouteDecision(context.Background(), service.CompositeRouteDecision{
		Matched:        true,
		GroupID:        originalGroupID,
		PublicModel:    requestModel,
		TargetPlatform: service.PlatformOpenAI,
		UpstreamModel:  "gpt-image-original-composite",
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)

	// 模拟内容审计将本次请求切到普通允许关键词分组。旧 composite（聚合）决定
	// 仍留在请求上下文中，但不得再参与目标分组的映射和账号选择。
	apiKey := &service.APIKey{
		GroupID: &effectiveGroupID,
		Group: &service.Group{
			ID:       effectiveGroupID,
			Platform: service.PlatformOpenAI,
			ModelMapping: map[string]string{
				requestModel: "gpt-image-effective-group",
			},
		},
	}

	routingModel := resolveOpenAIImagesRoutingModel(c, apiKey, requestModel)
	require.Equal(t, requestModel, routingModel)
	mappedModel, mapped := apiKey.Group.ResolveModelMapping(routingModel)
	require.True(t, mapped)
	require.Equal(t, "gpt-image-effective-group", mappedModel)
}

func TestResolveOpenAIImagesRoutingModelKeepsCompositeTargetForFinalCompositeGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(112)
	ctx := service.WithCompositeRouteDecision(context.Background(), service.CompositeRouteDecision{
		Matched:        true,
		GroupID:        groupID,
		PublicModel:    "gpt-image-public",
		TargetPlatform: service.PlatformOpenAI,
		UpstreamModel:  "gpt-image-composite-target",
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)
	apiKey := &service.APIKey{GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformComposite}}

	require.Equal(t, "gpt-image-composite-target", resolveOpenAIImagesRoutingModel(c, apiKey, "gpt-image-public"))
}

func TestOpenAIGatewayHandlerImages_DisabledGroupRejectsBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-image-2","prompt":"draw","size":"1024x1024"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	groupID := int64(111)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      222,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			AllowImageGeneration: false,
		},
		User: &service.User{ID: 333},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 333, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}

	h.Images(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "permission_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
}

func TestOpenAIGatewayHandlerImages_OpenAIGroupRejectsGrokModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok-imagine-image","prompt":"draw"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	groupID := int64(112)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      223,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			Platform:             service.PlatformOpenAI,
			AllowImageGeneration: true,
		},
		User: &service.User{ID: 334},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 334, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}

	h.Images(c)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "model_not_found", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), "Grok gateway group")
}
