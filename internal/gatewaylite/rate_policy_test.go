package gatewaylite

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRatePolicyAuditValidRejectsUnsafeOrInconsistentEvidence(t *testing.T) {
	valid := RatePolicyAudit{
		SchemaVersion:           1,
		BillingScope:            "token",
		GroupRateMultiplier:     1.2,
		ResolvedRateMultiplier:  1.2,
		PeakRateMultiplier:      1,
		AppliedPeakMultiplier:   1,
		EffectiveRateMultiplier: 1.2,
		Timezone:                "Asia/Shanghai",
		ResolvedAtMillis:        1_735_689_600_000,
		Version:                 "v1:0123456789abcdef",
	}
	require.True(t, valid.Valid())

	tests := map[string]func(*RatePolicyAudit){
		"unknown schema":       func(value *RatePolicyAudit) { value.SchemaVersion = 2 },
		"wrong scope":          func(value *RatePolicyAudit) { value.BillingScope = "image" },
		"invalid version":      func(value *RatePolicyAudit) { value.Version = "sha256:test" },
		"negative user rate":   func(value *RatePolicyAudit) { rate := -1.0; value.UserRateMultiplier = &rate },
		"infinite peak rate":   func(value *RatePolicyAudit) { value.PeakRateMultiplier = math.Inf(1) },
		"resolved mismatch":    func(value *RatePolicyAudit) { value.ResolvedRateMultiplier = 2 },
		"effective mismatch":   func(value *RatePolicyAudit) { value.EffectiveRateMultiplier = 2 },
		"disabled peak factor": func(value *RatePolicyAudit) { value.AppliedPeakMultiplier = 1.5 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			require.False(t, value.Valid())
		})
	}
}
