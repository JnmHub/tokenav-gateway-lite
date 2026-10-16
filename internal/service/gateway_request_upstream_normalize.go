package service

// [tokenav 定制] 本文件自官方 v0.2.5 引入（官方 gateway_request.go 中的新增能力）。
//
// 引入原因：官方 v0.2.5 的 gateway_request.go 相比我们仓库的版本多出 5 个函数，
// 这些函数在我们仓库中完全不存在（无重复定义风险），且其依赖
// （anthropicBetaTokensContains / stripEmptyTextBlocksFromSlice /
// claude.BetaMidConversationOutputConfig）在我们仓库中均已具备。
//
// 为什么不直接覆盖 gateway_request.go：我们的 gateway_request.go 带有本地定制
// （如 normalizeClaudeAuditEffort），整体覆盖会丢失定制。因此以「新文件 + 独立函数」
// 形式引入官方新增能力，避免与我们既有实现冲突。
//
// 两项能力：
//  1. GLM-5.3 的 Anthropic thinking 归一化——把客户端各种 effort 写法映射成上游
//     认识的 low/high/max 三档；
//  2. mid-conversation output_config 剥离——当 anthropic-beta 头未声明该能力时，
//     消息级的 output_config 会让上游 400，需要按消息粒度剥掉（空正文的 system
//     控制消息整条删除）。

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// stripAnthropicMessageOutputConfigUnlessBeta 在 anthropic-beta header 未声明
// mid-conversation-output-config 能力时，剥离 messages[] 各条消息上的 output_config 字段。
//
// 注意：只在 header 缺失该 beta token 时剥离——上游支持该能力时保留字段是正确行为，
// 盲目剥离会静默改变请求语义。返回的 bool 表示是否真的改动了 body。
func stripAnthropicMessageOutputConfigUnlessBeta(body []byte, anthropicBetaHeader string) ([]byte, bool) {
	if anthropicBetaTokensContains(anthropicBetaHeader, claude.BetaMidConversationOutputConfig) {
		return body, false
	}
	// 快速路径：body 中不含 output_config 字面量时无需解析。
	if !bytes.Contains(body, []byte("output_config")) {
		return body, false
	}
	msgsRes := gjson.GetBytes(body, "messages")
	if !msgsRes.Exists() || !msgsRes.IsArray() {
		return body, false
	}

	hasMessageOutputConfig := false
	for _, msg := range msgsRes.Array() {
		if msg.Get("output_config").Exists() {
			hasMessageOutputConfig = true
			break
		}
	}
	if !hasMessageOutputConfig {
		return body, false
	}

	var messages []json.RawMessage
	if err := json.Unmarshal([]byte(msgsRes.Raw), &messages); err != nil {
		// gjson 的 IsArray 只做形态判断、不保证 JSON 完整合法；此分支保守返回原 body。
		return body, false
	}

	changed := false
	rebuilt := make([]json.RawMessage, 0, len(messages))
	for _, msg := range messages {
		if !gjson.GetBytes(msg, "output_config").Exists() {
			rebuilt = append(rebuilt, msg)
			continue
		}
		changed = true

		// 空正文的 system 控制消息整条删除；其余消息只剥字段。
		if gjson.GetBytes(msg, "role").String() == "system" &&
			!anthropicMessageContentHasBody(gjson.GetBytes(msg, "content")) {
			continue
		}
		stripped, err := sjson.DeleteBytes(msg, "output_config")
		if err != nil {
			// 不应发生：字段存在且 msg 是合法 JSON。保守整条保留，不产出半成品。
			rebuilt = append(rebuilt, msg)
			continue
		}
		rebuilt = append(rebuilt, json.RawMessage(stripped))
	}
	if !changed {
		return body, false
	}

	rebuiltBytes, err := json.Marshal(rebuilt)
	if err != nil {
		return body, false
	}
	out, err := sjson.SetRawBytes(body, "messages", rebuiltBytes)
	if err != nil {
		return body, false
	}
	return out, true
}

// anthropicMessageContentHasBody 判断一条消息的 content 是否含实际正文。
// 用于识别「只带 output_config、没有正文」的 system 控制消息。
func anthropicMessageContentHasBody(content gjson.Result) bool {
	switch {
	case !content.Exists():
		return false
	case content.Type == gjson.String:
		return content.String() != ""
	case content.IsArray():
		var blocks []any
		if err := json.Unmarshal([]byte(content.Raw), &blocks); err != nil {
			return true // 无法解析时保守视为有正文
		}
		cleaned, _ := stripEmptyTextBlocksFromSlice(blocks)
		return len(cleaned) > 0
	default:
		// null 视为无正文；object / number / bool 等非标准形态保守保留。
		return content.Type != gjson.Null
	}
}

// normalizeEffortToken 归一化 effort 写法：忽略大小写，并去掉 -、_ 和空格，
// 这样 "X-High" / "x_high" / "xhigh" 都能落到同一档。
func normalizeEffortToken(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
}

// isGLM53Model 判断是否为需要 thinking 归一化的 GLM-5.3 模型。
func isGLM53Model(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "glm-5.3")
}

// NormalizeGLM53AnthropicThinking 把客户端显式声明的 thinking effort 映射到
// GLM-5.3 上游认识的档位（low / high / max），并统一写成
// thinking.type=enabled + output_config.effort=<档位>。
//
// 取值优先级：output_config.effort 优先，缺失时回退 thinking.type。
// 无法识别的写法直接返回原 body（不猜），避免把语义改错。
func NormalizeGLM53AnthropicThinking(body []byte, mappedModel string) ([]byte, bool) {
	if !isGLM53Model(mappedModel) {
		return body, false
	}

	raw := gjson.GetBytes(body, "output_config.effort").String()
	if strings.TrimSpace(raw) == "" {
		raw = gjson.GetBytes(body, "thinking.type").String()
	}

	var effort string
	switch normalizeEffortToken(raw) {
	case "disabled", "off", "none", "minimal", "low":
		effort = "low"
	case "enabled", "adaptive", "medium", "high":
		effort = "high"
	case "xhigh", "max", "ultra":
		effort = "max"
	default:
		return body, false
	}

	modified, err := sjson.SetBytes(body, "thinking.type", "enabled")
	if err != nil {
		return body, false
	}
	modified, err = sjson.SetBytes(modified, "output_config.effort", effort)
	if err != nil {
		return body, false
	}
	return modified, true
}
