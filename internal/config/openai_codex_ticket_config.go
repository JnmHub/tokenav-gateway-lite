package config

// OpenAICodexTicketConfig 配置 OpenAI Codex 账号 292 turn-state 门票的
// 采集与注入行为。[v0.2.5 新增] 官方 v0.2.5 无该类型，字段集合按
// internal/service/openai_codex_ticket.go 与 handler/service 测试用例反推补齐。
type OpenAICodexTicketConfig struct {
	// Enabled 总开关；false 时完全不采票、不注入。
	Enabled bool `mapstructure:"enabled"`
	// FailClosed 门票缺失时拒绝请求（true）还是跳过注入放行（false，默认兜底）。
	FailClosed bool `mapstructure:"fail_closed"`
	// Models 需要门票门控的模型列表；空则使用内置默认模型集合。
	Models []string `mapstructure:"models"`
	// TargetLength 合格门票的精确长度（如 292）。
	TargetLength int `mapstructure:"target_length"`
	// TTLSeconds 门票有效时长（秒）。
	TTLSeconds int `mapstructure:"ttl_seconds"`
	// RefreshBeforeSeconds 到期前提前刷新窗口（秒）。[v0.2.5 新增字段]
	RefreshBeforeSeconds int `mapstructure:"refresh_before_seconds"`
	// HarvestProxyURL 打票请求走的代理地址（http/socks5h）。
	HarvestProxyURL string `mapstructure:"harvest_proxy_url"`
	// HarvestProbeIntervalSeconds 后台采集循环间隔（秒）。
	HarvestProbeIntervalSeconds int `mapstructure:"harvest_probe_interval_seconds"`
	// HarvestAttemptTimeoutSeconds 单次打票请求超时（秒）。
	HarvestAttemptTimeoutSeconds int `mapstructure:"harvest_attempt_timeout_seconds"`
}
