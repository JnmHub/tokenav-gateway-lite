//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayHandlerChatCompletionsRejectsImageModelsBeforeRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gpt-image-1", "gpt-image-2"} {
		t.Run(model, func(t *testing.T) {
			cfg := &config.Config{RunMode: config.RunModeSimple}
			billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billingCacheSvc.Stop)
			h := &OpenAIGatewayHandler{
				gatewayService:      &service.OpenAIGatewayService{},
				billingCacheService: billingCacheSvc,
				apiKeyService:       &service.APIKeyService{},
				concurrencyHelper: &ConcurrencyHelper{concurrencyService: service.NewConcurrencyService(
					&helperConcurrencyCacheStub{userSeq: []bool{true}},
				)},
				cfg: cfg,
			}

			groupID := int64(6401)
			userID := int64(6402)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(
				http.MethodPost,
				"/v1/chat/completions",
				strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"draw"}]}`),
			)
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
				ID: 6403, GroupID: &groupID,
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
				User:  &service.User{ID: userID, Status: service.StatusActive, Balance: 100},
			})
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID, Concurrency: 1})

			h.ChatCompletions(c)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), "Image generation models are not available on /v1/chat/completions")
			require.Contains(t, rec.Body.String(), "/v1/images/generations")
		})
	}
}
