package gatewaylite

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func oauthInvocationMiniQuota(t *testing.T) (*redis.Client, *RedisQuota) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client, NewRedisQuota(client, oauthInvocationTestPrefix(t))
}

func TestOAuthInvocationAdmissionForceFundingAndExactBinding(t *testing.T) {
	s, control := newOAuthInvocationTestServer(t)
	client, quota := oauthInvocationMiniQuota(t)
	ctx := oauthInvocationTestAdmit(t, s, control, quota, "request-one", 800_001)
	request, ok := OAuthInvocationRequestFromContext(ctx)
	require.True(t, ok)
	snapshot := request.Snapshot()
	require.True(t, snapshot.FundingRequest.ForceAuthorization)
	require.Equal(t, "bounded", snapshot.FundingRequest.BoundKind)
	require.Equal(t, "text_max_output", snapshot.FundingRequest.BoundEvidence["kind"])
	require.Equal(t, "gateway_runtime", snapshot.FundingRequest.BoundEvidence["price_source"])
	require.Equal(t, "/v1/responses", snapshot.FundingRequest.BoundEvidence["endpoint"])
	require.Equal(t, "active", snapshot.Reservation.Status)
	require.Equal(t, snapshot.Funding.AuthorizationID, snapshot.Reservation.FundingAuthorizationID)
	require.EqualValues(t, 800_001, snapshot.Funding.UpperBoundMicroCents)
	require.Equal(t, "time_card", snapshot.Funding.Items[0].Source)
	require.NoError(t, MarkOAuthInvocationExecutionStarted(ctx))
	require.Equal(t, snapshot.Funding.AuthorizationID, client.HGet(context.Background(), quota.requestKey("request-one"), "funding_authorization_id").Val())
	s.mu.Lock()
	calls := append([]string(nil), s.calls...)
	s.mu.Unlock()
	require.Equal(t, []string{"/internal/oauth/invocations/resolve", "/internal/oauth/invocations/reserve", "/internal/billing/authorize", "/internal/oauth/invocations/bind"}, calls)
}

func TestOAuthInvocationRejectsScopeConsentAndUnsupportedEndpoints(t *testing.T) {
	cases := []struct {
		name            string
		mutate          func(*oauthInvocationTestServer)
		meta            func(*RequestMeta)
		model, endpoint string
		err             error
	}{
		{name: "exact model case", model: "MODEL-TEST", err: ErrOAuthInvocationDenied},
		{name: "different global group", meta: func(m *RequestMeta) { m.EntryGlobalGroupID = 3 }, err: ErrOAuthInvocationDenied},
		{name: "different gateway", meta: func(m *RequestMeta) { m.GatewayID = "other" }, err: ErrOAuthInvocationDenied},
		{name: "enterprise cannot become personal", meta: func(m *RequestMeta) { m.OrganizationID = 7 }, err: ErrOAuthInvocationDenied},
		{name: "missing paid consent", mutate: func(s *oauthInvocationTestServer) { s.resolved.Invocation.CostConsent = false }, err: ErrOAuthInvocationDenied},
		{name: "selected empty", mutate: func(s *oauthInvocationTestServer) { s.resolved.Invocation.Models = nil }, err: ErrOAuthInvocationDenied},
		{name: "wildcard", mutate: func(s *oauthInvocationTestServer) { s.resolved.Invocation.Models = []string{"*"} }, err: ErrOAuthInvocationDenied},
		{name: "media", endpoint: "/v1/images/generations", err: ErrOAuthInvocationUnsupported},
		{name: "async", endpoint: "/v1/images/tasks", err: ErrOAuthInvocationUnsupported},
		{name: "live", endpoint: "/v1/realtime", err: ErrOAuthInvocationUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			ctx := oauthInvocationTestContext(t, s, "rejected")
			if tc.meta != nil {
				m, _ := RequestMetaFromContext(ctx)
				tc.meta(&m)
				ctx = ContextWithRequestMeta(ctx, m)
			}
			if tc.mutate != nil {
				s.mu.Lock()
				tc.mutate(s)
				s.mu.Unlock()
			}
			model, endpoint := tc.model, tc.endpoint
			if model == "" {
				model = "model-test"
			}
			if endpoint == "" {
				endpoint = "/v1/responses"
			}
			_, err := EnsureOAuthInvocationUpperBound(ctx, control, quota, 300_000, "text:model-test,output_tokens=32", model, endpoint)
			require.ErrorIs(t, err, tc.err)
			s.mu.Lock()
			defer s.mu.Unlock()
			require.Empty(t, s.reservations)
			require.Empty(t, s.funding)
		})
	}
}

