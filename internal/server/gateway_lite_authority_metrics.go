package server

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

type gatewayLiteAuthorityRuntimeMetrics struct {
	legacySnapshotSchedulerAuthority bool
	runtimeRef                       *gatewaylite.RuntimeConfigRef
}

func (m gatewayLiteAuthorityRuntimeMetrics) RuntimeMetrics(ctx context.Context) map[string]any {
	if ctx != nil && ctx.Err() != nil {
		return map[string]any{}
	}
	legacySnapshotSchedulerAuthority := m.legacySnapshotSchedulerAuthority
	if m.runtimeRef != nil {
		legacySnapshotSchedulerAuthority = m.runtimeRef.Current().LegacySnapshotSchedulerAuthority
	}
	return map[string]any{
		"gateway_lite_runtime_authority":                "local",
		"legacy_snapshot_scheduler_authority":           legacySnapshotSchedulerAuthority,
		"snapshot_accounts_groups_compatibility_only":   !legacySnapshotSchedulerAuthority,
		"local_accounts_groups_scheduler_authoritative": !legacySnapshotSchedulerAuthority,
	}
}
