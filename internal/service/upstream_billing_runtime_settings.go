package service

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	SettingKeyUpstreamBillingAutoProbeEnabled     = "upstream_billing_auto_probe_enabled"
	SettingKeyUpstreamBillingProbeIntervalMinutes = "upstream_billing_probe_interval_minutes"
	SettingKeyUpstreamBillingProbeBatchSize       = "upstream_billing_probe_batch_size"
	SettingKeyUpstreamBillingProbeConcurrency     = "upstream_billing_probe_concurrency"
	SettingKeyOpenAIUpstreamCostSchedulingEnabled = "openai_upstream_cost_scheduling_enabled"
	SettingKeyOpenAIUpstreamCostWeight            = "openai_upstream_cost_weight"
	SettingKeyOpenAIQuotaHeadroomWeight           = "openai_quota_headroom_weight"
	SettingKeyOpenAIPreferSoonestResetWeight      = "openai_prefer_soonest_reset_weight"
	UpstreamBillingAutoProbeAccountExtraKey       = "upstream_billing_auto_probe_enabled"
	defaultUpstreamBillingProbeIntervalMinutes    = 30
	defaultUpstreamBillingProbeBatchSize          = 20
	defaultUpstreamBillingProbeConcurrency        = 4
	maxUpstreamBillingProbeBatchSize              = 20
	maxUpstreamBillingProbeConcurrency            = 4
	minUpstreamBillingProbeIntervalMinutes        = 5
	maxUpstreamBillingProbeIntervalMinutes        = 1440
	maxOpenAISchedulerSoftWeight                  = 2.0
)

// UpstreamBillingRuntimeSettings is an immutable in-process snapshot. Request
// scheduling only reads this snapshot and account snapshots already in memory;
// it never reads settings from the database on the customer request path.
type UpstreamBillingRuntimeSettings struct {
	AutoProbeEnabled         bool
	ProbeInterval            time.Duration
	ProbeBatchSize           int
	ProbeConcurrency         int
	CostSchedulingEnabled    bool
	UpstreamCostWeight       float64
	QuotaHeadroomWeight      float64
	PreferSoonestResetWeight float64
}

var upstreamBillingRuntimeSettingsCache atomic.Pointer[UpstreamBillingRuntimeSettings]

func DefaultUpstreamBillingRuntimeSettings() UpstreamBillingRuntimeSettings {
	return UpstreamBillingRuntimeSettings{
		ProbeInterval:    defaultUpstreamBillingProbeIntervalMinutes * time.Minute,
		ProbeBatchSize:   defaultUpstreamBillingProbeBatchSize,
		ProbeConcurrency: defaultUpstreamBillingProbeConcurrency,
	}
}

func CurrentUpstreamBillingRuntimeSettings() UpstreamBillingRuntimeSettings {
	if cached := upstreamBillingRuntimeSettingsCache.Load(); cached != nil {
		return *cached
	}
	return DefaultUpstreamBillingRuntimeSettings()
}

func storeUpstreamBillingRuntimeSettings(settings UpstreamBillingRuntimeSettings) {
	normalized := normalizeUpstreamBillingRuntimeSettings(settings)
	upstreamBillingRuntimeSettingsCache.Store(&normalized)
}

