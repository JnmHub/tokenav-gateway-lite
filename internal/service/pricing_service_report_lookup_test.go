package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 取价入口契约：计费路径允许「未知 gpt-* → 默认测试模型（gpt-5.4）」兜底；
// 价格上报路径只认目录/官方快照/已知别名能支撑的价格，拿不到就返回 nil。
//
// 背景（生产实测）：上游账号的 credentials 里带着目录中没有的模型名（gpt-reserve），
// 网关把「账号实际公开的模型清单」逐条取价上报给主站做价格镜像。走 GetModelPricing 时
// 这些名字会被静默按 gpt-5.4 价格上报（管理端因此显示一个并不存在的价格），并且
// 每次上报都打一条 pricing fallback 告警——6 小时实测 1480 条，把真正的缺价告警淹掉。
func TestPricingReportLookupSkipsDefaultModelGuess(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(body)
	require.NoError(t, err)
	svc.pricingData = pricingData

	// 目录里确实有 gpt-5.4，因此「默认测试模型兜底」有据可依（计费路径行为不变）。
	require.NotNil(t, svc.pricingData["gpt-5.4"])

	// 计费路径：未知 gpt-* 仍然兜底成 gpt-5.4 的价格（保持既有语义与告警信号）。
	fallback := svc.GetModelPricing("gpt-reserve")
	require.NotNil(t, fallback, "计费路径必须继续按默认模型兜底")
	require.Equal(t, svc.pricingData["gpt-5.4"].InputCostPerToken, fallback.InputCostPerToken)

	// 上报路径：同一个未知名字不得凭空给出价格。
	require.Nil(t, svc.GetModelPricingForReport("gpt-reserve"),
		"上报路径不应把未知模型套成 gpt-5.4 的价格")

	// 上报路径对真实存在的型号（含官方静态兜底与别名）行为不变。
	require.NotNil(t, svc.GetModelPricingForReport("gpt-5.4"))
	require.NotNil(t, svc.GetModelPricingForReport("gpt-5.4-mini"))
}
