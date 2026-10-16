package gatewaylite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type configChangedClientFunc func(context.Context, GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error)

func (f configChangedClientFunc) ReportGatewayConfigChanged(ctx context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
	return f(ctx, req)
}

type configPushClientFunc func(context.Context, GatewayConfigPushRequest) (*GatewayConfigPushResponse, error)

func (f configPushClientFunc) ReportGatewayConfigPush(ctx context.Context, req GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
	return f(ctx, req)
}

type configPushSnapshotProviderFunc func(context.Context, string) (GatewayConfigPushSnapshot, error)

func (f configPushSnapshotProviderFunc) BuildGatewayConfigPushSnapshot(ctx context.Context, gatewayCode string) (GatewayConfigPushSnapshot, error) {
	return f(ctx, gatewayCode)
}

func TestConfigChangeNotifierPushesFullSnapshotWithoutFallback(t *testing.T) {
	SetDefaultEffectivePolicyAudit(nil)
	t.Cleanup(func() { SetDefaultEffectivePolicyAudit(nil) })
	var mu sync.Mutex
	pushes := make([]GatewayConfigPushRequest, 0)
	fallbackCalls := 0
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, _ GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		fallbackCalls++
		mu.Unlock()
		return &GatewayConfigChangedResponse{OK: true}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "jp-1", Region: "jp"}), 5*time.Millisecond, 50*time.Millisecond).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, req GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				mu.Lock()
				pushes = append(pushes, req)
				mu.Unlock()
				return &GatewayConfigPushResponse{OK: true, SnapshotChecksumAccepted: true}, nil
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, gatewayCode string) (GatewayConfigPushSnapshot, error) {
				require.Equal(t, "jp-1", gatewayCode)
				return GatewayConfigPushSnapshot{
					Accounts:    []GatewayAccountSnapshot{{ID: 11, Platform: "grok"}},
					Groups:      []GatewayGroupSnapshot{{ID: 21, Platform: "grok"}},
					ModelPrices: []GatewayModelPrice{{Model: "grok-imagine-video-1.5", VideoPricePerSecond1080P: 0.25}},
					EffectivePolicies: []GatewayEffectivePolicy{{
						LocalGroupID: 21, Revision: 31, Checksum: "policy-31",
						SourceVersions: EffectivePolicySourceVersions{Prices: 41},
					}},
					RuntimeSettings: &GatewayRuntimeSettings{
						OpenAIFastPolicySettings: &OpenAIFastPolicyRuntimeSettings{
							Version:                            1,
							PriorityDetectionByRequestEnabled:  true,
							PriorityDetectionByResponseEnabled: false,
							RuleCount:                          2,
							ReportedAtMillis:                   123456,
						},
					},
				}, nil
			}),
		)

	notifier.Notify("groups", "update", 21)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pushes) == 1
	}, time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, fallbackCalls)
	require.True(t, pushes[0].AccountFull)
	require.True(t, pushes[0].GroupsFull)
	require.True(t, pushes[0].ModelPricesFull)
	require.Equal(t, "jp-1", pushes[0].GatewayCode)
	require.Equal(t, "jp", pushes[0].Region)
	require.Equal(t, "groups", pushes[0].Resource)
	require.EqualValues(t, 21, pushes[0].ResourceID)
	require.Equal(t, "grok-imagine-video-1.5", pushes[0].ModelPrices[0].Model)
	require.NotNil(t, pushes[0].RuntimeSettings)
	require.NotNil(t, pushes[0].RuntimeSettings.OpenAIFastPolicySettings)
	require.True(t, pushes[0].RuntimeSettings.OpenAIFastPolicySettings.PriorityDetectionByRequestEnabled)
	require.False(t, pushes[0].RuntimeSettings.OpenAIFastPolicySettings.PriorityDetectionByResponseEnabled)
	require.Equal(t, 2, pushes[0].RuntimeSettings.OpenAIFastPolicySettings.RuleCount)
	require.True(t, pushes[0].EffectivePoliciesFull)
	require.Equal(t, EffectivePolicyAuditStamp{Revision: 31, Checksum: "policy-31", PriceVersion: 41}, EffectivePolicyAuditForGroup(21))
}

