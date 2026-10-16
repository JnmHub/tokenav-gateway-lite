package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type gatewayLiteUsageSinkFunc func(context.Context, gatewaylite.UsageEvent) error

func (f gatewayLiteUsageSinkFunc) ReportUsage(ctx context.Context, event gatewaylite.UsageEvent) error {
	return f(ctx, event)
}

type gatewayLiteFailingCommitReporter struct {
	inner            *gatewaylite.RedisUsageReporter
	loseSuccessReply bool
}

func (r *gatewayLiteFailingCommitReporter) ReportUsage(ctx context.Context, event gatewaylite.UsageEvent) error {
	return r.inner.ReportUsage(ctx, event)
}

func (r *gatewayLiteFailingCommitReporter) CommitAndReportUsage(ctx context.Context, quota *gatewaylite.RedisQuota, event gatewaylite.UsageEvent) (int64, error) {
	if r.loseSuccessReply {
		if _, err := r.inner.CommitAndReportUsage(ctx, quota, event); err != nil {
			return 0, err
		}
		return 0, errors.New("simulated lost redis success reply")
	}
	return 0, errors.New("simulated atomic finalize failure before execution")
}

func TestGatewayLiteRequestUsesPerTurnSettlement(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		upgrade    string
		connection string
		want       bool
	}{
		{name: "v1 responses websocket", method: http.MethodGet, path: "/v1/responses", upgrade: "websocket", connection: "keep-alive, Upgrade", want: true},
		{name: "responses alias websocket", method: http.MethodGet, path: "/responses/", upgrade: "WebSocket", connection: "upgrade", want: true},
		{name: "codex websocket", method: http.MethodGet, path: "/backend-api/codex/responses", upgrade: "websocket", connection: "Upgrade", want: true},
		{name: "ordinary responses post", method: http.MethodPost, path: "/v1/responses", upgrade: "websocket", connection: "Upgrade"},
		{name: "missing connection token", method: http.MethodGet, path: "/v1/responses", upgrade: "websocket", connection: "keep-alive"},
		{name: "different websocket path", method: http.MethodGet, path: "/v1/live", upgrade: "websocket", connection: "Upgrade"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Upgrade", tt.upgrade)
			req.Header.Set("Connection", tt.connection)
			c := &gin.Context{Request: req}
			require.Equal(t, tt.want, gatewayLiteRequestUsesPerTurnSettlement(c))
		})
	}
}

