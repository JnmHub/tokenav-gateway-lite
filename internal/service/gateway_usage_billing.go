// gateway_usage_billing 中的扣费后余额缓存同步。
// 自官方 0.2.7 backend/internal/service/gateway_usage_billing.go 移植的独立编排函数：
// 扣费结果落地后，按新余额是否仍满足放行阈值，选择"失效余额缓存（下次回源）"
// 或"排队继续扣减"，避免耗尽账户继续命中旧的余额缓存。
// 目前本地扣费主流程尚未接线该函数（行为无变化）；接线时在扣费事务成功后调用即可。
package service

import (
	"context"
	"log/slog"
)

func syncBalanceCacheAfterDeduction(ctx context.Context, p *postUsageBillingParams, deps *billingDeps, result *UsageBillingApplyResult) {
	if p == nil || p.Cost == nil || p.User == nil || deps == nil || deps.billingCacheService == nil {
		return
	}
	if result != nil && result.NewBalance != nil && deps.billingCacheService.balanceBelowEligibilityThreshold(*result.NewBalance) {
		if err := deps.billingCacheService.InvalidateUserBalance(ctx, p.User.ID); err != nil {
			slog.Warn("invalidate balance cache after exhausted deduction failed",
				"user_id", p.User.ID,
				"new_balance", *result.NewBalance,
				"balance_overdrafted", result.BalanceOverdrafted,
				"error", err,
			)
		}
		return
	}
	deps.billingCacheService.QueueDeductBalance(p.User.ID, p.Cost.ActualCost)
}
