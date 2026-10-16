package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestGetOpsAdvancedSettings_DefaultHidesOpenAITokenStats(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	cfg, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
	}
	if cfg.DisplayOpenAITokenStats {
		t.Fatalf("DisplayOpenAITokenStats = true, want false by default")
	}
	if !cfg.DisplayAlertEvents {
		t.Fatalf("DisplayAlertEvents = false, want true by default")
	}
	if repo.setCalls != 1 {
		t.Fatalf("expected defaults to be persisted once, got %d", repo.setCalls)
	}
}

func TestUpdateOpsAdvancedSettings_PersistsOpenAITokenStatsVisibility(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	cfg := defaultOpsAdvancedSettings()
	cfg.DisplayOpenAITokenStats = true
	cfg.DisplayAlertEvents = false

	updated, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg)
	if err != nil {
		t.Fatalf("UpdateOpsAdvancedSettings() error = %v", err)
	}
	if !updated.DisplayOpenAITokenStats {
		t.Fatalf("DisplayOpenAITokenStats = false, want true")
	}
	if updated.DisplayAlertEvents {
		t.Fatalf("DisplayAlertEvents = true, want false")
	}

	reloaded, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() after update error = %v", err)
	}
	if !reloaded.DisplayOpenAITokenStats {
		t.Fatalf("reloaded DisplayOpenAITokenStats = false, want true")
	}
	if reloaded.DisplayAlertEvents {
		t.Fatalf("reloaded DisplayAlertEvents = true, want false")
	}
}

func TestGetOpsAdvancedSettings_BackfillsNewDisplayFlagsFromDefaults(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	legacyCfg := map[string]any{
		"data_retention": map[string]any{
			"cleanup_enabled":               false,
			"cleanup_schedule":              "0 2 * * *",
			"error_log_retention_days":      30,
			"minute_metrics_retention_days": 30,
			"hourly_metrics_retention_days": 30,
		},
		"aggregation": map[string]any{
			"aggregation_enabled": false,
		},
		"ignore_count_tokens_errors":    true,
		"ignore_context_canceled":       true,
		"ignore_no_available_accounts":  false,
		"ignore_invalid_api_key_errors": false,
		"auto_refresh_enabled":          false,
		"auto_refresh_interval_seconds": 30,
	}
	raw, err := json.Marshal(legacyCfg)
	if err != nil {
		t.Fatalf("marshal legacy config: %v", err)
	}
	repo.values[SettingKeyOpsAdvancedSettings] = string(raw)

	cfg, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
	}
	if cfg.DisplayOpenAITokenStats {
		t.Fatalf("DisplayOpenAITokenStats = true, want false default backfill")
	}
	if !cfg.DisplayAlertEvents {
		t.Fatalf("DisplayAlertEvents = false, want true default backfill")
	}
}

func TestGetOpsAdvancedSettings_CodexTailBurstFallsBackToYAMLWhenUnset(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsAdvancedSettings] = `{"data_retention":{},"codex_tail_burst":{}}`
	staticCfg := &config.Config{}
	staticCfg.CodexTailBurst.Enabled = true
	svc := &OpsService{settingRepo: repo, cfg: staticCfg}

	got, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
	}
	if got.CodexTailBurst.Enabled == nil || !*got.CodexTailBurst.Enabled {
		t.Fatalf("CodexTailBurst.Enabled = %v, want effective YAML true", got.CodexTailBurst.Enabled)
	}
	var persisted map[string]any
	if err := json.Unmarshal([]byte(repo.values[SettingKeyOpsAdvancedSettings]), &persisted); err != nil {
		t.Fatalf("decode persisted settings: %v", err)
	}
	if block, ok := persisted["codex_tail_burst"].(map[string]any); ok {
		if _, exists := block["enabled"]; exists {
			t.Fatalf("read should not persist YAML fallback, got enabled=%v", block["enabled"])
		}
	}
}

func TestGetOpsAdvancedSettings_CodexTailBurstFallsBackToYAMLWhenLegacyJSONIsInvalid(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsAdvancedSettings] = "{invalid"
	staticCfg := &config.Config{}
	staticCfg.CodexTailBurst.Enabled = true
	svc := &OpsService{settingRepo: repo, cfg: staticCfg}

	got, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
	}
	if got.CodexTailBurst.Enabled == nil || !*got.CodexTailBurst.Enabled {
		t.Fatalf("invalid legacy config must use effective YAML true, got %v", got.CodexTailBurst.Enabled)
	}
}

