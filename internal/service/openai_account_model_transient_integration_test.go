//go:build integration

package service

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIAccountModelTransientIntegration_ConcurrentUpdatesStayBoundedAndModelScoped(t *testing.T) {
	const maxEntries = 64
	state := newOpenAIAccountModelTransientState(maxEntries)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for i := range 512 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			accountID := int64(i%96 + 1)
			model := fmt.Sprintf("gpt-integration-%d", i%128)
			state.recordFailure(accountID, model, now.Add(time.Duration(i)*time.Microsecond))
			_ = state.isBlocked(accountID, model, now.Add(time.Second))
		}(i)
	}
	wg.Wait()

	require.LessOrEqual(t, state.size(), maxEntries)
	state.recordFailure(9001, "gpt-5.6-sol", now)
	state.recordFailure(9001, "gpt-5.6-sol", now.Add(time.Millisecond))
	require.True(t, state.isBlocked(9001, "gpt-5.6-sol", now.Add(time.Second)))
	require.False(t, state.isBlocked(9001, "gpt-5.5", now.Add(time.Second)))
}