func TestConfigChangeNotifierUsesRefreshOnlyAfterFullSnapshotConfirmation(t *testing.T) {
	var requests []GatewayConfigPushRequest
	snapshot := GatewayConfigPushSnapshot{
		Accounts:          []GatewayAccountSnapshot{{ID: 11, Status: "active"}},
		EffectivePolicies: []GatewayEffectivePolicy{{LocalGroupID: 21, Revision: 31, Checksum: "policy-31"}},
	}
	notifier := NewConfigChangeNotifier(nil, NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "jp-1"}), time.Second, time.Second).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, req GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				requests = append(requests, req)
				return &GatewayConfigPushResponse{OK: true, SnapshotChecksumAccepted: true}, nil
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, _ string) (GatewayConfigPushSnapshot, error) {
				return snapshot, nil
			}),
		)
	changed := GatewayConfigChangedRequest{GatewayCode: "jp-1", ChangedAtMillis: 1, EventID: "evt-1"}
	require.NoError(t, notifier.reportConfigPush(changed, false))
	changed.EventID = "evt-2"
	require.NoError(t, notifier.reportConfigPush(changed, false))

	require.Len(t, requests, 2)
	require.False(t, requests[0].RefreshOnly)
	require.True(t, requests[0].AccountFull)
	require.NotEmpty(t, requests[0].SnapshotChecksum)
	require.True(t, requests[1].RefreshOnly)
	require.Equal(t, requests[0].SnapshotChecksum, requests[1].SnapshotChecksum)
	require.Empty(t, requests[1].Accounts)
	require.False(t, requests[1].AccountFull)
	require.Empty(t, requests[1].EffectivePolicies)
	require.False(t, requests[1].EffectivePoliciesFull)
}

func TestConfigChangeNotifierForcedSnapshotBypassesConfirmedChecksum(t *testing.T) {
	var mu sync.Mutex
	var requests []GatewayConfigPushRequest
	notifier := NewConfigChangeNotifier(nil, NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "jp-1"}), 5*time.Millisecond, time.Second).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, req GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				mu.Lock()
				requests = append(requests, req)
				mu.Unlock()
				return &GatewayConfigPushResponse{OK: true, SnapshotChecksumAccepted: true}, nil
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, _ string) (GatewayConfigPushSnapshot, error) {
				return GatewayConfigPushSnapshot{Groups: []GatewayGroupSnapshot{{ID: 21, Name: "Pro"}}}, nil
			}),
		)
	changed := GatewayConfigChangedRequest{GatewayCode: "jp-1", ChangedAtMillis: 1, EventID: "evt-1"}
	require.NoError(t, notifier.reportConfigPush(changed, false))
	notifier.NotifyFullSnapshot("mixed", "consistency_repair", 0)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) == 2
	}, time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2)
	require.False(t, requests[0].RefreshOnly)
	require.False(t, requests[1].RefreshOnly)
	require.True(t, requests[1].GroupsFull)
}

func TestConfigChangeNotifierKeepsSendingFullSnapshotToOlderControlPlane(t *testing.T) {
	var requests []GatewayConfigPushRequest
	notifier := NewConfigChangeNotifier(nil, NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "jp-1"}), time.Second, time.Second).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, req GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				requests = append(requests, req)
				// 旧主站不会返回 snapshot_checksum_accepted。
				return &GatewayConfigPushResponse{OK: true}, nil
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, _ string) (GatewayConfigPushSnapshot, error) {
				return GatewayConfigPushSnapshot{Groups: []GatewayGroupSnapshot{{ID: 21, Name: "Pro"}}}, nil
			}),
		)
	changed := GatewayConfigChangedRequest{GatewayCode: "jp-1", ChangedAtMillis: 1, EventID: "evt-1"}
	require.NoError(t, notifier.reportConfigPush(changed, false))
	changed.EventID = "evt-2"
	require.NoError(t, notifier.reportConfigPush(changed, false))

	require.Len(t, requests, 2)
	require.False(t, requests[0].RefreshOnly)
	require.False(t, requests[1].RefreshOnly)
	require.True(t, requests[1].GroupsFull)
}