func TestGetCodexTailBurstSettings_StandaloneValueWinsWithoutMutatingAdvancedSettings(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsCodexTailBurst] = `{"enabled":false}`
	repo.values[SettingKeyOpsAdvancedSettings] = `{"codex_tail_burst":{"enabled":true}}`
	svc := &OpsService{settingRepo: repo, cfg: &config.Config{}}

	got, err := svc.GetCodexTailBurstSettings(context.Background())
	if err != nil {
		t.Fatalf("GetCodexTailBurstSettings() error = %v", err)
	}
	if got.Enabled == nil || *got.Enabled {
		t.Fatalf("standalone value = %v, want explicit false", got.Enabled)
	}
	if repo.setCalls != 0 {
		t.Fatalf("narrow read must not persist settings, got %d writes", repo.setCalls)
	}
}

func TestGetCodexTailBurstSettings_MissingKeysDoesNotInitializeAdvancedSettings(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	cfg := &config.Config{}
	cfg.CodexTailBurst.Enabled = true
	svc := &OpsService{settingRepo: repo, cfg: cfg}

	got, err := svc.GetCodexTailBurstSettings(context.Background())
	if err != nil {
		t.Fatalf("GetCodexTailBurstSettings() error = %v", err)
	}
	if got.Enabled == nil || !*got.Enabled {
		t.Fatalf("YAML fallback = %v, want true", got.Enabled)
	}
	if repo.setCalls != 0 {
		t.Fatalf("narrow read must not initialize aggregate settings, got %d writes", repo.setCalls)
	}
	if _, ok := repo.values[SettingKeyOpsAdvancedSettings]; ok {
		t.Fatal("narrow read unexpectedly created aggregate advanced settings")
	}
}

func TestGetCodexTailBurstSettings_PropagatesStandaloneRepositoryError(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	dbErr := errors.New("database unavailable")
	repo.getValueFn = func(key string) (string, error) {
		if key == SettingKeyOpsCodexTailBurst {
			return "", dbErr
		}
		return "", ErrSettingNotFound
	}
	staticCfg := &config.Config{}
	staticCfg.CodexTailBurst.Enabled = true
	svc := &OpsService{settingRepo: repo, cfg: staticCfg}

	got, err := svc.GetCodexTailBurstSettings(context.Background())
	if !errors.Is(err, dbErr) {
		t.Fatalf("GetCodexTailBurstSettings() error = %v, want database error", err)
	}
	if got.Enabled != nil {
		t.Fatalf("failed read returned fallback setting %v, want empty result", got.Enabled)
	}
}

func TestGetCodexTailBurstSettings_PropagatesLegacyRepositoryError(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	dbErr := errors.New("legacy settings query failed")
	repo.getValueFn = func(key string) (string, error) {
		if key == SettingKeyOpsAdvancedSettings {
			return "", dbErr
		}
		return "", ErrSettingNotFound
	}
	staticCfg := &config.Config{}
	staticCfg.CodexTailBurst.Enabled = true
	svc := &OpsService{settingRepo: repo, cfg: staticCfg}

	got, err := svc.GetCodexTailBurstSettings(context.Background())
	if !errors.Is(err, dbErr) {
		t.Fatalf("GetCodexTailBurstSettings() error = %v, want database error", err)
	}
	if got.Enabled != nil {
		t.Fatalf("failed legacy read returned fallback setting %v, want empty result", got.Enabled)
	}
}

func TestGetOpsAdvancedSettings_PropagatesStandaloneRepositoryErrorForEffectiveTailBurst(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsAdvancedSettings] = `{"codex_tail_burst":{}}`
	dbErr := errors.New("standalone settings query failed")
	repo.getValueFn = func(key string) (string, error) {
		if key == SettingKeyOpsCodexTailBurst {
			return "", dbErr
		}
		return repo.values[key], nil
	}
	svc := &OpsService{settingRepo: repo, cfg: &config.Config{}}

	if _, err := svc.GetOpsAdvancedSettings(context.Background()); !errors.Is(err, dbErr) {
		t.Fatalf("GetOpsAdvancedSettings() error = %v, want database error", err)
	}
}

