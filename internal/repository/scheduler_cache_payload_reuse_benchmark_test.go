//go:build unit

package repository

import (
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func schedulerPayloadBenchmarkAccounts(count int) ([]service.Account, []int64) {
	accounts := make([]service.Account, 0, count)
	ids := make([]int64, 0, count)
	for i := 1; i <= count; i++ {
		id := int64(i)
		ids = append(ids, id)
		accounts = append(accounts, service.Account{
			ID:          id,
			Name:        fmt.Sprintf("account-%d", i),
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"api_key": "benchmark-secret", "base_url": "https://example.invalid/v1"},
			Extra:       map[string]any{"openai_responses_supported": true, "model_rate_limits": map[string]any{}},
		})
	}
	return accounts, ids
}

func BenchmarkSchedulerSnapshotPayloadReuse(b *testing.B) {
	accounts, ids := schedulerPayloadBenchmarkAccounts(1000)
	b.ReportAllocs()

	b.Run("full-account-json", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, account := range accounts {
				if _, _, err := marshalSchedulerCacheAccount(account); err != nil {
					b.Fatal(err)
				}
			}
		}
	})

	b.Run("id-only-members", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			members := schedulerSnapshotMembers(ids)
			if len(members) != len(ids) {
				b.Fatalf("unexpected members: %d", len(members))
			}
		}
	})
}

func TestSchedulerSnapshotMembersReturnsIndependentSlices(t *testing.T) {
	ids := []int64{1, 2, 3}
	first := schedulerSnapshotMembers(ids)
	second := schedulerSnapshotMembers(ids)
	require.Equal(t, first, second)

	first[0].Member = "changed"
	require.Equal(t, "1", second[0].Member)
	require.Equal(t, []int64{1, 2, 3}, ids)
}
