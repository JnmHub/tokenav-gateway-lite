package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type messagesMappingChannelRepoStub struct {
	service.ChannelRepository
}

func (messagesMappingChannelRepoStub) ListAll(context.Context) ([]service.Channel, error) {
	return nil, nil
}

func (messagesMappingChannelRepoStub) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{}, nil
}

func TestMessagesModelMappingUsesFinalModerationGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalID, effectiveID := int64(10), int64(20)
	original := &service.Group{
		ID: originalID, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true,
		ModelMapping: map[string]string{"customer-alias": "claude-original"},
	}
	effective := &service.Group{
		ID: effectiveID, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true,
		ModelMapping: map[string]string{"customer-alias": "claude-effective"},
	}
	apiKey := &service.APIKey{GroupID: &originalID, Group: original}

	gatewayService := newFinalGroupMappingGatewayService()
	requestContext := context.WithValue(context.Background(), ctxkey.Group, original)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(requestContext)
	handler := &GatewayHandler{gatewayService: gatewayService}
	before := handler.resolveMessagesChannelMapping(c, apiKey, "customer-alias")
	require.Equal(t, "claude-original", before.MappedModel)

	// 模拟内容审计将当前请求切到允许关键词分组；持久绑定不参与本次解析。
	apiKey.GroupID = &effectiveID
	apiKey.Group = effective
	requestContext = context.WithValue(requestContext, ctxkey.Group, effective)
	c.Request = c.Request.WithContext(requestContext)
	after := handler.resolveMessagesChannelMapping(c, apiKey, "customer-alias")
	require.True(t, after.Mapped)
	require.Equal(t, "claude-effective", after.MappedModel)
	require.Equal(t, []string{"customer-alias", "claude-effective"}, after.MappingChain)
}

type finalGroupMappingCase struct {
	name     string
	endpoint string
	platform string
	source   string
	target   string
}

func TestAuditedProtocolMappingsUseFinalGroup(t *testing.T) {
	tests := []finalGroupMappingCase{
		{name: "Chat", endpoint: "/v1/chat/completions", platform: service.PlatformOpenAI, source: "public-chat", target: "gpt-5.6-sol"},
		{name: "Responses", endpoint: "/v1/responses", platform: service.PlatformOpenAI, source: "public-responses", target: "gpt-5.6-sol"},
		{name: "Images", endpoint: "/v1/images/generations", platform: service.PlatformOpenAI, source: "public-image", target: "gpt-image-2"},
		{name: "Gemini", endpoint: "/v1beta/models/public-gemini:generateContent", platform: service.PlatformGemini, source: "public-gemini", target: "gemini-3.1-pro"},
		{name: "WebSocket", endpoint: "/openai/v1/responses", platform: service.PlatformOpenAI, source: "public-ws", target: "gpt-5.6-sol"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gatewayService := newFinalGroupMappingGatewayService()
			originalID := int64(100 + index)
			effectiveID := int64(200 + index)
			original := hydratedMappingGroup(originalID, test.platform, test.source, "old-upstream")
			effective := hydratedMappingGroup(effectiveID, test.platform, test.source, test.target)
			apiKey := &service.APIKey{GroupID: &originalID, Group: original}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, test.endpoint, nil).
				WithContext(context.WithValue(context.Background(), ctxkey.Group, original))

			// 与 runContentModeration 的请求内切组动作保持一致。
			apiKey.GroupID, apiKey.Group = &effectiveID, effective
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, effective))
			mapping, _ := gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, test.source)

			require.True(t, mapping.Mapped)
			require.Equal(t, test.target, mapping.MappedModel)
			require.Equal(t, []string{test.source, test.target}, mapping.MappingChain)
		})
	}
}

func TestEmbeddingsMappingRewritesForwardBody(t *testing.T) {
	gatewayService := newFinalGroupMappingGatewayService()
	groupID := int64(301)
	group := hydratedMappingGroup(groupID, service.PlatformOpenAI, "public-embedding", "text-embedding-3-large")
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	mapping, _ := gatewayService.ResolveChannelMappingAndRestrict(ctx, &groupID, "public-embedding")
	body := []byte(`{"model":"public-embedding","input":"hello"}`)
	forwardBody := body
	if mapping.Mapped {
		forwardBody = gatewayService.ReplaceModelInBody(body, mapping.MappedModel)
	}

	require.True(t, mapping.Mapped)
	require.Equal(t, "text-embedding-3-large", gjson.GetBytes(forwardBody, "model").String())
	require.Equal(t, "hello", gjson.GetBytes(forwardBody, "input").String())
}

func TestOpenAIWSContextAfterModerationUsesReroutedGroup(t *testing.T) {
	gatewayService := newFinalGroupMappingGatewayService()
	originalID, effectiveID := int64(401), int64(402)
	original := hydratedMappingGroup(originalID, service.PlatformOpenAI, "public-ws", "old-upstream")
	effective := hydratedMappingGroup(effectiveID, service.PlatformOpenAI, "public-ws", "gpt-5.6-sol")
	oldContext := context.WithValue(context.Background(), ctxkey.Group, original)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/openai/v1/responses", nil).
		WithContext(context.WithValue(oldContext, ctxkey.Group, effective))

	finalContext := openAIWSContextAfterModeration(c, oldContext)
	mapping, _ := gatewayService.ResolveChannelMappingAndRestrict(finalContext, &effectiveID, "public-ws")

	require.True(t, mapping.Mapped)
	require.Equal(t, "gpt-5.6-sol", mapping.MappedModel)
	requestGroup, ok := finalContext.Value(ctxkey.Group).(*service.Group)
	require.True(t, ok)
	require.Same(t, effective, requestGroup)
}

func newFinalGroupMappingGatewayService() *service.GatewayService {
	channelService := service.NewChannelService(messagesMappingChannelRepoStub{}, nil, nil, nil)
	return service.NewGatewayService(
		nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, channelService,
		nil, nil, nil, nil,
	)
}

func hydratedMappingGroup(id int64, platform, source, target string) *service.Group {
	return &service.Group{
		ID: id, Platform: platform, Status: service.StatusActive, Hydrated: true,
		ModelMapping: map[string]string{source: target},
	}
}