func TestWarmCodexTailBurstSettings_DatabaseErrorDoesNotFallbackToYAML(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	dbErr := errors.New("settings database unavailable")
	repo.getValueFn = func(key string) (string, error) {
		if key == SettingKeyOpsCodexTailBurst {
			return "", dbErr
		}
		return "", ErrSettingNotFound
	}
	staticCfg := &config.Config{}
	staticCfg.CodexTailBurst.Enabled = true
	settingService := NewSettingService(repo, staticCfg)

	got := settingService.WarmCodexTailBurstSettings(context.Background())
	if got.Enabled == nil || *got.Enabled {
		t.Fatalf("database error must not enable Tail-Burst from YAML, got %v", got.Enabled)
	}
}

func TestWarmCodexTailBurstSettings_DatabaseErrorKeepsPriorSnapshot(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	var fail bool
	dbErr := errors.New("settings database temporarily unavailable")
	repo.getValueFn = func(key string) (string, error) {
		if key == SettingKeyOpsCodexTailBurst {
			if fail {
				return "", dbErr
			}
			return `{"enabled":true}`, nil
		}
		return "", ErrSettingNotFound
	}
	settingService := NewSettingService(repo, &config.Config{})

	first := settingService.WarmCodexTailBurstSettings(context.Background())
	if first.Enabled == nil || !*first.Enabled {
		t.Fatalf("initial Tail-Burst snapshot = %v, want true", first.Enabled)
	}
	fail = true
	second := settingService.WarmCodexTailBurstSettings(context.Background())
	if second.Enabled == nil || !*second.Enabled {
		t.Fatalf("database error should keep prior true snapshot, got %v", second.Enabled)
	}
}

type opsSettingsCleanupReloadStub struct{ calls int }

func (s *opsSettingsCleanupReloadStub) Reload(context.Context) error {
	s.calls++
	return nil
}

func TestUpdateCodexTailBurstSettings_OnlyWritesStandaloneKeyAndTailBurstSink(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsAdvancedSettings] = `{"data_retention":{"cleanup_enabled":true}}`
	cleanup := &opsSettingsCleanupReloadStub{}
	var quotaSinkCalls, tailBurstSinkCalls int
	var received OpsCodexTailBurstSettings
	svc := &OpsService{settingRepo: repo, cleanupReloader: cleanup}
	svc.SetOpenAIQuotaAutoPauseSettingsSink(func(OpsOpenAIAccountQuotaAutoPauseSettings) { quotaSinkCalls++ })
	svc.SetCodexTailBurstSettingsSink(func(settings OpsCodexTailBurstSettings) {
		tailBurstSinkCalls++
		received = settings
	})

	got, err := svc.UpdateCodexTailBurstSettings(context.Background(), true)
	if err != nil {
		t.Fatalf("UpdateCodexTailBurstSettings() error = %v", err)
	}
	if got.Enabled == nil || !*got.Enabled || received.Enabled == nil || !*received.Enabled {
		t.Fatalf("returned/sunk setting = got %v, received %v; want true", got.Enabled, received.Enabled)
	}
	if repo.setCalls != 1 {
		t.Fatalf("narrow update should perform one write, got %d", repo.setCalls)
	}
	if repo.values[SettingKeyOpsCodexTailBurst] != `{"enabled":true}` {
		t.Fatalf("standalone value = %q, want {\"enabled\":true}", repo.values[SettingKeyOpsCodexTailBurst])
	}
	if repo.values[SettingKeyOpsAdvancedSettings] != `{"data_retention":{"cleanup_enabled":true}}` {
		t.Fatalf("aggregate settings changed unexpectedly: %q", repo.values[SettingKeyOpsAdvancedSettings])
	}
	if quotaSinkCalls != 0 {
		t.Fatalf("quota sink called %d times, want 0", quotaSinkCalls)
	}
	if tailBurstSinkCalls != 1 {
		t.Fatalf("tail-burst sink called %d times, want 1", tailBurstSinkCalls)
	}
	if cleanup.calls != 0 {
		t.Fatalf("cleanup reload called %d times, want 0", cleanup.calls)
	}
}

