package servertiming

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollectorOnlyRendersPublicWhitelistedStages(t *testing.T) {
	startedAt := time.Unix(100, 0)
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)
	Add(ctx, StageAuth, 2*time.Millisecond)
	Add(ctx, "postgres_account_42", 999*time.Millisecond)
	Set(ctx, StageQuota, 3*time.Millisecond)

	header := HeaderValue(ctx, startedAt.Add(10*time.Millisecond))
	require.Equal(t, "total;dur=10.0, auth;dur=2.0, quota;dur=3.0", header)
	require.NotContains(t, header, "postgres")
	require.NotContains(t, header, "account")
}

func TestCollectorFirstByteUsesUpstreamBoundaryOnce(t *testing.T) {
	collector := New(time.Now())
	ctx := WithCollector(context.Background(), collector)
	MarkUpstreamStart(ctx)
	time.Sleep(time.Millisecond)
	RecordFirstByte(ctx, time.Now())
	RecordFirstByte(ctx, time.Now().Add(time.Second))
	header := HeaderValue(ctx, time.Now())
	require.Contains(t, header, "firstbyte;dur=")
	require.NotContains(t, header, "1000.0")
}

func TestCollectorKeepsHandlerMeasuredFirstByte(t *testing.T) {
	collector := New(time.Now())
	ctx := WithCollector(context.Background(), collector)
	Set(ctx, StageFirstByte, 7*time.Millisecond)
	MarkUpstreamStart(ctx)
	RecordFirstByte(ctx, time.Now().Add(time.Second))

	header := HeaderValue(ctx, time.Now())
	require.Contains(t, header, "firstbyte;dur=7.0")
	require.NotContains(t, header, "1000.0")
}

func TestControllerSamplingAndTemporaryOverride(t *testing.T) {
	now := time.Unix(200, 0)
	controller := NewController(0)
	require.False(t, controller.ShouldCollect(now))

	until := controller.EnableTemporary(now, 15*time.Minute)
	require.Equal(t, now.Add(15*time.Minute).UTC(), until)
	require.True(t, controller.ShouldCollect(now))
	status := controller.Status(now)
	require.True(t, status.TemporaryEnabled)
	require.Equal(t, 15*time.Minute, status.TemporaryUntil.Sub(now))

	controller.DisableTemporary()
	require.False(t, controller.ShouldCollect(now))
	controller.SetSampleBasisPoints(10_000)
	require.True(t, controller.ShouldCollect(now))
	require.Equal(t, 100.0, controller.Status(now).SamplePercent)
}

func TestHeaderHasNoControlCharacters(t *testing.T) {
	collector := New(time.Now())
	ctx := WithCollector(context.Background(), collector)
	Add(ctx, "route\r\nInjected", time.Second)
	header := HeaderValue(ctx, time.Now())
	require.False(t, strings.ContainsAny(header, "\r\n"))
}

func BenchmarkDisabledController(b *testing.B) {
	controller := NewController(0)
	now := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = controller.ShouldCollect(now)
	}
}
