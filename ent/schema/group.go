package schema

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/domain"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Group holds the schema definition for the Group entity.
type Group struct {
	ent.Schema
}

func (Group) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "groups"},
	}
}

func (Group) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (Group) Fields() []ent.Field {
	return []ent.Field{
		// 唯一约束通过部分索引实现（WHERE deleted_at IS NULL），支持软删除后重用
		// 见迁移文件 016_soft_delete_partial_unique_indexes.sql
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		field.String("description").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Float("rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1.0),
		field.Bool("peak_rate_enabled").
			Default(false).
			Comment("是否在指定时区和时间窗口叠加 Token 销售倍率"),
		field.String("peak_start").
			MaxLen(5).
			Default("18:00").
			Comment("高峰开始时间，HH:MM，包含边界"),
		field.String("peak_end").
			MaxLen(5).
			Default("22:00").
			Comment("高峰结束时间，HH:MM，不包含边界；早于开始表示跨午夜"),
		field.Float("peak_rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1.0).
			Comment("仅在高峰窗口内叠加到 Token 销售倍率"),
		field.String("peak_timezone").
			MaxLen(64).
			Default("Asia/Shanghai").
			Comment("IANA 时区，保证多区域节点按同一规则计费"),
		field.Bool("is_exclusive").
			Default(false),
		field.Bool("local_only").
			Default(false).
			Comment("仅允许轻量网关本地 glk_ 密钥使用；不向主站同步"),
		field.Int("minimum_tier").
			Optional().
			Nillable().
			Min(0).
			Max(2).
			Comment("分组最低用户等级；NULL 表示继承主站节点等级"),
		field.String("status").
			MaxLen(20).
			Default(domain.StatusActive),
		field.String("scheduling_mode").
			MaxLen(20).
			Default("default").
			Comment("分组账号调度规则：default 负载感知；random 完全随机；strict_priority 严格按账号全局优先级分层"),
		field.Int("per_user_concurrency").
			Default(0).
			Min(0).
			Comment("每个主站用户在本分组的并发硬上限；0 表示不限，与用户等级并发同时生效"),
		field.String("duplicate_operation_id").
			MaxLen(64).
			Optional().
			Nillable().
			Immutable().
			Comment("内部幂等恢复标识，不对 API 暴露"),

		// Subscription-related fields (added by migration 003)
		field.String("platform").
			MaxLen(50).
			Default(domain.PlatformAnthropic),
		field.String("display_brand").
			MaxLen(40).
			Default("").
			Comment("用户界面展示的品牌类别，不参与请求调度"),
		field.String("display_icon").
			MaxLen(32).
			Default("").
			Comment("内置展示图标键，不接受外部 URL 或 SVG"),
		field.String("subscription_type").
			MaxLen(20).
			Default(domain.SubscriptionTypeStandard),
		field.Float("daily_limit_usd").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Float("weekly_limit_usd").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Float("monthly_limit_usd").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Int("default_validity_days").
			Default(30),

		// 图片生成计费配置（antigravity 和 gemini 平台使用）
		field.Bool("allow_image_generation").
			Default(false).
			Comment("是否允许该分组使用图片生成能力"),
		field.Bool("image_rate_independent").
			Default(false).
			Comment("图片生成是否使用独立倍率；false 表示共享分组有效倍率"),
		field.Float("image_rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1.0).
			Comment("图片生成独立倍率，仅 image_rate_independent=true 时生效"),
		field.Float("image_price_1k").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Float("image_price_2k").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Float("image_price_4k").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Bool("video_rate_independent").
			Default(false).
			Comment("视频生成是否使用独立倍率；false 表示共享分组倍率"),
		field.Float("video_rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1.0).
			Comment("视频生成独立倍率，仅 video_rate_independent=true 时生效"),
		field.Float("video_price_480p").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Float("video_price_720p").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Float("video_price_1080p").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.JSON("video_model_prices", map[string]map[string]float64{}).
			Default(map[string]map[string]float64{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Grok 视频模型族到 480p/720p/1080p 每秒价格矩阵"),
		field.Float("web_search_price_per_call").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Comment("Codex alpha/search 网页搜索单次价格（USD/次）；nil 使用默认价 0.01"),
		field.Float("search_price_per_1k").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Comment("Grok 搜索工具每千次价格；NULL 使用默认价 10，0 表示免费"),
		field.Float("audio_realtime_price_per_min").
			Optional().Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Comment("Grok Realtime 每分钟价格；NULL 使用默认价格，0 表示免费"),
		field.Float("audio_tts_price_per_million_chars").
			Optional().Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Comment("Grok TTS 每百万字符价格；NULL 使用默认价格，0 表示免费"),
		field.Float("audio_stt_price_per_hour").
			Optional().Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Comment("Grok STT 每小时价格；NULL 使用默认价格，0 表示免费"),
		field.Bool("allow_openai_long_context_billing").
			Default(false).
			Comment("是否允许 OpenAI 分组使用厂商长上下文计费账号；账号级开关仍表示上游成本事实"),
		field.Bool("allow_blocked_keyword_requests").
			Default(false).
			Comment("是否允许处理命中强拦截关键词的请求；开启表示允许全部已配置关键词，不按类别细分"),

		// Claude Code 客户端限制 (added by migration 029)
		field.Bool("claude_code_only").
			Default(false).
			Comment("是否仅允许 Claude Code 客户端"),
		field.Int64("fallback_group_id").
			Optional().
			Nillable().
			Comment("非 Claude Code 请求降级使用的分组 ID"),
		field.Int64("fallback_group_id_on_invalid_request").
			Optional().
			Nillable().
			Comment("无效请求兜底使用的分组 ID"),

		// 模型路由配置 (added by migration 040)
		field.JSON("model_routing", map[string][]int64{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("模型路由配置：模型模式 -> 优先账号ID列表"),
		field.JSON("model_mapping", map[string]string{}).
			Default(map[string]string{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("普通分组模型映射：客户请求模型 -> 分组目标模型；支持末尾 * 前缀匹配"),
		field.String("fallback_model").
			MaxLen(100).
			Default("").
			Comment("同分组无可用账号时仅尝试一次的兜底模型；不改变客户请求模型和默认计费口径"),
		// 智能分流映射 (added by migration 229)：公开模型 -> 带权重的目标模型列表。
		// 与 model_mapping 单目标映射互斥（同一公开模型只能出现在其中一边）；
		// 选择器按权重轮询并参考目标健康状态，失败目标自动冷却禁用。
		field.JSON("weighted_model_mapping", map[string][]map[string]any{}).
			Default(map[string][]map[string]any{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("智能分流映射：公开模型 -> [{model,weight}]；按权重分流，目标失败自动冷却"),
		field.Int64("failover_group_id").
			Optional().
			Nillable().
			Comment("普通文本上游故障时一次性转移到的兜底分组；独立于客户端限制兜底"),
		field.String("failover_strategy").
			MaxLen(32).
			Default("original_model_first").
			Comment("跨分组故障转移顺序：original_model_first 或 current_group_first"),
		field.String("billing_model_source").
			MaxLen(20).
			Optional().
			Comment("分组计费模型来源：requested/channel_mapped/upstream/response_model；分组设置优先于渠道设置"),

		// 模型路由开关 (added by migration 041)
		field.Bool("model_routing_enabled").
			Default(false).
			Comment("是否启用模型路由配置"),

		// MCP XML 协议注入开关 (added by migration 042)
		field.Bool("mcp_xml_inject").
			Default(true).
			Comment("是否注入 MCP XML 调用协议提示词（仅 antigravity 平台）"),

		// 支持的模型系列 (added by migration 046)
		field.JSON("supported_model_scopes", []string{}).
			Default([]string{"claude", "gemini_text", "gemini_image"}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("支持的模型系列：claude, gemini_text, gemini_image"),

		// 分组排序 (added by migration 052)
		field.Int("sort_order").
			Default(0).
			Comment("分组显示排序，数值越小越靠前"),

		// OpenAI Messages 调度配置 (added by migration 069)
		field.Bool("allow_messages_dispatch").
			Default(false).
			Comment("是否允许 /v1/messages 调度到此 OpenAI 分组"),
		field.Bool("allow_live").
			Default(false).
			Comment("是否允许此 OpenAI 分组访问 ChatGPT Live；节点总开关仍须开启"),
		field.Bool("require_oauth_only").
			Default(false).
			Comment("仅允许非 apikey 类型账号关联到此分组"),
		field.Bool("require_privacy_set").
			Default(false).
			Comment("调度时仅允许 privacy 已成功设置的账号"),
		field.String("default_mapped_model").
			MaxLen(100).
			Default("").
			Comment("默认映射模型 ID，当账号级映射找不到时使用此值"),
		field.JSON("messages_dispatch_model_config", domain.OpenAIMessagesDispatchModelConfig{}).
			Default(domain.OpenAIMessagesDispatchModelConfig{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("OpenAI Messages 调度模型配置：按 Claude 系列/精确模型映射到目标 GPT 模型"),
		field.JSON("models_list_config", domain.GroupModelsListConfig{}).
			Default(domain.GroupModelsListConfig{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("自定义 /v1/models 展示列表配置；仅影响模型列表响应，不影响调度"),

		// 分组级每分钟请求数上限（0 = 不限制）。设置后优先于用户级兜底生效。
		field.Int("rpm_limit").
			Default(0).
			Comment("分组 RPM 上限，0 表示不限制；设置后接管该分组用户的限流"),

		field.String("max_reasoning_effort").
			MaxLen(20).
			Default("").
			Comment("OpenAI reasoning effort 上限；可选 minimal/low/medium/high/xhigh/max"),
		field.JSON("reasoning_effort_mappings", []domain.ReasoningEffortMapping{}).
			Default([]domain.ReasoningEffortMapping{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("OpenAI reasoning effort 精确映射；先映射再应用上限"),

		field.Bool("allow_batch_image_generation").
			Default(false).
			Comment("是否允许该分组使用批量图片生成能力"),
		field.Float("batch_image_discount_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(0.5).
			Comment("批量图片生成折扣倍率，最终单价会乘以该值；0 表示免费"),
		field.Float("batch_image_hold_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(0.6).
			Comment("批量图片生成冻结价格比例，按普通生图原价乘以该比例冻结，结算后释放差额"),
		field.Bool("long_context_pricing_enabled").
			Default(true).
			Comment("是否按上下文长度应用模型阶梯价格；默认开启以保持官方/渠道长上下文价"),
		field.JSON("model_pricing", json.RawMessage{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("分组逐模型定价；优先级高于渠道和内置定价"),
		field.Bool("force_openai_fast").
			Default(false).
			Comment("是否强制此 OpenAI/Composite 分组请求使用 service_tier=priority"),
		field.Bool("free_openai_fast").
			Default(false).
			Comment("是否让此 OpenAI/Composite 分组的 Fast 请求按 Standard 价格计费"),
		
		field.JSON("codex_models_manifest_config", domain.GroupCodexModelsManifestConfig{}).
			Default(domain.GroupCodexModelsManifestConfig{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("固定账号获取 Codex Model Manifest 配置；开启后 /models 请求只用选定账号拉取（仅 openai 平台）"),
		field.String("max_reasoning_effort_over_limit").
			MaxLen(20).
			Default("downgrade").
			Comment("超过推理强度上限时的访问控制：downgrade 自动降档，deny 拒绝访问"),
		field.Bool("profit_control_enabled").
			Default(false).
			Comment("是否启用利润控制：调度时仅允许账号计费倍率满足毛利率要求的账号进入候选池"),
		field.Float("profit_min_margin").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(0).
			Comment("最低毛利率，小数（0.30=30%）；账号准入条件为 U <= D*(1-margin-buffer)"),
		field.Float("profit_safety_buffer").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(0).
			Comment("安全缓冲，小数；与 margin 相加后从下游倍率中扣除，默认 0"),
	}
}

func (Group) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("api_keys", APIKey.Type),
		edge.To("redeem_codes", RedeemCode.Type),
		edge.To("subscriptions", UserSubscription.Type),
		edge.To("usage_logs", UsageLog.Type),
		edge.From("accounts", Account.Type).
			Ref("groups").
			Through("account_groups", AccountGroup.Type),
		edge.From("allowed_users", User.Type).
			Ref("allowed_groups").
			Through("user_allowed_groups", UserAllowedGroup.Type),
		// 注意：fallback_group_id 直接作为字段使用，不定义 edge
		// 这样允许多个分组指向同一个降级分组（M2O 关系）
	}
}

func (Group) Indexes() []ent.Index {
	return []ent.Index{
		// name 字段已在 Fields() 中声明 Unique()，无需重复索引
		index.Fields("status"),
		index.Fields("platform"),
		index.Fields("subscription_type"),
		index.Fields("is_exclusive"),
		index.Fields("deleted_at"),
		index.Fields("sort_order"),
		index.Fields("duplicate_operation_id").
			Unique().
			StorageKey("idx_groups_duplicate_operation_id_active").
			Annotations(entsql.IndexWhere("duplicate_operation_id IS NOT NULL AND deleted_at IS NULL")),
	}
}