func TestUpdateOpsAdvancedSettings_NilCodexTailBurstPreservesPersistedValue(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsAdvancedSettings] = `{"codex_tail_burst":{"enabled":true}}`
	staticCfg := &config.Config{}
	staticCfg.CodexTailBurst.Enabled = false
	svc := &OpsService{settingRepo: repo, cfg: staticCfg}

	input := defaultOpsAdvancedSettings()
	if input.CodexTailBurst.Enabled != nil {
		t.Fatal("test input must omit CodexTailBurst.Enabled")
	}
	updated, err := svc.UpdateOpsAdvancedSettings(context.Background(), input)
	if err != nil {
		t.Fatalf("UpdateOpsAdvancedSettings() error = %v", err)
	}
	if updated.CodexTailBurst.Enabled == nil || !*updated.CodexTailBurst.Enabled {
		t.Fatalf("nil update must preserve persisted true, got %v", updated.CodexTailBurst.Enabled)
	}
	var persisted struct {
		CodexTailBurst OpsCodexTailBurstSettings `json:"codex_tail_burst"`
	}
	if err := json.Unmarshal([]byte(repo.values[SettingKeyOpsAdvancedSettings]), &persisted); err != nil {
		t.Fatalf("decode updated settings: %v", err)
	}
	if persisted.CodexTailBurst.Enabled == nil || !*persisted.CodexTailBurst.Enabled {
		t.Fatalf("persisted CodexTailBurst.Enabled = %v, want true", persisted.CodexTailBurst.Enabled)
	}
}

func TestUpdateOpsAdvancedSettings_NilCodexTailBurstUsesYAMLWhenNoPersistedValue(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	staticCfg := &config.Config{}
	staticCfg.CodexTailBurst.Enabled = true
	svc := &OpsService{settingRepo: repo, cfg: staticCfg}

	updated, err := svc.UpdateOpsAdvancedSettings(context.Background(), defaultOpsAdvancedSettings())
	if err != nil {
		t.Fatalf("UpdateOpsAdvancedSettings() error = %v", err)
	}
	if updated.CodexTailBurst.Enabled == nil || !*updated.CodexTailBurst.Enabled {
		t.Fatalf("nil update must use YAML true, got %v", updated.CodexTailBurst.Enabled)
	}
	var persisted struct {
		CodexTailBurst OpsCodexTailBurstSettings `json:"codex_tail_burst"`
	}
	if err := json.Unmarshal([]byte(repo.values[SettingKeyOpsAdvancedSettings]), &persisted); err != nil {
		t.Fatalf("decode updated settings: %v", err)
	}
	if persisted.CodexTailBurst.Enabled == nil || !*persisted.CodexTailBurst.Enabled {
		t.Fatalf("persisted YAML fallback = %v, want true", persisted.CodexTailBurst.Enabled)
	}
}

func TestUpdateOpsAdvancedSettings_ExplicitTailBurstUsesAtomicSetMultiple(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}
	enabled := true
	cfg := defaultOpsAdvancedSettings()
	cfg.CodexTailBurst.Enabled = &enabled

	if _, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg); err != nil {
		t.Fatalf("UpdateOpsAdvancedSettings() error = %v", err)
	}
	if repo.setMultipleCalls != 1 {
		t.Fatalf("expected one atomic SetMultiple call, got %d", repo.setMultipleCalls)
	}
	if repo.setCalls != 0 {
		t.Fatalf("explicit Tail-Burst update unexpectedly used separate Set calls: %d", repo.setCalls)
	}
	if _, ok := repo.values[SettingKeyOpsAdvancedSettings]; !ok {
		t.Fatal("aggregate settings were not written")
	}
	if repo.values[SettingKeyOpsCodexTailBurst] != `{"enabled":true}` {
		t.Fatalf("standalone value = %q, want explicit true", repo.values[SettingKeyOpsCodexTailBurst])
	}
}

