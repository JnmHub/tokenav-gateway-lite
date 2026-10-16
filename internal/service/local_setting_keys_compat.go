package service

// [tokenav 定制] 升级前本地定制 setting key，从备份 domain_constants.go / 结算设置恢复。
const (
	// SettingKeyGatewayLiteIdempotentSettlementEnabled gateway_lite 幂等结算开关。
	SettingKeyGatewayLiteIdempotentSettlementEnabled = "gateway_lite_idempotent_settlement_enabled"

	// SettingKeyCodexClientAdmissionSettings 全局 Codex 客户端准入配置（JSON）。
	SettingKeyCodexClientAdmissionSettings = "codex_client_admission_settings"

	// SettingKeyCompositeGroupsEnabled 组合分组开关。
	SettingKeyCompositeGroupsEnabled = "composite_groups_enabled"

	// SettingKeyGrokVideoTransformsEnabled Grok 视频编辑/扩展（transforms）节点级热开关。
	// 存数据库 settings 表，保存即发布内存快照：路由常驻注册、handler 内按快照判断，
	// 管理员改开关不需要重启进程；未保存过时沿用 config.gateway.grok_video_transforms_enabled。
	SettingKeyGrokVideoTransformsEnabled = "grok_video_transforms_enabled"

	// SettingKeyOpsCodexTailBurst Codex 尾部爆发运行时开关。
	SettingKeyOpsCodexTailBurst = "ops_codex_tail_burst"

	// SettingKeyGrokSchedulingThresholdPercent 控制 Grok 账号按上游配额头提前停调；100 表示关闭。
	SettingKeyGrokSchedulingThresholdPercent = "grok_scheduling_threshold_percent"

	// SettingKeyGatewayCustomErrorMessage 客户端网关错误统一文案，留空关闭。
	SettingKeyGatewayCustomErrorMessage = "gateway_custom_error_message"

	// SettingKeyFallbackQueueSettings 上游全失败兜底队列开关设置（JSON）。
	SettingKeyFallbackQueueSettings = "fallback_queue_settings"
)
