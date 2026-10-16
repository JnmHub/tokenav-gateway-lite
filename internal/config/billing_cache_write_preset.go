package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

const (
	BillingCacheWritePresetLow      = "low"
	BillingCacheWritePresetStandard = "standard"
	BillingCacheWritePresetHigh     = "high"
)

// BillingCacheWritePresetNames is intentionally small and stable so an admin
// UI can render three buttons instead of asking operators to type five related
// queue values. Numeric fields remain optional overrides for exceptional nodes.
func BillingCacheWritePresetNames() []string {
	return []string{
		BillingCacheWritePresetLow,
		BillingCacheWritePresetStandard,
		BillingCacheWritePresetHigh,
	}
}

func NormalizeBillingCacheWritePreset(name string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		normalized = BillingCacheWritePresetStandard
	}
	if _, err := billingCacheWritePreset(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

// ResolveBillingCacheWriteConfig applies one bounded preset and then keeps any
// positive numeric override supplied by the operator. An empty preset means
// standard, preserving safe defaults for programmatic Config construction.
func ResolveBillingCacheWriteConfig(in BillingCacheWriteConfig) (BillingCacheWriteConfig, error) {
	presetName, err := NormalizeBillingCacheWritePreset(in.Preset)
	if err != nil {
		return BillingCacheWriteConfig{}, err
	}
	preset, err := billingCacheWritePreset(presetName)
	if err != nil {
		return BillingCacheWriteConfig{}, err
	}
	preset.Preset = presetName
	if in.CriticalWorkerCount != 0 {
		preset.CriticalWorkerCount = in.CriticalWorkerCount
	}
	if in.CriticalBufferSize != 0 {
		preset.CriticalBufferSize = in.CriticalBufferSize
	}
	if in.BestEffortWorkerCount != 0 {
		preset.BestEffortWorkerCount = in.BestEffortWorkerCount
	}
	if in.BestEffortBufferSize != 0 {
		preset.BestEffortBufferSize = in.BestEffortBufferSize
	}
	if in.TimeoutMilliseconds != 0 {
		preset.TimeoutMilliseconds = in.TimeoutMilliseconds
	}
	return preset, nil
}

func billingCacheWritePreset(name string) (BillingCacheWriteConfig, error) {
	switch name {
	case BillingCacheWritePresetLow:
		return BillingCacheWriteConfig{
			CriticalWorkerCount: 2, CriticalBufferSize: 512,
			BestEffortWorkerCount: 1, BestEffortBufferSize: 128,
			TimeoutMilliseconds: 2000,
		}, nil
	case BillingCacheWritePresetStandard:
		return BillingCacheWriteConfig{
			CriticalWorkerCount: 8, CriticalBufferSize: 2048,
			BestEffortWorkerCount: 2, BestEffortBufferSize: 512,
			TimeoutMilliseconds: 2000,
		}, nil
	case BillingCacheWritePresetHigh:
		return BillingCacheWriteConfig{
			CriticalWorkerCount: 16, CriticalBufferSize: 8192,
			BestEffortWorkerCount: 4, BestEffortBufferSize: 2048,
			TimeoutMilliseconds: 1500,
		}, nil
	default:
		return BillingCacheWriteConfig{}, fmt.Errorf(
			"billing.cache_write.preset must be one of: %s/%s/%s",
			BillingCacheWritePresetLow,
			BillingCacheWritePresetStandard,
			BillingCacheWritePresetHigh,
		)
	}
}

// applyBillingCacheWritePresetDefaults runs after the config file is read but
// before Unmarshal. Viper's precedence then lets explicit YAML/env values
// override the selected preset without us having to guess whether a zero value
// came from an omitted key or an intentional operator setting.
func applyBillingCacheWritePresetDefaults() error {
	name, err := NormalizeBillingCacheWritePreset(viper.GetString("billing.cache_write.preset"))
	if err != nil {
		return err
	}
	preset, err := billingCacheWritePreset(name)
	if err != nil {
		return err
	}
	viper.SetDefault("billing.cache_write.preset", name)
	viper.SetDefault("billing.cache_write.critical_worker_count", preset.CriticalWorkerCount)
	viper.SetDefault("billing.cache_write.critical_buffer_size", preset.CriticalBufferSize)
	viper.SetDefault("billing.cache_write.best_effort_worker_count", preset.BestEffortWorkerCount)
	viper.SetDefault("billing.cache_write.best_effort_buffer_size", preset.BestEffortBufferSize)
	viper.SetDefault("billing.cache_write.timeout_milliseconds", preset.TimeoutMilliseconds)
	return nil
}