func TestConfigChangeNotifierImmediatelyFallsBackWhenRefreshNeedsFullSnapshot(t *testing.T) {
	var requests []GatewayConfigPushRequest
	call := 0
	notifier := NewConfigChangeNotifier(nil, NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "jp-1"}), time.Second, time.Second).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, req GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				requests = append(requests, req)
				call++
				if call == 2 {
					return &GatewayConfigPushResponse{OK: true, FullSnapshotRequired: true}, nil
				}
				return &GatewayConfigPushResponse{OK: true, SnapshotChecksumAccepted: true}, nil
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, _ string) (GatewayConfigPushSnapshot, error) {
				return GatewayConfigPushSnapshot{Groups: []GatewayGroupSnapshot{{ID: 21, Name: "Pro"}}}, nil
			}),
		)
	changed := GatewayConfigChangedRequest{GatewayCode: "jp-1", ChangedAtMillis: 1, EventID: "evt-1"}
	require.NoError(t, notifier.reportConfigPush(changed, false))
	changed.EventID = "evt-2"
	require.NoError(t, notifier.reportConfigPush(changed, false))

	require.Len(t, requests, 3)
	require.False(t, requests[0].RefreshOnly)
	require.True(t, requests[1].RefreshOnly)
	require.False(t, requests[2].RefreshOnly)
	require.True(t, requests[2].GroupsFull)
	require.Equal(t, requests[1].SnapshotChecksum, requests[2].SnapshotChecksum)
}

func TestConfigChangeNotifierRefreshesQuietPolicySnapshotPeriodically(t *testing.T) {
	var mu sync.Mutex
	requests := make([]GatewayConfigChangedRequest, 0)
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		return &GatewayConfigChangedResponse{OK: true}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "quiet-1", Region: "quiet"}), 2*time.Millisecond, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifier.StartPeriodicSnapshot(ctx, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) > 0 && requests[0].Action == "periodic_snapshot" && requests[0].Resource == "mixed"
	}, time.Second, 5*time.Millisecond)
}

func TestConfigChangeNotifierDoesNotPublishPolicyAuditWhenPushFails(t *testing.T) {
	t.Cleanup(func() { SetDefaultEffectivePolicyAudit(nil) })
	SetDefaultEffectivePolicyAudit([]GatewayEffectivePolicy{{
		LocalGroupID: 21, Revision: 30, Checksum: "policy-30",
		SourceVersions: EffectivePolicySourceVersions{Prices: 40},
	}})
	notifier := NewConfigChangeNotifier(nil, NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "jp-1"}), time.Second, time.Second).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, _ GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				return nil, errors.New("control plane unavailable")
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, _ string) (GatewayConfigPushSnapshot, error) {
				return GatewayConfigPushSnapshot{EffectivePolicies: []GatewayEffectivePolicy{{
					LocalGroupID: 21, Revision: 31, Checksum: "policy-31",
					SourceVersions: EffectivePolicySourceVersions{Prices: 41},
				}}}, nil
			}),
		)

	err := notifier.reportConfigPush(GatewayConfigChangedRequest{GatewayCode: "jp-1"}, false)
	require.ErrorContains(t, err, "control plane unavailable")
	require.Equal(t, EffectivePolicyAuditStamp{Revision: 30, Checksum: "policy-30", PriceVersion: 40}, EffectivePolicyAuditForGroup(21))
}

func TestConfigChangeNotifierFallsBackAndRetriesFailedPush(t *testing.T) {
	var mu sync.Mutex
	pushCalls := 0
	fallbackCalls := 0
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, _ GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		fallbackCalls++
		mu.Unlock()
		return &GatewayConfigChangedResponse{OK: true}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "jp-1", Region: "jp"}), 5*time.Millisecond, 50*time.Millisecond).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, _ GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				mu.Lock()
				defer mu.Unlock()
				pushCalls++
				if pushCalls == 1 {
					return nil, errors.New("push unavailable")
				}
				return &GatewayConfigPushResponse{OK: true}, nil
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, _ string) (GatewayConfigPushSnapshot, error) {
				return GatewayConfigPushSnapshot{}, nil
			}),
		)
	notifier.retryBaseDelay = 5 * time.Millisecond
	notifier.retryMaxDelay = 20 * time.Millisecond
	notifier.nextRetryDelay = notifier.retryBaseDelay

	notifier.Notify("accounts", "update", 11)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushCalls >= 2 && fallbackCalls == 1
	}, time.Second, 10*time.Millisecond)

	metrics := notifier.RuntimeMetrics(context.Background())
	require.Equal(t, false, metrics["config_changed_pending_retry"])
	require.NotZero(t, metrics["config_changed_last_success_at_millis"])
}

