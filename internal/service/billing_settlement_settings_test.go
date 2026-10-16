//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type billingSettlementFailRepo struct {
	mockSettingRepo
	readErr error
	setErr  error
}

func TestWarmBillingSettlementSettingsUntilReadyRecoversStartupReadFailure(t *testing.T) {
	repo := &billingSettlementFailRepo{
		mockSettingRepo: *newMockSettingRepo(),
		readErr:         errors.New("database starting"),
	}
	repo.mockSettingRepo.data[SettingKeyGatewayLiteIdempotentSettlementEnabled] = "true"
	svc := NewSettingService(repo, &config.Config{})
	require.Error(t, svc.WarmBillingSettlementSettings(context.Background()))
	require.False(t, svc.GatewayLiteIdempotentSettlementEnabled())

	repo.readErr = nil
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	svc.WarmBillingSettlementSettingsUntilReady(ctx, time.Millisecond)
	require.True(t, svc.GatewayLiteIdempotentSettlementEnabled())
}

func (r *billingSettlementFailRepo) GetValue(_ context.Context, _ string) (string, error) {
	if r.readErr != nil {
		return "", r.readErr
	}
	return r.mockSettingRepo.GetValue(context.Background(), SettingKeyGatewayLiteIdempotentSettlementEnabled)
}

func (r *billingSettlementFailRepo) Set(ctx context.Context, key, value string) error {
	if r.setErr != nil {
		return r.setErr
	}
	return r.mockSettingRepo.Set(ctx, key, value)
}

func TestBillingSettlementSettings_DefaultsToLegacy(t *testing.T) {
	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	settings, err := svc.GetBillingSettlementSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.IdempotentEnabled)
	require.Equal(t, BillingSettlementModeLegacy, settings.Mode)
	require.True(t, settings.LegacyWarning)
	require.False(t, svc.GatewayLiteIdempotentSettlementEnabled())
}

func TestWarmBillingSettlementSettings_PublishesEnabledSnapshot(t *testing.T) {
	repo := newMockSettingRepo()
	repo.data[SettingKeyGatewayLiteIdempotentSettlementEnabled] = " TrUe "
	svc := NewSettingService(repo, &config.Config{})

	require.NoError(t, svc.WarmBillingSettlementSettings(context.Background()))
	require.True(t, svc.GatewayLiteIdempotentSettlementEnabled())
}

func TestWarmBillingSettlementSettings_MissingFallsBackAndReadFailurePreservesLastGood(t *testing.T) {
	t.Run("missing setting", func(t *testing.T) {
		svc := NewSettingService(newMockSettingRepo(), &config.Config{})
		svc.gatewayLiteIdempotentSettlementEnabled.Store(true)

		require.NoError(t, svc.WarmBillingSettlementSettings(context.Background()))
		require.False(t, svc.GatewayLiteIdempotentSettlementEnabled())
	})

	t.Run("read failure", func(t *testing.T) {
		repo := &billingSettlementFailRepo{
			mockSettingRepo: *newMockSettingRepo(),
			readErr:         errors.New("read failed"),
		}
		svc := NewSettingService(repo, &config.Config{})
		svc.gatewayLiteIdempotentSettlementEnabled.Store(true)

		require.Error(t, svc.WarmBillingSettlementSettings(context.Background()))
		require.True(t, svc.GatewayLiteIdempotentSettlementEnabled())

		repo.readErr = nil
		repo.mockSettingRepo.data[SettingKeyGatewayLiteIdempotentSettlementEnabled] = "true"
		settings, err := svc.GetBillingSettlementSettings(context.Background())
		require.NoError(t, err)
		require.True(t, settings.IdempotentEnabled)
		require.True(t, svc.GatewayLiteIdempotentSettlementEnabled())
	})
}

func TestSetBillingSettlementSettings_PersistsAndPublishesSnapshot(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo, &config.Config{})

	require.NoError(t, svc.SetBillingSettlementSettings(context.Background(), &BillingSettlementSettings{IdempotentEnabled: true}))
	require.Equal(t, "true", repo.data[SettingKeyGatewayLiteIdempotentSettlementEnabled])
	require.True(t, svc.GatewayLiteIdempotentSettlementEnabled())

	require.NoError(t, svc.SetBillingSettlementSettings(context.Background(), &BillingSettlementSettings{IdempotentEnabled: false}))
	require.Equal(t, "false", repo.data[SettingKeyGatewayLiteIdempotentSettlementEnabled])
	require.False(t, svc.GatewayLiteIdempotentSettlementEnabled())
}

func TestSetBillingSettlementSettings_FailedWriteDoesNotPublishSnapshot(t *testing.T) {
	repo := &billingSettlementFailRepo{
		mockSettingRepo: *newMockSettingRepo(),
		setErr:          errors.New("write failed"),
	}
	svc := NewSettingService(repo, &config.Config{})
	svc.gatewayLiteIdempotentSettlementEnabled.Store(true)

	require.Error(t, svc.SetBillingSettlementSettings(context.Background(), &BillingSettlementSettings{IdempotentEnabled: false}))
	require.True(t, svc.GatewayLiteIdempotentSettlementEnabled())
}
