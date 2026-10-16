package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorBusinessDayUsesShanghaiBoundary(t *testing.T) {
	// UTC 仍是 8 月 3 日，上海已经进入 8 月 4 日。
	instant := time.Date(2026, 8, 3, 16, 30, 0, 0, time.UTC)
	day := channelMonitorStartOfDay(instant)

	require.Equal(t, "Asia/Shanghai", day.Location().String())
	require.Equal(t, time.Date(2026, 8, 4, 0, 0, 0, 0, channelMonitorShanghaiLocation), day)
	require.Equal(t, time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC), day.UTC())
}

func TestChannelMonitorAggregationWatermarkAdvancesByShanghaiDate(t *testing.T) {
	svc := &ChannelMonitorService{}
	watermark := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 8, 5, 0, 0, 0, 0, channelMonitorShanghaiLocation)

	start := svc.resolveAggregationStart(&watermark, today)
	require.Equal(t, time.Date(2026, 8, 4, 0, 0, 0, 0, channelMonitorShanghaiLocation), start)
}

func TestBalanceNotifyReminderKeyUsesShanghaiDate(t *testing.T) {
	instant := time.Date(2026, 8, 3, 16, 30, 0, 0, time.UTC)
	require.Equal(t, "2026-08-04", balanceNotifyReminderKey(instant))
}
