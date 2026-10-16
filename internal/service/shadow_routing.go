package service

// parentHealthyForShadow 只检查共享凭据/传输的健康度。Spark 有独立配额，
// 所以母账号 global 429 不会把影子一起停掉。
func parentHealthyForShadow(account *Account, lookup func(int64) *Account) bool {
	if account == nil || !account.IsShadow() {
		return true
	}
	parent := lookup(*account.ParentAccountID)
	return parent != nil && parent.IsOpenAIOAuth() && !parent.IsShadow() && parent.IsCredentialUsableForShadow()
}

func sparkModelVariants() []string {
	out := make([]string, 0, 5)
	for alias, target := range codexModelMap {
		if target == "gpt-5.3-codex-spark" {
			out = append(out, alias)
		}
	}
	return out
}

func defaultSparkShadowModelMapping() map[string]any {
	out := make(map[string]any)
	for _, model := range sparkModelVariants() {
		out[model] = model
	}
	return out
}