func TestConfigChangeNotifierStopsAfterInactiveConfigPush(t *testing.T) {
	var mu sync.Mutex
	pushCalls := 0
	fallbackCalls := 0
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, _ GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		fallbackCalls++
		mu.Unlock()
		return &GatewayConfigChangedResponse{OK: true}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "archived-1", Region: "us"}), 5*time.Millisecond, 50*time.Millisecond).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, _ GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				mu.Lock()
				pushCalls++
				mu.Unlock()
				return &GatewayConfigPushResponse{
					OK:                       false,
					Error:                    "gateway_node_inactive",
					SnapshotChecksumAccepted: true,
				}, nil
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, _ string) (GatewayConfigPushSnapshot, error) {
				return GatewayConfigPushSnapshot{Groups: []GatewayGroupSnapshot{{ID: 21, Name: "Pro"}}}, nil
			}),
		)
	notifier.retryBaseDelay = 5 * time.Millisecond
	notifier.retryMaxDelay = 20 * time.Millisecond
	notifier.nextRetryDelay = notifier.retryBaseDelay

	notifier.Notify("groups", "update", 21)
	require.Eventually(t, func() bool {
		metrics := notifier.RuntimeMetrics(context.Background())
		return metrics["config_changed_last_failure_error"] != nil
	}, time.Second, 5*time.Millisecond)

	// 归档决定是当前事件的终止结果，不能回退或按退避策略重新发送。
	require.Never(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushCalls > 1 || fallbackCalls > 0
	}, 50*time.Millisecond, 5*time.Millisecond)

	mu.Lock()
	require.Equal(t, 1, pushCalls)
	require.Zero(t, fallbackCalls)
	mu.Unlock()
	metrics := notifier.RuntimeMetrics(context.Background())
	require.Equal(t, false, metrics["config_changed_pending_retry"])
	require.Contains(t, metrics["config_changed_last_failure_error"], "gateway_node_inactive")
	require.Empty(t, notifier.lastSnapshotChecksum, "rejected snapshots must never confirm their checksum")
}

func TestConfigChangeNotifierStopsWhenInactiveFallbackRejectsPushFailure(t *testing.T) {
	var mu sync.Mutex
	pushCalls := 0
	fallbackCalls := 0
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, _ GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		fallbackCalls++
		mu.Unlock()
		return &GatewayConfigChangedResponse{OK: false, Error: " gateway_node_inactive "}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "archived-1", Region: "us"}), 5*time.Millisecond, 50*time.Millisecond).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, _ GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				mu.Lock()
				pushCalls++
				mu.Unlock()
				return nil, errors.New("control plane temporarily unavailable")
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, _ string) (GatewayConfigPushSnapshot, error) {
				return GatewayConfigPushSnapshot{}, nil
			}),
		)
	notifier.retryBaseDelay = 5 * time.Millisecond
	notifier.retryMaxDelay = 20 * time.Millisecond
	notifier.nextRetryDelay = notifier.retryBaseDelay

	notifier.Notify("accounts", "update", 11)
	require.Eventually(t, func() bool {
		metrics := notifier.RuntimeMetrics(context.Background())
		return metrics["config_changed_last_failure_error"] != nil
	}, time.Second, 5*time.Millisecond)
	require.Never(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushCalls > 1 || fallbackCalls > 1
	}, 50*time.Millisecond, 5*time.Millisecond)

	mu.Lock()
	require.Equal(t, 1, pushCalls)
	require.Equal(t, 1, fallbackCalls)
	mu.Unlock()
	require.Equal(t, false, notifier.RuntimeMetrics(context.Background())["config_changed_pending_retry"])
}

func TestConfigChangeNotifierStopsLegacyNotificationsForInactiveNode(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, _ GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return &GatewayConfigChangedResponse{OK: false, Error: "gateway_node_inactive"}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "archived-1", Region: "us"}), 5*time.Millisecond, 50*time.Millisecond)
	notifier.retryBaseDelay = 5 * time.Millisecond
	notifier.retryMaxDelay = 20 * time.Millisecond
	notifier.nextRetryDelay = notifier.retryBaseDelay

	notifier.Notify("groups", "delete", 7)
	require.Eventually(t, func() bool {
		metrics := notifier.RuntimeMetrics(context.Background())
		return metrics["config_changed_last_failure_error"] != nil
	}, time.Second, 5*time.Millisecond)
	require.Never(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls > 1
	}, 50*time.Millisecond, 5*time.Millisecond)

	mu.Lock()
	require.Equal(t, 1, calls)
	mu.Unlock()
	require.Equal(t, false, notifier.RuntimeMetrics(context.Background())["config_changed_pending_retry"])
}

