package service

import (
	"context"
	"fmt"
	"strings"
)

// SparkShadowRepository 是影子账号专用的可选仓储能力，不扩大通用
// AccountRepository，避免所有测试桩被迫实现无关方法。
type SparkShadowRepository interface {
	ListShadowsByParent(ctx context.Context, parentID int64) ([]*Account, error)
}

func listShadowsByParent(ctx context.Context, repo AccountRepository, parentID int64) ([]*Account, error) {
	shadowRepo, ok := repo.(SparkShadowRepository)
	if !ok {
		return nil, fmt.Errorf("spark shadow repository is not configured")
	}
	return shadowRepo.ListShadowsByParent(ctx, parentID)
}

// resolveCredentialAccount 把影子账号解析为真正持有 OAuth 凭据的母账号。
// 只解一层，影子套影子或母账号类型被破坏时 fail-closed（拒绝请求）。
func resolveCredentialAccount(ctx context.Context, repo AccountRepository, account *Account) (*Account, error) {
	if account == nil || !account.IsShadow() {
		return account, nil
	}
	parent, err := repo.GetByID(ctx, *account.ParentAccountID)
	if err != nil {
		return nil, fmt.Errorf("resolve spark shadow parent %d: %w", *account.ParentAccountID, err)
	}
	if parent == nil {
		return nil, fmt.Errorf("spark shadow parent %d not found", *account.ParentAccountID)
	}
	if parent.IsShadow() {
		return nil, fmt.Errorf("spark shadow parent %d is itself a shadow", parent.ID)
	}
	if !parent.IsOpenAIOAuth() {
		return nil, fmt.Errorf("spark shadow parent %d is not OpenAI OAuth", parent.ID)
	}
	return parent, nil
}

// resolveShadowRuntimeAccount 保留影子 ID（用于并发、日志和计费），
// 仅把母账号的实时代理关系复制到这次请求对象中。
func resolveShadowRuntimeAccount(ctx context.Context, repo AccountRepository, account *Account) (*Account, *Account, error) {
	parent, err := resolveCredentialAccount(ctx, repo, account)
	if err != nil || account == nil || !account.IsShadow() {
		return account, parent, err
	}
	runtime := *account
	runtime.ProxyID = parent.ProxyID
	runtime.Proxy = parent.Proxy
	runtime.credentialParent = parent
	return &runtime, parent, nil
}

func isAllowedSparkShadowCredentialsUpdate(credentials map[string]any) bool {
	for key := range credentials {
		if key != "model_mapping" {
			return false
		}
	}
	mapping, ok := sparkShadowModelMapping(credentials["model_mapping"])
	return ok && len(mapping) > 0
}

func sanitizeSparkShadowCredentials(credentials map[string]any) map[string]any {
	if credentials != nil {
		if mapping, ok := sparkShadowModelMapping(credentials["model_mapping"]); ok && len(mapping) > 0 {
			return map[string]any{"model_mapping": mapping}
		}
	}
	// 影子没有显式 mapping 时，Account 会回退到 OpenAI 默认模型集合，
	// 这会让非 Spark 请求误用 Spark 独立额度，所以必须恢复安全默认值。
	return map[string]any{"model_mapping": defaultSparkShadowModelMapping()}
}

func sparkShadowModelMapping(raw any) (map[string]any, bool) {
	result := map[string]any{}
	appendMapping := func(from, to string) bool {
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		if from == "" || to == "" || normalizeCodexModel(from) != "gpt-5.3-codex-spark" || normalizeCodexModel(to) != "gpt-5.3-codex-spark" {
			return false
		}
		result[from] = to
		return true
	}
	switch mapping := raw.(type) {
	case map[string]any:
		for from, value := range mapping {
			to, ok := value.(string)
			if !ok || !appendMapping(from, to) {
				return nil, false
			}
		}
	case map[string]string:
		for from, to := range mapping {
			if !appendMapping(from, to) {
				return nil, false
			}
		}
	default:
		return nil, false
	}
	return result, true
}