func TestOAuthInvocationBudgetAndConcurrencyAcrossRequests(t *testing.T) {
	for _, tc := range []struct {
		name        string
		maximum     int64
		concurrency int
		expected    int
	}{{"micro budget", 1_000_000, 100, 1}, {"delegated concurrency", 100_000_000, 2, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			s.mu.Lock()
			s.maximum = tc.maximum
			s.resolved.Invocation.Concurrency = tc.concurrency
			s.mu.Unlock()
			const workers = 16
			results := make(chan error, workers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				ctx := oauthInvocationTestContext(t, s, fmt.Sprintf("concurrent-%d", i))
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := EnsureOAuthInvocationUpperBound(ctx, control, quota, 600_001, "text:model-test,output_tokens=32", "model-test", "/v1/responses")
					results <- err
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else {
					require.ErrorIs(t, err, ErrOAuthInvocationDenied)
				}
			}
			require.Equal(t, tc.expected, success)
			s.mu.Lock()
			defer s.mu.Unlock()
			var held int64
			for _, a := range s.reservations {
				held += a.UpperBoundMicroCents
			}
			require.LessOrEqual(t, held, tc.maximum)
			require.Len(t, s.funding, tc.expected)
		})
	}
}

func TestOAuthInvocationEachWebSocketTurnRechecksExpiryRevocationAndRPM(t *testing.T) {
	for _, state := range []string{"expiry", "revocation", "rpm"} {
		t.Run(state, func(t *testing.T) {
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			ctx := oauthInvocationTestAdmit(t, s, control, quota, "ws:server-id:1", 500_000)
			e := oauthInvocationTestEvent(t, ctx, 125_001, 2)
			sink := NewOAuthInvocationUsageSink(control, control)
			require.NoError(t, sink.ReportUsage(context.Background(), e))
			s.mu.Lock()
			switch state {
			case "expiry":
				s.resolved.ExpiresAtMillis = time.Now().Add(-time.Second).UnixMilli()
			case "revocation":
				s.revoked = true
			case "rpm":
				s.resolved.Invocation.RateLimitRPM = 1
			}
			s.mu.Unlock()
			turn := ContextForOAuthInvocationTurn(ctx, "ws:server-id:2")
			m, ok := RequestMetaFromContext(turn)
			require.True(t, ok)
			require.Nil(t, m.Funding)
			require.Nil(t, m.Reservation)
			require.Empty(t, m.LeaseID)
			_, err := EnsureOAuthInvocationUpperBound(turn, control, quota, 500_000, "text:model-test,output_tokens=32", "model-test", "/v1/responses")
			require.Error(t, err)
			s.mu.Lock()
			defer s.mu.Unlock()
			require.Len(t, s.reservations, 1)
			require.EqualValues(t, 125_001, s.spent)
		})
	}
}

func TestOAuthInvocationKnownFundingOrBindFailureReleasesBeforeExecution(t *testing.T) {
	for _, phase := range []string{"/internal/billing/authorize", "/internal/oauth/invocations/bind"} {
		t.Run(phase, func(t *testing.T) {
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			s.deny[phase] = "denied"
			ctx, err := EnsureOAuthInvocationUpperBound(oauthInvocationTestContext(t, s, "known-failure"), control, quota, 500_000, "text:model-test,output_tokens=32", "model-test", "/v1/responses")
			require.ErrorIs(t, err, ErrOAuthInvocationDenied)
			request, ok := OAuthInvocationRequestFromContext(ctx)
			require.True(t, ok)
			require.True(t, request.Snapshot().Closed)
			require.Error(t, MarkOAuthInvocationExecutionStarted(ctx))
			s.mu.Lock()
			defer s.mu.Unlock()
			require.Equal(t, "released", s.reservations["known-failure"].Status)
			if phase == "/internal/oauth/invocations/bind" {
				require.Equal(t, "released", s.funding["known-failure"].Status)
			} else {
				require.Empty(t, s.funding)
			}
		})
	}
}

