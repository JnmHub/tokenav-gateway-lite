package gatewaylite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func fundingTestAuthorization() FundingAuthorization {
	return FundingAuthorization{AuthorizationID: "fund_test", RequestID: "server-request", KeyID: "key-test", UserID: 42, BillingSubjectType: "user", BillingSubjectID: 42, GatewayCode: "gw", Region: "sg", EntryGatewayNodeID: 8, EntryGroupID: 80, RequestedModel: "gpt-4o", Endpoint: "/v1/responses", Status: "active", Disposition: "authorized", Version: 2, Revision: 2, UpperBoundMicroCents: 10_000_000, MaximumCents: 10, ExecutionDeadlineMillis: time.Now().Add(time.Hour).UnixMilli(), BoundKind: "bounded", BoundEvidence: FundingBoundEvidence("text:gpt-4o,input_bytes_upper_tokens=5000,output_tokens=100,n=1,tariff_max"), Items: []FundingAuthorizationItem{{ItemID: 1, Ordinal: 1, Source: "time_card", EntitlementID: 11, ReservedMicroCents: 6_000_000, MaximumCents: 6}, {ItemID: 2, Ordinal: 2, Source: "balance", ReservedMicroCents: 4_000_000, MaximumCents: 4}}}
}
func fundingTestRedis(t *testing.T) (*redis.Client, *RedisQuota, *RedisUsageReporter) {
	t.Helper()
	var options *redis.Options
	prefix := "fund-test"
	if target := os.Getenv("TOKENAV_FUNDING_TEST_REDIS_URL"); target != "" {
		var err error
		options, err = redis.ParseURL(target)
		require.NoError(t, err)
		prefix = fmt.Sprintf("fund-test-%d", time.Now().UnixNano())
	} else {
		mr := miniredis.RunT(t)
		options = &redis.Options{Addr: mr.Addr()}
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(context.Background()).Err())
	t.Cleanup(func() {
		// 只清理本测试随机命名空间，真实共享 Redis 不得执行 FLUSHDB。
		keys, err := client.Keys(context.Background(), prefix+":*").Result()
		require.NoError(t, err)
		if len(keys) > 0 {
			require.NoError(t, client.Del(context.Background(), keys...).Err())
		}
	})
	return client, NewRedisQuota(client, prefix), NewRedisUsageReporter(client, prefix, nil)
}

