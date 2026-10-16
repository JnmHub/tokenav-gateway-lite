package service

import (
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestPaymentDashboardStatsUseShanghaiBusinessDay(t *testing.T) {
	// UTC 8 月 2 日 16:30 在上海已经是 8 月 3 日；每日趋势必须归入 8 月 3 日。
	paidAt := time.Date(2026, 8, 2, 16, 30, 0, 0, time.UTC)
	orders := []*dbent.PaymentOrder{{PaidAt: &paidAt, PayAmount: 12.5}}
	since := time.Date(2026, 8, 1, 12, 0, 0, 0, paymentStatsShanghaiLocation)

	series := buildDailySeries(orders, since, 2)
	require.Len(t, series, 2)
	require.Equal(t, "2026-08-03", series[1].Date)
	// 金额按币种聚合（CurrencyAmounts）：订单没带支付渠道快照时归入默认币种 CNY。
	require.Equal(t, CurrencyAmounts{"CNY": 12.5}, series[1].Amount)
	require.Equal(t, 1, series[1].Count)

	stats := &DashboardStats{}
	todayStart := time.Date(2026, 8, 3, 0, 0, 0, 0, paymentStatsShanghaiLocation)
	computeBasicStats(stats, orders, todayStart)
	require.Equal(t, CurrencyAmounts{"CNY": 12.5}, stats.TodayAmount)
	require.Equal(t, 1, stats.TodayCount)
}
