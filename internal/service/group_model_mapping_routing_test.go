//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestGatewayService_GroupModelMappingSelectsTargetModelAccount(t *testing.T) {
	tests := []struct {
		name     string
		platform string
	}{
		{name: "Anthropic shared chat responses and messages selector", platform: PlatformAnthropic},
		{name: "Gemini native selector", platform: PlatformGemini},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			groupID := int64(7100 + index)
			group := &Group{
				ID:           groupID,
				Platform:     test.platform,
				Status:       StatusActive,
				Hydrated:     true,
				ModelMapping: map[string]string{"public-alias": "real-target"},
			}
			repo := &mockAccountRepoForPlatform{
				accounts: []Account{
					{
						ID: 1, Platform: test.platform, Type: AccountTypeAPIKey,
						Status: StatusActive, Schedulable: true, Priority: 0,
						Credentials: map[string]any{"model_mapping": map[string]any{"public-alias": "public-alias"}},
					},
					{
						ID: 2, Platform: test.platform, Type: AccountTypeAPIKey,
						Status: StatusActive, Schedulable: true, Priority: 5,
						Credentials: map[string]any{"model_mapping": map[string]any{"real-target": "upstream-real-target"}},
					},
				},
				accountsByID: map[int64]*Account{},
			}
			for accountIndex := range repo.accounts {
				repo.accountsByID[repo.accounts[accountIndex].ID] = &repo.accounts[accountIndex]
			}
			svc := &GatewayService{
				accountRepo: repo,
				groupRepo:   &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}},
				cache:       &mockGatewayCacheForPlatform{},
				cfg:         testConfig(),
			}

			account, err := svc.SelectAccountForModelWithExclusions(
				context.WithValue(context.Background(), ctxkey.Group, group),
				&groupID,
				"",
				"public-alias",
				nil,
			)
			require.NoError(t, err)
			require.NotNil(t, account)
			require.Equal(t, int64(2), account.ID, "account selection must filter by the group mapping target")
		})
	}
}

func TestGatewayService_KeywordProtectedAccountsStayExcludedAcrossLoadAwareRetry(t *testing.T) {
	groupID := int64(7201)
	group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0, Concurrency: 4},
			{ID: 12, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 5, Concurrency: 4},
			{ID: 13, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 9, Concurrency: 4},
		},
		accountsByID: map[int64]*Account{},
	}
	for index := range repo.accounts {
		repo.accountsByID[repo.accounts[index].ID] = &repo.accounts[index]
	}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &GatewayService{
		accountRepo:        repo,
		groupRepo:          &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}},
		cache:              &mockGatewayCacheForPlatform{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(&mockConcurrencyCache{}),
	}
	ctx := ContextWithKeywordProtectedAccountIDs(context.Background(), map[int64]struct{}{11: {}})

	first, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "claude-sonnet-4-6", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.NotNil(t, first.Account)
	require.Equal(t, int64(12), first.Account.ID)
	if first.ReleaseFunc != nil {
		first.ReleaseFunc()
	}

	second, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "claude-sonnet-4-6", map[int64]struct{}{12: {}}, "", 0)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotNil(t, second.Account)
	require.Equal(t, int64(13), second.Account.ID, "retry exclusions must merge with protected accounts")
	if second.ReleaseFunc != nil {
		second.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_GroupModelMappingCoversEndpointCapabilitiesAndTransport(t *testing.T) {
	tests := []struct {
		name               string
		capability         OpenAIEndpointCapability
		transport          OpenAIUpstreamTransport
		requiredImageModel string
		imageCapability    OpenAIImagesCapability
		configureAccount   func(*Account)
	}{
		{name: "chat", capability: OpenAIEndpointCapabilityChatCompletions, transport: OpenAIUpstreamTransportAny},
		{name: "responses", capability: OpenAIEndpointCapabilityResponses, transport: OpenAIUpstreamTransportAny},
		{name: "messages", capability: OpenAIEndpointCapabilityChatCompletions, transport: OpenAIUpstreamTransportAny},
		{name: "embeddings", capability: OpenAIEndpointCapabilityEmbeddings, transport: OpenAIUpstreamTransportHTTPSSE},
		{
			name: "images", transport: OpenAIUpstreamTransportHTTPSSE,
			requiredImageModel: "public-alias", imageCapability: OpenAIImagesCapabilityNative,
		},
		{
			name: "websocket", capability: OpenAIEndpointCapabilityChatCompletions,
			transport: OpenAIUpstreamTransportResponsesWebsocketV2,
			configureAccount: func(account *Account) {
				account.Extra = map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			groupID := int64(7300 + index)
			group := &Group{
				ID: groupID, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true,
				ModelMapping: map[string]string{"public-alias": "real-target"},
			}
			aliasAccount := Account{
				ID: 21, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Status: StatusActive, Schedulable: true, Priority: 0, Concurrency: 4,
				Credentials: map[string]any{
					"api_key":             "alias-key",
					"model_mapping":       map[string]any{"public-alias": "public-alias"},
					"openai_capabilities": []any{"chat_completions", "embeddings"},
				},
			}
			targetAccount := Account{
				ID: 22, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Status: StatusActive, Schedulable: true, Priority: 5, Concurrency: 4,
				Credentials: map[string]any{
					"api_key":             "target-key",
					"model_mapping":       map[string]any{"real-target": "upstream-real-target"},
					"openai_capabilities": []any{"chat_completions", "embeddings"},
				},
			}
			if test.configureAccount != nil {
				test.configureAccount(&targetAccount)
			}
			cfg := &config.Config{}
			cfg.Gateway.Scheduling.LoadBatchEnabled = false
			if test.transport == OpenAIUpstreamTransportResponsesWebsocketV2 {
				cfg = newSchedulerTestOpenAIWSV2Config()
				cfg.Gateway.Scheduling.LoadBatchEnabled = false
			}
			svc := &OpenAIGatewayService{
				accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{aliasAccount, targetAccount}},
				cache:              &schedulerTestGatewayCache{},
				cfg:                cfg,
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}

			selection, _, err := svc.SelectAccountWithSchedulerForCapabilityAndImageModel(
				context.WithValue(context.Background(), ctxkey.Group, group),
				&groupID,
				"",
				"",
				"public-alias",
				test.requiredImageModel,
				nil,
				test.transport,
				test.capability,
				test.imageCapability,
				false,
			)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.NotNil(t, selection.Account)
			require.Equal(t, int64(22), selection.Account.ID)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestOpenAIGatewayService_KeywordProtectedAccountsStayExcludedAcrossRetry(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	groupID := int64(7401)
	group := &Group{ID: groupID, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}
	accounts := []Account{
		{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0, Concurrency: 4},
		{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 5, Concurrency: 4},
		{ID: 33, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 9, Concurrency: 4},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	ctx := ContextWithKeywordProtectedAccountIDs(
		context.WithValue(context.Background(), ctxkey.Group, group),
		map[int64]struct{}{31: {}},
	)

	first, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx, &groupID, "", "", "gpt-5.6-sol", nil,
		OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false,
	)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.NotNil(t, first.Account)
	require.Equal(t, int64(32), first.Account.ID)
	if first.ReleaseFunc != nil {
		first.ReleaseFunc()
	}

	second, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx, &groupID, "", "", "gpt-5.6-sol", map[int64]struct{}{32: {}},
		OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false,
	)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotNil(t, second.Account)
	require.Equal(t, int64(33), second.Account.ID, "retry exclusions must merge with protected accounts")
	if second.ReleaseFunc != nil {
		second.ReleaseFunc()
	}
}
