package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_gateway_forward.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。


// codexIdentityOverrideUA 返回账号级显式配置的出站 User-Agent，供强制统一身份时作为覆写来源。
// ForceCodexCLI 语义是「强制使用 Codex CLI 身份」，等价于使用网关规范身份，故返回空串；
// 该优先级与历史行为一致（ForceCodexCLI 在账号自定义 UA 之后生效）。
func (s *OpenAIGatewayService) codexIdentityOverrideUA(account *Account) string {
	if s != nil && s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		return ""
	}
	return account.GetOpenAIUserAgent()
}

