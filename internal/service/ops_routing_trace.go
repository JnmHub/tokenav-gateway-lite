package service

import (
	"context"
	"sync/atomic"
	"time"
)

type opsRoutingTraceContextKey struct{}

// OpsRoutingSelectionBreakdown is a request-local snapshot of non-overlapping
// account-selection stages. The trace uses per-request atomics because some
// scheduler helpers may evolve to run concurrently; there is no shared global
// lock or cross-request contention.
type OpsRoutingSelectionBreakdown struct {
	GroupValidation     int64
	StickyLookup        int64
	AccountResolve      int64
	AccountSlot         int64
	StickyRefresh       int64
	CandidateResolution int64
	Settings            int64
}

type opsRoutingTrace struct {
	groupValidation     atomic.Int64
	stickyLookup        atomic.Int64
	accountResolve      atomic.Int64
	accountSlot         atomic.Int64
	stickyRefresh       atomic.Int64
	candidateResolution atomic.Int64
	settings            atomic.Int64
}

const (
	OpsRoutingSelectionStageGroupValidation     = "group_validation"
	OpsRoutingSelectionStageStickyLookup        = "sticky_lookup"
	OpsRoutingSelectionStageAccountResolve      = "account_resolve"
	OpsRoutingSelectionStageAccountSlot         = "account_slot"
	OpsRoutingSelectionStageStickyRefresh       = "sticky_refresh"
	OpsRoutingSelectionStageCandidateResolution = "candidate_resolution"
	OpsRoutingSelectionStageSettings            = "settings"
)

func ContextWithOpsRoutingTrace(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(opsRoutingTraceContextKey{}).(*opsRoutingTrace); ok {
		return ctx
	}
	return context.WithValue(ctx, opsRoutingTraceContextKey{}, &opsRoutingTrace{})
}

func AddOpsRoutingSelectionLatency(ctx context.Context, stage string, elapsed time.Duration) {
	if ctx == nil || elapsed < 0 {
		return
	}
	trace, _ := ctx.Value(opsRoutingTraceContextKey{}).(*opsRoutingTrace)
	if trace == nil {
		return
	}
	value := elapsed.Milliseconds()
	switch stage {
	case OpsRoutingSelectionStageGroupValidation:
		trace.groupValidation.Add(value)
	case OpsRoutingSelectionStageStickyLookup:
		trace.stickyLookup.Add(value)
	case OpsRoutingSelectionStageAccountResolve:
		trace.accountResolve.Add(value)
	case OpsRoutingSelectionStageAccountSlot:
		trace.accountSlot.Add(value)
	case OpsRoutingSelectionStageStickyRefresh:
		trace.stickyRefresh.Add(value)
	case OpsRoutingSelectionStageCandidateResolution:
		trace.candidateResolution.Add(value)
	case OpsRoutingSelectionStageSettings:
		trace.settings.Add(value)
	}
}

func OpsRoutingSelectionBreakdownFromContext(ctx context.Context) (OpsRoutingSelectionBreakdown, bool) {
	if ctx == nil {
		return OpsRoutingSelectionBreakdown{}, false
	}
	trace, _ := ctx.Value(opsRoutingTraceContextKey{}).(*opsRoutingTrace)
	if trace == nil {
		return OpsRoutingSelectionBreakdown{}, false
	}
	return OpsRoutingSelectionBreakdown{
		GroupValidation:     trace.groupValidation.Load(),
		StickyLookup:        trace.stickyLookup.Load(),
		AccountResolve:      trace.accountResolve.Load(),
		AccountSlot:         trace.accountSlot.Load(),
		StickyRefresh:       trace.stickyRefresh.Load(),
		CandidateResolution: trace.candidateResolution.Load(),
		Settings:            trace.settings.Load(),
	}, true
}
