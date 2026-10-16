package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestLiveCallIdentityUsesStableRemoteAPIKeyNamespace(t *testing.T) {
	newContext := func(ctx context.Context) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/live/calls", nil).WithContext(ctx)
		return c
	}
	local := liveCallIdentity(newContext(context.Background()), &service.APIKey{ID: 77}, 42, nil)
	require.Equal(t, int64(77), local.APIKeyID)

	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "request-1", KeyID: "remote-key-1", UserID: 42})
	first := liveCallIdentity(newContext(ctx), &service.APIKey{Name: "remote-key-1"}, 42, nil).APIKeyID
	second := liveCallIdentity(newContext(ctx), &service.APIKey{Name: "remote-key-1"}, 42, nil).APIKeyID
	require.Negative(t, first)
	require.Equal(t, first, second)

	otherCtx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "request-2", KeyID: "remote-key-2", UserID: 42,
	})
	require.NotEqual(t, first, liveCallIdentity(newContext(otherCtx), &service.APIKey{Name: "remote-key-2"}, 42, nil).APIKeyID)
	require.Zero(t, liveCallIdentity(newContext(context.Background()), &service.APIKey{}, 42, nil).APIKeyID)
}

func TestLiveEnabledForAPIKeyRequiresOpenAIGroupSwitch(t *testing.T) {
	require.False(t, liveEnabledForAPIKey(nil))
	require.False(t, liveEnabledForAPIKey(&service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}}))
	require.False(t, liveEnabledForAPIKey(&service.APIKey{Group: &service.Group{Platform: service.PlatformGrok, AllowLive: true}}))
	require.True(t, liveEnabledForAPIKey(&service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, AllowLive: true}}))
	require.True(t, liveEnabledForAPIKey(&service.APIKey{Group: &service.Group{Platform: service.PlatformComposite, AllowLive: true}}))
}

func TestLiveCallIdentityUsesFinalReroutedGroupConcurrency(t *testing.T) {
	originalGroupID := int64(10)
	apiKey := &service.APIKey{
		ID:      77,
		GroupID: &originalGroupID,
		Group:   &service.Group{ID: originalGroupID, PerUserConcurrency: 9},
	}
	finalGroup := &service.Group{ID: 20, PerUserConcurrency: 2}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/live/calls", nil).
		WithContext(context.WithValue(context.Background(), ctxkey.Group, finalGroup))

	identity := liveCallIdentity(c, apiKey, 42, nil)
	require.NotNil(t, identity.GroupID)
	require.Equal(t, int64(20), *identity.GroupID)
	require.Equal(t, 2, identity.GroupMaxConcurrency)
}

func TestLiveAppliesBlockedInstructionIsolationToForwardSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, SampleRate: 100,
		AllGroups: true, BlockMessage: "blocked", BlockedKeywords: []string{"forbidden directive"},
		KeywordBlockingMode: service.ContentModerationKeywordModeKeywordOnly,
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	moderationSvc := service.NewContentModerationService(
		&contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil,
	)
	groupID := int64(10)
	apiKey := &service.APIKey{
		ID: 8, GroupID: &groupID,
		User: &service.User{ID: 7}, Group: &service.Group{ID: groupID, Name: "strict", Status: service.StatusActive},
	}

	firstC, _ := gin.CreateTestContext(httptest.NewRecorder())
	firstC.Request = httptest.NewRequest("POST", "/v1/live/calls", nil)
	firstSession := []byte(`{"model":"gpt-test","instructions":"forbidden directive"}`)
	firstDecision := runContentModeration(
		firstC, nil, moderationSvc, nil, apiKey, middleware2.AuthSubject{UserID: 7},
		service.ContentModerationProtocolOpenAIResponses, "gpt-test", firstSession,
	)
	require.NotNil(t, firstDecision)
	require.True(t, firstDecision.Blocked)

	nextC, _ := gin.CreateTestContext(httptest.NewRecorder())
	nextC.Request = httptest.NewRequest("POST", "/v1/live/calls", nil)
	nextSession := []byte(`{"model":"gpt-test","instructions":"forbidden directive"}`)
	nextDecision := runContentModeration(
		nextC, nil, moderationSvc, nil, apiKey, middleware2.AuthSubject{UserID: 7},
		service.ContentModerationProtocolOpenAIResponses, "gpt-test", nextSession,
	)
	require.NotNil(t, nextDecision)
	require.True(t, nextDecision.Allowed)

	request := &service.LiveCallRequest{SDP: "v=0", Session: append(json.RawMessage(nil), nextSession...)}
	applyLiveContentModerationForwardSession(nextC, request)
	require.False(t, gjson.GetBytes(request.Session, "instructions").Exists(), "Live upstream must receive the isolated session copy")
	require.Equal(t, "gpt-test", gjson.GetBytes(request.Session, "model").String())
}

