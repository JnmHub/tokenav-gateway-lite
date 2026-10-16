package service

import (
	"context"
	"log/slog"
	"strings"
)

const responseModelBillingCostEpsilon = 1e-12

// responseModelBillingDeclaration 是外部响应声明进入计费前的统一门禁。
// 图片、视频、搜索、音频等非 Token 价格不能用 Token 模型价覆盖；同一轮声明冲突时也必须回退。
func responseModelBillingDeclaration(source, responseModel string, conflict, mediaBilled bool) string {
	if source != BillingModelSourceResponse || conflict || mediaBilled {
		return ""
	}
	return strings.TrimSpace(responseModel)
}

func responseModelBillingAdoptable(baseline, response *CostBreakdown, baselineChannelPriced, responseChannelPriced bool) bool {
	if baseline == nil || response == nil || response.TotalCost > baseline.TotalCost+responseModelBillingCostEpsilon {
		return false
	}
	if response.TotalCost <= 0 && baseline.TotalCost > 0 {
		return false
	}
	return !baselineChannelPriced || responseChannelPriced
}

// 上游声明只允许降低或保持费用，不能抬高用户账单；实际采用时留下可审计日志。
func logResponseModelBillingApplied(component string, account *Account, requestID, baselineModel, responseModel string, baselineCost, responseCost *CostBreakdown) {
	baselineModel = strings.TrimSpace(baselineModel)
	responseModel = strings.TrimSpace(responseModel)
	if strings.EqualFold(baselineModel, responseModel) {
		return
	}
	attributes := []any{
		"component", component,
		"request_id", strings.TrimSpace(requestID),
		"baseline_model", baselineModel,
		"response_model", responseModel,
	}
	if baselineCost != nil && responseCost != nil {
		attributes = append(attributes, "baseline_cost", baselineCost.TotalCost, "billed_cost", responseCost.TotalCost)
	}
	if account != nil {
		attributes = append(attributes, "platform", account.Platform, "account_id", account.ID)
	}
	slog.Info("billing.response_model_applied", attributes...)
}

func (s *OpenAIGatewayService) hasIdentifiedOpenAIResponsePricing(ctx context.Context, model string, apiKey *APIKey) (identified bool, channelPriced bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return false, false
	}
	if s.resolveOpenAIChannelPricing(ctx, model, apiKey) != nil {
		return true, true
	}
	return s.billingService != nil && s.billingService.HasIdentifiedTokenPricing(model), false
}
