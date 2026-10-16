package service

// [tokenav 定制] 本文件自官方 v0.2.5 引入（官方把 gateway_service.go 拆分出的新文件）。
// 引入判断：这 4 个符号在我们仓库中原本不存在，且依赖（ctxkey.Group /
// IsGroupContextValid / timezone.Now）在我们仓库中均已具备，属于纯新增能力，
// 不会与我们的 gateway_service.go（11963 行 / 322 个函数）产生重复定义。
//
// 作用：为「共享网关 token 请求」打标记，并在请求生命周期内冻结计费时点；
// 调度过程中 fallback / composite 路由可能覆盖 ctxkey.Group，此文件保证计费
// 分组始终取自认证时刻的父分组，与最终 RecordUsage 的归属保持一致。

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

type gatewayTokenRequestPricingAtCtxKey struct{}
type gatewayTokenRequestBillingGroupCtxKey struct{}

// WithGatewayTokenRequestPricing marks a shared-gateway request as token billed
// and freezes the downstream pricing instant for its whole lifetime. Media and
// metadata-only handlers deliberately do not call this helper.
func WithGatewayTokenRequestPricing(ctx context.Context) (context.Context, time.Time) {
	if ctx == nil {
		ctx = context.Background()
	}
	pricingAt := timezone.Now()
	ctx = context.WithValue(ctx, gatewayTokenRequestPricingAtCtxKey{}, pricingAt)
	// 调度过程中可能因 fallback/composite 路由覆盖 ctxkey.Group；计费 D 仍必须
	// 使用认证时刻的父分组，和最终 RecordUsage 的计费归属保持一致。
	if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && IsGroupContextValid(group) {
		ctx = context.WithValue(ctx, gatewayTokenRequestBillingGroupCtxKey{}, group)
	}
	return ctx, pricingAt
}

func gatewayTokenRequestPricingAtFromContext(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	pricingAt, ok := ctx.Value(gatewayTokenRequestPricingAtCtxKey{}).(time.Time)
	return pricingAt, ok && !pricingAt.IsZero()
}

// GatewayTokenRequestPricingAtFromContext exposes the frozen instant to
// handlers before they detach asynchronous usage-recording work.
func GatewayTokenRequestPricingAtFromContext(ctx context.Context) time.Time {
	pricingAt, _ := gatewayTokenRequestPricingAtFromContext(ctx)
	return pricingAt
}

func gatewayTokenRequestBillingGroupFromContext(ctx context.Context) *Group {
	if ctx == nil {
		return nil
	}
	group, _ := ctx.Value(gatewayTokenRequestBillingGroupCtxKey{}).(*Group)
	if IsGroupContextValid(group) {
		return group
	}
	return nil
}
