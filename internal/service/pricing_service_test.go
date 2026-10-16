package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type pricingRemoteClientStub struct {
	body      []byte
	hash      string
	fetchErr  error
	hashErr   error
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

type pricingRemoteCatalogStub struct {
	bodies map[string][]byte
}

func (c *pricingRemoteCatalogStub) FetchPricingJSON(_ context.Context, rawURL string) ([]byte, error) {
	body, ok := c.bodies[rawURL]
	if !ok {
		return nil, fmt.Errorf("unexpected URL %s", rawURL)
	}
	return append([]byte(nil), body...), nil
}

func (c *pricingRemoteCatalogStub) FetchHashText(context.Context, string) (string, error) {
	return "", errors.New("hash endpoint is not used with a manifest")
}

func (c *pricingRemoteClientStub) FetchPricingJSON(context.Context, string) ([]byte, error) {
	if c.started != nil {
		c.startOnce.Do(func() { close(c.started) })
	}
	if c.release != nil {
		<-c.release
	}
	return c.body, c.fetchErr
}

func (c *pricingRemoteClientStub) FetchHashText(context.Context, string) (string, error) {
	return c.hash, c.hashErr
}

func pricingCatalogForTest(overrides map[string]any) []byte {
	catalog := make(map[string]any, minRemotePricingEntries)
	for i := 0; i < minRemotePricingEntries; i++ {
		catalog[fmt.Sprintf("test-model-%02d", i)] = map[string]any{
			"input_cost_per_token":  1e-6,
			"output_cost_per_token": 2e-6,
			"litellm_provider":      "test",
			"mode":                  "chat",
		}
	}
	for model, entry := range overrides {
		catalog[model] = entry
	}
	body, _ := json.Marshal(catalog)
	return body
}

func pricingSHA256(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func pricingManifestForTest(t *testing.T, body []byte, sequence uint64, modelCount int) []byte {
	t.Helper()
	manifest, err := json.Marshal(pricingCatalogManifest{
		SchemaVersion: 1, CatalogVersion: "2026.08.22.2", PublishedAt: "2026-08-22T02:00:00Z",
		Sequence: sequence, ModelCount: modelCount,
		PricingCatalog: pricingCatalogManifestFile{
			Path: "model_prices_and_context_window.json", SHA256: pricingSHA256(body), Size: int64(len(body)),
		},
	})
	require.NoError(t, err)
	return manifest
}

func TestUseFallbackPricingUsesEmbeddedSnapshotWhenFileMissing(t *testing.T) {
	dataDir := t.TempDir()
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		DataDir:      dataDir,
		FallbackFile: filepath.Join(dataDir, "missing-pricing.json"),
	}}, nil)

	require.NoError(t, svc.useFallbackPricing())
	require.NotNil(t, svc.GetModelPricing("gpt-5.6-terra"))
	require.FileExists(t, filepath.Join(dataDir, "model_pricing.json"))
}

