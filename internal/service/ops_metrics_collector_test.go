package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestWriteOpenAIFastPolicyBlockedResponseMarksBusinessLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	writeOpenAIFastPolicyBlockedResponse(c, &OpenAIFastBlockedError{Message: "custom fast policy block"})

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.True(t, HasOpsClientBusinessLimited(c))
	reason, ok := c.Get(OpsClientBusinessLimitedReasonKey)
	require.True(t, ok)
	require.Equal(t, OpsClientBusinessLimitedReasonLocalPolicyDenied, reason)
}

func TestOpsMetricsCollectorQueryErrorCountsExcludesCountTokens(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	collector := &OpsMetricsCollector{db: db}
	start := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	mock.ExpectQuery(`(?s)FROM ops_error_logs\s+WHERE created_at >= \$1 AND created_at < \$2\s+AND is_count_tokens = FALSE`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"error_total",
			"business_limited",
			"error_sla",
			"upstream_excl",
			"upstream_429",
			"upstream_529",
		}).AddRow(int64(5), int64(2), int64(3), int64(1), int64(1), int64(1)))

	errorTotal, businessLimited, errorSLA, upstreamExcl429529, upstream429, upstream529, err := collector.queryErrorCounts(context.Background(), start, end)
	require.NoError(t, err)
	require.Equal(t, int64(5), errorTotal)
	require.Equal(t, int64(2), businessLimited)
	require.Equal(t, int64(3), errorSLA)
	require.Equal(t, int64(1), upstreamExcl429529)
	require.Equal(t, int64(1), upstream429)
	require.Equal(t, int64(1), upstream529)
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsMetricsCollectorRedisPoolWindowDelta(t *testing.T) {
	collector := &OpsMetricsCollector{}

	baseline := collector.redisPoolWindowDelta(&redis.PoolStats{
		TotalConns:     20,
		IdleConns:      12,
		WaitCount:      100,
		WaitDurationNs: int64(2500 * time.Millisecond),
		Timeouts:       3,
	})
	require.True(t, baseline.ok)
	require.Equal(t, 20, baseline.total)
	require.Equal(t, 12, baseline.idle)
	require.Zero(t, baseline.waitCount)
	require.Zero(t, baseline.waitDurationMs)
	require.Zero(t, baseline.timeouts)

	window := collector.redisPoolWindowDelta(&redis.PoolStats{
		TotalConns:     22,
		IdleConns:      9,
		WaitCount:      107,
		WaitDurationNs: int64(2525 * time.Millisecond),
		Timeouts:       5,
	})
	require.Equal(t, int64(7), window.waitCount)
	require.Equal(t, int64(25), window.waitDurationMs)
	require.Equal(t, int64(2), window.timeouts)

	// A pool/client restart rolls counters back. Treat the current counters as the
	// first delta of the new process instead of underflowing to a huge number.
	restarted := collector.redisPoolWindowDelta(&redis.PoolStats{
		WaitCount:      2,
		WaitDurationNs: int64(4 * time.Millisecond),
		Timeouts:       1,
	})
	require.Equal(t, int64(2), restarted.waitCount)
	require.Equal(t, int64(4), restarted.waitDurationMs)
	require.Equal(t, int64(1), restarted.timeouts)
}
