package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrapGatewayGroupUnavailableSelectionError(t *testing.T) {
	groupID := int64(42)

	err := wrapGatewayGroupUnavailableSelectionError(context.Background(), &groupID, ErrNoAvailableAccounts)

	require.ErrorIs(t, err, ErrGatewayGroupUnavailable)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)

	var groupErr *GatewayGroupUnavailableError
	require.True(t, errors.As(err, &groupErr))
	require.Equal(t, groupID, groupErr.GroupID)
}

func TestWrapGatewayGroupUnavailableSelectionErrorSkipsNonFixedGroupAndCompact(t *testing.T) {
	groupID := int64(42)

	require.ErrorIs(t, wrapGatewayGroupUnavailableSelectionError(context.Background(), nil, ErrNoAvailableAccounts), ErrNoAvailableAccounts)
	require.ErrorIs(t, wrapGatewayGroupUnavailableSelectionError(context.Background(), &groupID, ErrNoAvailableCompactAccounts), ErrNoAvailableCompactAccounts)
	require.False(t, errors.Is(wrapGatewayGroupUnavailableSelectionError(context.Background(), &groupID, ErrNoAvailableCompactAccounts), ErrGatewayGroupUnavailable))
}
