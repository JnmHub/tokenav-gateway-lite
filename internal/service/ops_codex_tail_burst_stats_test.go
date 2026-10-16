package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type codexTailBurstStatsRepoMock struct {
	*opsRepoMock
	response *CodexTailBurstStatsResponse
	filter   CodexTailBurstStatsFilter
}

func (m *codexTailBurstStatsRepoMock) GetCodexTailBurstStats(_ context.Context, filter CodexTailBurstStatsFilter) (*CodexTailBurstStatsResponse, error) {
	m.filter = filter
	return m.response, nil
}

func TestOpsServiceGetCodexTailBurstStatsRejectsInvalidEnum(t *testing.T) {
	repo := &codexTailBurstStatsRepoMock{opsRepoMock: &opsRepoMock{}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	start := time.Now().Add(-time.Hour)
	_, err := svc.GetCodexTailBurstStats(context.Background(), CodexTailBurstStatsFilter{
		StartTime: start,
		EndTime:   time.Now(),
		Window:    "1h",
	})
	require.Error(t, err)
	require.Equal(t, 400, int(infraerrors.Code(err)))
	require.Nil(t, repo.response)
}

func TestOpsServiceGetCodexTailBurstStatsPassesValidFilter(t *testing.T) {
	repo := &codexTailBurstStatsRepoMock{
		opsRepoMock: &opsRepoMock{},
		response:    &CodexTailBurstStatsResponse{Total: 1},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	start := time.Now().Add(-time.Hour)
	end := time.Now()
	result, err := svc.GetCodexTailBurstStats(context.Background(), CodexTailBurstStatsFilter{
		StartTime: start,
		EndTime:   end,
		Window:    "5h",
		Reason:    "used_ratio",
		Status:    "active",
		Page:      2,
		PageSize:  50,
	})
	require.NoError(t, err)
	require.Same(t, repo.response, result)
	require.Equal(t, 2, repo.filter.Page)
	require.Equal(t, 50, repo.filter.PageSize)
}
