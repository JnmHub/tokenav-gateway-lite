package gatewaylite

import "sync/atomic"

type EffectivePolicyAuditStamp struct {
	Revision     int64
	Checksum     string
	PriceVersion int64
}

type effectivePolicyAuditSnapshot struct {
	byLocalGroupID map[int64]EffectivePolicyAuditStamp
}

var defaultEffectivePolicyAudit atomic.Pointer[effectivePolicyAuditSnapshot]

// SetDefaultEffectivePolicyAudit swaps one immutable lookup after config push
// calculation. Request reporting performs only a lock-free map read and never
// recalculates or serializes a policy in the hot path.
func SetDefaultEffectivePolicyAudit(policies []GatewayEffectivePolicy) {
	snapshot := &effectivePolicyAuditSnapshot{byLocalGroupID: make(map[int64]EffectivePolicyAuditStamp, len(policies))}
	for _, policy := range policies {
		snapshot.byLocalGroupID[policy.LocalGroupID] = EffectivePolicyAuditStamp{
			Revision: policy.Revision, Checksum: policy.Checksum, PriceVersion: policy.SourceVersions.Prices,
		}
	}
	defaultEffectivePolicyAudit.Store(snapshot)
}

func EffectivePolicyAuditForGroup(localGroupID int64) EffectivePolicyAuditStamp {
	snapshot := defaultEffectivePolicyAudit.Load()
	if snapshot == nil {
		return EffectivePolicyAuditStamp{}
	}
	return snapshot.byLocalGroupID[localGroupID]
}
