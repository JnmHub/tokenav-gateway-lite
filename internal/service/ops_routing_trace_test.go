package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsRoutingTraceAccumulatesSelectionStages(t *testing.T) {
	ctx := ContextWithOpsRoutingTrace(context.Background())
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageStickyLookup, 3*time.Millisecond)
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountSlot, 7*time.Millisecond)
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountSlot, 5*time.Millisecond)

	got, ok := OpsRoutingSelectionBreakdownFromContext(ctx)
	require.True(t, ok)
	require.EqualValues(t, 3, got.StickyLookup)
	require.EqualValues(t, 12, got.AccountSlot)
}

func TestOpsRoutingTraceIsAbsentUntilExplicitlyEnabled(t *testing.T) {
	_, ok := OpsRoutingSelectionBreakdownFromContext(context.Background())
	require.False(t, ok)
}
