package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageBillingCommandQuantizesEveryMonetaryField(t *testing.T) {
	command := &UsageBillingCommand{
		RequestID: "quantize", BalanceCost: 0.000078125, SubscriptionCost: 0.000078125,
		APIKeyQuotaCost: 0.000078125, APIKeyRateLimitCost: 0.000078125, AccountQuotaCost: 0.000078125,
	}
	command.Normalize()
	require.Equal(t, 0.00007813, command.BalanceCost)
	require.Equal(t, command.BalanceCost, command.SubscriptionCost)
	require.Equal(t, command.BalanceCost, command.APIKeyQuotaCost)
	require.Equal(t, command.BalanceCost, command.APIKeyRateLimitCost)
	require.Equal(t, command.BalanceCost, command.AccountQuotaCost)
}

func TestQuantizeUsageBillingAmountPassesThroughNonFinite(t *testing.T) {
	require.True(t, math.IsNaN(QuantizeUsageBillingAmount(math.NaN())))
	require.True(t, math.IsInf(QuantizeUsageBillingAmount(math.Inf(1)), 1))
}
