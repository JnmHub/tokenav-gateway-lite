package gatewaylite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOAuthInvocationUsageCorrectionsAndOperationConflict(t *testing.T) {
	s, control := newOAuthInvocationTestServer(t)
	_, quota := oauthInvocationMiniQuota(t)
	ctx := oauthInvocationTestAdmit(t, s, control, quota, "correction", 900_000)
	sink := NewOAuthInvocationUsageSink(control, control)
	event := oauthInvocationTestEvent(t, ctx, 600_001, 2)
	require.NoError(t, sink.ReportUsage(context.Background(), event))
	s.mu.Lock()
	s.revoked = true
	s.resolved.ExpiresAtMillis = time.Now().Add(-time.Second).UnixMilli()
	s.mu.Unlock()
	require.NoError(t, sink.ReportUsage(context.Background(), event))
	corrected := oauthInvocationTestEvent(t, ctx, 250_001, 3)
	require.NoError(t, sink.ReportUsage(context.Background(), corrected))
	require.NoError(t, sink.ReportUsage(context.Background(), event)) // Older absolute revision cannot recharge spent funds.
	s.mu.Lock()
	require.EqualValues(t, 250_001, s.spent)
	require.EqualValues(t, 3, s.reservations["correction"].Revision)
	s.mu.Unlock()
	r := event.OAuthInvocationReservation
	response, err := control.SettleOAuthInvocation(context.Background(), OAuthInvocationFinalizeRequest{ReservationID: r.ReservationID, RequestID: r.RequestID,
		GatewayCode: r.GatewayCode, FundingAuthorizationID: r.FundingAuthorizationID, OperationID: fmt.Sprintf("%s:oauth-settle:2:v%d", r.RequestID, r.Version), ExpectedVersion: r.Version,
		Revision: 2, ActualMicroCents: 600_002, TerminalConfirmed: true})
	require.Error(t, err)
	require.ErrorIs(t, oauthInvocationOperationError(err, response), ErrOAuthInvocationDenied)
}

func TestOAuthInvocationUsageIdentityRejectsBeforeEitherSettlement(t *testing.T) {
	s, control := newOAuthInvocationTestServer(t)
	_, quota := oauthInvocationMiniQuota(t)
	ctx := oauthInvocationTestAdmit(t, s, control, quota, "identity", 900_000)
	event := oauthInvocationTestEvent(t, ctx, 300_001, 2)
	event.FundingAuthorizationID = "other-funding"
	sink := NewOAuthInvocationUsageSink(control, control)
	require.ErrorIs(t, sink.ReportUsage(context.Background(), event), ErrOAuthInvocationConflict)
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Zero(t, s.spent)
	require.Equal(t, "active", s.funding["identity"].Status)
}

func TestOAuthInvocationDetachedUsageHasNoCredentialOrExecutionPermission(t *testing.T) {
	s, control := newOAuthInvocationTestServer(t)
	_, quota := oauthInvocationMiniQuota(t)
	parent := oauthInvocationTestAdmit(t, s, control, quota, "detached", 900_000)
	meta, ok := RequestMetaFromContext(parent)
	require.True(t, ok)
	worker := ContextWithOAuthInvocationUsageSnapshot(ContextWithRequestMeta(context.Background(), meta), parent)
	require.False(t, HasOAuthInvocationCredential(worker))
	_, executable := OAuthInvocationRequestFromContext(worker)
	require.False(t, executable)
	r, ok := OAuthInvocationReservationFromContext(worker)
	require.True(t, ok)
	r.FundingAuthorizationID = "changed"
	frozen, ok := OAuthInvocationReservationFromContext(worker)
	require.True(t, ok)
	require.NotEqual(t, r.FundingAuthorizationID, frozen.FundingAuthorizationID)
	event := UsageEvent{RequestID: meta.RequestID, KeyID: meta.KeyID, UserID: meta.UserID, GatewayID: meta.GatewayID, Region: meta.Region}
	auth, ok := FundingFromContext(worker)
	require.True(t, ok)
	AttachFundingUsage(&event, auth, 300_001, 2, true)
	require.NoError(t, AttachOAuthInvocationUsage(worker, &event))
	require.Equal(t, frozen, *event.OAuthInvocationReservation)
	require.NoError(t, NewOAuthInvocationUsageSink(control, control).ReportUsage(context.Background(), event))
	missing := ContextWithOAuthInvocationUsageSnapshot(context.Background(), oauthInvocationTestContext(t, s, "not-admitted"))
	require.ErrorIs(t, AttachOAuthInvocationUsage(missing, &UsageEvent{}), ErrOAuthInvocationConflict)
}