func TestUpdateOpsAdvancedSettings_AtomicSetMultipleFailureLeavesBothKeysUntouched(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	oldAdvanced := `{"codex_tail_burst":{"enabled":false}}`
	oldStandalone := `{"enabled":false}`
	repo.values[SettingKeyOpsAdvancedSettings] = oldAdvanced
	repo.values[SettingKeyOpsCodexTailBurst] = oldStandalone
	dbErr := errors.New("transaction aborted")
	repo.setMultipleFn = func(map[string]string) error { return dbErr }
	svc := &OpsService{settingRepo: repo}
	enabled := true
	cfg := defaultOpsAdvancedSettings()
	cfg.CodexTailBurst.Enabled = &enabled

	if _, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg); !errors.Is(err, dbErr) {
		t.Fatalf("UpdateOpsAdvancedSettings() error = %v, want transaction error", err)
	}
	if repo.values[SettingKeyOpsAdvancedSettings] != oldAdvanced || repo.values[SettingKeyOpsCodexTailBurst] != oldStandalone {
		t.Fatalf("atomic failure partially changed settings: advanced=%q standalone=%q", repo.values[SettingKeyOpsAdvancedSettings], repo.values[SettingKeyOpsCodexTailBurst])
	}
}

func TestGetOpenAIQuotaAutoPauseSettings_ReadsDefaultsFromOpsAdvancedSettings(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsAdvancedSettings] = `{"openai_account_quota_auto_pause":{"default_threshold_5h":0.95,"default_threshold_7d":0.9}}`
	svc := NewSettingService(repo, &config.Config{})

	// Warm the in-memory cache synchronously so the assertion below is deterministic.
	// GetOpenAIQuotaAutoPauseSettings is non-blocking on the hot path (returns the
	// cached value, refreshes asynchronously); for tests and startup, Warm is the
	// synchronous entry point that guarantees a populated cache.
	settings := svc.WarmOpenAIQuotaAutoPauseSettings(context.Background())
	if settings.DefaultThreshold5h != 0.95 {
		t.Fatalf("DefaultThreshold5h = %v, want 0.95", settings.DefaultThreshold5h)
	}
	if settings.DefaultThreshold7d != 0.9 {
		t.Fatalf("DefaultThreshold7d = %v, want 0.9", settings.DefaultThreshold7d)
	}

	// Subsequent Get must hit the warm cache and return the same value without any DB
	// access — that's the hot-path invariant.
	cached := svc.GetOpenAIQuotaAutoPauseSettings(context.Background())
	if cached.DefaultThreshold5h != 0.95 || cached.DefaultThreshold7d != 0.9 {
		t.Fatalf("cached read = %+v, want {0.95, 0.9}", cached)
	}
}

// Hot-path invariant: a Get with cold cache must return immediately (zero defaults)
// rather than blocking on the DB. The async refresher will populate the cache for
// subsequent calls.
func TestGetOpenAIQuotaAutoPauseSettings_ColdCacheNonBlocking(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsAdvancedSettings] = `{"openai_account_quota_auto_pause":{"default_threshold_5h":0.7}}`
	svc := NewSettingService(repo, &config.Config{})

	start := time.Now()
	settings := svc.GetOpenAIQuotaAutoPauseSettings(context.Background())
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("cold-cache Get must be non-blocking, took %v", elapsed)
	}
	// Cold cache means we get zero defaults (the async refresh hasn't completed yet).
	if settings.DefaultThreshold5h != 0 || settings.DefaultThreshold7d != 0 {
		t.Fatalf("cold-cache Get = %+v, want zeroes", settings)
	}
}

// Explicit cache write (e.g. from UpdateOpsAdvancedSettings) must be visible on the
// very next read without any DB roundtrip.
func TestSetOpenAIQuotaAutoPauseSettings_VisibleImmediately(t *testing.T) {
	svc := NewSettingService(newRuntimeSettingRepoStub(), &config.Config{})

	svc.SetOpenAIQuotaAutoPauseSettings(OpsOpenAIAccountQuotaAutoPauseSettings{
		DefaultThreshold5h: 0.88,
		DefaultThreshold7d: 0.77,
	})

	got := svc.GetOpenAIQuotaAutoPauseSettings(context.Background())
	if got.DefaultThreshold5h != 0.88 || got.DefaultThreshold7d != 0.77 {
		t.Fatalf("after Set, Get = %+v, want {0.88, 0.77}", got)
	}
}
