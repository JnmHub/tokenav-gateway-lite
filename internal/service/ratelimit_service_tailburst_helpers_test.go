//go:build unit

// codex tail burst 403 测试的缺失 helper：原文件引用的
// tailBurstTestUsageRepo / newTailBurstAccount 未随测试文件一起移植，此处补齐。
package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// tailBurstTestUsageRepo 记录 GetAccountStatsAggregated 的调用次数，
// 并返回预置的全局用量统计（用于断言结算写入的 requests/tokens）。
type tailBurstTestUsageRepo struct {
	UsageLogRepository
	stats *usagestats.UsageStats
	calls int
}

func (r *tailBurstTestUsageRepo) GetAccountStatsAggregated(context.Context, int64, time.Time, time.Time) (*usagestats.UsageStats, error) {
	r.calls++
	return r.stats, nil
}

// newTailBurstAccount 构造一个 OpenAI OAuth 账号（403 结算路径的判定对象）。
func newTailBurstAccount() *Account {
	return &Account{
		ID:          421,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{},
		Extra:       map[string]any{},
	}
}
