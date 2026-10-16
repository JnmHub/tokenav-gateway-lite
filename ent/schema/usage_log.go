// Package schema 定义 Ent ORM 的数据库 schema。
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UsageLog 定义使用日志实体的 schema。
//
// 使用日志记录每次 API 调用的详细信息，包括 token 使用量、成本计算等。
// 这是一个只追加的表，不支持更新和删除。
type UsageLog struct {
	ent.Schema
}

// Annotations 返回 schema 的注解配置。
func (UsageLog) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usage_logs"},
	}
}

// Fields 定义使用日志实体的所有字段。
func (UsageLog) Fields() []ent.Field {
	return []ent.Field{
		// 关联字段
		field.Int64("user_id"),
		field.Int64("api_key_id"),
		field.Int64("account_id"),
		field.String("request_id").
			MaxLen(64).
			NotEmpty(),
		field.String("model").
			MaxLen(100).
			NotEmpty(),
		// RequestedModel stores the client-requested model name for stable display and analytics.
		// NULL means historical rows written before requested_model dual-write was introduced.
		field.String("requested_model").
			MaxLen(100).
			Optional().
			Nillable(),
		// UpstreamModel stores the actual upstream model name when model mapping
		// is applied. NULL means no mapping — the requested model was used as-is.
		field.String("upstream_model").
			MaxLen(100).
			Optional().
			Nillable(),
		field.Int64("channel_id").Optional().Nillable().Comment("渠道 ID"),
		field.String("model_mapping_chain").MaxLen(500).Optional().Nillable().Comment("模型映射链"),
		field.String("billing_tier").MaxLen(50).Optional().Nillable().Comment("计费层级标签"),
		field.String("billing_mode").MaxLen(20).Optional().Nillable().Comment("计费模式：token/per_request/image"),
		field.Int64("group_id").
			Optional().
			Nillable(),
		// group_id 是实际计费分组；original_group_id 只保存本次关键词切组前的分组，
		// 不建立外键，避免历史分组删除后丢失审计证据。
		field.Int64("original_group_id").
			Optional().
			Nillable(),
		field.Bool("keyword_policy_matched").Default(false),
		field.Bool("keyword_policy_rerouted").Default(false),
		field.String("keyword_policy_source").MaxLen(16).Default(""),
		field.String("keyword_policy_reason").MaxLen(64).Default(""),
		field.JSON("detected_languages", []string{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int64("subscription_id").
			Optional().
			Nillable(),

		// Token 计数字段
		field.Int("input_tokens").
			Default(0),
		field.Int("output_tokens").
			Default(0),
		field.Int("cache_creation_tokens").
			Default(0),
		field.Int("cache_read_tokens").
			Default(0),
		field.Int("cache_creation_5m_tokens").
			Default(0),
		field.Int("cache_creation_1h_tokens").
			Default(0),
		field.Int("image_input_tokens").
			Default(0),
		field.Int("image_output_tokens").
			Default(0),

		// 成本字段
		field.Float("input_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("image_input_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("image_input_price").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,12)"}),
		field.String("image_input_price_source").
			MaxLen(32).
			Optional().
			Nillable(),
		field.Float("output_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("cache_creation_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("cache_read_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("image_output_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("total_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("actual_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("rate_multiplier").
			Default(1).
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}),
		// rate_policy_audit 保存请求开始时冻结的销售倍率证据。它只用于审计，
		// 不包含账号采购成本倍率，避免后续查询时把成本倍率误当成客户扣费倍率。
		field.JSON("rate_policy_audit", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// account_rate_multiplier: 账号计费倍率快照（NULL 表示按 1.0 处理）
		field.Float("account_rate_multiplier").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}),
		// account_stats_cost stores the raw procurement cost resolved for the
		// final upstream model. NULL means that no dedicated cost evidence was
		// available; an explicit zero is a confirmed zero-cost result.
		field.Float("account_stats_cost").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),

		// 其他字段
		field.Int8("billing_type").
			Default(0),
		field.Bool("stream").
			Default(false),
		field.Int("duration_ms").
			Optional().
			Nillable(),
		field.Int("first_token_ms").
			Optional().
			Nillable(),
		// fake_first_token_ms 伪造首字（SSE 注释行）发出时刻，距请求开始的毫秒数；
		// 未开启伪造或未触发时为 nil。与 first_token_ms（上游真实首字）并列展示。
		field.Int("fake_first_token_ms").
			Optional().
			Nillable(),
		field.String("user_agent").
			MaxLen(512).
			Optional().
			Nillable(),
		field.String("ip_address").
			MaxLen(45). // 支持 IPv6
			Optional().
			Nillable(),

		// 图片生成字段（仅 gemini-3-pro-image 等图片模型使用）
		field.Int("image_count").
			Default(0),
		field.String("image_size").
			MaxLen(10).
			Optional().
			Nillable(),
		field.String("image_input_size").
			MaxLen(32).
			Optional().
			Nillable(),
		field.String("image_output_size").
			MaxLen(32).
			Optional().
			Nillable(),
		field.String("image_size_source").
			MaxLen(16).
			Optional().
			Nillable(),
		field.JSON("image_size_breakdown", map[string]int{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int("video_count").
			Default(0),
		field.String("video_resolution").
			MaxLen(10).
			Optional().
			Nillable(),
		field.Int("video_duration_seconds").
			Optional().
			Nillable(),
		// 思考档次仅作为请求审计证据；原始值和实际上游值必须独立保存，
		// 不能互相回填，也不能根据模型默认值猜测历史记录。
		field.String("requested_reasoning_effort").
			MaxLen(20).
			Optional().
			Nillable(),
		field.String("reasoning_effort").
			MaxLen(20).
			Optional().
			Nillable(),
		// Cache TTL Override 标记（管理员强制替换了缓存 TTL 计费）
		field.Bool("cache_ttl_overridden").
			Default(false),

		// 时间戳（只有 created_at，日志不可修改）
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		field.String("upstream_response_model").
			MaxLen(200).
			Optional().
			Nillable(),
		field.Bool("upstream_model_mismatch").
			Optional().
			Nillable(),
		field.Bool("long_context_billing_applied").
			Default(false).
			Comment("Whether long-context pricing changed token prices for this request"),
	}
}

// Edges 定义使用日志实体的关联关系。
func (UsageLog) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("usage_logs").
			Field("user_id").
			Required().
			Unique(),
		edge.From("api_key", APIKey.Type).
			Ref("usage_logs").
			Field("api_key_id").
			Required().
			Unique(),
		edge.From("account", Account.Type).
			Ref("usage_logs").
			Field("account_id").
			Required().
			Unique(),
		edge.From("group", Group.Type).
			Ref("usage_logs").
			Field("group_id").
			Unique(),
		edge.From("subscription", UserSubscription.Type).
			Ref("usage_logs").
			Field("subscription_id").
			Unique(),
	}
}

// Indexes 定义数据库索引，优化查询性能。
func (UsageLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id"),
		index.Fields("api_key_id"),
		index.Fields("account_id"),
		index.Fields("group_id"),
		index.Fields("original_group_id"),
		index.Fields("subscription_id"),
		index.Fields("created_at"),
		index.Fields("model"),
		index.Fields("requested_model"),
		index.Fields("request_id"),
		// 复合索引用于时间范围查询
		index.Fields("user_id", "created_at"),
		index.Fields("api_key_id", "created_at"),
		// 分组维度时间范围查询（线上由 SQL 迁移创建 group_id IS NOT NULL 的部分索引）
		index.Fields("group_id", "created_at"),
	}
}