func TestParsePricingData_RemoteValueWinsAndMissingMetadataUsesFallback(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gpt-5.4":{"input_cost_per_token":0,"output_cost_per_token":0.000099}
	}`))
	require.NoError(t, err)
	pricing := data["gpt-5.4"]
	require.NotNil(t, pricing)
	require.Zero(t, pricing.InputCostPerToken, "explicit remote zero must not be treated as missing")
	require.InDelta(t, 0.000099, pricing.OutputCostPerToken, 1e-12)
	require.Equal(t, "openai", pricing.LiteLLMProvider)
	require.Equal(t, "chat", pricing.Mode)
}

func TestLoadPricingOverridesDoesNotInjectOfficialFallbacks(t *testing.T) {
	dataDir := t.TempDir()
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: dataDir}}, nil)
	overrides := []byte(`{"custom-model":{"input_cost_per_token":0.000007}}`)
	require.NoError(t, os.WriteFile(svc.getPricingOverridesFilePath(), overrides, 0644))

	loaded, err := svc.loadPricingOverrides()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Contains(t, loaded, "custom-model")
	require.NotContains(t, loaded, "gpt-5.4")
}

func TestDownloadPricingDataRejectsHashMismatchAndKeepsActiveSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	body := pricingCatalogForTest(map[string]any{
		"gpt-5.4": map[string]any{"input_cost_per_token": 0.000007, "output_cost_per_token": 0.000008},
	})
	remote := &pricingRemoteClientStub{body: body, hash: strings.Repeat("0", sha256.Size*2)}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://example.com/pricing.json",
		HashURL:   "https://example.com/pricing.sha256",
		DataDir:   dataDir,
	}}, remote)
	svc.basePricingData["active"] = &LiteLLMModelPricing{InputCostPerToken: 1}
	svc.pricingData["active"] = &LiteLLMModelPricing{InputCostPerToken: 1}

	err := svc.downloadPricingData()
	require.ErrorContains(t, err, "hash mismatch")
	require.InDelta(t, 1, svc.GetModelPricing("active").InputCostPerToken, 1e-12)
	require.Nil(t, svc.GetIdentifiedModelPricing("gpt-5.4"))
	require.NoFileExists(t, svc.getPricingFilePath())
	status := svc.GetStatus()
	require.Contains(t, status["last_error"], "hash mismatch")
}

func TestDownloadPricingDataKeepsActiveOverridesWhenOverrideFileIsCorrupt(t *testing.T) {
	dataDir := t.TempDir()
	body := pricingCatalogForTest(map[string]any{
		"gpt-5.4": map[string]any{"input_cost_per_token": 0.000007, "output_cost_per_token": 0.000008},
	})
	// 公开目录按 sha256sum 标准输出“摘要  文件名”，网关必须兼容。
	remote := &pricingRemoteClientStub{body: body, hash: pricingSHA256(body) + "  model_prices_and_context_window.json\n"}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://example.com/pricing.json",
		HashURL:   "https://example.com/pricing.sha256",
		DataDir:   dataDir,
	}}, remote)
	svc.pricingOverrides["gpt-5.4"] = &LiteLLMModelPricing{InputCostPerToken: 0.000123}
	svc.pricingData["gpt-5.4"] = &LiteLLMModelPricing{InputCostPerToken: 0.000123}
	require.NoError(t, os.WriteFile(svc.getPricingOverridesFilePath(), []byte("{"), 0644))

	require.NoError(t, svc.downloadPricingData())
	require.InDelta(t, 0.000123, svc.GetModelPricing("gpt-5.4").InputCostPerToken, 1e-12)
	require.Equal(t, 1, svc.GetStatus()["override_count"])
}

func TestDownloadPricingDataRejectsUnsafeCatalogValues(t *testing.T) {
	dataDir := t.TempDir()
	body := pricingCatalogForTest(map[string]any{
		"negative-model": map[string]any{"input_cost_per_token": -1},
	})
	remote := &pricingRemoteClientStub{body: body}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://example.com/pricing.json",
		DataDir:   dataDir,
	}}, remote)

	err := svc.downloadPricingData()
	require.ErrorContains(t, err, "invalid InputCostPerToken")
	require.NoFileExists(t, svc.getPricingFilePath())
}

func TestDownloadPricingDataRejectsRemoteDisabledField(t *testing.T) {
	dataDir := t.TempDir()
	body := pricingCatalogForTest(map[string]any{
		"remote-disabled": map[string]any{"input_cost_per_token": 1e-6, "disabled": false},
	})
	remote := &pricingRemoteClientStub{body: body}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://example.com/pricing.json",
		DataDir:   dataDir,
	}}, remote)

	err := svc.downloadPricingData()
	require.ErrorContains(t, err, "forbidden remote disabled field")
	require.NoFileExists(t, svc.getPricingFilePath())
}

func TestPricingManifestRejectsRollbackAndUnexpectedShrink(t *testing.T) {
	svc := NewPricingService(&config.Config{}, nil)
	svc.catalogSequence = 8
	svc.remoteModelCount = 200
	svc.localHash = strings.Repeat("a", sha256.Size*2)

	rollback := &pricingCatalogManifest{Sequence: 7, ModelCount: 200, PricingCatalog: pricingCatalogManifestFile{SHA256: svc.localHash}}
	require.ErrorContains(t, svc.validatePricingManifestTransition(rollback), "sequence rollback")

	shrink := &pricingCatalogManifest{Sequence: 9, ModelCount: 100, PricingCatalog: pricingCatalogManifestFile{SHA256: strings.Repeat("b", sha256.Size*2)}}
	require.ErrorContains(t, svc.validatePricingManifestTransition(shrink), "shrank unexpectedly")
}

func TestLoadCachedPricingRequiresValidatedManifest(t *testing.T) {
	dataDir := t.TempDir()
	body := pricingCatalogForTest(nil)
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		ManifestURL: "https://example.com/manifest.json",
		DataDir:     dataDir,
	}}, nil)
	require.NoError(t, os.WriteFile(svc.getPricingFilePath(), body, 0644))
	require.ErrorContains(t, svc.loadPricingData(svc.getPricingFilePath()), "cached pricing manifest")

	require.NoError(t, os.WriteFile(svc.getPricingManifestFilePath(), pricingManifestForTest(t, body, 3, minRemotePricingEntries), 0644))
	require.NoError(t, svc.loadPricingData(svc.getPricingFilePath()))
	require.Equal(t, uint64(3), svc.catalogSequence)
	require.Equal(t, minRemotePricingEntries, svc.remoteModelCount)
}

func TestDownloadPricingDataUsesManifestAndPersistsValidatedSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	body := pricingCatalogForTest(nil)
	manifestURL := "https://catalog.example.test/pricing/v1/manifest.json"
	catalogURL := "https://catalog.example.test/pricing/v1/model_prices_and_context_window.json"
	manifest := pricingManifestForTest(t, body, 4, minRemotePricingEntries)
	remote := &pricingRemoteCatalogStub{bodies: map[string][]byte{
		manifestURL: manifest,
		catalogURL:  body,
	}}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		ManifestURL: manifestURL,
		DataDir:     dataDir,
	}}, remote)

	require.NoError(t, svc.downloadPricingData())
	require.Equal(t, uint64(4), svc.catalogSequence)
	require.Equal(t, minRemotePricingEntries, svc.remoteModelCount)
	require.FileExists(t, svc.getPricingFilePath())
	require.FileExists(t, svc.getPricingManifestFilePath())
	require.NotNil(t, svc.GetModelPricing("test-model-00"))
}

func TestPricingSnapshotRecoversLastKnownGoodPairAndClearsOldError(t *testing.T) {
	dataDir := t.TempDir()
	oldBody := pricingCatalogForTest(map[string]any{
		"old-only-model": map[string]any{"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000004},
	})
	newBody := pricingCatalogForTest(map[string]any{
		"new-only-model": map[string]any{"input_cost_per_token": 0.000005, "output_cost_per_token": 0.000006},
	})
	manifestURL := "https://catalog.example.test/pricing/v1/manifest.json"
	catalogURL := "https://catalog.example.test/pricing/v1/model_prices_and_context_window.json"
	oldManifest := pricingManifestForTest(t, oldBody, 1, minRemotePricingEntries+1)
	newManifest := pricingManifestForTest(t, newBody, 2, minRemotePricingEntries+1)
	remote := &pricingRemoteCatalogStub{bodies: map[string][]byte{
		manifestURL: newManifest,
		catalogURL:  newBody,
	}}
	cfg := &config.Config{Pricing: config.PricingConfig{ManifestURL: manifestURL, DataDir: dataDir}}
	svc := NewPricingService(cfg, remote)
	require.NoError(t, os.WriteFile(svc.getPricingFilePath(), oldBody, 0644))
	require.NoError(t, os.WriteFile(svc.getPricingManifestFilePath(), oldManifest, 0644))
	svc.lastError = "temporary remote failure"
	svc.lastErrorAt = time.Now()

	require.NoError(t, svc.downloadPricingData())
	require.FileExists(t, svc.getPricingBackupFilePath())
	require.FileExists(t, svc.getPricingBackupManifestFilePath())
	require.Empty(t, svc.GetStatus()["last_error"])
	require.True(t, svc.GetStatus()["last_error_at"].(time.Time).IsZero())
	require.NotNil(t, svc.GetModelPricing("new-only-model"))

	require.NoError(t, os.WriteFile(svc.getPricingFilePath(), []byte("corrupt"), 0644))
	recovered := NewPricingService(cfg, nil)
	require.NoError(t, recovered.loadBestPricingCache())
	require.Equal(t, uint64(1), recovered.catalogSequence)
	require.NotNil(t, recovered.GetModelPricing("old-only-model"))
	require.Nil(t, recovered.GetIdentifiedModelPricing("new-only-model"))
}

func TestDownloadPricingDataRejectsCatalogWithTooFewRemoteEntries(t *testing.T) {
	dataDir := t.TempDir()
	body := []byte(`{"only-one":{"input_cost_per_token":0.000001}}`)
	remote := &pricingRemoteClientStub{body: body}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://example.com/pricing.json",
		DataDir:   dataDir,
	}}, remote)

	err := svc.downloadPricingData()
	require.ErrorContains(t, err, "too few entries")
	require.NoFileExists(t, svc.getPricingFilePath())
}

func TestDownloadPricingDataRejectsCatalogWithTooManyInvalidEntries(t *testing.T) {
	dataDir := t.TempDir()
	overrides := make(map[string]any)
	for i := 0; i < 6; i++ {
		overrides[fmt.Sprintf("invalid-model-%d", i)] = map[string]any{"input_cost_per_token": "not-a-number"}
	}
	body := pricingCatalogForTest(overrides)
	remote := &pricingRemoteClientStub{body: body}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://example.com/pricing.json",
		DataDir:   dataDir,
	}}, remote)

	err := svc.downloadPricingData()
	require.ErrorContains(t, err, "parsed only")
	require.NoFileExists(t, svc.getPricingFilePath())
}

func TestDownloadPricingDataRejectsOversizedCatalog(t *testing.T) {
	dataDir := t.TempDir()
	remote := &pricingRemoteClientStub{body: make([]byte, MaxPricingCatalogBytes+1)}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://example.com/pricing.json",
		DataDir:   dataDir,
	}}, remote)

	err := svc.downloadPricingData()
	require.ErrorContains(t, err, "exceeds")
	require.NoFileExists(t, svc.getPricingFilePath())
}

func TestDownloadPricingDataSerializesConcurrentRefreshes(t *testing.T) {
	dataDir := t.TempDir()
	body := pricingCatalogForTest(nil)
	remote := &pricingRemoteClientStub{body: body, started: make(chan struct{}), release: make(chan struct{})}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://example.com/pricing.json",
		DataDir:   dataDir,
	}}, remote)

	done := make(chan error, 2)
	go func() { done <- svc.downloadPricingData() }()
	<-remote.started
	require.Equal(t, true, svc.GetStatus()["refreshing"])
	go func() { done <- svc.downloadPricingData() }()
	close(remote.release)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	require.Equal(t, false, svc.GetStatus()["refreshing"])
}

func TestPricingInitializeDoesNotWaitForRemoteCatalog(t *testing.T) {
	dataDir := t.TempDir()
	body := pricingCatalogForTest(nil)
	remote := &pricingRemoteClientStub{body: body, started: make(chan struct{}), release: make(chan struct{})}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL:    "https://example.com/pricing.json",
		DataDir:      dataDir,
		FallbackFile: filepath.Join(dataDir, "missing-pricing.json"),
	}}, remote)

	initialized := make(chan error, 1)
	go func() { initialized <- svc.Initialize() }()
	select {
	case err := <-initialized:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("pricing initialization waited for the remote catalog")
	}
	require.NotNil(t, svc.GetModelPricing("gpt-5.6-terra"))
	<-remote.started
	close(remote.release)
	svc.Stop()
}

func TestOverridePersistenceFailureDoesNotChangeActiveMemory(t *testing.T) {
	dataDir := t.TempDir()
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: dataDir}}, nil)
	base := &LiteLLMModelPricing{InputCostPerToken: 0.000001}
	svc.basePricingData["test-model"] = base
	svc.pricingData["test-model"] = cloneLiteLLMModelPricing(base)
	require.NoError(t, os.Mkdir(svc.getPricingOverridesFilePath(), 0755))

	_, err := svc.UpsertModelPricingOverride("test-model", LiteLLMModelPricing{InputCostPerToken: 0.000009})
	require.Error(t, err)
	require.Empty(t, svc.pricingOverrides)
	require.InDelta(t, 0.000001, svc.GetModelPricing("test-model").InputCostPerToken, 1e-12)
}

func TestOverrideCanBeAtomicallyUpdatedMoreThanOnce(t *testing.T) {
	dataDir := t.TempDir()
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: dataDir}}, nil)
	svc.basePricingData["test-model"] = &LiteLLMModelPricing{InputCostPerToken: 0.000001}
	svc.pricingData = clonePricingMap(svc.basePricingData)

	_, err := svc.UpsertModelPricingOverride("test-model", LiteLLMModelPricing{InputCostPerToken: 0.000009})
	require.NoError(t, err)
	_, err = svc.UpsertModelPricingOverride("test-model", LiteLLMModelPricing{InputCostPerToken: 0.000010})
	require.NoError(t, err)
	require.InDelta(t, 0.000010, svc.GetModelPricing("test-model").InputCostPerToken, 1e-12)

	loaded, err := svc.loadPricingOverrides()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.InDelta(t, 0.000010, loaded["test-model"].InputCostPerToken, 1e-12)
}

func TestDeleteOverridePersistenceFailureDoesNotChangeActiveMemory(t *testing.T) {
	dataDir := t.TempDir()
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: dataDir}}, nil)
	base := &LiteLLMModelPricing{InputCostPerToken: 0.000001}
	override := &LiteLLMModelPricing{InputCostPerToken: 0.000009}
	svc.basePricingData["test-model"] = base
	svc.pricingOverrides["test-model"] = override
	svc.pricingData["test-model"] = cloneLiteLLMModelPricing(override)
	require.NoError(t, os.Mkdir(svc.getPricingOverridesFilePath(), 0755))

	_, _, err := svc.DeleteModelPricingOverride("test-model")
	require.Error(t, err)
	require.Contains(t, svc.pricingOverrides, "test-model")
	require.InDelta(t, 0.000009, svc.GetModelPricing("test-model").InputCostPerToken, 1e-12)
}

func TestPricingStatusIncludesRemoteSourceAndErrors(t *testing.T) {
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://raw.githubusercontent.com/example/catalog.json",
	}}, &pricingRemoteClientStub{fetchErr: errors.New("offline")})
	err := svc.ForceUpdate()
	require.ErrorContains(t, err, "offline")
	status := svc.GetStatus()
	require.Equal(t, "https://raw.githubusercontent.com/example/catalog.json", status["remote_url"])
	require.Equal(t, "raw.githubusercontent.com", status["remote_host"])
	require.Contains(t, status["last_error"], "offline")
}

func TestParsePricingData_ParsesPriorityAndServiceTierFields(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_creation_input_token_cost": 0.0000025,
			"cache_creation_input_token_cost_priority": 0.000005,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"input_cost_per_token_above_272k_tokens": 0.000005,
			"output_cost_per_token_above_272k_tokens": 0.0000225,
			"cache_creation_input_token_cost_above_272k_tokens": 0.000005,
			"cache_read_input_token_cost_above_272k_tokens": 0.0000005,
			"supports_service_tier": true,
			"supports_prompt_caching": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)
	pricing := data["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 3e-5, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 2.5e-6, pricing.CacheCreationInputTokenCost, 1e-12, "explicit remote price must override the embedded fallback")
	require.InDelta(t, 5e-7, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenAbove272K, 1e-12)
	require.InDelta(t, 2.25e-5, pricing.OutputCostPerTokenAbove272K, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_PreservesImageInputPriceConfiguredState(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"configured": {
			"input_cost_per_token": 0.000002,
			"input_cost_per_image_token": 0.000008
		},
		"explicit-free": {
			"input_cost_per_token": 0.000002,
			"input_cost_per_image_token": 0
		},
		"inherits": {
			"input_cost_per_token": 0.000002
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)
	require.NotNil(t, data["configured"].InputCostPerImageToken)
	require.InDelta(t, 8e-6, *data["configured"].InputCostPerImageToken, 1e-15)
	require.NotNil(t, data["explicit-free"].InputCostPerImageToken)
	require.Zero(t, *data["explicit-free"].InputCostPerImageToken)
	require.Nil(t, data["inherits"].InputCostPerImageToken)
}

func TestDefaultPricingIncludesGPTImage2ImageInputPrice(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)
	pricing := pricingData["gpt-image-2"]
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerImageToken)
	require.InDelta(t, 8e-6, *pricing.InputCostPerImageToken, 1e-15)

	modelPricing := modelPricingFromLiteLLM(pricing)
	require.True(t, modelPricing.ImageInputPriceExplicit)
	require.InDelta(t, 8e-6, modelPricing.ImageInputPricePerToken, 1e-15)
	require.Equal(t, "model_catalog", modelPricing.ImageInputPriceSource)
}

func TestGetModelPricing_Gpt53CodexSparkUsesGpt51CodexPricing(t *testing.T) {
	sparkPricing := &LiteLLMModelPricing{InputCostPerToken: 1}
	gpt53Pricing := &LiteLLMModelPricing{InputCostPerToken: 9}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": sparkPricing,
			"gpt-5.3":       gpt53Pricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex-spark")
	require.Same(t, sparkPricing, got)
}

func TestGetModelPricing_Gpt53CodexFallbackStillUsesGpt52Codex(t *testing.T) {
	gpt52CodexPricing := &LiteLLMModelPricing{InputCostPerToken: 2}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2-codex": gpt52CodexPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, gpt52CodexPricing, got)
}

func TestGetModelPricing_OpenAIFallbackMatchedLoggedAsInfo(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	gpt52CodexPricing := &LiteLLMModelPricing{InputCostPerToken: 2}
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2-codex": gpt52CodexPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, gpt52CodexPricing, got)

	require.True(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.2-codex", "info"))
	require.False(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.2-codex", "warn"))
}

func TestGetModelPricing_Gpt54UsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": &LiteLLMModelPricing{InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.5e-7, got.CacheReadInputTokenCost, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, got.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, got.LongContextOutputCostMultiplier, 1e-12)
}

func TestGetModelPricing_OpenAICompactAliasUsesStaticFallback(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("openai/gpt5.5")
	require.NotNil(t, got)
	require.InDelta(t, 5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 3e-5, got.OutputCostPerToken, 1e-12)
}

// 空目录时，价格展示和计费必须直接找到独立审查价卡；通用 Codex 不代表该模型。
func TestPricingCodexAutoReviewStaticDoesNotBorrowGenericCodex(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-5.3-codex": {InputCostPerToken: 1.5e-6, OutputCostPerToken: 12e-6},
	}}
	for _, model := range []string{"codex-auto-review", "openai/codex-auto-review"} {
		t.Run(model, func(t *testing.T) {
			for name, lookup := range map[string]func(string) *LiteLLMModelPricing{
				"billing": svc.GetModelPricing,
				"report":  svc.GetModelPricingForReport,
			} {
				t.Run(name, func(t *testing.T) {
					got := lookup(model)
					require.NotNil(t, got, "静态价卡也必须供价格展示路径使用")
					require.InDelta(t, 5e-6, got.InputCostPerToken, 1e-12)
					require.InDelta(t, 30e-6, got.OutputCostPerToken, 1e-12)
					require.InDelta(t, 0.5e-6, got.CacheReadInputTokenCost, 1e-12)
					require.Equal(t, 272000, got.LongContextInputTokenThreshold)
					got.InputCostPerToken = 1
					require.InDelta(t, 5e-6, lookup(model).InputCostPerToken, 1e-12,
						"调用方不得污染进程共享的静态价卡")
				})
			}
		})
	}
}

func TestDefaultPricingIncludesCodexAutoReview(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)
	svc.pricingData = pricingData

	// 固定权威目录：JnmHub/tokenav-catalog@451ff802b6e826bf9ab128c391f7a13ddac07e64，
	// manifest 2026.10.03.4 / sequence=4；正文 SHA-256
	// 96252a561f5ae38c775545bf5e3a1e2d225d380d9dc4d238466134da4bce86e3。
	// 原编译快照低了25倍；此处由价源证据钉住，不把失败测试改成陈旧价格。
	got := svc.GetModelPricing("codex-auto-review")
	require.NotNil(t, got)
	require.InDelta(t, 5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 3e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 5e-7, got.CacheReadInputTokenCost, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 1e-5, got.InputCostPerTokenAbove272K, 1e-12)
	require.InDelta(t, 4.5e-5, got.OutputCostPerTokenAbove272K, 1e-12)
	require.InDelta(t, 1e-6, got.CacheReadInputTokenCostAbove272K, 1e-12)
	require.InDelta(t, 1e-5, got.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 6e-5, got.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 1e-6, got.CacheReadInputTokenCostPriority, 1e-12)
	var catalog map[string]map[string]any
	require.NoError(t, json.Unmarshal(data, &catalog))
	require.Equal(t, 2.5e-6, catalog["codex-auto-review"]["input_cost_per_token_flex"])
	require.Equal(t, 1.5e-5, catalog["codex-auto-review"]["output_cost_per_token_flex"])
	require.Equal(t, 2.5e-7, catalog["codex-auto-review"]["cache_read_input_token_cost_flex"])
}

func TestCodexAutoReviewRemoteCatalogAndAdminOverrideRemainAuthoritative(t *testing.T) {
	body := pricingCatalogForTest(map[string]any{
		"codex-auto-review": map[string]any{
			"input_cost_per_token": 7e-6, "output_cost_per_token": 42e-6, "cache_read_input_token_cost": 0.7e-6,
			"input_cost_per_token_priority": 11e-6, "output_cost_per_token_priority": 66e-6, "cache_read_input_token_cost_priority": 1.1e-6,
			"input_cost_per_token_above_272k_tokens": 13e-6, "output_cost_per_token_above_272k_tokens": 59e-6,
			"input_cost_per_token_priority_above_272k_tokens": 17e-6, "output_cost_per_token_priority_above_272k_tokens": 81e-6,
		},
	})
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL: "https://catalog.example.test/pricing.json", HashURL: "https://catalog.example.test/pricing.sha256", DataDir: t.TempDir(),
	}}, &pricingRemoteClientStub{body: body, hash: pricingSHA256(body)})
	require.NoError(t, svc.downloadPricingData())
	assertRates := func(input, output, cacheRead float64) {
		t.Helper()
		for _, model := range []string{"codex-auto-review", "openai/codex-auto-review"} {
			got := svc.GetModelPricing(model)
			require.NotNil(t, got)
			require.InDelta(t, input, got.InputCostPerToken, 1e-12)
			require.InDelta(t, output, got.OutputCostPerToken, 1e-12)
			require.InDelta(t, cacheRead, got.CacheReadInputTokenCost, 1e-12)
			cost, err := NewBillingService(&config.Config{}, svc).CalculateCost(model, UsageTokens{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 100}, 1)
			require.NoError(t, err)
			require.InDelta(t, 1000*input+100*output+100*cacheRead, cost.ActualCost, 1e-12)
		}
	}
	assertRates(7e-6, 42e-6, 0.7e-6)
	// Priority 长上下文独立绝对价不能由标准倍率再推导，保留远程目录的独立档位。
	priorityLong, err := NewBillingService(&config.Config{}, svc).CalculateCostWithServiceTier("codex-auto-review", UsageTokens{InputTokens: 272001, OutputTokens: 100}, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, 272001*17e-6, priorityLong.InputCost, 1e-12)
	require.InDelta(t, 100*81e-6, priorityLong.OutputCost, 1e-12)

	_, err = svc.UpsertModelPricingOverride("codex-auto-review", LiteLLMModelPricing{InputCostPerToken: 9e-6, OutputCostPerToken: 54e-6, CacheReadInputTokenCost: 0.9e-6})
	require.NoError(t, err)
	require.NoError(t, svc.downloadPricingData(), "刷新远程目录必须继续保留管理员覆盖")
	assertRates(9e-6, 54e-6, 0.9e-6)
	_, found, err := svc.DeleteModelPricingOverride("codex-auto-review")
	require.NoError(t, err)
	require.True(t, found)
	assertRates(7e-6, 42e-6, 0.7e-6)
}

func loadDefaultGPT56PricingForTest(t *testing.T, model string) *LiteLLMModelPricing {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)
	svc.pricingData = pricingData
	got := svc.GetModelPricing(model)
	require.NotNil(t, got, "pricing missing for %s", model)
	return got
}

func TestDefaultPricingIncludesGPT56Sol(t *testing.T) {
	got := loadDefaultGPT56PricingForTest(t, "gpt-5.6-sol")
	require.Equal(t, "openai", got.LiteLLMProvider)
	require.InDelta(t, 5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.5e-6, got.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 6.25e-6, got.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 30e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 10e-6, got.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 1e-6, got.CacheReadInputTokenCostPriority, 1e-12)
	require.InDelta(t, 12.5e-6, got.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 60e-6, got.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 10e-6, got.InputCostPerTokenAbove272K, 1e-12)
	require.InDelta(t, 1e-6, got.CacheReadInputTokenCostAbove272K, 1e-12)
	require.InDelta(t, 12.5e-6, got.CacheCreationInputTokenCostAbove272K, 1e-12)
	require.InDelta(t, 45e-6, got.OutputCostPerTokenAbove272K, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.True(t, got.SupportsServiceTier)
}

func TestDefaultPricingIncludesGPT56Terra(t *testing.T) {
	got := loadDefaultGPT56PricingForTest(t, "gpt-5.6-terra")
	require.Equal(t, "openai", got.LiteLLMProvider)
	require.InDelta(t, 2e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.2e-6, got.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 2.5e-6, got.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 12e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 4e-6, got.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.4e-6, got.CacheReadInputTokenCostPriority, 1e-12)
	require.InDelta(t, 5e-6, got.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 24e-6, got.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 4e-6, got.InputCostPerTokenAbove272K, 1e-12)
	require.InDelta(t, 0.4e-6, got.CacheReadInputTokenCostAbove272K, 1e-12)
	require.InDelta(t, 5e-6, got.CacheCreationInputTokenCostAbove272K, 1e-12)
	require.InDelta(t, 18e-6, got.OutputCostPerTokenAbove272K, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.True(t, got.SupportsServiceTier)
}

func TestDefaultPricingIncludesGPT56Luna(t *testing.T) {
	got := loadDefaultGPT56PricingForTest(t, "gpt-5.6-luna")
	require.Equal(t, "openai", got.LiteLLMProvider)
	require.InDelta(t, 0.2e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.02e-6, got.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.25e-6, got.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 1.2e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 0.4e-6, got.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.04e-6, got.CacheReadInputTokenCostPriority, 1e-12)
	require.InDelta(t, 0.5e-6, got.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 2.4e-6, got.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.4e-6, got.InputCostPerTokenAbove272K, 1e-12)
	require.InDelta(t, 0.04e-6, got.CacheReadInputTokenCostAbove272K, 1e-12)
	require.InDelta(t, 0.5e-6, got.CacheCreationInputTokenCostAbove272K, 1e-12)
	require.InDelta(t, 1.8e-6, got.OutputCostPerTokenAbove272K, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.True(t, got.SupportsServiceTier)
}

func TestGetModelPricing_UnknownGPT56VariantDoesNotFallbackToGPT54(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)
	svc.pricingData = pricingData
	require.Nil(t, svc.GetModelPricing("gpt-5.6-unknown"))
	require.Nil(t, svc.GetModelPricing("gpt-5.5-unknown"))
}

func TestDefaultPricingIncludesMimoTextModels(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)
	svc.pricingData = pricingData

	pro := svc.GetModelPricing("mimo-v2.5-pro")
	require.NotNil(t, pro)
	require.Equal(t, "xiaomi", pro.LiteLLMProvider)
	require.InDelta(t, 4.35e-7, pro.InputCostPerToken, 1e-12)
	require.InDelta(t, 8.7e-7, pro.OutputCostPerToken, 1e-12)
	require.InDelta(t, 3.6e-9, pro.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, pro.CacheCreationInputTokenCost)

	base := svc.GetModelPricing("mimo-v2.5")
	require.NotNil(t, base)
	require.InDelta(t, 1.4e-7, base.InputCostPerToken, 1e-12)
	require.InDelta(t, 2.8e-7, base.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.8e-9, base.CacheReadInputTokenCost, 1e-12)
}

func TestDefaultPricingIncludesOfficialGLM52AndKimiK3Models(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)

	tests := []struct {
		model     string
		provider  string
		input     float64
		cacheRead float64
		output    float64
	}{
		{model: "GLM-5.2", provider: "zai", input: 1.4e-6, cacheRead: 0.26e-6, output: 4.4e-6},
		{model: "glm-5.2", provider: "zai", input: 1.4e-6, cacheRead: 0.26e-6, output: 4.4e-6},
		{model: "kimi-k3", provider: "moonshot", input: 3e-6, cacheRead: 0.3e-6, output: 15e-6},
		{model: "kimi-k3-code", provider: "moonshot", input: 3e-6, cacheRead: 0.3e-6, output: 15e-6},
		{model: "k3", provider: "moonshot", input: 3e-6, cacheRead: 0.3e-6, output: 15e-6},
		{model: "k3-256k", provider: "moonshot", input: 3e-6, cacheRead: 0.3e-6, output: 15e-6},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing := pricingData[tt.model]
			require.NotNil(t, pricing)
			require.Equal(t, tt.provider, pricing.LiteLLMProvider)
			require.Equal(t, "chat", pricing.Mode)
			require.Equal(t, "token", pricing.BillingMode)
			require.InDelta(t, tt.input, pricing.InputCostPerToken, 1e-12)
			require.InDelta(t, tt.cacheRead, pricing.CacheReadInputTokenCost, 1e-12)
			require.InDelta(t, tt.output, pricing.OutputCostPerToken, 1e-12)
			require.Zero(t, pricing.CacheCreationInputTokenCost)
			require.True(t, pricing.SupportsPromptCaching)
		})
	}
}

func TestGetModelPricing_OfficialGLM52AndKimiK3FallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData:      map[string]*LiteLLMModelPricing{},
		pricingOverrides: map[string]*LiteLLMModelPricing{},
	}

	for _, model := range []string{"GLM-5.2", "glm-5.2", "kimi-k3", "kimi-k3-code", "k3", "k3-256k"} {
		pricing := svc.GetModelPricing(model)
		require.NotNil(t, pricing, "pricing missing for %s", model)
		require.Equal(t, "token", pricing.BillingMode)
	}

	catalogModels := make(map[string]struct{})
	for _, entry := range svc.ListModelPricingCatalog() {
		catalogModels[entry.Model] = struct{}{}
	}
	for _, model := range []string{"GLM-5.2", "glm-5.2", "kimi-k3", "kimi-k3-code", "k3", "k3-256k"} {
		require.Contains(t, catalogModels, model)
	}
}

func TestGetModelPricing_OfficialDomesticExactIDsFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData:      map[string]*LiteLLMModelPricing{},
		pricingOverrides: map[string]*LiteLLMModelPricing{},
	}
	tests := []struct {
		model          string
		provider       string
		input          float64
		cacheWrite     float64
		cacheRead      float64
		output         float64
		longInput      float64
		longCacheWrite float64
		longCacheRead  float64
		longOutput     float64
		threshold      int
	}{
		{model: "deepseek-v4-flash-0731", provider: "deepseek", input: 0.14e-6, cacheRead: 0.0028e-6, output: 0.28e-6},
		{model: "deepseek-v4-flash-vip", provider: "deepseek", input: 0.14e-6, cacheRead: 0.0028e-6, output: 0.28e-6},
		{model: "glm-5.1-vip", provider: "zai", input: 1.4e-6, cacheRead: 0.26e-6, output: 4.4e-6},
		{model: "glm-5.2-vip", provider: "zai", input: 1.4e-6, cacheRead: 0.26e-6, output: 4.4e-6},
		{model: "glm-5.2-vip1", provider: "zai", input: 1.4e-6, cacheRead: 0.26e-6, output: 4.4e-6},
		{model: "kimi-k3-vip", provider: "moonshot", input: 3e-6, cacheRead: 0.3e-6, output: 15e-6},
		{model: "kimi-k3-vip2", provider: "moonshot", input: 3e-6, cacheRead: 0.3e-6, output: 15e-6},
		{model: "kimi-k3-vip3", provider: "moonshot", input: 3e-6, cacheRead: 0.3e-6, output: 15e-6},
		{model: "minimax-m2.1", provider: "minimax", input: 0.3e-6, cacheWrite: 0.375e-6, cacheRead: 0.03e-6, output: 1.2e-6},
		{model: "minimax-m2.5", provider: "minimax", input: 0.3e-6, cacheWrite: 0.375e-6, cacheRead: 0.03e-6, output: 1.2e-6},
		{
			model: "qwen3.5-plus", provider: "qwen",
			input: 0.4e-6, cacheWrite: 0.5e-6, cacheRead: 0.04e-6, output: 2.4e-6,
			longInput: 0.5e-6, longCacheWrite: 0.625e-6, longCacheRead: 0.05e-6, longOutput: 3e-6,
			threshold: 256000,
		},
		{model: "qwen3.8-max", provider: "qwen", input: 2e-6, cacheWrite: 2.5e-6, cacheRead: 0.25e-6, output: 6e-6},
	}

	catalogModels := make(map[string]struct{})
	for _, entry := range svc.ListModelPricingCatalog() {
		catalogModels[entry.Model] = struct{}{}
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing := svc.GetModelPricing(tt.model)
			require.NotNil(t, pricing)
			require.Equal(t, tt.provider, pricing.LiteLLMProvider)
			require.Equal(t, "token", pricing.BillingMode)
			require.InDelta(t, tt.input, pricing.InputCostPerToken, 1e-15)
			require.InDelta(t, tt.cacheWrite, pricing.CacheCreationInputTokenCost, 1e-15)
			require.InDelta(t, tt.cacheRead, pricing.CacheReadInputTokenCost, 1e-15)
			require.InDelta(t, tt.output, pricing.OutputCostPerToken, 1e-15)
			require.InDelta(t, tt.longInput, pricing.InputCostPerTokenAbove272K, 1e-15)
			require.InDelta(t, tt.longCacheWrite, pricing.CacheCreationInputTokenCostAbove272K, 1e-15)
			require.InDelta(t, tt.longCacheRead, pricing.CacheReadInputTokenCostAbove272K, 1e-15)
			require.InDelta(t, tt.longOutput, pricing.OutputCostPerTokenAbove272K, 1e-15)
			require.Equal(t, tt.threshold, pricing.LongContextInputTokenThreshold)
			require.Contains(t, catalogModels, tt.model)
		})
	}
}

func TestGetModelPricing_MimoUsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}

	pro := svc.GetModelPricing("mimo-v2-pro")
	require.NotNil(t, pro)
	require.InDelta(t, 4.35e-7, pro.InputCostPerToken, 1e-12)
	require.InDelta(t, 8.7e-7, pro.OutputCostPerToken, 1e-12)
	require.InDelta(t, 3.6e-9, pro.CacheReadInputTokenCost, 1e-12)

	flash := svc.GetModelPricing("mimo-v2-flash")
	require.NotNil(t, flash)
	require.InDelta(t, 1.4e-7, flash.InputCostPerToken, 1e-12)
	require.InDelta(t, 2.8e-7, flash.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.8e-9, flash.CacheReadInputTokenCost, 1e-12)

	require.Nil(t, svc.GetModelPricing("mimo-v2.5-asr"))
	require.Nil(t, svc.GetModelPricing("mimo-v2.5-tts"))
}

func TestGetModelPricing_Gpt54MiniUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-mini")
	require.NotNil(t, got)
	require.InDelta(t, 7.5e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 4.5e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 7.5e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_Gpt54NanoUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-nano")
	require.NotNil(t, got)
	require.InDelta(t, 2e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.25e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_ImageModelDoesNotFallbackToTextModel(t *testing.T) {
	imagePricing := &LiteLLMModelPricing{InputCostPerToken: 3}
	textPricing := &LiteLLMModelPricing{InputCostPerToken: 9}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-image-2": imagePricing,
			"gpt-5.4":     textPricing,
		},
	}

	got := svc.GetModelPricing("gpt-image-3")
	require.Same(t, imagePricing, got)
}

func TestParsePricingData_PreservesPriorityAndServiceTierFields(t *testing.T) {
	raw := map[string]any{
		"gpt-5.4": map[string]any{
			"input_cost_per_token":                 2.5e-6,
			"input_cost_per_token_priority":        5e-6,
			"output_cost_per_token":                15e-6,
			"output_cost_per_token_priority":       30e-6,
			"cache_read_input_token_cost":          0.25e-6,
			"cache_read_input_token_cost_priority": 0.5e-6,
			"supports_service_tier":                true,
			"supports_prompt_caching":              true,
			"litellm_provider":                     "openai",
			"mode":                                 "chat",
		},
	}
	body, err := json.Marshal(raw)
	require.NoError(t, err)

	svc := &PricingService{}
	pricingMap, err := svc.parsePricingData(body)
	require.NoError(t, err)

	pricing := pricingMap["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 2.5e-6, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 15e-6, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 30e-6, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.5e-6, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_PreservesServiceTierPriorityFields(t *testing.T) {
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	pricing := pricingData["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 0.0000025, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.000005, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.000015, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 0.00003, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.00000025, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.0000005, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

// ---------------------------------------------------------------------------
// ListModelNamesByProvider
// ---------------------------------------------------------------------------

func TestListModelNamesByProvider_ReturnsMatchingModels(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-opus-4-5-20251101": {LiteLLMProvider: "anthropic", InputCostPerToken: 1.5e-5},
			"claude-sonnet-4-5":        {LiteLLMProvider: "anthropic", InputCostPerToken: 3e-6},
			"gpt-4o":                   {LiteLLMProvider: "openai", InputCostPerToken: 5e-6},
			"gemini-2.5-pro":           {LiteLLMProvider: "google", InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.ListModelNamesByProvider("anthropic")
	require.ElementsMatch(t, []string{"claude-opus-4-5-20251101", "claude-sonnet-4-5"}, got)
	// Must be sorted
	require.Equal(t, "claude-opus-4-5-20251101", got[0])
	require.Equal(t, "claude-sonnet-4-5", got[1])
}

func TestListModelNamesByProvider_CaseInsensitive(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-4o": {LiteLLMProvider: "OpenAI", InputCostPerToken: 5e-6},
		},
	}

	got := svc.ListModelNamesByProvider("openai")
	require.Equal(t, []string{"gpt-4o"}, got)

	got2 := svc.ListModelNamesByProvider("OPENAI")
	require.Equal(t, []string{"gpt-4o"}, got2)
}

func TestListModelNamesByProvider_NoMatch(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-4o": {LiteLLMProvider: "openai", InputCostPerToken: 5e-6},
		},
	}

	got := svc.ListModelNamesByProvider("anthropic")
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestListModelNamesByProvider_EmptyCatalog(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{},
	}

	got := svc.ListModelNamesByProvider("openai")
	require.NotNil(t, got)
	require.Empty(t, got)
}