func TestFundingReserveAllOrNoneAndLeaseConflict(t *testing.T) {
	ctx := context.Background()
	client, q, _ := fundingTestRedis(t)
	a := fundingTestAuthorization()
	bad := q.fundingItemKey(a.AuthorizationID, 2)
	require.NoError(t, client.Set(ctx, bad, "wrong-type", 0).Err())
	require.ErrorContains(t, q.ReserveFunding(ctx, a), "WRONGTYPE")
	require.EqualValues(t, 0, client.Exists(ctx, q.requestKey(a.RequestID), q.fundingKey(a.AuthorizationID), q.fundingItemKey(a.AuthorizationID, 1)).Val())
	require.NoError(t, client.Del(ctx, bad).Err())
	require.NoError(t, q.ReserveFunding(ctx, a))
	require.NoError(t, q.ReserveFunding(ctx, a))
	changed := a.Clone()
	changed.Items[0].EntitlementID = 99
	require.ErrorContains(t, q.ReserveFunding(ctx, changed), "FUNDING_CONFLICT")
	legacy := a.Clone()
	legacy.AuthorizationID = "other-fund"
	legacy.RequestID = "legacy-request"
	require.NoError(t, client.HSet(ctx, q.requestKey(legacy.RequestID), "lease_id", "old-lease", "reserved_cents", 10).Err())
	require.ErrorContains(t, q.ReserveFunding(ctx, legacy), "FUNDING_CONFLICT")
	require.EqualValues(t, 0, client.Exists(ctx, q.fundingKey(legacy.AuthorizationID)).Val())
}
func fundingUsage(a FundingAuthorization, micro, revision int64) UsageEvent {
	e := UsageEvent{RequestID: a.RequestID, KeyID: a.KeyID, UserID: a.UserID, OrganizationID: a.OrganizationID, BillingSubjectType: a.BillingSubjectType, BillingSubjectID: a.BillingSubjectID, GatewayID: a.GatewayCode, Region: a.Region, Model: a.RequestedModel, RequestedModel: a.RequestedModel, SettlementMode: string(BillingSettlementModeIdempotentV2)}
	AttachFundingUsage(&e, a, micro, revision, true)
	return e
}
func TestFundingQueueDuplicateDisorderRefundLateSettlementAndRestart(t *testing.T) {
	ctx := context.Background()
	client, q, r := fundingTestRedis(t)
	a := fundingTestAuthorization()
	require.NoError(t, q.ReserveFunding(ctx, a))
	e := fundingUsage(a, 8_000_001, 2)
	_, err := r.reportFundedUsage(ctx, e)
	require.NoError(t, err)
	_, err = r.reportFundedUsage(ctx, e)
	require.NoError(t, err)
	require.EqualValues(t, 1, client.XLen(ctx, r.streamKey()).Val())
	_, err = r.reportFundedUsage(ctx, fundingUsage(a, 0, 1))
	require.NoError(t, err)
	require.Equal(t, "8000001", client.HGet(ctx, q.requestKey(a.RequestID), "actual_micro_cents").Val())
	conflict := e
	conflict.Model = "different-model"
	_, err = r.reportFundedUsage(ctx, conflict)
	require.ErrorContains(t, err, "FUNDING_CONFLICT")
	_, err = r.reportFundedUsage(ctx, fundingUsage(a, 2_000_000, 3))
	require.NoError(t, err)
	require.Equal(t, "2000000", client.HGet(ctx, q.fundingItemKey(a.AuthorizationID, 1), "settled_micro_cents").Val())
	require.Equal(t, "0", client.HGet(ctx, q.fundingItemKey(a.AuthorizationID, 2), "settled_micro_cents").Val())
	_, err = r.reportFundedUsage(ctx, fundingUsage(a, 12_000_000, 4))
	require.NoError(t, err)
	require.Equal(t, "10000000", client.HGet(ctx, q.requestKey(a.RequestID), "unfunded_micro_cents").Val(), "terminal refunds cannot recreate released capacity")
	require.ErrorContains(t, q.ReserveFunding(ctx, a), "FUNDING_FINAL")
	// Losing Redis records never loses the queued original funding snapshot.
	messages, err := client.XRange(ctx, r.streamKey(), "-", "+").Result()
	require.NoError(t, err)
	var queued queuedUsageEvent
	require.NoError(t, json.Unmarshal([]byte(messages[len(messages)-1].Values["event"].(string)), &queued))
	require.Equal(t, int64(11), queued.Event.FundingSnapshot.Items[0].EntitlementID)
	require.NoError(t, client.Del(ctx, q.requestKey(a.RequestID), q.fundingKey(a.AuthorizationID), q.fundingItemKey(a.AuthorizationID, 1), q.fundingItemKey(a.AuthorizationID, 2)).Err())
	_, err = r.reportFundedUsage(ctx, queued.Event)
	require.NoError(t, err)
	require.Equal(t, "12000000", client.HGet(ctx, q.requestKey(a.RequestID), "actual_micro_cents").Val())
}
func TestFundingQueueWrongStreamTypeLeavesAllHolds(t *testing.T) {
	ctx := context.Background()
	client, q, r := fundingTestRedis(t)
	a := fundingTestAuthorization()
	require.NoError(t, q.ReserveFunding(ctx, a))
	before := client.HGetAll(ctx, q.requestKey(a.RequestID)).Val()
	require.NoError(t, client.Set(ctx, r.streamKey(), "broken", 0).Err())
	_, err := r.reportFundedUsage(ctx, fundingUsage(a, 0, 1))
	require.ErrorContains(t, err, "WRONGTYPE")
	require.Equal(t, before, client.HGetAll(ctx, q.requestKey(a.RequestID)).Val())
	require.Equal(t, "6000000", client.HGet(ctx, q.fundingItemKey(a.AuthorizationID, 1), "reserved_micro_cents").Val())
}
func TestFundingConcurrentSameRequestInstallsOnce(t *testing.T) {
	_, q, _ := fundingTestRedis(t)
	a := fundingTestAuthorization()
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- q.ReserveFunding(context.Background(), a) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
func TestFundingClientRealActiveContractAndAmbiguousResize(t *testing.T) {
	ctx := context.Background()
	_, q, _ := fundingTestRedis(t)
	a := fundingTestAuthorization()
	selected := a.Clone()
	selected.Status = "selected"
	selected.Disposition = "authorization_required"
	selected.Version = 1
	selected.Items = nil
	selected.UpperBoundMicroCents = 0
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer internal-test", r.Header.Get("Authorization"))
		if r.Method == http.MethodPost {
			var req AuthorizeFundingRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Equal(t, "text_max_output", req.BoundEvidence["kind"])
			require.Equal(t, "gateway_runtime", req.BoundEvidence["price_source"])
			calls++
		}
		_ = json.NewEncoder(w).Encode(FundingAuthorizationResponse{OK: true, Disposition: "authorized", Authorization: &a})
	}))
	defer srv.Close()
	client, err := NewControlPlaneClient(srv.URL, "internal-test", time.Second)
	require.NoError(t, err)
	funding := NewRequestFunding(selected, client, q)
	got, err := funding.Ensure(ctx, 10_000_000, "text:gpt-4o,input_bytes_upper_tokens=5000,output_tokens=100,n=1")
	require.NoError(t, err)
	require.Equal(t, "active", got.Status)
	require.Equal(t, 1, calls)
	got.Status = "released"
	funding = NewRequestFunding(got, client, q)
	_, err = funding.Ensure(ctx, 1, "text:gpt-4o")
	require.ErrorIs(t, err, ErrRequestQuotaUnavailable)
	fake := &fundingRecoveryControl{auth: a.Clone()}
	fake.auth.Version = 3
	fake.auth.UpperBoundMicroCents = 12_000_000
	fake.auth.MaximumCents = 12
	fake.auth.Items[1].ReservedMicroCents = 6_000_000
	fake.auth.Items[1].MaximumCents = 6
	fake.auth.LastOperationID = "server-request:resize:2:12000000"
	funding = NewRequestFunding(a, fake, q)
	got, err = funding.Ensure(ctx, 12_000_000, "text:gpt-4o")
	require.NoError(t, err)
	require.EqualValues(t, 3, got.Version)
	require.Equal(t, 1, fake.resizeCalls)
}