func TestGatewayLiteFinalizeFailedWebSocketKeepsCompletedTurnQuota(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	const (
		prefix       = "middleware-ws-settlement"
		connectionID = "ws-connection"
		leaseID      = "lease-ws"
		keyID        = "key-ws"
	)
	quota := gatewaylite.NewRedisQuota(client, prefix)
	reporter := gatewaylite.NewRedisUsageReporter(client, prefix, gatewayLiteUsageSinkFunc(func(context.Context, gatewaylite.UsageEvent) error { return nil }))
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: leaseID, UserID: 42, Region: "sg", AllocatedCents: 100,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	reserved, err := quota.Reserve(ctx, gatewaylite.ReserveRequest{
		RequestID: connectionID, KeyID: keyID, UserID: 42, Region: "sg",
		LeaseID: leaseID, EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	require.NoError(t, reporter.ReportUsage(ctx, gatewaylite.UsageEvent{
		RequestID: "resp_completed", QuotaRequestID: connectionID,
		UserID: 42, KeyID: keyID, Region: "sg", GatewayID: "sg",
		SettlementMode:       string(gatewaylite.BillingSettlementModeIdempotentV2),
		SettlementPending:    true,
		SettlementMicroCents: 1_500_000,
	}))

	started := time.Now().Add(-time.Second)
	gatewayLiteFinalizeUsage(reporter, quota, gatewayLiteFinalizeInput{
		RequestID: connectionID, KeyID: keyID, UserID: 42, LeaseID: leaseID,
		Region: "sg", GatewayID: "sg", Method: http.MethodGet, Path: "/v1/responses",
		Status: http.StatusBadGateway, EstimatedCents: 1, StartedAt: started, EndedAt: time.Now(),
		Aborted: true, BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2,
		PerTurnSettlement: true,
	})

	request, err := client.HGetAll(ctx, prefix+":req:"+connectionID).Result()
	require.NoError(t, err)
	require.Equal(t, "committed", request["status"])
	require.Equal(t, "1", request["actual_cents"])
	lease, err := client.HGetAll(ctx, prefix+":lease:42:sg").Result()
	require.NoError(t, err)
	require.Zero(t, gatewaylite.ParseInt64Field(lease, "reserved_cents"))
	require.EqualValues(t, 1, gatewaylite.ParseInt64Field(lease, "spent_cents"))

	messages, err := client.XRange(ctx, prefix+":usage:stream", "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Equal(t, "0", fmt.Sprint(messages[1].Values["resolved_actual_cents"]))
}

func TestGatewayLiteFinalizeWebSocketFallbackDoesNotClearCompletedTurnQuota(t *testing.T) {
	tests := []struct {
		name             string
		loseSuccessReply bool
		wantMessages     int
	}{
		{name: "atomic script fails before execution", wantMessages: 2},
		{name: "atomic script succeeds but reply is lost", loseSuccessReply: true, wantMessages: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })

			const (
				prefix       = "middleware-ws-fallback"
				connectionID = "ws-fallback-connection"
				leaseID      = "lease-ws-fallback"
				keyID        = "key-ws-fallback"
			)
			quota := gatewaylite.NewRedisQuota(client, prefix)
			inner := gatewaylite.NewRedisUsageReporter(client, prefix, gatewayLiteUsageSinkFunc(func(context.Context, gatewaylite.UsageEvent) error { return nil }))
			reporter := &gatewayLiteFailingCommitReporter{inner: inner, loseSuccessReply: tt.loseSuccessReply}
			require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
				LeaseID: leaseID, UserID: 42, Region: "sg", AllocatedCents: 100,
				Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
			}))
			reserved, err := quota.Reserve(ctx, gatewaylite.ReserveRequest{
				RequestID: connectionID, KeyID: keyID, UserID: 42, Region: "sg",
				LeaseID: leaseID, EstimatedCostCents: 1,
			})
			require.NoError(t, err)
			require.True(t, reserved)

			require.NoError(t, inner.ReportUsage(ctx, gatewaylite.UsageEvent{
				RequestID: "resp_completed", QuotaRequestID: connectionID,
				UserID: 42, KeyID: keyID, Region: "sg", GatewayID: "sg",
				SettlementMode:       string(gatewaylite.BillingSettlementModeIdempotentV2),
				SettlementPending:    true,
				SettlementMicroCents: 1_500_000,
			}))

			started := time.Now().Add(-time.Second)
			gatewayLiteFinalizeUsage(reporter, quota, gatewayLiteFinalizeInput{
				RequestID: connectionID, KeyID: keyID, UserID: 42, LeaseID: leaseID,
				Region: "sg", GatewayID: "sg", Method: http.MethodGet, Path: "/v1/responses",
				Status: http.StatusOK, EstimatedCents: 1, StartedAt: started, EndedAt: time.Now(),
				BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2,
				PerTurnSettlement:     true,
			})

			request, err := client.HGetAll(ctx, prefix+":req:"+connectionID).Result()
			require.NoError(t, err)
			require.Equal(t, "committed", request["status"])
			require.Equal(t, "1", request["actual_cents"])
			require.Equal(t, "1", request["child_actual_cents"])
			lease, err := client.HGetAll(ctx, prefix+":lease:42:sg").Result()
			require.NoError(t, err)
			require.Zero(t, gatewaylite.ParseInt64Field(lease, "reserved_cents"))
			require.EqualValues(t, 1, gatewaylite.ParseInt64Field(lease, "spent_cents"))

			messages, err := client.XRange(ctx, prefix+":usage:stream", "-", "+").Result()
			require.NoError(t, err)
			require.Len(t, messages, tt.wantMessages)
			for _, message := range messages[1:] {
				require.Equal(t, "0", fmt.Sprint(message.Values["resolved_actual_cents"]))
			}
		})
	}
}
