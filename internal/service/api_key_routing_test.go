package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/stretchr/testify/require"
)

func TestAPIKeySchedulerGroupID(t *testing.T) {
	groupID := int64(7)

	tests := []struct {
		name     string
		key      *APIKey
		wantLock bool
	}{
		{
			name:     "inherit does not force scheduler group",
			key:      &APIKey{GroupID: &groupID, GatewayStrategy: "inherit"},
			wantLock: false,
		},
		{
			name:     "auto does not force scheduler group",
			key:      &APIKey{GroupID: &groupID, GatewayStrategy: "auto"},
			wantLock: false,
		},
		{
			name:     "empty strategy does not force scheduler group",
			key:      &APIKey{GroupID: &groupID},
			wantLock: false,
		},
		{
			name:     "legacy fixed no longer locks scheduler group",
			key:      &APIKey{GroupID: &groupID, GatewayStrategy: "fixed", KeyDefaultGatewayNodeID: 9},
			wantLock: false,
		},
		{
			name:     "fixed group locks scheduler group",
			key:      &APIKey{GroupID: &groupID, GatewayStrategy: " fixed_group ", KeyDefaultGatewayNodeID: 9},
			wantLock: true,
		},
		{
			name:     "missing group never locks scheduler group",
			key:      &APIKey{GatewayStrategy: "fixed_group", KeyDefaultGatewayNodeID: 9},
			wantLock: false,
		},
		{
			name:     "missing gateway never locks scheduler group",
			key:      &APIKey{GroupID: &groupID, GatewayStrategy: "fixed_group"},
			wantLock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.key.SchedulerGroupID()
			if !tt.wantLock {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, groupID, *got)
		})
	}
}

func TestAPIKeyWSIngressConcurrencyIDSeparatesLocalAndControlPlaneKeys(t *testing.T) {
	local := &APIKey{ID: 42, Name: "ignored"}
	require.EqualValues(t, 42, local.WSIngressConcurrencyID(context.Background()))

	remote := &APIKey{Name: "snapshot-key-fallback"}
	ctxA := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "request-a", KeyID: "remote-key-a", UserID: 7})
	ctxB := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "request-b", KeyID: "remote-key-b", UserID: 7})
	idA := remote.WSIngressConcurrencyID(ctxA)
	require.Negative(t, idA)
	require.Equal(t, idA, remote.WSIngressConcurrencyID(ctxA))
	require.NotEqual(t, idA, remote.WSIngressConcurrencyID(ctxB))
	require.Negative(t, remote.WSIngressConcurrencyID(context.Background()))
	require.Zero(t, (&APIKey{}).WSIngressConcurrencyID(context.Background()))
	require.Zero(t, (*APIKey)(nil).WSIngressConcurrencyID(context.Background()))
}