func TestFundingPartialMirrorRecoveryAndIdentityPreflight(t *testing.T) {
	ctx := context.Background()
	client, q, r := fundingTestRedis(t)
	a := fundingTestAuthorization()
	require.NoError(t, q.ReserveFunding(ctx, a))
	for _, key := range []string{q.requestKey(a.RequestID), q.fundingKey(a.AuthorizationID), q.fundingItemKey(a.AuthorizationID, 2)} {
		require.NoError(t, client.Del(ctx, key).Err())
		require.NoError(t, q.ReserveFunding(ctx, a))
		require.EqualValues(t, 4, client.Exists(ctx, q.requestKey(a.RequestID), q.fundingKey(a.AuthorizationID), q.fundingItemKey(a.AuthorizationID, 1), q.fundingItemKey(a.AuthorizationID, 2)).Val())
	}
	before := client.HGetAll(ctx, q.requestKey(a.RequestID)).Val()
	require.NoError(t, client.HSet(ctx, q.fundingItemKey(a.AuthorizationID, 2), "entitlement_id", "99").Err())
	require.ErrorContains(t, q.ReserveFunding(ctx, a), "FUNDING_CONFLICT")
	require.Equal(t, before, client.HGetAll(ctx, q.requestKey(a.RequestID)).Val())
	require.NoError(t, client.HSet(ctx, q.fundingItemKey(a.AuthorizationID, 2), "entitlement_id", "0").Err())
	event := fundingUsage(a, 8_000_001, 2)
	_, err := r.reportFundedUsage(ctx, event)
	require.NoError(t, err)
	require.NoError(t, client.Del(ctx, q.requestKey(a.RequestID), q.fundingItemKey(a.AuthorizationID, 2)).Err())
	retry := event
	retry.ReportLatencyMillis, retry.LatencyMillis, retry.EndedAtMillis = 99, 120, time.Now().UnixMilli()
	_, err = r.reportFundedUsage(ctx, retry)
	require.NoError(t, err)
	require.EqualValues(t, 1, client.XLen(ctx, r.streamKey()).Val())
	require.Equal(t, "2000001", client.HGet(ctx, q.fundingItemKey(a.AuthorizationID, 2), "settled_micro_cents").Val())
	require.ErrorContains(t, q.ReserveFunding(ctx, a), "FUNDING_FINAL")
}

