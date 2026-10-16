package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const maxReasoningEffortMappings = 64

var openAIReasoningEffortValues = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

type openAIReasoningEffortPolicyContextKey struct{}

type openAIReasoningEffortPolicy struct {
	maxEffort string
	mappings  []ReasoningEffortMapping
}

// NormalizeMaxReasoningEffort 规范化 OpenAI 推理档位。空字符串表示分组不设上限。
func NormalizeMaxReasoningEffort(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	switch value {
	case "":
		return ""
	case "minimal", "low", "medium", "high", "max":
		return value
	case "xhigh", "extrahigh":
		return "xhigh"
	default:
		return ""
	}
}

func reasoningEffortValuesForPlatform(platform string) []string {
	if platform != PlatformOpenAI && platform != PlatformComposite {
		return nil
	}
	return openAIReasoningEffortValues
}

func normalizeMaxReasoningEffortForPlatform(platform, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	allowed := reasoningEffortValuesForPlatform(platform)
	if len(allowed) == 0 {
		return "", fmt.Errorf("reasoning effort policy is only supported for platforms %q and %q", PlatformOpenAI, PlatformComposite)
	}
	value := NormalizeMaxReasoningEffort(raw)
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("reasoning effort %q is not supported; allowed values: %s", raw, strings.Join(allowed, ", "))
}

// WithOpenAIReasoningEffortPolicy 在组合分组已经解析到 OpenAI 后绑定本次请求的
// 策略副本。复制映射切片，避免重试或异步转发观察到后台随后发生的配置修改。
func WithOpenAIReasoningEffortPolicy(ctx context.Context, maxEffort string, mappings []ReasoningEffortMapping) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIReasoningEffortPolicyContextKey{}, openAIReasoningEffortPolicy{
		maxEffort: maxEffort,
		mappings:  append([]ReasoningEffortMapping(nil), mappings...),
	})
}

// ApplyOpenAIReasoningEffortPolicyFromContext 只应用处理器明确绑定的策略；没有
// 绑定时按字节返回原请求，保证客户端省略 effort 后仍由上游决定默认档位。
func ApplyOpenAIReasoningEffortPolicyFromContext(ctx context.Context, body []byte) ([]byte, bool) {
	if ctx == nil {
		return body, false
	}
	policy, ok := ctx.Value(openAIReasoningEffortPolicyContextKey{}).(openAIReasoningEffortPolicy)
	if !ok {
		return body, false
	}
	return ApplyOpenAIReasoningEffortPolicy(body, policy.maxEffort, policy.mappings)
}

func reasoningEffortRank(raw string) (int, bool) {
	switch NormalizeMaxReasoningEffort(raw) {
	case "minimal":
		return 1, true
	case "low":
		return 2, true
	case "medium":
		return 3, true
	case "high":
		return 4, true
	case "xhigh":
		return 5, true
	case "max":
		return 6, true
	default:
		return 0, false
	}
}

// NormalizeReasoningEffortMappings 校验并规范化分组精确映射。固定枚举与数量上限
// 避免任意字符串进入每个请求的热路径，也让前端可以完全使用点击选择。
func NormalizeReasoningEffortMappings(platform string, raw []ReasoningEffortMapping) ([]ReasoningEffortMapping, error) {
	if len(raw) > maxReasoningEffortMappings {
		return nil, fmt.Errorf("reasoning effort mappings cannot exceed %d entries", maxReasoningEffortMappings)
	}
	normalized := make([]ReasoningEffortMapping, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, mapping := range raw {
		from, fromErr := normalizeMaxReasoningEffortForPlatform(platform, mapping.From)
		to, toErr := normalizeMaxReasoningEffortForPlatform(platform, mapping.To)
		if fromErr != nil || toErr != nil || from == "" || to == "" {
			return nil, fmt.Errorf("reasoning effort mapping %d contains an empty or unsupported value", i+1)
		}
		if _, exists := seen[from]; exists {
			return nil, fmt.Errorf("duplicate reasoning effort mapping source %q", from)
		}
		seen[from] = struct{}{}
		normalized = append(normalized, ReasoningEffortMapping{From: from, To: to})
	}
	return normalized, nil
}

func sanitizeGroupReasoningEffortPolicy(group *Group) {
	if group == nil {
		return
	}
	maxEffort, maxErr := normalizeMaxReasoningEffortForPlatform(group.Platform, group.MaxReasoningEffort)
	mappings, mappingsErr := NormalizeReasoningEffortMappings(group.Platform, group.ReasoningEffortMappings)
	if maxErr != nil {
		maxEffort = ""
	}
	if mappingsErr != nil {
		mappings = []ReasoningEffortMapping{}
	}
	group.MaxReasoningEffort = maxEffort
	group.ReasoningEffortMappings = mappings
}

// ApplyOpenAIReasoningEffortPolicy 先应用一次精确映射，再把已知档位限制到分组上限。
// 客户端没有显式提交时保持缺省；未知扩展值不会被上限错误改写。
func ApplyOpenAIReasoningEffortPolicy(body []byte, maxEffort string, mappings []ReasoningEffortMapping) ([]byte, bool) {
	maxRank, hasMax := reasoningEffortRank(maxEffort)
	if len(body) == 0 || (!hasMax && len(mappings) == 0) {
		return body, false
	}

	result := body
	changed := false
	for _, path := range []string{"reasoning.effort", "reasoning_effort"} {
		field := gjson.GetBytes(result, path)
		if !field.Exists() || field.Type != gjson.String {
			continue
		}
		original := strings.TrimSpace(field.String())
		if original == "" {
			continue
		}
		effective := original
		canonical := NormalizeMaxReasoningEffort(original)
		for _, mapping := range mappings {
			if canonical != "" && canonical == NormalizeMaxReasoningEffort(mapping.From) {
				effective = NormalizeMaxReasoningEffort(mapping.To)
				break
			}
		}
		if rank, recognized := reasoningEffortRank(effective); recognized {
			effective = NormalizeMaxReasoningEffort(effective)
			if hasMax && rank > maxRank {
				effective = NormalizeMaxReasoningEffort(maxEffort)
			}
		}
		if effective == original {
			continue
		}
		updated, err := sjson.SetBytes(result, path, effective)
		if err != nil {
			continue
		}
		result = updated
		changed = true
	}
	return result, changed
}