func TestConfigChangeNotifierFallsBackWhenSnapshotBuildFails(t *testing.T) {
	var mu sync.Mutex
	fallbackCalls := 0
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, _ GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		fallbackCalls++
		mu.Unlock()
		return &GatewayConfigChangedResponse{OK: true}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "jp-1", Region: "jp"}), 5*time.Millisecond, 50*time.Millisecond).
		WithConfigPush(
			configPushClientFunc(func(_ context.Context, _ GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
				return &GatewayConfigPushResponse{OK: true}, nil
			}),
			configPushSnapshotProviderFunc(func(_ context.Context, _ string) (GatewayConfigPushSnapshot, error) {
				return GatewayConfigPushSnapshot{}, errors.New("database unavailable")
			}),
		)
	notifier.retryBaseDelay = time.Hour
	notifier.retryMaxDelay = time.Hour
	notifier.nextRetryDelay = notifier.retryBaseDelay

	notifier.Notify("groups", "update", 22)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return fallbackCalls == 1
	}, time.Second, 10*time.Millisecond)

	var metrics map[string]any
	require.Eventually(t, func() bool {
		metrics = notifier.RuntimeMetrics(context.Background())
		pending, _ := metrics["config_changed_pending_retry"].(bool)
		return pending
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, true, metrics["config_changed_pending_retry"])
	require.Contains(t, metrics["config_changed_last_failure_error"], "database unavailable")
}

func TestConfigChangeNotifierDebouncesAndBuildsEventID(t *testing.T) {
	var mu sync.Mutex
	requests := make([]GatewayConfigChangedRequest, 0)
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		return &GatewayConfigChangedResponse{OK: true}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "sg-1", Region: "sg"}), 5*time.Millisecond, 50*time.Millisecond)

	notifier.Notify("accounts", "update", 10)
	notifier.Notify("groups", "update", 20)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) == 1
	}, time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "sg-1", requests[0].GatewayCode)
	require.Equal(t, "sg", requests[0].Region)
	require.Equal(t, "mixed", requests[0].Resource)
	require.EqualValues(t, 0, requests[0].ResourceID)
	require.NotEmpty(t, requests[0].EventID)
}

func TestConfigChangeNotifierRetriesFailedReport(t *testing.T) {
	var mu sync.Mutex
	requests := make([]GatewayConfigChangedRequest, 0)
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		if len(requests) == 1 {
			return nil, errors.New("control plane unavailable")
		}
		return &GatewayConfigChangedResponse{OK: true}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "sg-1", Region: "sg"}), 5*time.Millisecond, 50*time.Millisecond)
	notifier.retryBaseDelay = 5 * time.Millisecond
	notifier.retryMaxDelay = 20 * time.Millisecond
	notifier.nextRetryDelay = notifier.retryBaseDelay

	notifier.Notify("model_prices", "update", 33)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) >= 2
	}, time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "model_prices", requests[0].Resource)
	require.Equal(t, "model_prices", requests[1].Resource)
	require.NotEmpty(t, requests[0].EventID)
	require.Equal(t, requests[0].EventID, requests[1].EventID)
}

func TestConfigChangeNotifierRetriesRejectedReport(t *testing.T) {
	var mu sync.Mutex
	requests := make([]GatewayConfigChangedRequest, 0)
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		if len(requests) == 1 {
			return &GatewayConfigChangedResponse{OK: false, Error: "sync failed"}, nil
		}
		return &GatewayConfigChangedResponse{OK: true}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "sg-1", Region: "sg"}), 5*time.Millisecond, 50*time.Millisecond)
	notifier.retryBaseDelay = 5 * time.Millisecond
	notifier.retryMaxDelay = 20 * time.Millisecond
	notifier.nextRetryDelay = notifier.retryBaseDelay

	notifier.Notify("groups", "delete", 7)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) >= 2
	}, time.Second, 10*time.Millisecond)
}

func TestConfigChangeNotifierRuntimeMetricsAfterFailure(t *testing.T) {
	var mu sync.Mutex
	requests := make([]GatewayConfigChangedRequest, 0)
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		return nil, errors.New("control plane unavailable")
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "sg-1", Region: "sg"}), 5*time.Millisecond, 50*time.Millisecond)
	notifier.retryBaseDelay = time.Hour
	notifier.retryMaxDelay = time.Hour
	notifier.nextRetryDelay = notifier.retryBaseDelay

	notifier.Notify("accounts", "update", 11)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) == 1
	}, time.Second, 10*time.Millisecond)

	metrics := notifier.RuntimeMetrics(context.Background())
	require.Equal(t, true, metrics["config_changed_pending_retry"])
	require.Equal(t, "control plane unavailable", metrics["config_changed_last_failure_error"])
	require.NotEmpty(t, metrics["config_changed_last_event_id"])
	require.NotZero(t, metrics["config_changed_last_failure_at_millis"])
}