func TestFundingOldUsageNeverDowngradesAllocationVersion(t *testing.T) {
	ctx := context.Background()
	client, q, r := fundingTestRedis(t)
	old := fundingTestAuthorization()
	next := old.Clone()
	next.Version++
	next.UpperBoundMicroCents = 12_000_000
	next.Items[1].ReservedMicroCents = 6_000_000
	require.NoError(t, q.ReserveFunding(ctx, next))
	_, err := r.reportFundedUsage(ctx, fundingUsage(old, 2_000_000, 2))
	require.NoError(t, err)
	require.Equal(t, "3", client.HGet(ctx, q.fundingKey(old.AuthorizationID), "version").Val())
	require.Equal(t, "3", client.HGet(ctx, q.requestKey(old.RequestID), "funding_version").Val())
	require.Equal(t, "6000000", client.HGet(ctx, q.fundingItemKey(old.AuthorizationID, 2), "reserved_micro_cents").Val())
}

func TestFundingExactNumbersOrdinalsAndEnterpriseSubjects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*FundingAuthorization)
	}{
		{"ordinal", func(a *FundingAuthorization) { a.Items[1].Ordinal = 1 }},
		{"item_id_precision", func(a *FundingAuthorization) { a.Items[1].ItemID = maxFundingExactInteger + 1 }},
		{"entitlement_precision", func(a *FundingAuthorization) { a.Items[0].EntitlementID = maxFundingExactInteger + 1 }},
		{"organization_subject", func(a *FundingAuthorization) { a.OrganizationID = 8; a.BillingSubjectType = "organization" }},
		{"user_subject", func(a *FundingAuthorization) { a.BillingSubjectID = 8 }},
		{"balance_entitlement", func(a *FundingAuthorization) { a.Items[1].EntitlementID = 9 }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			client, q, _ := fundingTestRedis(t)
			a := fundingTestAuthorization()
			tt.mutate(&a)
			require.Error(t, q.ReserveFunding(context.Background(), a))
			keys, err := client.Keys(context.Background(), q.Prefix()+":*").Result()
			require.NoError(t, err)
			require.Empty(t, keys)
		})
	}
	a := fundingTestAuthorization()
	a.OrganizationID = 8
	a.BillingSubjectType = "organization"
	a.BillingSubjectID = 8
	_, q, _ := fundingTestRedis(t)
	require.NoError(t, q.ReserveFunding(context.Background(), a))
}

func TestFundingPreDispatchDelegationReleasesOriginalPairAndKeepsUnknown(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_sent", true: "sent"}[dispatched], func(t *testing.T) {
			server, control := newOAuthInvocationTestServer(t)
			_, quota, _ := fundingTestRedis(t)
			resolved, err := ResolveOAuthInvocationCredential(context.Background(), control, oauthInvocationTestToken, "node-test", "sg")
			require.NoError(t, err)
			ctx, err := ContextWithOAuthInvocationCredential(context.Background(), oauthInvocationTestToken, "node-test", "sg", resolved)
			require.NoError(t, err)
			ctx = ContextWithRequestMeta(ctx, RequestMeta{RequestID: "pre-dispatch-test", KeyID: resolved.Key.KeyID, UserID: 42, GatewayID: "node-test", Region: "sg", EntryGatewayNodeID: 5, EntryGlobalGroupID: 50, RateMultiplier: 1})
			ctx, err = EnsureOAuthInvocationUpperBound(ctx, control, quota, 10_000_000, "text:model-test,output_tokens=100,n=1", "model-test", "/v1/responses")
			require.NoError(t, err)
			if dispatched {
				require.NoError(t, MarkOAuthInvocationExecutionStarted(ctx))
				MarkFundingDispatched(ctx)
			}
			err = ReleaseFundingPreDispatch(ctx, "test-admission-failure")
			server.mu.Lock()
			defer server.mu.Unlock()
			if dispatched {
				require.ErrorIs(t, err, ErrOAuthInvocationUnknown)
				require.Equal(t, "active", server.funding["pre-dispatch-test"].Status)
				require.Equal(t, "active", server.reservations["pre-dispatch-test"].Status)
			} else {
				require.NoError(t, err)
				require.Equal(t, "released", server.funding["pre-dispatch-test"].Status)
				require.Equal(t, "released", server.reservations["pre-dispatch-test"].Status)
				require.Equal(t, []string{"/internal/billing/release-authorization", "/internal/oauth/invocations/release"}, server.calls[len(server.calls)-2:])
			}
		})
	}
}

