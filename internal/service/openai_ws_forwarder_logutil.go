package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_ws_forwarder_logutil.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。

import (
	"bytes"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)


func openAIWSMessageShouldParseUsage(eventType string, message []byte) bool {
	return openAIWSEventShouldParseUsage(eventType) && bytes.Contains(message, []byte(`"usage"`))
}

func logOpenAIWSModeWarn(format string, args ...any) {
	logger.LegacyPrintf("service.openai_gateway", "[warn] [OpenAI WS Mode][openai_ws_mode=true] "+format, args...)
}