func TestLiveSidebandContentModerationHookBlocksBeforeUpstreamWrite(t *testing.T) {
	tests := []struct {
		name            string
		blockedPayload  string
		safePayload     string
		instructionPath string
	}{
		{
			name:            "session update",
			blockedPayload:  `{"type":"session.update","session":{"model":"gpt-test","instructions":"forbidden directive"}}`,
			safePayload:     `{"type":"session.update","session":{"model":"gpt-test","instructions":"safe directive"}}`,
			instructionPath: "session.instructions",
		},
		{
			name:            "response create",
			blockedPayload:  `{"type":"response.create","response":{"model":"gpt-test","instructions":"forbidden directive"}}`,
			safePayload:     `{"type":"response.create","response":{"model":"gpt-test","instructions":"safe directive"}}`,
			instructionPath: "response.instructions",
		},
		{
			name:            "session update uppercase field",
			blockedPayload:  `{"type":"session.update","session":{"model":"gpt-test","Instructions":"forbidden directive"}}`,
			safePayload:     `{"type":"session.update","session":{"model":"gpt-test","Instructions":"safe directive"}}`,
			instructionPath: "session.Instructions",
		},
		{
			name:            "response create uppercase field",
			blockedPayload:  `{"type":"response.create","response":{"model":"gpt-test","INSTRUCTIONS":"forbidden directive"}}`,
			safePayload:     `{"type":"response.create","response":{"model":"gpt-test","INSTRUCTIONS":"safe directive"}}`,
			instructionPath: "response.INSTRUCTIONS",
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			cfg := service.ContentModerationConfig{
				Enabled: true, Mode: service.ContentModerationModePreBlock, SampleRate: 100,
				AllGroups: true, BlockMessage: "blocked", BlockedKeywords: []string{"forbidden directive"},
				KeywordBlockingMode: service.ContentModerationKeywordModeKeywordOnly,
			}
			rawCfg, err := json.Marshal(cfg)
			require.NoError(t, err)
			moderationSvc := service.NewContentModerationService(
				&contentModerationHandlerSettingRepo{values: map[string]string{
					service.SettingKeyRiskControlEnabled:      "true",
					service.SettingKeyContentModerationConfig: string(rawCfg),
				}},
				&contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil,
			)
			handler := &OpenAIGatewayHandler{
				contentModerationService: moderationSvc,
				gatewayService:           &service.OpenAIGatewayService{},
			}
			groupID := int64(10)
			apiKey := &service.APIKey{
				ID: int64(610 + index), GroupID: &groupID,
				User: &service.User{ID: 7},
				Group: &service.Group{
					ID: groupID, Name: "strict", Status: service.StatusActive,
					Platform: service.PlatformOpenAI,
				},
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/v1/live/calls/call-test/sideband", nil)
			c.Set("content_moderation_disable_keyword_reroute", true)
			blockedEvents := 0
			hook := handler.liveSidebandContentModerationHook(
				c, nil, apiKey, middleware2.AuthSubject{UserID: 7}, "gpt-live",
				func(context.Context, *service.ContentModerationDecision) { blockedEvents++ },
			)

			blocked := []byte(tt.blockedPayload)
			forwardPayload, forward, err := hook(context.Background(), coderws.MessageText, blocked)
			require.NoError(t, err)
			require.False(t, forward, "最终拒绝的控制帧不得写入 Live 上游")
			require.Equal(t, blocked, forwardPayload)
			require.Equal(t, 1, blockedEvents)

			safe := []byte(tt.safePayload)
			forwardPayload, forward, err = hook(context.Background(), coderws.MessageText, safe)
			require.NoError(t, err)
			require.True(t, forward)
			require.Equal(t, safe, forwardPayload, "安全帧应保持原样转发")
			require.Equal(t, 1, blockedEvents)

			// 同一被拒指令由现有指纹隔离移除后可以继续会话，但真正转发的
			// 必须是已删除 instructions 的副本，不能把原始危险字段送上游。
			forwardPayload, forward, err = hook(context.Background(), coderws.MessageText, blocked)
			require.NoError(t, err)
			require.True(t, forward)
			require.False(t, gjson.GetBytes(forwardPayload, tt.instructionPath).Exists())
			require.Equal(t, 1, blockedEvents)
		})
	}
}

