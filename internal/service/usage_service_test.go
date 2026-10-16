package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type usageLogRepoCapture struct {
	UsageLogRepository
	got *UsageLog
}

func (r *usageLogRepoCapture) Create(_ context.Context, log *UsageLog) (bool, error) {
	r.got = log
	return true, nil
}

type usageUserRepoCapture struct {
	UserRepository
}

func (r *usageUserRepoCapture) GetByID(_ context.Context, id int64) (*User, error) {
	return &User{ID: id}, nil
}

func (r *usageUserRepoCapture) UpdateBalance(context.Context, int64, float64) error {
	return nil
}

func TestUsageServiceCreateCarriesGroupID(t *testing.T) {
	t.Parallel()

	groupID := int64(42)
	repo := &usageLogRepoCapture{}
	svc := NewUsageService(repo, &usageUserRepoCapture{}, nil, nil)

	log, err := svc.Create(context.Background(), CreateUsageLogRequest{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		GroupID:        &groupID,
		RequestID:      "req-group",
		Model:          "gpt-5.4-mini",
		RateMultiplier: 1,
	})

	require.NoError(t, err)
	require.Same(t, repo.got, log)
	require.NotNil(t, repo.got.GroupID)
	require.Equal(t, groupID, *repo.got.GroupID)
}