func normalizeUpstreamBillingRuntimeSettings(settings UpstreamBillingRuntimeSettings) UpstreamBillingRuntimeSettings {
	minutes := int(settings.ProbeInterval / time.Minute)
	if minutes < minUpstreamBillingProbeIntervalMinutes {
		minutes = defaultUpstreamBillingProbeIntervalMinutes
	}
	if minutes > maxUpstreamBillingProbeIntervalMinutes {
		minutes = maxUpstreamBillingProbeIntervalMinutes
	}
	settings.ProbeInterval = time.Duration(minutes) * time.Minute
	if settings.ProbeBatchSize <= 0 {
		settings.ProbeBatchSize = defaultUpstreamBillingProbeBatchSize
	}
	if settings.ProbeBatchSize > maxUpstreamBillingProbeBatchSize {
		settings.ProbeBatchSize = maxUpstreamBillingProbeBatchSize
	}
	if settings.ProbeConcurrency <= 0 {
		settings.ProbeConcurrency = defaultUpstreamBillingProbeConcurrency
	}
	if settings.ProbeConcurrency > maxUpstreamBillingProbeConcurrency {
		settings.ProbeConcurrency = maxUpstreamBillingProbeConcurrency
	}
	settings.UpstreamCostWeight = normalizeSchedulerSoftWeight(settings.UpstreamCostWeight)
	settings.QuotaHeadroomWeight = normalizeSchedulerSoftWeight(settings.QuotaHeadroomWeight)
	settings.PreferSoonestResetWeight = normalizeSchedulerSoftWeight(settings.PreferSoonestResetWeight)
	return settings
}

func normalizeSchedulerSoftWeight(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > maxOpenAISchedulerSoftWeight {
		return maxOpenAISchedulerSoftWeight
	}
	return value
}

// WarmUpstreamBillingRuntimeSettings is fail-safe: a read failure leaves the
// process on the default disabled switches instead of guessing an enabled mode.
func (s *SettingService) WarmUpstreamBillingRuntimeSettings(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		storeUpstreamBillingRuntimeSettings(DefaultUpstreamBillingRuntimeSettings())
		return nil
	}
	keys := []string{
		SettingKeyUpstreamBillingAutoProbeEnabled,
		SettingKeyUpstreamBillingProbeIntervalMinutes,
		SettingKeyUpstreamBillingProbeBatchSize,
		SettingKeyUpstreamBillingProbeConcurrency,
		SettingKeyOpenAIUpstreamCostSchedulingEnabled,
		SettingKeyOpenAIUpstreamCostWeight,
		SettingKeyOpenAIQuotaHeadroomWeight,
		SettingKeyOpenAIPreferSoonestResetWeight,
	}
	values, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return err
	}
	storeUpstreamBillingRuntimeSettings(decodeUpstreamBillingRuntimeSettings(values))
	return nil
}

func decodeUpstreamBillingRuntimeSettings(values map[string]string) UpstreamBillingRuntimeSettings {
	settings := DefaultUpstreamBillingRuntimeSettings()
	settings.AutoProbeEnabled = strings.EqualFold(strings.TrimSpace(values[SettingKeyUpstreamBillingAutoProbeEnabled]), "true")
	settings.CostSchedulingEnabled = strings.EqualFold(strings.TrimSpace(values[SettingKeyOpenAIUpstreamCostSchedulingEnabled]), "true")
	if value, err := strconv.Atoi(strings.TrimSpace(values[SettingKeyUpstreamBillingProbeIntervalMinutes])); err == nil {
		settings.ProbeInterval = time.Duration(value) * time.Minute
	}
	if value, err := strconv.Atoi(strings.TrimSpace(values[SettingKeyUpstreamBillingProbeBatchSize])); err == nil {
		settings.ProbeBatchSize = value
	}
	if value, err := strconv.Atoi(strings.TrimSpace(values[SettingKeyUpstreamBillingProbeConcurrency])); err == nil {
		settings.ProbeConcurrency = value
	}
	settings.UpstreamCostWeight = parseRuntimeWeight(values[SettingKeyOpenAIUpstreamCostWeight])
	settings.QuotaHeadroomWeight = parseRuntimeWeight(values[SettingKeyOpenAIQuotaHeadroomWeight])
	settings.PreferSoonestResetWeight = parseRuntimeWeight(values[SettingKeyOpenAIPreferSoonestResetWeight])
	return normalizeUpstreamBillingRuntimeSettings(settings)
}

func parseRuntimeWeight(raw string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return normalizeSchedulerSoftWeight(value)
}

func resetUpstreamBillingRuntimeSettingsForTest() {
	upstreamBillingRuntimeSettingsCache = atomic.Pointer[UpstreamBillingRuntimeSettings]{}
}
