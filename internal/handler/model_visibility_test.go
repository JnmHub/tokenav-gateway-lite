package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestFilterDisabledModelIDsHidesPricingDisabledModels(t *testing.T) {
	pricing := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	_, err := pricing.UpsertModelPricingOverride("gpt-5.5", service.LiteLLMModelPricing{Disabled: true})
	require.NoError(t, err)

	got := filterDisabledModelIDs([]string{"gpt-5.4", "gpt-5.5", "gpt-5.4-mini"}, pricing)
	require.Equal(t, []string{"gpt-5.4", "gpt-5.4-mini"}, got)
}
