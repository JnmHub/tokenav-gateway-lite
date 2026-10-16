package domain

const (
	ReasoningEffortMatchExact  = "exact"
	ReasoningEffortMatchPrefix = "prefix"
	ReasoningEffortMatchSuffix = "suffix"
)

// ReasoningEffortMapping rewrites one explicit OpenAI/Codex reasoning effort
// value to another before the group ceiling is applied. To may also be "deny"
// to reject the request when the matching source value is present.
//
// Model and MatchType optionally scope the rewrite to a request model:
// exact matches the full model id, prefix/suffix match a model-id affix.
// Empty MatchType and empty Model mean the mapping applies to every model.
//
// [tokenav 定制] 保留本地实现：本地语义为「分组推理上限生效前，把客户端显式提交的
// OpenAI/Codex 推理档位精确改写为另一个档位」，与官方新增的 MatchType/Model 作用域
// 并不冲突，因此以官方字段为基础，同时保留本地中文语义说明。
type ReasoningEffortMapping struct {
	From      string `json:"from"`
	To        string `json:"to"`
	MatchType string `json:"match_type,omitempty"`
	Model     string `json:"model,omitempty"`
}
