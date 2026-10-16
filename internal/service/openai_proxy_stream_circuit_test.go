package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIProxyStreamCircuitThresholdTTLAndSuccessReset(t *testing.T) {
	now := time.Unix(1000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 2, failureWindow: time.Minute, quarantineTTL: 10 * time.Minute, maxEntries: 8,
	})
	tripped, _ := circuit.recordFailure(10, now)
	require.False(t, tripped)
	tripped, until := circuit.recordFailure(10, now.Add(10*time.Second))
	require.True(t, tripped)
	require.True(t, circuit.isBlocked(10, now.Add(time.Minute)))
	require.False(t, circuit.isBlocked(10, until))
	circuit.recordFailure(10, until.Add(time.Second))
	require.True(t, circuit.recordSuccess(10))
}

func TestOpenAIProxyStreamCircuitCollapsesBurstFailures(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 2, failureWindow: time.Minute, quarantineTTL: 10 * time.Minute,
		collapseInterval: 3 * time.Second, maxEntries: 16,
	})

	tripped, _ := circuit.recordFailure(1, base)
	require.False(t, tripped)
	tripped, _ = circuit.recordFailure(1, base.Add(time.Second))
	require.False(t, tripped, "3 秒内的并发断流必须合并")
	tripped, _ = circuit.recordFailure(1, base.Add(2*time.Second))
	require.False(t, tripped)

	tripped, _ = circuit.recordFailure(1, base.Add(5*time.Second))
	require.True(t, tripped, "超过合并窗口的独立故障应触发熔断")
}

func TestOpenAIProxyStreamCircuitDisabled(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		disabled: true, failureThreshold: 1, failureWindow: time.Minute,
		quarantineTTL: 10 * time.Minute, maxEntries: 16,
	})

	tripped, _ := circuit.recordFailure(1, base)
	require.False(t, tripped)
	require.False(t, circuit.isBlocked(1, base))
	require.Zero(t, circuit.activeBlockCount(base))
}

func TestOpenAIProxyStreamQuarantineBypassContext(t *testing.T) {
	proxyID := int64(7)
	account := &Account{ID: 1, Platform: PlatformOpenAI, ProxyID: &proxyID}
	svc := &OpenAIGatewayService{openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: 10 * time.Minute, maxEntries: 16,
	})}
	svc.openaiProxyStreamCircuit.recordFailure(proxyID, time.Now())

	ctx := context.Background()
	require.True(t, svc.isOpenAIProxyStreamQuarantined(ctx, account))
	require.False(t, svc.isOpenAIProxyStreamQuarantined(withOpenAIProxyStreamQuarantineBypass(ctx), account))
}

func TestOpenAIProxyStreamCircuitBoundsEntries(t *testing.T) {
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 2, failureWindow: time.Minute, quarantineTTL: time.Minute, maxEntries: 2,
	})
	now := time.Unix(2000, 0)
	circuit.recordFailure(1, now)
	circuit.recordFailure(2, now.Add(time.Second))
	circuit.recordFailure(3, now.Add(2*time.Second))
	require.LessOrEqual(t, len(circuit.entries), 2)
}

func TestOpenAIProxyStreamCircuitSkipsEveryAccountOnQuarantinedSharedProxy(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	proxyA, proxyB := int64(4698), int64(4699)
	accounts := []Account{
		{ID: 469801, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, ProxyID: &proxyA},
		{ID: 469802, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, ProxyID: &proxyA},
		{ID: 469803, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, ProxyID: &proxyB},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
			failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: 10 * time.Minute, maxEntries: 16,
		}),
	}
	svc.openaiProxyStreamCircuit.recordFailure(proxyA, time.Now())

	selection, _, err := svc.SelectAccountWithScheduler(
		context.Background(), nil, "", "", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(469803), selection.Account.ID)
}

func TestOpenAIProxyStreamCircuitFailsOpenWhenAllAccountsShareQuarantinedProxy(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	proxyID := int64(5056)
	accounts := []Account{
		{ID: 505601, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, ProxyID: &proxyID},
		{ID: 505602, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, ProxyID: &proxyID},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cfg: cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
			failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: 10 * time.Minute, maxEntries: 16,
		}),
	}
	tripped, _ := svc.openaiProxyStreamCircuit.recordFailure(proxyID, time.Now())
	require.True(t, tripped)

	selection, _, err := svc.SelectAccountWithScheduler(
		context.Background(), nil, "", "", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, proxyID, *selection.Account.ProxyID)
	require.True(t, svc.openaiProxyStreamCircuit.isBlocked(proxyID, time.Now()),
		"fail-open 只放行这次调度，不能清除熔断状态")
}

func TestOpenAIProxyStreamCircuitAdvancedSchedulerFailsOpenWhenAllAccountsQuarantined(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{
		enabled: true, expiresAt: time.Now().Add(time.Minute).UnixNano(),
	})

	proxyID := int64(5057)
	accounts := []Account{
		{ID: 505701, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, ProxyID: &proxyID},
		{ID: 505702, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, ProxyID: &proxyID},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cfg: cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
			failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: 10 * time.Minute, maxEntries: 16,
		}),
	}
	svc.openaiProxyStreamCircuit.recordFailure(proxyID, time.Now())

	selection, _, err := svc.SelectAccountWithScheduler(
		context.Background(), nil, "", "", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, proxyID, *selection.Account.ProxyID)
}