func TestOAuthInvocationReplyLossRetainsBothSidesAndStatusRecovers(t *testing.T) {
	for _, phase := range []string{"/internal/oauth/invocations/reserve", "/internal/billing/authorize", "/internal/oauth/invocations/bind"} {
		t.Run(phase, func(t *testing.T) {
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			s.failAfter[phase] = 1
			ctx, err := EnsureOAuthInvocationUpperBound(oauthInvocationTestContext(t, s, "lost-reply"), control, quota, 500_000, "text:model-test,output_tokens=32", "model-test", "/v1/responses")
			require.ErrorIs(t, err, ErrOAuthInvocationUnknown)
			require.ErrorIs(t, ReleaseOAuthInvocationRequest(ctx, true), ErrOAuthInvocationUnknown)
			request, ok := OAuthInvocationRequestFromContext(ctx)
			require.True(t, ok)
			snapshot := request.Snapshot()
			require.True(t, snapshot.OutcomeUnknown)
			body, err := json.Marshal(snapshot)
			require.NoError(t, err)
			require.NotContains(t, string(body), oauthInvocationTestToken)
			require.NotContains(t, string(body), "secret_hash")
			var restored OAuthInvocationRequestSnapshot
			require.NoError(t, json.Unmarshal(body, &restored))
			recovery, err := RestoreOAuthInvocationRequest(context.Background(), restored, control, quota)
			require.NoError(t, err)
			s.mu.Lock()
			s.revoked = true
			s.mu.Unlock()
			recovered, err := RecoverOAuthInvocationRequest(recovery)
			require.NoError(t, err)
			require.False(t, recovered.OutcomeUnknown)
			require.Error(t, MarkOAuthInvocationExecutionStarted(recovery))
			require.NoError(t, ReleaseOAuthInvocationRequest(recovery, true))
			s.mu.Lock()
			defer s.mu.Unlock()
			require.Equal(t, "released", s.reservations["lost-reply"].Status)
			if a, ok := s.funding["lost-reply"]; ok {
				require.Equal(t, "released", a.Status)
			}
		})
	}
}

func TestOAuthInvocationResizeFailureBlocksAdditionalExecution(t *testing.T) {
	for _, phase := range []string{"/internal/billing/resize-authorization", "/internal/oauth/invocations/resize"} {
		t.Run(phase, func(t *testing.T) {
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			ctx := oauthInvocationTestAdmit(t, s, control, quota, "resize", 500_000)
			require.NoError(t, MarkOAuthInvocationExecutionStarted(ctx))
			s.mu.Lock()
			s.deny[phase] = "insufficient_scope"
			s.mu.Unlock()
			handled, err := EnsureOAuthInvocationFundingForContext(ctx, 800_000, "text:model-test,output_tokens=64")
			require.True(t, handled)
			require.Error(t, err)
			require.Error(t, MarkOAuthInvocationExecutionStarted(ctx))
			request, _ := OAuthInvocationRequestFromContext(ctx)
			snapshot := request.Snapshot()
			require.False(t, snapshot.Closed)
			require.False(t, snapshot.OutcomeUnknown)
			s.mu.Lock()
			defer s.mu.Unlock()
			require.Equal(t, "active", s.reservations["resize"].Status)
			require.EqualValues(t, 500_000, s.reservations["resize"].UpperBoundMicroCents)
			if phase == "/internal/oauth/invocations/resize" {
				require.EqualValues(t, 800_000, s.funding["resize"].UpperBoundMicroCents)
			}
		})
	}
}

func TestOAuthInvocationUnknownReleaseRetainsAndReplaysOriginalOperation(t *testing.T) {
	for _, phase := range []string{"/internal/billing/release-authorization", "/internal/oauth/invocations/release"} {
		t.Run(phase, func(t *testing.T) {
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			ctx := oauthInvocationTestAdmit(t, s, control, quota, "release-reply", 500_000)
			s.mu.Lock()
			s.failAfter[phase] = 1
			s.mu.Unlock()
			require.ErrorIs(t, ReleaseOAuthInvocationRequest(ctx, true), ErrOAuthInvocationUnknown)
			snapshot, err := RecoverOAuthInvocationRequest(ctx)
			require.NoError(t, err)
			require.NotNil(t, snapshot.PendingFundingRelease)
			require.NoError(t, ReleaseOAuthInvocationRequest(ctx, true))
			s.mu.Lock()
			defer s.mu.Unlock()
			require.Equal(t, "released", s.reservations["release-reply"].Status)
			require.Equal(t, "released", s.funding["release-reply"].Status)
		})
	}
}