func TestLiveSidebandInstructionModerationRequestSupportsTopLevelCompatibility(t *testing.T) {
	for _, eventType := range []string{"session.update", "response.create"} {
		payload := []byte(`{"type":"` + eventType + `","instructions":"control rule"}`)
		body, path, model, ok, err := liveSidebandInstructionModerationRequest(payload, "gpt-live")

		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "instructions", path)
		require.Equal(t, "gpt-live", model)
		require.Equal(t, "control rule", gjson.GetBytes(body, "instructions").String())
	}

	_, _, _, ok, err := liveSidebandInstructionModerationRequest(
		[]byte(`{"type":"conversation.item.create","instructions":"control rule"}`),
		"gpt-live",
	)
	require.NoError(t, err)
	require.False(t, ok, "非目标 Live 事件不能扩大审计范围")
}

func TestLiveSidebandHookRejectsDuplicateInstructionVariantsAndContinues(t *testing.T) {
	handler := &OpenAIGatewayHandler{}
	var decisions []*service.ContentModerationDecision
	hook := handler.liveSidebandContentModerationHook(
		nil, nil, nil, middleware2.AuthSubject{}, "gpt-live",
		func(_ context.Context, decision *service.ContentModerationDecision) {
			decisions = append(decisions, decision)
		},
	)
	duplicates := []string{
		`{"type":"session.update","session":{"instructions":"first","Instructions":"second"}}`,
		`{"type":"response.create","response":{"instructions":"first","instructions":"second"}}`,
		`{"type":"session.update","session":{},"instructions":"first","INSTRUCTIONS":"second"}`,
		`{"type":"response.create","response":{"instructions":"first"},"instructions":"second"}`,
	}
	for index, duplicate := range duplicates {
		payload := []byte(duplicate)
		forwardPayload, forward, err := hook(context.Background(), coderws.MessageText, payload)

		require.NoError(t, err)
		require.False(t, forward, "重复 instructions 变体不得写入上游")
		require.Equal(t, payload, forwardPayload)
		require.Len(t, decisions, index+1)
		require.Equal(t, service.ContentModerationActionError, decisions[index].Action)
		require.Equal(t, 400, decisions[index].StatusCode)
		require.NotContains(t, decisions[index].Message, "first")
		require.NotContains(t, decisions[index].Message, "second")

		// forward=false 只拒绝当前帧。即使没有启用内容审查服务，连接上的
		// 后续安全帧仍应照常交给 service 写上游。
		safe := []byte(`{"type":"session.update","session":{"Instructions":"safe directive"}}`)
		forwardPayload, forward, err = hook(context.Background(), coderws.MessageText, safe)
		require.NoError(t, err)
		require.True(t, forward)
		require.Equal(t, safe, forwardPayload)
	}
}
