package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCaptureImageTaskPolicyPreservesKeywordAccountExclusions(t *testing.T) {
	groupID := int64(7)
	group := &Group{ID: groupID, Name: "images", Platform: PlatformOpenAI, Status: StatusActive, AllowImageGeneration: true}
	apiKey := &APIKey{ID: 8, User: &User{ID: 9, Status: StatusActive}, GroupID: &groupID, Group: group}
	ctx := ContextWithKeywordProtectedAccountIDs(context.Background(), map[int64]struct{}{42: {}, 11: {}})

	snapshot, err := CaptureImageTaskPolicy(ctx, apiKey, nil, ChannelUsageFields{}, "", "", "", "")
	require.NoError(t, err)
	require.Equal(t, []int64{11, 42}, snapshot.KeywordExcludedAccountIDs)

	payload, err := snapshot.Marshal()
	require.NoError(t, err)
	decoded, err := DecodeImageTaskPolicy(payload)
	require.NoError(t, err)
	require.Equal(t, []int64{11, 42}, decoded.KeywordExcludedAccountIDs)
}
