package ingress

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAggregatorKeepsOnlyCountsAndBuildsWindowTrend(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 15, 0, time.UTC)
	a := NewAggregator()
	a.now = func() time.Time { return now }
	a.SetNode("us-1")

	a.ObserveRequest()
	a.ObserveRequest()
	a.Record(RejectBodyTooLarge)
	now = now.Add(time.Minute)
	a.ObserveRequest()
	a.Record(RejectBodyTooLarge)
	a.Record(RejectInvalidPath)

	snapshot := a.Snapshot(5)
	require.Equal(t, "us-1", snapshot.Node)
	require.Equal(t, uint64(3), snapshot.RequestCount)
	require.Equal(t, uint64(3), snapshot.RejectionCount)
	// 返回完整分钟轴（包含 0）才能让前端柱状图保持真实时间间距。
	require.Len(t, snapshot.Trend, 5)
	require.Zero(t, snapshot.Trend[0].Total)
	require.Equal(t, uint64(1), snapshot.Trend[3].Total)
	require.Equal(t, uint64(2), snapshot.Trend[4].Total)
	require.Equal(t, RejectBodyTooLarge, snapshot.TopReasons[0].Reason)
	require.Equal(t, uint64(2), snapshot.TopReasons[0].Count)
	require.Equal(t, RejectInvalidPath, snapshot.TopReasons[1].Reason)
	require.False(t, a.Health().SensitiveFieldsStored)
}

func TestAggregatorConcurrentRecordIsExact(t *testing.T) {
	a := NewAggregator()
	const workers = 32
	const perWorker = 100
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				a.ObserveRequest()
				a.Record(RejectAuthRateLimit)
			}
		}()
	}
	wg.Wait()

	snapshot := a.Snapshot(5)
	require.Equal(t, uint64(workers*perWorker), snapshot.RequestCount)
	require.Equal(t, uint64(workers*perWorker), snapshot.RejectionCount)
	require.Equal(t, uint64(workers*perWorker), snapshot.TopReasons[0].Count)
}

func TestAggregatorRejectsUnknownReason(t *testing.T) {
	a := NewAggregator()
	a.Record(RejectionReason("raw-secret-value"))
	require.Zero(t, a.Health().RejectionsTotal)
	require.Empty(t, a.Snapshot(60).TopReasons)
}

func TestAggregatorRuntimeMetricsUsesSameReasonTotalsWithoutTrend(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 15, 0, time.UTC)
	a := NewAggregator()
	a.now = func() time.Time { return now }
	a.ObserveRequest()
	a.Record(RejectBodyTooLarge)
	a.Record(RejectBodyTooLarge)
	a.Record(RejectInvalidPath)

	metrics := a.RuntimeMetrics()
	require.Equal(t, uint64(2), metrics["ingress_rejection_body_too_large_total"])
	require.Equal(t, uint64(1), metrics["ingress_rejection_invalid_path_total"])
	require.Equal(t, uint64(3), metrics["ingress_rejections_5m_total"])
}

func BenchmarkAggregatorRuntimeMetricsSparse24Hours(b *testing.B) {
	a := NewAggregator()
	for i := 0; i < 10; i++ {
		a.ObserveRequest()
		a.Record(RejectInvalidPath)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.RuntimeMetrics()
	}
}
