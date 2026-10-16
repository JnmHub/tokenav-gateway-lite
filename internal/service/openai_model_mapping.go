package service

import "strings"

// OpenAI OAuth（Codex 上游）不可能服务这些明确属于其他厂商的模型。
// 使用保守黑名单而不是 OpenAI 白名单，避免误伤稍后才由渠道映射改写的自定义别名。
var openAIOAuthForeignModelPrefixes = []string{
	"deepseek",
	"glm-",
	"kimi-",
	"moonshot",
	"qwen",
	"qwq-",
	"minimax",
	"gemini-",
	"gemma-",
	"grok-",
	"doubao-",
	"hunyuan-",
	"llama",
	"meta-llama",
	"mistral",
	"mixtral",
	"baichuan",
	"ernie-",
	"step-",
	"seed-",
	"yi-",
}

func isOpenAIOAuthServableModel(requestedModel string) bool {
	model := strings.ToLower(lastOpenAIModelSegment(requestedModel))
	if model == "" {
		return true
	}
	// Kimi Code 官方 bare ID 没有厂商前缀。精确排除，避免空映射 OpenAI OAuth
	// 账号吸收请求；显式 model_mapping 和 passthrough 会在上层优先生效。
	if model == "k3" || model == "k3-256k" {
		return false
	}
	for _, prefix := range openAIOAuthForeignModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return false
		}
	}
	return true
}

// resolveOpenAIForwardModel keeps tests, runtime health probes and legacy
// callers that have no request group context on the account-wide mapping.
// Real forwarding paths must use ResolveOpenAIForwardModelForRequest so an
// authenticated account/group mapping cannot be lost.
func resolveOpenAIForwardModel(account *Account, requestedModel, defaultMappedModel string) string {
	return ResolveOpenAIForwardModelForRequest(nil, nil, account, requestedModel, defaultMappedModel)
}

// resolveOpenAICompactForwardModel determines the compact-only upstream model
// for /responses/compact requests. It never affects normal /responses traffic.
// When no compact-specific mapping matches, the input model is returned as-is.
func resolveOpenAICompactForwardModel(account *Account, model string) string {
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" || account == nil {
		return trimmedModel
	}

	mappedModel, matched := account.ResolveCompactMappedModel(trimmedModel)
	if !matched {
		return trimmedModel
	}
	if trimmedMapped := strings.TrimSpace(mappedModel); trimmedMapped != "" {
		return trimmedMapped
	}
	return trimmedModel
}