func TestOAuthInvocationUnknownResizeRetainsAndRecoveryNeverExecutes(t *testing.T) {
	for _, phase := range []string{"/internal/billing/resize-authorization", "/internal/oauth/invocations/resize"} {
		t.Run(phase, func(t *testing.T) {
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			ctx := oauthInvocationTestAdmit(t, s, control, quota, "resize-reply", 500_000)
			require.NoError(t, MarkOAuthInvocationExecutionStarted(ctx))
			s.mu.Lock()
			s.failAfter[phase] = 1
			s.mu.Unlock()
			handled, err := EnsureOAuthInvocationFundingForContext(ctx, 800_000, "text:model-test,output_tokens=64")
			require.True(t, handled)
			require.ErrorIs(t, err, ErrOAuthInvocationUnknown)
			require.Error(t, MarkOAuthInvocationExecutionStarted(ctx))
			require.ErrorIs(t, ReleaseOAuthInvocationRequest(ctx, true), ErrOAuthInvocationUnknown)
			recovered, err := RecoverOAuthInvocationRequest(ctx)
			require.NoError(t, err)
			require.False(t, recovered.OutcomeUnknown)
			require.EqualValues(t, 800_000, recovered.Funding.UpperBoundMicroCents)
			if phase == "/internal/oauth/invocations/resize" {
				require.EqualValues(t, 800_000, recovered.Reservation.UpperBoundMicroCents)
			} else {
				require.EqualValues(t, 500_000, recovered.Reservation.UpperBoundMicroCents)
			}
			require.Error(t, MarkOAuthInvocationExecutionStarted(ctx))
			s.mu.Lock()
			defer s.mu.Unlock()
			require.Equal(t, "active", s.funding["resize-reply"].Status)
			require.Equal(t, "active", s.reservations["resize-reply"].Status)
		})
	}
}

func TestOAuthInvocationRedisInstallFailureNeverExecutesAndRetriesSameHolds(t *testing.T) {
	s, control := newOAuthInvocationTestServer(t)
	client, quota := oauthInvocationMiniQuota(t)
	ctx := oauthInvocationTestContext(t, s, "install-failure")
	broken := quota.fundingKey("f-install-failure")
	require.NoError(t, client.Set(ctx, broken, "wrong-type", 0).Err())
	ctx, err := EnsureOAuthInvocationUpperBound(ctx, control, quota, 500_000, "text:model-test,output_tokens=32", "model-test", "/v1/responses")
	require.ErrorIs(t, err, ErrOAuthInvocationUnavailable)
	require.Error(t, MarkOAuthInvocationExecutionStarted(ctx))
	require.EqualValues(t, 0, client.Exists(ctx, quota.requestKey("install-failure")).Val())
	require.NoError(t, client.Del(ctx, broken).Err())
	ctx, err = EnsureOAuthInvocationUpperBound(ctx, control, quota, 500_000, "text:model-test,output_tokens=32", "model-test", "/v1/responses")
	require.NoError(t, err)
	require.NoError(t, MarkOAuthInvocationExecutionStarted(ctx))
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Len(t, s.reservations, 1)
	require.Len(t, s.funding, 1)
}

func TestOAuthInvocationUnknownOutcomeCannotBeReleasedByTimeout(t *testing.T) {
	s, control := newOAuthInvocationTestServer(t)
	_, quota := oauthInvocationMiniQuota(t)
	ctx := oauthInvocationTestAdmit(t, s, control, quota, "upstream-unknown", 500_000)
	require.NoError(t, MarkOAuthInvocationExecutionStarted(ctx))
	require.ErrorIs(t, ReleaseOAuthInvocationRequest(ctx, false), ErrOAuthInvocationUnknown)
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, "active", s.reservations["upstream-unknown"].Status)
	require.Equal(t, "active", s.funding["upstream-unknown"].Status)
}
