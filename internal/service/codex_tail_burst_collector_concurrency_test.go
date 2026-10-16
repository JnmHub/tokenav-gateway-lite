package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type tailBurstCollectorListProbeRepo struct {
	AccountRepository
	listCalls int
}

func (r *tailBurstCollectorListProbeRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	r.listCalls++
	return nil, nil
}

func TestCodexTailBurstCollectorSkipsOverlappingRound(t *testing.T) {
	repo := &tailBurstCollectorListProbeRepo{}
	collector := &CodexTailBurstCollector{accountRepo: repo}

	collector.roundMu.Lock()
	collector.collectOnce(context.Background())
	collector.roundMu.Unlock()

	require.Zero(t, repo.listCalls, "an overlapping round must not query accounts")
}