func TestFundingLegacyTurnFinalUsageCommitsOnlyOnceAndKeepsFractionalCarry(t *testing.T) {
	ctx := context.Background()
	client, quota, _ := fundingTestRedis(t)
	previous := DefaultUsageReporter()
	reporter := NewRedisUsageReporter(client, quota.Prefix(), fundingUsageNoopSink{})
	SetDefaultUsageReporter(reporter)
	t.Cleanup(func() { SetDefaultUsageReporter(previous) })
	lease := LeaseSnapshot{LeaseID: "turn-lease", UserID: 42, Region: "sg", AllocatedCents: 100, Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, quota.EnsureLease(ctx, lease))
	for index, micro := range []int64{600_000, 400_000} {
		id := []string{"turn-one", "turn-two"}[index]
		allowed, err := quota.Reserve(ctx, ReserveRequest{RequestID: id, KeyID: "key-test", UserID: 42, Region: "sg", LeaseID: lease.LeaseID, EstimatedCostCents: 1})
		require.NoError(t, err)
		require.True(t, allowed)
		turnCtx := ContextWithRequestMeta(ctx, RequestMeta{RequestID: id, KeyID: "key-test", UserID: 42, Region: "sg", GatewayID: "gw", LeaseID: lease.LeaseID, BillingSettlementMode: BillingSettlementModeIdempotentV2, Reservation: NewTurnRequestReservation(1, lease.LeaseID)})
		event := UsageEvent{RequestID: id, QuotaRequestID: id, SettlementMode: string(BillingSettlementModeIdempotentV2), SettlementPending: true, SettlementMicroCents: micro, Final: true}
		if index == 0 {
			require.NoError(t, client.Set(ctx, reporter.streamKey(), "wrong-type", 0).Err())
			require.ErrorContains(t, ReportUsageCorrection(turnCtx, event), "WRONGTYPE")
			require.Equal(t, "reserved", client.HGet(ctx, quota.requestKey(id), "status").Val())
			require.NoError(t, client.Del(ctx, reporter.streamKey()).Err())
		}
		require.NoError(t, ReportUsageCorrection(turnCtx, event))
		require.NoError(t, ReportUsageCorrection(turnCtx, event))
		require.Equal(t, "committed", client.HGet(ctx, quota.requestKey(id), "status").Val())
		mirror, ok, err := quota.LoadLease(ctx, 42, "sg")
		require.NoError(t, err)
		require.True(t, ok)
		require.Zero(t, mirror.ReservedCents)
		require.EqualValues(t, index, mirror.SpentCents)
		require.EqualValues(t, index+1, client.XLen(ctx, reporter.streamKey()).Val())
	}
	// A missing original lease is unknown, not a reason to settle the remainder
	// or silently emit an unbacked event. Reinstalling the same mirror permits retry.
	allowed, err := quota.Reserve(ctx, ReserveRequest{RequestID: "turn-missing", KeyID: "key-test", UserID: 42, Region: "sg", LeaseID: lease.LeaseID, EstimatedCostCents: 1})
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, client.Del(ctx, quota.leaseKey(42, "sg")).Err())
	err = reporter.ReportUsage(ctx, UsageEvent{RequestID: "turn-missing", QuotaRequestID: "turn-missing", SettlementMode: string(BillingSettlementModeIdempotentV2), SettlementPending: true, SettlementMicroCents: 1_000_000, Final: true})
	require.ErrorContains(t, err, "SETTLEMENT_UNAVAILABLE")
	require.EqualValues(t, 2, client.XLen(ctx, reporter.streamKey()).Val())
	require.Empty(t, client.HGet(ctx, quota.requestKey("turn-missing"), "settlement_actual_cents").Val())
}

type fundingUsageNoopSink struct{}

func (fundingUsageNoopSink) ReportUsage(context.Context, UsageEvent) error { return nil }

type fundingRecoveryControl struct {
	auth        FundingAuthorization
	resizeCalls int
}

func (f *fundingRecoveryControl) AuthorizeFunding(context.Context, AuthorizeFundingRequest) (*FundingAuthorizationResponse, error) {
	return nil, errors.New("unused")
}
func (f *fundingRecoveryControl) ResizeFundingAuthorization(context.Context, ResizeFundingAuthorizationRequest) (*FundingAuthorizationResponse, error) {
	f.resizeCalls++
	return nil, context.DeadlineExceeded
}
func (f *fundingRecoveryControl) ReleaseFundingAuthorization(context.Context, ReleaseFundingAuthorizationRequest) (*FundingAuthorizationResponse, error) {
	return nil, errors.New("unused")
}
func (f *fundingRecoveryControl) LookupFundingAuthorization(context.Context, string) (*FundingAuthorizationResponse, error) {
	return &FundingAuthorizationResponse{OK: true, Authorization: &f.auth}, nil
}
