//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestOpenAISelectAccountForModelWithExclusions_ChannelMappedRestrictionRejectsEarly(t *testing.T) {
	t.Parallel()

	channelSvc := newTestChannelService(makeStandardRepo(Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceChannelMapped,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformOpenAI, Models: []string{"gpt-4o"}},
		},
		ModelMapping: map[string]map[string]string{
			PlatformOpenAI: {"gpt-4.1": "o3-mini"},
		},
	}, map[int64]string{10: PlatformOpenAI}))

	svc := &OpenAIGatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{
			{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true},
		}},
		channelService: channelSvc,
	}

	groupID := int64(10)
	_, err := svc.SelectAccountForModelWithExclusions(context.Background(), &groupID, "", "gpt-4.1", nil)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Contains(t, err.Error(), "channel pricing restriction")
}

func TestOpenAISelectAccountForModelWithExclusions_UpstreamRestrictionSkipsDisallowedAccount(t *testing.T) {
	t.Parallel()

	channelSvc := newTestChannelService(makeStandardRepo(Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformOpenAI, Models: []string{"o3-mini"}},
		},
	}, map[int64]string{10: PlatformOpenAI}))

	svc := &OpenAIGatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformOpenAI,
				Status:      StatusActive,
				Schedulable: true,
				Priority:    10,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gpt-4.1": "gpt-4o"},
				},
			},
			{
				ID:          2,
				Platform:    PlatformOpenAI,
				Status:      StatusActive,
				Schedulable: true,
				Priority:    20,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gpt-4.1": "o3-mini"},
				},
			},
		}},
		channelService: channelSvc,
	}

	groupID := int64(10)
	account, err := svc.SelectAccountForModelWithExclusions(context.Background(), &groupID, "", "gpt-4.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(2), account.ID)
}

func TestIsUpstreamModelRestrictedByChannel_UsesForwardModelContext(t *testing.T) {
	t.Parallel()

	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping":         map[string]any{"gpt-channel": "gpt-account"},
			"compact_model_mapping": map[string]any{"gpt-account": "gpt-compact"},
		},
	}

	for _, tc := range []struct {
		name         string
		allowedModel string
		legacy       bool
	}{
		{name: "native v2 uses normal account mapping", allowedModel: "gpt-account"},
		{name: "legacy compact applies compact mapping", allowedModel: "gpt-compact", legacy: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			channelSvc := newTestChannelService(makeStandardRepo(Channel{
				ID:                 1,
				Status:             StatusActive,
				GroupIDs:           []int64{10},
				RestrictModels:     true,
				BillingModelSource: BillingModelSourceUpstream,
				ModelPricing:       []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{tc.allowedModel}}},
			}, map[int64]string{10: PlatformOpenAI}))
			svc := &OpenAIGatewayService{channelService: channelSvc}
			ctx := WithOpenAIForwardModel(context.Background(), "gpt-channel", tc.legacy)

			require.False(t, svc.isUpstreamModelRestrictedByChannel(ctx, 10, account, "gpt-client", true))
		})
	}
}

func TestResolveOpenAIAccountUpstreamModelForRequest_MatchesForwardBranch(t *testing.T) {
	t.Parallel()

	credentials := map[string]any{
		"model_mapping":         map[string]any{"gpt-channel": "gpt-account"},
		"compact_model_mapping": map[string]any{"gpt-channel": "gpt-compact", "gpt-account": "gpt-account-compact"},
	}

	t.Run("responses passthrough applies account mapping", func(t *testing.T) {
		account := &Account{
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Credentials: credentials,
			Extra: map[string]any{
				"openai_passthrough":         true,
				"openai_responses_supported": true,
			},
		}
		require.Equal(t, "gpt-account", resolveOpenAIAccountUpstreamModelForRequest(context.Background(), nil, account, "gpt-channel", false))
		require.Equal(t, "gpt-account-compact", resolveOpenAIAccountUpstreamModelForRequest(context.Background(), nil, account, "gpt-channel", true))
	})

	t.Run("responses passthrough applies selected group account mapping", func(t *testing.T) {
		groupID := int64(10)
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"gpt-channel": "gpt-global"},
			},
			AccountGroups: []AccountGroup{{
				GroupID:      groupID,
				ModelMapping: map[string]string{"gpt-channel": "gpt-group"},
			}},
			Extra: map[string]any{
				"openai_passthrough":         true,
				"openai_responses_supported": true,
			},
		}
		ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
			ID: groupID, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true,
		})
		require.Equal(t, "gpt-group", resolveOpenAIAccountUpstreamModelForRequest(ctx, &groupID, account, "gpt-channel", false))
	})

	t.Run("raw chat fallback applies normal mapping only", func(t *testing.T) {
		account := &Account{
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Credentials: credentials,
			Extra: map[string]any{
				"openai_passthrough":         true,
				"openai_responses_supported": false,
			},
		}
		require.Equal(t, "gpt-account", resolveOpenAIAccountUpstreamModelForRequest(context.Background(), nil, account, "gpt-channel", false))
		require.Equal(t, "gpt-account", resolveOpenAIAccountUpstreamModelForRequest(context.Background(), nil, account, "gpt-channel", true))
	})

	t.Run("normal responses applies account then legacy mapping", func(t *testing.T) {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: credentials}
		require.Equal(t, "gpt-account", resolveOpenAIAccountUpstreamModelForRequest(context.Background(), nil, account, "gpt-channel", false))
		require.Equal(t, "gpt-account-compact", resolveOpenAIAccountUpstreamModelForRequest(context.Background(), nil, account, "gpt-channel", true))
	})
}

func TestOpenAISelectAccountForModelWithExclusions_StickyRestrictedUpstreamFallsBack(t *testing.T) {
	t.Parallel()

	channelSvc := newTestChannelService(makeStandardRepo(Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformOpenAI, Models: []string{"o3-mini"}},
		},
	}, map[int64]string{10: PlatformOpenAI}))

	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{"openai:sticky-session": 1},
	}
	svc := &OpenAIGatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformOpenAI,
				Status:      StatusActive,
				Schedulable: true,
				Priority:    10,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gpt-4.1": "gpt-4o"},
				},
			},
			{
				ID:          2,
				Platform:    PlatformOpenAI,
				Status:      StatusActive,
				Schedulable: true,
				Priority:    20,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gpt-4.1": "o3-mini"},
				},
			},
		}},
		channelService: channelSvc,
		cache:          cache,
	}

	groupID := int64(10)
	account, err := svc.SelectAccountForModelWithExclusions(context.Background(), &groupID, "sticky-session", "gpt-4.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(2), account.ID)
	require.Equal(t, 1, cache.deletedSessions["openai:sticky-session"])
	require.Equal(t, int64(2), cache.sessionBindings["openai:sticky-session"])
}
