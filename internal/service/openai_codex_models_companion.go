package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_codex_models_companion.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。

import (
	"encoding/json"
)


type UpstreamModelMetadata struct {
	ID                       string                     `json:"id"`
	DisplayName              string                     `json:"display_name,omitempty"`
	Description              string                     `json:"description,omitempty"`
	Reasoning                *bool                      `json:"reasoning,omitempty"`
	DefaultReasoningLevel    string                     `json:"default_reasoning_level,omitempty"`
	SupportedReasoningLevels []string                   `json:"supported_reasoning_levels,omitempty"`
	InputModalities          []string                   `json:"input_modalities,omitempty"`
	ContextWindow            int64                      `json:"context_window,omitempty"`
	MaxContextWindow         int64                      `json:"max_context_window,omitempty"`
	MaxOutputTokens          int64                      `json:"max_output_tokens,omitempty"`
	CodexToolCapabilities    map[string]json.RawMessage `json:"codex_tool_capabilities,omitempty"`
}