func TestOAuthInvocationReliableQueueKeepsFailedSidesAndReplays(t *testing.T) {
	client, quota := oauthInvocationMiniQuota(t)
	runOAuthInvocationReliableQueue(t, client, quota)
}

// This uses the actual local Redis supplied to this task. It proves the durable
// Stream/consumer-group boundary, unlike a queue mock or a successful agent report.
func TestOAuthInvocationReliableQueueRealRedis(t *testing.T) {
	redisURL := strings.TrimSpace(os.Getenv("TOKENAV_TEST_REDIS_URL"))
	if redisURL == "" {
		t.Skip("TOKENAV_TEST_REDIS_URL is required for real Redis verification")
	}
	options, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Ping(ctx).Err())
	prefix := oauthInvocationTestPrefix(t)
	quota := NewRedisQuota(client, prefix)
	t.Cleanup(func() { oauthInvocationDeletePrefix(t, client, prefix) })
	runOAuthInvocationReliableQueue(t, client, quota)
}

func runOAuthInvocationReliableQueue(t *testing.T, client *redis.Client, quota *RedisQuota) {
	t.Helper()
	ctx := context.Background()
	s, control := newOAuthInvocationTestServer(t)
	requestCtx := oauthInvocationTestAdmit(t, s, control, quota, "durable-queue", 900_000)
	event := oauthInvocationTestEvent(t, requestCtx, 450_001, 2)
	sink := NewOAuthInvocationUsageSink(control, control)
	reporter := NewRedisUsageReporter(client, quota.Prefix(), sink)
	require.NoError(t, reporter.ensureGroup(ctx))
	require.NoError(t, reporter.ReportUsage(ctx, event))
	streams, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: reporter.groupNameForPrefix(quota.Prefix()), Consumer: "oauth-test", Streams: []string{reporter.streamKey(), ">"}, Count: 1}).Result()
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Len(t, streams[0].Messages, 1)
	message := streams[0].Messages[0]
	var queued queuedUsageEvent
	require.NoError(t, json.Unmarshal([]byte(message.Values["event"].(string)), &queued))
	require.NotNil(t, queued.Event.OAuthInvocationReservation)
	require.EqualValues(t, 450_001, *queued.Event.ActualMicroCents)
	s.mu.Lock()
	s.failBefore["/internal/usage/report-batch"] = 1
	s.mu.Unlock()
	require.Error(t, reporter.reportBatch(ctx, []queuedUsageEvent{queued}))
	assertOAuthInvocationPending(t, ctx, client, reporter, 1)
	s.mu.Lock()
	require.Equal(t, "active", s.funding[event.RequestID].Status)
	require.Equal(t, "active", s.reservations[event.RequestID].Status)
	s.failBefore["/internal/oauth/invocations/settle"] = 1
	s.mu.Unlock()
	require.ErrorIs(t, reporter.reportBatch(ctx, []queuedUsageEvent{queued}), ErrOAuthInvocationUnknown)
	assertOAuthInvocationPending(t, ctx, client, reporter, 1)
	s.mu.Lock()
	require.Equal(t, "settled", s.funding[event.RequestID].Status)
	require.Equal(t, "active", s.reservations[event.RequestID].Status)
	require.Zero(t, s.spent)
	s.failAfter["/internal/oauth/invocations/settle"] = 1
	s.mu.Unlock()
	// Each operation freezes its payload. After reply loss, status may select a
	// newer version/operation; the original persisted fee revision stays immutable.
	require.ErrorIs(t, reporter.reportBatch(ctx, []queuedUsageEvent{queued}), ErrOAuthInvocationUnknown)
	assertOAuthInvocationPending(t, ctx, client, reporter, 1)
	restarted := NewRedisUsageReporter(client, quota.Prefix(), NewOAuthInvocationUsageSink(control, control))
	require.NoError(t, restarted.reportBatch(ctx, []queuedUsageEvent{queued}))
	restarted.ack(ctx, []string{message.ID}, quota.Prefix())
	assertOAuthInvocationPending(t, ctx, client, restarted, 0)
	require.EqualValues(t, 0, client.XLen(ctx, reporter.streamKey()).Val())
	s.mu.Lock()
	defer s.mu.Unlock()
	require.EqualValues(t, 450_001, s.spent)
	require.Equal(t, "settled", s.reservations[event.RequestID].Status)
}

func assertOAuthInvocationPending(t *testing.T, ctx context.Context, client *redis.Client, reporter *RedisUsageReporter, count int64) {
	t.Helper()
	pending, err := client.XPending(ctx, reporter.streamKey(), reporter.groupNameForPrefix(reporter.Prefix())).Result()
	require.NoError(t, err)
	require.EqualValues(t, count, pending.Count)
}
