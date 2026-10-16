package service

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

const gatewayLiteUsageEnqueueTimeout = 10 * time.Second

func reportGatewayLiteUsageCorrection(ctx context.Context, usageLog *UsageLog) bool {
	event, ok := gatewayLiteUsageCorrectionEvent(ctx, usageLog)
	if !ok {
		return false
	}
	// 本地 usage log（用量日志）和账号统计可能已经用掉 worker 的大部分超时时间；
	// 最终计费事件进入 Redis Stream 是独立的持久化边界，不能复用即将取消的上下文。
	// WithoutCancel 保留调用链值但去掉原 deadline，再用固定上限避免异常时长期占住 worker。
	reportBase := context.Background()
	if ctx != nil {
		reportBase = context.WithoutCancel(ctx)
	}
	reportCtx, cancel := context.WithTimeout(reportBase, gatewayLiteUsageEnqueueTimeout)
	defer cancel()
	if err := gatewaylite.ReportUsageCorrection(reportCtx, event); err != nil {
		slog.Warn("gateway-lite usage correction report failed", "request_id", usageLog.RequestID, "error", err)
		return false
	}
	return true
}

func gatewayLiteUsageCorrectionEvent(ctx context.Context, usageLog *UsageLog) (gatewaylite.UsageEvent, bool) {
	if usageLog == nil {
		return gatewaylite.UsageEvent{}, false
	}
	if _, ok := gatewaylite.RequestMetaFromContext(ctx); !ok {
		return gatewaylite.UsageEvent{}, false
	}
	meta, _ := gatewaylite.RequestMetaFromContext(ctx)
	actualCents := int64(0)
	// Async work may have already frozen and reserved the integer-cent result.
	// In that case advancing the sub-cent carry before applying the override
	// would silently bill a later request for the same amount a second time.
	if meta.CommitAsyncReservation {
		// 原预留提交必须先校验身份/租约，再在同一 Lua 中推进零钱、提交及入队。
		// 此处不能调用 legacy 的有副作用换算；模糊重试只能复用原冻结结果。
		actualCents = 0
	} else if meta.Funding != nil {
		actualCents = 0
	} else if meta.ActualCentsOverride != nil {
		actualCents = *meta.ActualCentsOverride
	} else if meta.BillingSettlementMode == gatewaylite.BillingSettlementModeIdempotentV2 {
		// The Redis reporter freezes this fixed-point value and enqueues the
		// correction in one Lua transaction. Using usageLog.RequestID below is
		// essential for WebSocket sessions where each turn has its own resp_* ID.
		actualCents = 0
	} else {
		actualCents = gatewaylite.ActualCostToCentsForContext(ctx, usageLog.ActualCost)
	}
	settlementMode := meta.BillingSettlementMode
	if settlementMode == "" {
		settlementMode = gatewaylite.BillingSettlementModeLegacy
	}
	event := gatewaylite.UsageEvent{
		RequestID:                 usageLog.RequestID,
		UserID:                    meta.UserID,
		OrganizationID:            meta.OrganizationID,
		BillingSubjectType:        strings.TrimSpace(meta.BillingSubjectType),
		BillingSubjectID:          meta.BillingSubjectID,
		KeyID:                     meta.KeyID,
		LeaseID:                   meta.LeaseID,
		Region:                    meta.Region,
		GatewayID:                 meta.GatewayID,
		QuotaRequestID:            meta.RequestID,
		UpstreamID:                gatewayLiteUsageUpstreamID(usageLog.AccountID),
		Protocol:                  "openai_compatible",
		Model:                     usageLog.Model,
		RequestedModel:            strings.TrimSpace(usageLog.RequestedModel),
		ChannelMappedModel:        strings.TrimSpace(usageLog.ChannelMappedModel),
		UpstreamModel:             optionalStringValue(usageLog.UpstreamModel),
		TargetPlatform:            strings.TrimSpace(usageLog.TargetPlatform),
		RouteSource:               strings.TrimSpace(usageLog.CompositeRouteSource),
		ModelMappingChain:         optionalStringValue(usageLog.ModelMappingChain),
		Path:                      optionalStringValue(usageLog.InboundEndpoint),
		TokensIn:                  int64(usageLog.InputTokens),
		TokensOut:                 int64(usageLog.OutputTokens),
		CacheReadTokens:           int64(usageLog.CacheReadTokens),
		CacheWriteTokens:          int64(usageLog.CacheCreationTokens),
		RequestedReasoningEffort:  optionalStringValue(usageLog.RequestedReasoningEffort),
		ReasoningEffort:           optionalStringValue(usageLog.ReasoningEffort),
		ServiceTier:               optionalStringValue(usageLog.ServiceTier),
		SentServiceTier:           optionalStringValue(usageLog.SentServiceTier),
		ResponseServiceTier:       optionalStringValue(usageLog.ResponseServiceTier),
		RequestedServiceTier:      optionalStringValue(usageLog.RequestedServiceTier),
		PriorityPolicyMode:        optionalStringValue(usageLog.PriorityPolicyMode),
		PriorityDetectionSource:   optionalStringValue(usageLog.PriorityDetectionSource),
		PriorityPolicyAction:      optionalStringValue(usageLog.PriorityPolicyAction),
		PriorityPolicyRuleID:      optionalStringValue(usageLog.PriorityPolicyRuleID),
		PriorityPolicyFallback:    usageLog.PriorityPolicyFallback,
		LongContextBillingApplied: usageLog.LongContextBillingApplied,
		BillingMode:               optionalStringValue(usageLog.BillingMode),
		BillingTier:               optionalStringValue(usageLog.BillingTier),
		SettlementMode:            string(settlementMode),
		Final:                     meta.Reservation.FinalizesOnUsage(),
		ImageCount:                int64(usageLog.ImageCount),
		ImageSize:                 optionalStringValue(usageLog.ImageSize),
		ImageInputTokens:          int64(usageLog.ImageInputTokens),
		ImageInputCost:            usageLog.ImageInputCost,
		ImageInputPrice:           usageLog.ImageInputPrice,
		ImageInputPriceSource:     optionalStringValue(usageLog.ImageInputPriceSource),
		ImageOutputTokens:         int64(usageLog.ImageOutputTokens),
		ImageOutputCost:           usageLog.ImageOutputCost,
		VideoCount:                int64(usageLog.VideoCount),
		VideoResolution:           optionalStringValue(usageLog.VideoResolution),
		VideoDurationSeconds:      optionalInt64Value(usageLog.VideoDurationSeconds),
		ActualCents:               actualCents,
		FirstTokenMillis:          optionalIntMillis(usageLog.FirstTokenMs),
	}
	if meta.CommitAsyncReservation {
		micro, err := gatewaylite.AsyncReservationCostMicroCents(usageLog.ActualCost)
		if err != nil {
			return gatewaylite.UsageEvent{}, false
		}
		event.CommitAsyncReservation = true
		event.AsyncReservationMicroCents = micro
	}
	if usageLog.RatePolicyAudit != nil && usageLog.RatePolicyAudit.Valid() {
		policy := usageLog.RatePolicyAudit.Clone()
		event.RatePolicyAudit = &policy
	}
	if settlementMode == gatewaylite.BillingSettlementModeIdempotentV2 {
		// Async jobs may already have frozen the integer result, but the reporter
		// still needs the original fixed-point amount. If Redis was restored
		// between prepare and enqueue it can rebuild exactly the same decision;
		// otherwise it validates and reuses the existing frozen result.
		event.SettlementPending = true
		event.SettlementMicroCents = gatewaylite.ActualCostMicroCents(usageLog.ActualCost)
	}
	if auth, funded := gatewaylite.FundingFromContext(ctx); funded {
		// Main-site card allocation owns fractional cents. Never advance the old
		// per-key balance remainder or adjust its lease for this same request.
		gatewaylite.AttachFundingUsage(&event, auth, gatewaylite.ActualCostMicroCents(usageLog.ActualCost), 2, true)
	}
	if usageLog.RequestType == RequestTypeLive {
		event.Protocol = "openai_live"
		event.Method = "POST"
		event.Status = 200
		if usageLog.DurationMs != nil && *usageLog.DurationMs > 0 {
			event.LatencyMillis = int64(*usageLog.DurationMs)
		}
	}
	// 远程用量回传必须记录请求最终实际承接的分组。API Key 仍绑定入口 A，
	// 因此 usageLog.GroupID 在计费阶段可能暂时是 A；跨分组故障转移后，
	// RequestMeta 才是唯一可靠的 B（有效分组）与 A（原始分组）证据。
	// 没有上下文分组时再回退到 usageLog，兼容历史/内部调用方。
	if meta.GroupID > 0 {
		event.GatewayGroupID = meta.GroupID
		event.GatewayGroupName = strings.TrimSpace(meta.GroupName)
	} else if usageLog.GroupID != nil {
		event.GatewayGroupID = *usageLog.GroupID
	}
	if meta.OriginalGroupID > 0 {
		event.RequestedGatewayGroupID = meta.OriginalGroupID
		event.RequestedGatewayGroupName = strings.TrimSpace(meta.OriginalGroupName)
	} else if usageLog.OriginalGroupID != nil {
		event.RequestedGatewayGroupID = *usageLog.OriginalGroupID
	}
	if finiteNonNegative(usageLog.RateMultiplier) && usageLog.RateMultiplier > 0 {
		event.RateMultiplier = usageLog.RateMultiplier
	}
	if usageLog.AccountRateMultiplier != nil && finiteNonNegative(*usageLog.AccountRateMultiplier) {
		accountRateMultiplier := *usageLog.AccountRateMultiplier
		event.AccountRateMultiplier = &accountRateMultiplier
	}
	if usageLog.AccountStatsCost != nil && finiteNonNegative(*usageLog.AccountStatsCost) {
		// Preserve the raw procurement price. The control plane applies the
		// account multiplier only when presenting account cost, never to the
		// customer-facing actual_cents amount.
		accountStatsCost := *usageLog.AccountStatsCost
		event.AccountStatsCost = &accountStatsCost
	}
	localGroupID := event.GatewayGroupID
	if localGroupID == 0 {
		if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
			localGroupID = meta.GroupID
		}
	}
	meta, hasMeta := gatewaylite.RequestMetaFromContext(ctx)
	if hasMeta && (meta.PolicyRevision > 0 || meta.PolicyChecksum != "") {
		event.PolicyRevision = meta.PolicyRevision
		event.PolicyChecksum = meta.PolicyChecksum
		event.PriceVersion = meta.PriceVersion
	} else if stamp := gatewaylite.EffectivePolicyAuditForGroup(localGroupID); stamp.Revision > 0 || stamp.Checksum != "" {
		event.PolicyRevision = stamp.Revision
		event.PolicyChecksum = stamp.Checksum
		event.PriceVersion = stamp.PriceVersion
	}
	return event, true
}

func gatewayLiteUsageUpstreamID(accountID int64) string {
	if accountID <= 0 {
		return ""
	}
	return strconv.FormatInt(accountID, 10)
}

func optionalIntMillis(value *int) int64 {
	if value == nil || *value <= 0 {
		return 0
	}
	return int64(*value)
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalInt64Value(value *int) int64 {
	if value == nil || *value <= 0 {
		return 0
	}
	return int64(*value)
}
