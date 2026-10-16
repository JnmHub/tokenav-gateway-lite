package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSchedulerSnapshotPreservesExplicitEndpointCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		capabilities               any
		seedance, chat, embeddings bool
	}{
		{name: "seedance_only", capabilities: []string{"seedance"}, seedance: true},
		{name: "chat_only", capabilities: []string{"chat_completions"}, chat: true},
		{name: "explicit_empty", capabilities: []string{}},
		{name: "map_denies_chat", capabilities: map[string]any{"seedance": true, "chat_completions": false}, seedance: true},
		{name: "default_text", chat: true, embeddings: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mini := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			cache := NewSchedulerCache(rdb)
			account := service.Account{
				ID: 12, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3},
				Credentials: map[string]any{
					"api_key": "synthetic-local", "base_url": "http://127.0.0.1:12345",
					"model_mapping": map[string]any{"e2e-seedance": "ep-seedance-test"},
					"unused_debug":  "must-not-enter-metadata",
				},
			}
			if tc.capabilities != nil {
				account.Credentials["openai_capabilities"] = tc.capabilities
			}
			bucket := service.SchedulerBucket{GroupID: 3, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{account}))
			snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.True(t, hit)
			require.Len(t, snapshot, 1)
			full, err := cache.GetAccount(ctx, account.ID)
			require.NoError(t, err)
			require.NotNil(t, full)
			for _, candidate := range []*service.Account{&account, full, snapshot[0]} {
				require.Equal(t, tc.seedance, candidate.SupportsOpenAIEndpointCapability(service.OpenAIEndpointCapabilitySeedance))
				require.Equal(t, tc.chat, candidate.SupportsOpenAIEndpointCapability(service.OpenAIEndpointCapabilityChatCompletions))
				require.Equal(t, tc.chat, candidate.SupportsOpenAIEndpointCapability(service.OpenAIEndpointCapabilityResponses))
				require.Equal(t, tc.embeddings, candidate.SupportsOpenAIEndpointCapability(service.OpenAIEndpointCapabilityEmbeddings))
			}
			require.NotContains(t, snapshot[0].Credentials, "unused_debug")
		})
	}
}
