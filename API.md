# gateway-lite API（轻量网关接口）文档

## 2026-10-16 公开部署的 Google OAuth 配置

公开源码和镜像不再内置 Antigravity / Gemini CLI 的固定 Google OAuth（第三方授权）客户端 ID 或 Secret。接口路径、请求与成功响应字段保持不变，相关授权地址生成、授权码交换、令牌刷新在配置缺失时返回明确的配置错误，且不向上游发送不完整凭据。

- Antigravity：部署时成对配置 `ANTIGRAVITY_OAUTH_CLIENT_ID`、`ANTIGRAVITY_OAUTH_CLIENT_SECRET`。
- Gemini CLI / Google One：成对配置 `GEMINI_CLI_OAUTH_CLIENT_ID`、`GEMINI_CLI_OAUTH_CLIENT_SECRET`。既有 `gemini.oauth` / `GEMINI_OAUTH_CLIENT_ID` / `GEMINI_OAUTH_CLIENT_SECRET` 仍表示 AI Studio 自定义客户端，不与 CLI 默认客户端混用。
- 客户端必须与目标服务及令牌签发来源匹配。缺少上述可选配置不阻止网关启动、管理员登录和主站绑定，也不影响其他平台账号。

私密值保存在部署环境；Docker Compose（容器编排）提供四个可选环境变量透传，安装器不自动生成或预置 Google 客户端凭据。生成授权地址失败时不保留不可完成的临时授权会话。

## 2026-10-12 升级合同与能力边界

本节对应 [主站接口文档](../control-plane/API.md) 与 [接口同步计划](../docs/modules/tokenav-upgrade-20261012/接口文档同步计划.md)，按实际路由与处理器同步。源码接入不代表已完成部署、真实上游验收或总目标；严格资金授权、付费委托和异步恢复仍在并行收口，以下只声明可核实的合同。

### 本轮新修源码与验收状态

| 新修 | 当前合同与验收边界 |
|---|---|
| O4 未知生产执行结果 outbox（持久恢复事件箱） | 已接真实 SQL（数据库）、主站客户端、生产启动和等待退出的停机链。变更前持久保存原操作，派发前写不可逆栅栏，跨进程消费者只恢复账务；可能已执行结果未知保留占用与告警。真实数据库/缓存重建测试已有证据，双系统端到端仍待 |
| 运行价格 snapshot（冻结快照） | 最终文本派发深拷数值价格，首份冻结值生效；后台记账复制独立仓，长连接每轮新仓。Seedance 创建前持久原数值价，完成按原价恢复，缺损拒绝；完整增量回归仍在收口。严格卡图片暂不具备闭合的原价发送与结算，发送前拒绝 |
| AI 整体判定与完整提取文本 | 2026-10-15 简化合同：每请求在父请求上下文内至多一次 HTTP 审查，只返回策略版本、整体判断、类别和原因；原字段文本不按长度切片，可选内联图片，未知扩展及不可展开内容记录未覆盖。专用 SQL（数据库）存储仅负责处罚事件箱，日志列表与管理员全文详情分离。此前测试仅为历史证据，本轮文档同步未运行测试，不表示并行实现、真实供应商、端到端、界面或部署已验收 |

这些新修不扩大尚未接入审查的独立协议端点或业务执行费用上界；未覆盖内容也不能据此称为已审查。

### 配置快照与模型目录一致性

完整配置推送用本次分页读取的同批账号生成目录候选，再读取对应真实目录价；不会混入客户模型短缓存，也不会再次查询另一批账号。目录候选按账号持久状态 `active` 和 `schedulable=true` 筛选，短期限流不撤销配置镜像能力。健康上报仍使用实时可调度仓储，但跳过客户模型缓存。动态目录非空时不合并静态列表，动态为空才保留原静态回退；分组有效模型、平台隔离、未知模型缺价省略及 `model_prices_reported`（权威价格已上报）语义不变。账号凭据只在本机参与计算，对外账号快照仍按原规则脱敏，接口字段不变。

修前已实测缓存导致新增模型缺价、移除模型后目录残价；修后定向快照及客户缓存回归已通过，完整双系统与Seedance真实钱包仍按各自结果签收。详见[配置快照一致性计划](../docs/modules/tokenav-upgrade-20261012/配置快照模型目录缓存一致性修正计划.md)。

### 通用与 Google 无效鉴权 HTTP 429（当前源码合同）

通用 `APIKeyAuthMiddleware`（接口密钥鉴权）与 Google `APIKeyAuthMiddlewareForGoogle` 共用来源封锁判定。在调用认证仓库 `GetByKey` 前检查已封锁来源；blocked（已封锁）请求返回 HTTP 429，不再查库，也不重复增加无效鉴权失败次数。`Retry-After`（重试等待秒数）是剩余封锁时间向上取整的整数，至少 `1`；入口拒绝原因为 `invalid_auth_rate_limited`，与数据库运行错误分开记录。

通用响应为顶层 `code/message`：

```json
{"code":"INVALID_AUTH_RATE_LIMITED","message":"Too many invalid API key attempts. Please retry later."}
```

Google 协议保留自己的错误结构：

```json
{"error":{"code":429,"message":"Too many invalid API key attempts. Please retry later.","status":"RESOURCE_EXHAUSTED"}}
```

只有缺失 Key 或 `ErrAPIKeyNotFound`（密钥不存在）计入来源失败次数；数据库/运行故障返回内部错误，不记成非法鉴权，不能误封客户。Key 停用、用户停用、分组/IP（来源地址）限制等后续拒绝也不经这两处失败计数。上述准确文案限定通用/Google 入口，不覆盖专用轻量网关鉴权中间件的全部错误格式。

实现依据：[通用鉴权](internal/server/middleware/api_key_auth.go)、[Google 鉴权](internal/server/middleware/api_key_auth_google.go)、[封锁与重试头](internal/server/middleware/ingress_reject.go)、[通用响应](internal/server/middleware/middleware.go)、[Google 状态映射](internal/pkg/googleapi/status.go)。

### 系统 settings（设置）部分更新与版本

已注册 `GET/PATCH/PUT /api/v1/admin/settings`，使用既有管理端鉴权。读取数据新增 `version`（版本），为规范十进制字符串，必须原样用于保存；不是 JavaScript number（浮点数字），也不是 `If-Match`（条件请求头）替代值。GET 同一数据库快照读取设置和版本，秘密只回显 `*_configured`（是否配置）等状态。

| 保存行为 | 当前合同 |
|---|---|
| `PATCH` 与 `PUT` | 均按字段部分更新，JSON 对象内必带当前 `version`；PUT 不作整对象替换 |
| 省略字段 | 保留数据库当前值，不把默认值、零值或掩码写回 |
| 显式 `false/0/[]` | 是修改请求，不等于省略；仍遵守该字段校验和规范化（例如并发必须为正，不能宣传所有 0 都合法） |
| 密码/秘密空串 | 通用系统设置沿用只写秘密合同：空值保留已有秘密，不等于删除；读取不会给出历史明文。不要把别的模块 `clear_api_key`（清除密钥）套到本接口 |
| `null` | 任一显式 null 拒绝；版本 null 同样 400，不表示清空 |
| 缺失/空版本 | HTTP 428 `SETTINGS_VERSION_REQUIRED`（请刷新设置后再保存） |
| 过期版本 | HTTP 409 `SETTINGS_VERSION_CONFLICT`（已被他人修改）；刷新并让管理员确认，不能强行自动覆盖 |
| 非规范版本 | HTTP 400 `SETTINGS_VERSION_INVALID`；拒绝数值类型、前导零、溢出等 |

提交处由数据库事务执行 CAS（比较旧版本再保存），设置字段与修订号一起提交，关闭“先读再普通写入”的竞态；成功回读并发布新快照，返回结构仍用管理 API（接口）统一 `{code,message,data}` 包装。其他设置子接口有自己的合同，不能假定同一个版本适用全部路径。

实现依据：[设置处理器](internal/handler/admin/setting_handler.go)、[部分更新服务](internal/service/setting_service_partial.go)、[实际管理路由](internal/server/routes/admin.go)。

### 风险控制 AI（人工智能）审查管理路径

实际统一前缀为 `/api/v1/admin/risk-control`；总目标早期 `content-moderation/ai/*` 只是草案，不是本轮新路由。以下为 2026-10-15 简化合同；并行改动和历史测试记录不构成本轮测试通过或上线证据。

| 方法与路径 | 请求/响应与用途 |
|---|---|
| `GET /api/v1/admin/risk-control/config` | 读取已保存配置及默认规范化值，返回原风险配置与 `ai:ContentModerationAIConfigView`，含密钥配置/掩码、能力矩阵及只读 `ai.config_fingerprint`（配置指纹）；回显不表示生产审查已生效 |
| `PUT /api/v1/admin/risk-control/config` | 原指针字段部分更新，含 `ai:UpdateContentModerationAIConfigInput`；省略保持，显式布尔值按字段保存；专用配置的有效更新字段仅为下文简化字段及清除密钥操作，移除字段不会恢复旧限制，响应回显保存后的配置指纹 |
| `POST /api/v1/admin/risk-control/ai/test` | `{case_id, config_fingerprint?}`；可只传 `case_id`。内置案例为 `allow/block/injection/long_tail/unsupported_media/uncovered_field`（正常/违规/指令注入/长尾/文本加外链图片/未知扩展）；拒绝任意提示词、未知请求字段、额外 JSON，正文上限 1024 字节；不发送业务上游、不处罚客户 |
| `GET /api/v1/admin/risk-control/ai/metrics` | 返回配置指纹、实际审查服务路径、当前配置测试状态、生产运行条件诊断、独立生产计数、调用观测与处罚待发送状态；没有 AI 预算、结论缓存、熔断或审查排队统计，不返回秘密或客户全文 |
| `GET /api/v1/admin/risk-control/logs` | 分页返回预览与判定元数据，不读取或返回两份全文；筛选和搜索合同见下文日志段 |
| `GET /api/v1/admin/risk-control/logs/:id` | 管理员按日志编号读取详情，含已保存的 `full_input_text/review_text_excerpt` 两份完整脱敏文本及判定元数据；非法/非正编号 400，不存在 404 |

这些路径使用既有网关管理鉴权，不借用主站的 OAuth（开放授权）应用管理权限。测试请求格式错误、未知案例或配置不可读取返回 HTTP 400；能取得 `ContentModerationDecision`（审查判断）时，管理处理器返回 HTTP 200 和 `{code:0,message:"success",data:...}`，实际失败在 `data.verdict/error_code/status_code/coverage` 中表达，不能把包装成功解释为 `verdict=allow` 或生产已启用。合成测试只使用内置案例和后台保存的专用 AI 配置，跳过生产总开关、引擎、范围与真实客户外发授权门，并强制同步判定；有可送审内容时仍须配置有效、专用密钥非空且父请求未取消，可以访问审查供应商并产生供应商费用。本接口不估价、不预留运营预算、不另设审查并发/RPM（每分钟请求数）、队列、重试或截止时间。

请求可带从读取配置或指标取得的 `config_fingerprint`。非空指纹与当前保存配置不一致时，在调用供应商前返回判断 `verdict=error/error_code=ai_test_config_changed/status_code=503`，并附当前 `config_fingerprint`；外层仍为 HTTP 200。前端应刷新已保存配置后重新测试；省略或空指纹保留只传 `case_id` 的调用能力。

测试判断包含 `config_fingerprint`、可选 `test`（测试证据）、`provider_http_status`（供应商实际状态码），以及 `verdict/categories/reason/phase/provider_called/provider_latency_ms/latency_ms/error_code/status_code/coverage/shadow/penalty_state/allowed/blocked/flagged/message/action` 等网关判断字段；不再返回估算费用或缓存命中字段，也不要求供应商输出片段判断。`coverage` 包含 `complete/input_hash/fragment_count/image_count/reviewed_count/text_runes/media_supported/uncovered_fields`。`test` 字段与状态见下文；每次有可送审内容的测试取得新调用证据，不复用旧判定。测试不授权真实客户内容外发，也不计入 `production_*`（生产审查计数）。

`coverage.complete=true` 仅表示全部已选文本/内联图片已取得有效整体判定，可与非空 `uncovered_fields` 同时出现，不表示整份业务请求全部已审。`fragment_count/image_count/text_runes` 统计抽取出的已选内容；`reviewed_count` 在有效整体判定后等于已选文本字段数加图片数，准备失败、调用失败和跳过时为 0。`media_supported` 仅表示本次选中了内联图片，不证明未选音视频、远程图或任意独立端点受支持。

专用适配器仅按供应商 HTTP 状态分类，不解析或回显供应商错误正文：

| 供应商 HTTP 状态 | `error_code` | 判断中的 `status_code` |
|---|---|---|
| 401 | `ai_provider_invalid_credential`（供应商凭据无效） | 503 |
| 403 | `ai_provider_forbidden`（供应商拒绝访问） | 503 |
| 404 | `ai_provider_not_found`（供应商路径或资源不存在） | 503 |
| 400 / 422 | `ai_provider_invalid_request`（供应商拒绝请求参数） | 503 |
| 429 | `ai_provider_rate_limit`（供应商限流） | 429 |
| 其他非 200 状态，含重定向及 5xx | `ai_provider_http_error`（供应商 HTTP 错误） | 503 |

`provider_http_status` 保留收到的状态码。只有真实供应商 429 映射为审查失败 429，其他审查失败均为 503；网络、父请求取消/截止和非法结果分别使用安全错误码，例如 `ai_transport/ai_deadline/ai_result_schema`，不再产生 AI 价格、预算、容量、排队或熔断拒绝。404 不单独证明协议不受支持、密钥失效或账户封禁；失败不算违规，不处罚，也不回显密钥、客户原文或供应商原始错误正文。

### 纯 AI 模式、配置与三态

顶层 `keyword_blocking_mode` 显式支持 `keyword_only`（本地关键词）和 `ai_only`（纯 AI）。新提交 `keyword_and_api/api_only` 等旧选择返回 `INVALID_CONTENT_MODERATION_ENGINE`，不再“保存成功却改回 keyword_only”。保存 `enabled=true` 同步打开 `risk_control_enabled`（风险总开关）；`all_groups/group_ids` 和原 `model_filter` 决定适用范围。

`ai_only` 跳过关键词、语种判断及关键词切组，采样固定 100%，全部已提取文本和开启图片审查后选中的内联图片在同一次调用中整体判定；没有可送内容时明确跳过。鉴权、基本 JSON/协议形状、模型权限、业务账户并发、资金计费和系统安全门仍执行。首次切纯 AI 且没指定 `mode` 默认 `shadow`（影子审查）；模式为 `off/shadow/pre_block`（关闭/记录判断/请求前阻断），旧 `observe`（异步观察）不是新纯 AI 影子模式。

| verdict（判断） | `pre_block`（请求前阻断） | `shadow`（影子审查） |
|---|---|---|
| `allow` | 有效整体判断放行已选内容；无可送内容则按 `phase=skipped` 跳过放行，均不能把未覆盖内容称为已审 | 放行并记录；同样区分有效审查与跳过 |
| `block` | 按配置策略拒绝；释放未消费业务预留；处罚另需明确开关 | 记录 `ai_shadow_block`，不标违规、不处罚，业务放行 |
| `error` | 真实供应商限流为 429，其他审查失败为 503；不调业务上游、不记违规、不处罚 | 保留真实 error，仍拒绝业务，不将未知结果伪装成通过 |

`ai.external_data_consent`（真实客户资料外发同意）与 `ai.penalties_enabled`（违规处置授权）默认均 `false`；AI 有专用 `api_key`，不复用旧远程审查密钥或业务上游账号。存在可送审内容但未明确允许外发时，真实请求返回 `ai_external_data_not_authorized`；影子也不默许外发。没有可送内容的跳过不发生外发，不证明已获得外发授权或供应商审查成功。切纯 AI 时未明确提交的旧自动封禁、扣款、永久封禁、命中邮件均置为关闭。处罚只针对有效 `block` 且独立开启处罚策略，`error`、影子和合成测试均不处罚。

持久 `ai` 配置仅有以下十个字段；读取额外返回只读 `api_key_configured/api_key_masked/capabilities/config_fingerprint`，不返回密钥明文。`config_fingerprint` 由专用全部配置（密钥只参与摘要）、内容审查开关、模式、引擎和规范化分组/模型范围生成，是只读配置身份，不能由管理员编辑的 `model_revision` 替代。更新字段为指针，缺失保留；专用 `ai.api_key` 显式写入包含空串的值，或更新操作 `ai.clear_api_key=true` 清除，是此模块自己的合同，`clear_api_key` 不属于持久配置字段，与通用系统设置空密码保留不同。

| 字段 | 含义与默认/约束 |
|---|---|
| `adapter` | `deepseek_chat/deepseek_responses`（聊天/响应适配），默认 `deepseek_chat`；这是审查供应商协议，与客户入站协议分开 |
| `base_url` | 默认 `https://api.deepseek.com`；任意合法主机的 HTTP、HTTPS 均可用，包含内网 IP、域名和端口，不需要额外放行开关；路径规则见下文 |
| `model` | 默认 `deepseek-flash`，规范化后原样发送；默认值不是当前账户模型可用性证明，不强制供应商返回精确同名别名 |
| `model_revision` | 默认 `unverified`，仅为管理员保存的配置身份材料，不证明模型能力或测试通过，不用于结论缓存 |
| `api_key` | 专用只写密钥；有可送审内容时实际调用必须非空；读取仅给配置状态与掩码 |
| `external_data_consent` | 默认 `false`，真实客户审查内容外发必须显式同意；合成测试不代表客户已同意 |
| `policy_version` | 默认 `tokenav-text-v1`；整体结果必须精确回传当前策略版本 |
| `policy` | 管理员审查指令；客户角色、系统指令、提醒标签和工具内容都作为被审数据，不能改写管理员策略 |
| `penalties_enabled` | 默认 `false`；有效非影子、非合成 `block` 才可按独立处罚策略登记事件 |
| `image_review_enabled` | 默认 `true`；开启时已支持格式的内联图片与文本一并送审；关闭时不解码、不发送图片，只记录未覆盖；外链始终不下载 |

`ai.base_url` 去首尾空白和尾斜杠后保留填写的协议及基础路径；必须有合法主机，端口如提供须为 1–65535，拒绝非 HTTP/HTTPS、账号密码段、查询参数及片段。专用 Responses（响应接口）以 `url.JoinPath(base_url,"responses")` 拼路径，专用 Chat（对话接口）以 `url.JoinPath(base_url,"chat/completions")` 拼路径；不会自动增加 `/v1`、转换协议或跟随 HTTP 重定向。例如 `http://review.internal:8080/api` 对应 `/api/responses` 或 `/api/chat/completions`，确需 `/v1` 时应把它填写在基础地址中。`metrics.service_endpoint` 按同一规则回显实际服务路径。模型名和修订最长各 200 字节；策略版本非空且最长 200 字节，策略最长 16000 字节。

AI 专属价格、金额/令牌预算、并发、RPM、审查队列、独立截止、人工切片长度/重叠、输入容量、输出令牌、图片张数/字节上限、自动重试、结论缓存与熔断配置及其运行拒绝逻辑均已删除。有可送审内容且前置条件满足时，每个请求在父 `context`（请求上下文）的取消/截止生命周期内只发一次 HTTP 审查；不另设审查截止、不重试，不要求供应商返回 `usage`（令牌用量），也不依赖用量决定放行。网关通用请求体上限、业务账户限额和资金执行边界仍按各自合同执行。配置可保存为尚未具备运行条件的状态；保存密钥、手写修订或合成测试通过不自动开启外发授权和生产开关。

旧 `POST /api/v1/admin/risk-control/api-keys/test`（旧审核密钥测试）读取顶层 `base_url/model/timeout_ms` 和旧审核密钥集合，可由该测试请求自己的 `base_url/model/timeout_ms/api_keys` 临时覆盖，固定调用 `{base_url}/v1/moderations`，与专用 `ai` 密钥、适配协议及 `/ai/test` 独立；不能用旧字段恢复纯 AI 已删除的限制。旧结果中的 `frozen` 是网关本进程的密钥冷却状态，不证明供应商封禁，也不能作为专用 AI 已测试的证据。

供应商分类结果必须是恰含 `policy_version/verdict/categories/reason` 四个字段的 JSON 对象，不得附加字段、片段判断或动作。`policy_version` 必须匹配当前策略；`verdict` 仅 `allow/block`；`block` 的 `categories` 须至少一个已知类别，不能重复或使用未知类别，`allow` 必须为 `[]`；`reason` 为去空白后非空的简短原因。AI 不返回概率，不得把类别伪造成百分比置信度。判定原因在返回后台测试接口和写入日志前均执行专用密钥精确替换及通用密钥脱敏，供应商回显凭据不会作为原因明文返回。合法示例：

```json
{"policy_version":"tokenav-text-v1","verdict":"allow","categories":[],"reason":"已选内容为正常问候"}
```

Chat 要求单条助手结果、`finish_reason=stop`；Responses 要求整体 `completed`（已完成）、单条已完成助手消息及单条 `output_text`（输出文本）。工具/动作/拒绝/错误或未完成外壳为 `ai_response_incomplete_or_action`，非法 JSON、四字段结构、类别或一致性不满足均为 error；缺失 `usage` 或供应商模型别名不同不单独导致拒绝。`coverage` 和输入/图片元数据由网关记录，不要求模型逐段或逐图返回判断；有效整体结论不能证明未覆盖部分已被审核。

### canonical inputs（规范化完整输入）覆盖矩阵

审查解析与执行共用原输入证据，拒绝大小写变体重复 JSON 键、尾部多对象、非法 UTF-8（文本编码）及 JSON 解析器本身不能接受的语法/嵌套。固定已知字段中的 `instructions/input/messages/contents/system/systemInstruction/prompt`、全部角色和当前提交的完整历史、工具定义/参数/输出、推理文本与标签均提取；纯 AI 不沿用“只取最后用户内容”的旧关键词范围。每个原文本字段完整形成一个片段，不截尾、不重叠、不拆批，没有人工片长或 128 片段限制；工具参数和输出对象完整序列化，字符串值保持原文。所有已选文本和内联图片一起送入一次模型调用，只产生整体结论；业务 `ForwardBody`（转发正文）保留原始字节。

`tokenav-ai-input-v8`（完整输入证据版本）对严格解析的全请求、待审文本、图片元信息与稳定排序的未覆盖清单共同计算身份摘要；不同未覆盖内容不能误认成同一份证据。该摘要只用于核对输入，不是缓存键。Chat `response_format.json_schema`、Responses `text.format`、Anthropic `output_config.format/output_format`（含消息级 `output_config`）、Gemini `generationConfig.responseSchema/responseJsonSchema` 及已支持下划线别名的当前提交定义完整作为不可信客户数据送审。工具选择、停止词、`prediction`（预填文本）、助手 `prefix`（续写开关）、采样/推理/客户业务输出上限等已知生成设置同样完整进入约束片段；这些客户字段不构成审查模型自身的输出令牌配置。结构定义中的 `image` 属性按文本处理，不误认成真实图片。

协议仍须满足必要的主输入形状：Chat/Responses 为数组 `messages` 或字符串/数组 `input`，Anthropic 为数组 `messages`，Gemini 为数组 `contents`，图片生成提示为字符串 `prompt`。空字符串、空数组、仅空段落、系统角色、工具元信息均不再引入额外的“实际主输入”数量门槛；完全没有合法主字段形状仍返回 `ai_empty_or_uncovered_input`。未知根字段、未知消息字段、未知内容类型、远程历史/文件引用、不支持的媒体、`truncation:auto`、非纯文本 `modalities`、远程 `$ref/$dynamicRef/$recursiveRef`、未知格式/工具定义与托管运行时只记录 `uncovered_fields`（未覆盖清单），继续处理固定已知文本，不因这些内容整单拒绝。未知内容块内的已知 `text/content` 等文本字段仍提取；未知扩展值不会被臆测成已审内容。

`prompt.id/version` 指向的托管提示本体只记录 `history:prompt`，不下载、不推定已审；本次明确提交的 `prompt.variables` 文本/内容块可按同一抽取规则进入审查，不能把远程引用与当前变量混为一谈。本地 schema（结构定义）原文可见不表示远程引用已展开或工具已执行。清单进入判断 `coverage.uncovered_fields`、日志 `ai_review_detail.uncovered_fields` 与输入摘要。主输入形状合法但没有任何已选文本或内联图片时，返回 `verdict=allow/phase=skipped/provider_called=false`，`coverage.complete=false/reviewed_count=0`；这表示未发生审查，不是模型给出的安全结论。

Responses WebSocket（响应长连接）的首轮和后续 `response.create` 通过服务端专用入口选择信封校验；客户 HTTP 正文、请求头不能开启该例外。严格 JSON 解析后，顶层 `type` 必须精确为 `response.create`，可选 `event_id` 必须为不超过 512 字节的字符串（允许空串），大小写别名和错误类型拒绝。只有这两个已验证传输字段不记录为未知业务字段；其余业务文本和当前工具/约束照常抽取，原树含 `type/event_id` 与全部片段共同参与摘要，转发保留原字节。非法信封错误为 `ai_invalid_ws_envelope`（503 无法判定），由审查入口拒绝时发送既有 `type:error` 帧后以 `1008` 策略关闭，严格 JSON 错误沿用原码。HTTP 正文中的 `type/event_id` 只记录未覆盖，不能借此启用 WS 信封能力；HTTP 与 WS 输入使用不同摘要域以区分证据。

原生严格帧检查继续优先。首帧在 AI 外发前与后续帧共用同一严格校验和拒绝通知。已被拒绝的帧，只有能严格证明是完整、无重复键的 JSON 对象、文本帧及精确 `type=response.create` 时，适配器才增加只读拒绝通知：同一配置快照确认纯 AI 启用、分组及模型范围命中后，才分配一次服务端审查轮次并记录错误，关闭和范围外不增加轮次。模型复用本帧/会话正常解析结果，不使用前轮账务模型。输入可抽取但严格传输已拒绝时使用 `ai_strict_frame_rejected`（503 无法判定）；真实抽取/信封语法错误保留原错误码，不能因未覆盖字段改为放行。通知发送预算仍为 2 秒，这是拒绝帧传输边界，不是 AI 审查总超时；通知写失败不替换原严格错误及 `1008`。畸形/重复键/非法编码 JSON、二进制和非创建帧不凭猜测补轮次。

严格拒绝通知不调用审查供应商、不触发处罚、客户资金操作或业务派发。可提取的脱敏文本记入 `full_input_text`，`provider_called=false` 且 `review_text_excerpt` 留空；原始帧和图片数据不入审计队列。仍使用既有有界审计落库队列，不能把它称为模型排队，或宣称队列满、进程退出也保证持久化。长连接资金与传输的额外严格规则仍按下文合同执行，源码接入不代表真实供应商、数据库或全部端到端验收。

| 入口/内容 | 当前能力 |
|---|---|
| OpenAI Chat、Responses（聊天/响应） | 本次提交的完整已知文本，包含 input-only（仅 input）、全部角色和工具数据；Responses 每轮审查本轮提交内容，内联图片按开关送审 |
| Anthropic Messages、Gemini（消息/原生生成） | 已知文本含 `system/systemInstruction` 和工具数据；base64/`inline_data` 图片按开关送审 |
| OpenAI Images 纯文字生成提示 | `generation_prompt_only`（仅生成提示词），不表示生成结果、编辑图片或参考图已审核 |
| 已接入对话内的远程图片、音频、视频、文件及未知块 | 媒体本体不送审，只记录未覆盖；已知文本仍参与一次整体判定，没有已选内容时跳过 |
| `previous_response_id/conversation/conversation_id/cachedContent/cached_content/file_id/attachments` 等远程历史/文件引用 | 不下载或重建；记录 `history:*` 未覆盖，当前提交文本照常抽取，不能推定历史已审 |
| 独立音频/视频、文件、Live/Realtime（实时）、embeddings（向量）等未接入端点 | 纯 AI 前置能力门仍返回 HTTP 503 `ai_unsupported_protocol`，不调审查或业务上游；对话内媒体未覆盖规则不扩大端点支持 |

图片关闭时首先跳过，不尝试 base64 解码；开启时支持的内联图片与文本一起送审，外链始终不下载。真实的内联图片编码语法错误仍为 `ai_invalid_image_encoding`，保留拒绝但继续收齐后续已知文本用于未调用日志复核。未知格式/媒体本身不新增整单拒绝；供应商无法处理已选图片仍按真实调用失败拒绝。`capabilities`（能力矩阵）描述实际已接入范围，不证明某个供应商或模型支持所有图片格式。

### AI metrics（运行指标）与处罚 outbox（可靠事件箱）

AI 专属存储只负责处罚事件登记、认领、完成和待发送/死信统计，不再做 `Reserve/Finish`（审查预算预留/完成）、并发、RPM、金额/令牌预算或费用统计。审查仍可能在供应商处产生费用，但网关不估价、不预占审查运营额度，也不据供应商 `usage` 决定判定是否有效。历史预算与尝试表不删除、不继续用于专用审查准入；此项删除不改变客户钱包、企业预算、时效卡、业务模型计价或资金结算。

计数、最近测试和调用耗时是进程内观测，重启归零；没有结论缓存或熔断状态。相同请求内容的每轮已选审查也重新调用一次，配置指纹和输入摘要仅标识证据，不复用旧结论。处罚事件才保存在共享 SQL（数据库）中，消费者重试用于可靠交付已确定的处罚，不是重试审查模型。

处罚使用服务端 admission ID（准入编号）与 turn（轮次）构成稳定事件身份，放入 SQL outbox；主站通过 `POST /internal/content-moderation/penalty`（内容审查处罚）按持久 Key 反查真实员工/企业付款主体，不信任客户端主体。仅有效 `block`、非影子/非合成检测且显式开启处罚后才登记；缺主站 Key 的本地身份返回 `ai_penalty_subject_unsupported`。处罚持久提交回执不确定时保留原 `verdict=block/penalty_state=uncertain`（有效违规/处罚待核实），不能改报承诺不处罚的 error。相同事件核原主体/金额/Key 等输入，冲突拒绝；消费者按认领令牌确认，可恢复重试并计待发送/死信，陈旧认领不能覆盖新记录。罚款、永久封禁和企业暂停仍需各自明确策略；未提供独立远程临时封禁动作，资金处罚不直接减少卡额度。

`GET /api/v1/admin/risk-control/ai/metrics` 在既有指标上增加以下实际字段；均位于管理包装的 `data` 内：

| 字段 | 类型与口径 |
|---|---|
| `config_saved` | 布尔；内容审查配置存储非空，不等于专用密钥有效、测试通过或生产已启用 |
| `config_fingerprint` | 字符串；当前规范化保存配置的只读指纹，与 `config.ai.config_fingerprint` 同口径 |
| `service_endpoint` | 字符串；按当前有效适配配置拼出的供应商实际路径；配置无效时为空 |
| `test` | `ContentModerationAITestStatus`；当前进程最近一次测试证据及其配置身份 |
| `production` | `ContentModerationAIReadiness`；当前配置、开关、范围、外发授权与按需处罚存储的诊断，不依赖合成测试状态，不是一次客户请求已完成的证明 |
| `production_checked/production_allowed/production_blocked/production_errors` | 整数；仅真实请求进入专用审查或已接线的不支持端点/严格帧拒绝观察入口的累计次数与 `allow/block/error` 判断计数，不含合成测试；进程重启归零，不按配置指纹分桶。因此只记录本进程历史审查发生量，不保证当前配置或后续请求命中范围 |

`test` 返回 `status/config_fingerprint/coverage_complete`，并按是否存在证据返回 `case_id/verdict/error_code/provider_http_status/tested_at`（时间为 UTC 的 JSON 时间字符串）。当前 `status` 为 `not_tested/running/passed/failed/stale`（未测试/测试中/通过/失败/证据已过期）；不再产生旧媒体整拒的 `expected_rejection`。`allow` 须得到 `allow`，`block/long_tail` 须得到 `block`，否则为 `failed` 且测试错误 `ai_test_unexpected_verdict`。`injection` 要求无错误、已选内容覆盖完成的有效 `allow/block`，不以单一合成样本证明全部抗注入质量。`unsupported_media/uncovered_field` 必须取得 `allow`、已选内容完成且带实际未覆盖标记，否则失败，不可落入通用成功分支。供应商失败保留安全错误码和真实 HTTP 状态；200 但 JSON、外壳或四字段判定非法也保留 `provider_http_status=200`，不能显示成供应商未响应。

测试证据绑定测试开始时的配置指纹。保存/读取观察到不同指纹后，原测试永久成为 `stale`；把配置改回原值不会复活，只有主动重新测试才能取得新证据。测试运行中修改配置、结束时无法重新读取配置，或旧并发测试迟到完成，也不能覆盖较新的测试证据。证据只在进程内，重启恢复 `not_tested`；不会据持久 `model_revision` 或已保存密钥恢复 `passed`。合成测试是可选验证，所有测试状态均不影响 `production.ready/checks` 或生产审查准入；保存配置、重读配置及指标均不自动调用 `/ai/test` 或供应商。

`production` 返回 `state/ready/checks`，状态只有 `production_conditions_met`（当前诊断条件满足）或 `production_not_ready`（条件未满足）。`checks` 每项为 `{code,ready,message}`，返回全部检查，`ready=false` 的项表示具体缺项；`message` 是该项未满足时的说明，不能把 `ready=true` 项的说明当成当前故障。实际检查码如下：

| 检查码 | 当前核对条件 |
|---|---|
| `config_saved/config_valid` | 存储存在配置且专用地址、模型和高级参数有效 |
| `risk_control_enabled/content_moderation_enabled` | 风控总开关和内容审查开关开启 |
| `ai_engine/review_mode` | 引擎为 `ai_only`，模式为 `shadow` 或 `pre_block` |
| `group_scope/model_scope` | 全分组或指定了分组；模型全选或指定/排除模型清单非空 |
| `dedicated_key/external_data_consent` | 已保存专用密钥且显式允许真实客户内容外发 |
| `penalty_store` | 仅显式启用 `ai.penalties_enabled` 时要求处罚存储就绪；关闭处罚不阻止模型审查 |

`checks` 不再包含 `saved_config_tested`；合成测试证据仅通过独立的 `test` 字段返回。

`ready=true` 只说明读取指标时上述条件满足；实际请求仍须命中分组/模型范围、通过原业务鉴权和资金门、具备合法协议形状、父请求未取消，并取得有效审查结果。没有已选内容时可以跳过，因此该诊断也不是每次请求的强制调用证明。`test.status=passed`、`production.state=production_conditions_met` 和生产计数是不同证据，均不能单独证明真实客户业务已成功完成。生产判断计数也不是最终阻断/处罚计数：影子有效 `block` 计入 `production_blocked`，但不阻断、不处罚；`error` 不算违规。

其余指标字段为 `configured/external_data_consent/store_ready/adapter/model/policy_version/state_scope/active/checked/allowed/blocked/errors/shadow_checked/total_calls/avg_latency_ms/last_latency_ms/last_error_code/penalty_pending/penalty_dead/capabilities`。`configured` 仅表示专用密钥非空；`store_ready` 只诊断处罚存储；`active` 是当前正在调用模型的数量，不是准入上限。`total_calls` 统计进入供应商调用的次数，`checked/allowed/blocked/errors/shadow_checked`、平均和最近耗时为包含合成检测的进程观测，不能冒充纯生产发生量；没有已选内容的 `skipped` 记允许但不增加调用数。`state_scope=process_counters_sql_penalty_outbox` 表示计数/测试在进程内、处罚在共享数据库；不再返回 `queue_length/cache_hits/circuit_open_until/day_reserved_usd_micros/day_reserved_tokens` 或任何审查费用统计。

实现依据：[AI 管理处理器](internal/handler/admin/content_moderation_ai_handler.go)、[配置与指标类型](internal/service/content_moderation_ai_types.go)、[配置校验与指纹](internal/service/content_moderation_ai_config.go)、[测试证据与运行诊断](internal/service/content_moderation_ai_readiness.go)、[完整输入抽取](internal/service/content_moderation_ai_input.go)、[约束与未覆盖记录](internal/service/content_moderation_ai_constraints.go)、[单次调用与严格结果](internal/service/content_moderation_ai_adapter.go)、[运行链与处罚登记](internal/service/content_moderation_ai_runtime.go)、[处罚事件箱存储](internal/service/content_moderation_ai_store.go)、[日志证据](internal/service/content_moderation_ai_log.go)。本轮范围与验收清单见 [2026-10-15 简化实施计划](../docs/modules/tokenav-upgrade-20261012/AI内容审查简化实施计划-20261015.md)。

[2026-10-14 反馈实施记录](../docs/modules/tokenav-upgrade-20261012/AI审查反馈修复实施计划-20261014.md)属于简化前的历史验证，不能证明本轮删除预算、缓存和切片后的行为已验收。本节是源码接口合同；定向测试、真实数据库、供应商质量、双系统端到端、桌面/手机界面与部署须各自记录真实结果，不能互相替代。

### 企业范围、WebSocket（长连接）逐轮模型权限

企业/成员持久空允许列表表示该层不追加限制；求交为空是 `deny_all`（全部拒绝），不得清空成不限或回退个人付款。主站权限、Key `allowed_models`、effective policy（最终生效模型策略）与分组 `models_list_config` 同时生效。

Responses WebSocket 的首帧以及每个 `response.create` 重新核三个模型层：Key 允许模型、分组生效模型策略、分组自定义模型目录。正文明确模型或继承上轮模型后再检查，不能用握手空模型绕过后续检查。远程逐轮资金准入在取得第一轮授权前就启用严格帧模式；付费委托或 `ai_only`（纯 AI）配置同样启用，审查配置读取失败拒绝。严格模式仅接受完整 `response.create` 及合法 `response.cancel`，不能先透传 `session.update`、音频缓冲或会话项形成未审/未资助状态。首帧入口可解析合法 JSON 二进制，原生透传的严格帧校验仅接受文本帧，后续 JSON 二进制也拒绝；通用透传规则受此优先限制。严格创建须有字符串/数组 `input`，`conversation/session` 字段即使空值也拒绝；取消帧仅接受有界字符串 `type/event_id/response_id`，非法帧按客户端协议错误关闭。模型映射只影响执行，不扩权限。每轮形成独立服务端资金请求编号、独立预留与费用事件；同一轮重连和重发复用原身份，握手不重复收费。前置强审查与费用上界要在实际发送每轮载荷前再次完成。

### CCode 应用密钥实时鉴权（2026-10-14 源码合同）

普通主站 Key（接口密钥）解析响应 `KeySnapshot` 新增可选 `token_use`。**只有精确 `token_use="agent_api_key"`** 表示应用授权关联密钥，需要实时核验主站的应用、授权及关联密钥状态；不依据客户端名称、密钥前缀、TTL（缓存有效期）或未知用途猜测。该值不把普通 API Key 改为 `tokenav_oa_` 付费委托凭据。

应用密钥成功鉴权不能复用任何 Redis（缓存服务）或进程内正快照：`Set/SetIfGenerationUnchanged` 拒绝写快照、索引和热缓存，解码已有 Redis 快照及热缓存读取/写入也拒绝该用途。每个进入该远程鉴权链的 HTTP 请求都以自身请求实际调用 `ResolveKey`（主站密钥核验），不会等失效通知或缓存到期才发现撤销。`singleflight`（并发请求合并）首次取到应用快照后，其他等候请求必须各自回源，不能共用另一请求撤销前取得的成功结果。普通、缺失或未知 `token_use` 保留原缓存语义；标准付费委托继续走原专用委托解析与资金流程。

HTTP（短连接）沿用网关原错误映射：应用、授权或关联凭据无效时返回 401 `INVALID_API_KEY`，主站解析调用的网络/服务故障返回 503 `CONTROL_PLANE_UNAVAILABLE`；模型、线路、IP（来源地址）等明确权限拒绝沿用各自 403，套餐/额度门槛保留原分类。无效凭据与主站故障不能混成同一种错误，也不会因先前成功缓存而继续放行。主站已确认来自历史无应用身份的设备配对密钥会标记需要重新授权并回无效快照，不能补一条展示记录继续授予旧权限；该凭据在网关仍为 401 `INVALID_API_KEY`。设备配对端的 `reauthorization_required`（需要重新授权）属于主站合同，客户端应重新完成真实应用授权，不能将其文案当成网关新增错误码。

普通 Responses WebSocket（响应长连接）中的应用密钥在首个和每个后续 `response.create`（创建响应）取得新资金准入前，实际调用 `ResolveKey`；实际逐轮入口为 `/v1/responses`、`/responses`、`/backend-api/codex/responses` 的 WebSocket GET。重新核精确用途、活动状态、`expires_at`（Unix 秒到期时间）、原 Key 编号、秘密摘要、真实用户/企业及付款主体、允许入口节点与冻结全局分组、本地分组活动/非本地专用状态、用户等级、Key/分组模型策略和原客户端 IP；正在关键词转路由时还核当前路线是否仍被允许。握手成功、上轮成功或内存快照均不能替代本轮核验。

权限、撤销、到期或身份/线路变化以 `1008`（策略拒绝）关闭；主站核验或本地线路读取故障以 `1013`（稍后重试）关闭，消息不含主站地址、密钥或秘密摘要。处理器保留该鉴权关闭状态，不改包成资金准入错误。每轮先核权限再新增资金预留；已经准入的在途轮次保留原资金授权、结算主体、冻结价格和用量快照，后续撤销只阻止新轮，不重写旧账或释放结果未知的占用。普通/未知用途和标准委托的既有资金、补偿及结算逻辑保持原合同。

实现依据：[用途类型](internal/gatewaylite/types.go)、[快照缓存用途隔离](internal/gatewaylite/redis_key_cache.go)、[HTTP 实时解析与逐轮挂接](internal/server/middleware/gateway_lite_api_key_auth.go)、[逐轮应用授权核验](internal/server/middleware/gateway_lite_agent_auth.go)、[长连接关闭状态与原资金上下文](internal/handler/openai_gateway_handler.go)。本节描述实际源码行为，不代表本轮界面或部署已经验收。

### 时效卡与严格执行前费用上界

主站分配卡和原来源分摊合同见 [主站接口文档](../control-plane/API.md)。销售/退款固定 `sale_currency=CNY`（人民币分），额度固定 `quota_currency=USD`（美元）；不能把售价或面额作为网关消费现金、自动换汇或叠加币种。网关不复制一套卡余额、不按内部映射重选卡，实际客户价格继续由现有计价链决定，采购成本另计。

主站 `/api/logs`、管理员用量列表/详情已读取六个持久字段 `funding_authorization_id/actual_micro_cents/card_covered_micro_cents/wallet_charged_micro_cents/unfunded_micro_cents/settlement_revision`；只有资金编号非空才存在来源分摊，普通旧余额记录的整数 0 不表示全卡支付、免费或终态。此合同不表示网关自身所有日志读口已展示分摊；原资金用量仍由主站确认。

1. 权限检查后向 `POST /internal/billing/authorize` 提交原 Key、真实用户、入口网关/全局分组/请求模型与 `bound_kind=selection`；`legacy_balance` 才回旧租约。`authorization_required` 只是选定资金，需要有界授权后才可业务执行。
2. 使用现有真实 pricing resolver（定价解析器）、分组/用户倍率、各阶梯与可能计价路径，计算保守上界；上游输出限制必须真正保留在最终发送体。扩容先提交主站并确认，再原子预留全部资金条目；失败不得暗改余额或换主体。
3. 最终出站包装器重新检查转换、映射、重试去参数后的请求，按实际协议检查可执行输出上限；更贵备用路径先扩容。主站 `status=active` 表示持久活动状态，`disposition=authorized` 表示资金分支，不混用名称；终态查询也可能返回 authorized，不能据此再次发送，必须同时核活动状态、原身份、期限和上界。
4. 原实际用量挂 `funding_authorization_id/funding_revision/settlement_revision/actual_micro_cents/final`，从原授权结算并释放。当前正常最终费用事件使用修订2，派发前已确认零成本终态使用修订1；成功不必先有修订1。主站按正数单调修订核原历史，不要求连续；修订不是扣费次数，同修订只允许原金额与终态幂等重放，费用冲正必须使用更高修订。资金微分为 `1 USD=100,000,000 micro_cents`；旧 AI 审查预算历史记录的 `usd_micros` 单位为 `1 USD=1,000,000 usd_micros`，不能混用；该历史单位不表示当前仍有审查费用统计。

| 严格卡路径 | 当前确切支持边界 |
|---|---|
| 标准 Chat Completions（聊天）纯文本 | 最终发送体必须保留正整数 `max_completion_tokens` 或 `max_tokens`；可计价、有界正文；`n` 为 1–128，输出上限至多 16,777,216，按候选最高价覆盖 |
| 标准 Anthropic Messages（消息）纯文本 | 最终体必须保留 `max_tokens`，实际计价与转换后输入完整可界定 |
| 标准 API Key 上游 Responses（响应）纯文本 | 最终体必须保留 `max_output_tokens`，输出有界；处理器不能在重试中删掉该限制 |
| Responses WebSocket 每轮 | 仅同样具备真实输出上限与支持上游的轮次，最终 payload（载荷）再次核；不是所有长连接天然支持 |
| 标准 OpenAI Images（图片）及其他图片派发 | 当前严格卡路径发送前返回 `strict_card_endpoint_unsupported`。现有固定媒体上界和初次扩容不能保证完成结算使用持久原数值价，因此包括固定按张 API Key 上游也不开放；普通旧钱包路径保留 |
| Grok 视频生成/编辑/扩展 | `PrepareGrokVideoTaskBilling` 已在实际处理器发送前扩容；冻结真实价格和倍率，覆盖已声明分辨率及最长可计费时长（当前转换费用上界 3600 秒），发送包装器再核活动授权。最终按原报价真实费用结算，不按预留额收费；不适用于 Seedance 的未知 token |
| 其他图片/视频入口、工具生图与异步重放 | 不从媒体价格函数、初始小额占用、主站端点名单或已有任务快照推定支持；仅协议专属上界、参数限制和真实发送链全部验证后才可开放，未接部分为 unsupported |
| Codex OAuth `/backend-api/*`、无可执行最大输出、服务端历史引用、不能验证的媒体输入/工具费用 | `unsupported`（不支持严格上界），不能靠普通模型权限或无限卡放行 |
| Seedance 按未知 completion_tokens 结算、Gemini/实时音频/向量/其他未接上界发送口 | 未完成对应最大成本/增量控制时按 unsupported；支持任务身份恢复不代表支持严格卡 |

文本正文上限 32 MiB，有界价格采用最高可达费率/阶梯等证据，输入估算不使用低估 token 的 RPM/TPM 门禁估计，而按完整 `body`（正文）字节的 `len(body)*8+4096` 计界。仅 `fundingClientToolDataField`（协议工具数据位置判定）按真实 JSON（结构化正文）对象键/数组层级逐段匹配的路径内工具结构/调用数据跳过递归控制字段扫描；原始对象键完整保留，字面 `[]` 或含点键不能冒充数组层级或多段路径：`tools[].parameters` 要求 `type=function`；`tools[].input_schema` 要求 `type` 为空且有非 null `name`；`tools[].function.parameters`、`functions[].parameters` 要求所在对象有非 null `name`；`input[].arguments` 要求 `type=function_call`；`messages[].tool_calls[].function.arguments`、`messages[].function_call.arguments` 要求所在对象有非 null `name`；`messages[].content[].input`、`input[].content[].input` 要求 `type=tool_use`。namespace（工具命名空间）子函数仅从真实 `tools[]` 根出发，每级祖先对象均为精确 `type=namespace` 且字符串 `name` 去空白非空，只沿其真实 `tools/children` 数组内直接工具对象传播；该位置只有精确 `type=function` 且字符串 `name` 去空白非空的 `parameters` 为数据。无名/非法祖先、函数下乱嵌 `tools`、非工具根或数组/对象伪装不能取得此资格，命名空间/子函数自身的真实控制字段及内建收费工具也不豁免。这些位置的完整客户数据，包括同名 `session/conversation` 等，仍全部计入正文上界，不当作远程历史；任意其他位置的同名 `parameters/arguments/input` 字段不豁免。`validateFundingTextShape`（文本形状校验）对非上述数据位置继续递归拒绝非 null/非空串的 `previous_response_id/conversation/session`、含非 null `prompt.id` 的托管提示、`background:true`（后台异步执行），以及 `modalities/output_modalities` 数组内任一非精确 `text` 值；真实媒体输入及没有独立费用上界的内建工具也拒绝。最终发送体 `body.model` 缺失或去空白后为空，仍返回 `strict_card_endpoint_unsupported`，不能用入口模型或已有冻结价替代实际执行模型。远程提示词和不可重建历史不能靠正文长度推导上界。缺验证上界返回 HTTP 400 `strict_card_endpoint_unsupported`；资金/计价暂不可用 503 `FUNDING_UNAVAILABLE`，额度不足 429 `INSUFFICIENT_QUOTA`。无限卡同样受有限执行截止、权限和企业预算约束，不自动允许无界业务。最终文本派发使用包含数值费率、阶梯、倍率与策略证据的深拷冻结价，首次最终派发的同模型价格生效；长连接每轮使用独立仓，后台记账只复制计价材料和原资金只读快照，不取得再次派发权限。`RecordUsage`（用量记账）在存在 `RequestFunding`（请求资金授权）时遇到 `ErrModelPricingUnavailable` 或其他计价错误直接返回，保留结算不确定性，不能生成零价终态释放卡/钱包占用；旧无资金授权路径的缺价兼容不适用于此处。异步支持按专属持久原价合同核验，不从通用价格函数推定所有媒体已支持。

使用主站 `resize-authorization/release-authorization/settle-authorization` 和 `GET /internal/billing/authorizations/:authorization_id` 恢复，不凭 Redis（缓存服务）TTL 认定释放。纯余额批次保持旧合同；含资金的混合用量批次逐事件事务且可安全重放，不保证整个 batch（批次）原子。

### 旧余额用量的本地入口与主站冻结全局入口（静态合同已核）

`gatewayLiteRequestMeta`（认证请求元数据）从实际本地分组写入 `GroupID`；首次 `ContextWithRequestMeta`（安装请求上下文）在原入口未填时以该值设置 `OriginalGroupID`。认证另从 KeySnapshot（主站密钥快照）设置 `EntryGatewayNodeID=key.KeyDefaultGatewayNodeID` 和 `EntryGlobalGroupID=key.Group.ID`，资金选择提交这两个主站身份；`legacy_balance`（旧余额）返回后继续原 lease（额度租约）流程，不附卡/强制余额资金结算字段。

旧余额请求收尾把 `OriginalGroupID` 写成 `requested_gateway_group_id`（原入口），把最终 `GroupID` 写成 `gateway_group_id`（实际承接分组）。真实用量修正优先使用这两个上下文编号，缺上下文分组时才回退本地 UsageLog（用量日志）；`ReportUsageCorrection`（交付用量修正）只为缺省字段补上下文。资金事件在该交付路径再由 `AttachFundingUsage`（附加资金结算信息）以原授权 `EntryGroupID` 恢复全局请求入口，不能让前面的本地路由元数据替代原资金入口。

主站新增 `guardFundingUsageTx` / `resolveFundingUsageGatewayGroupsTx`（资金保护/冻结节点分组规范化）仅对持久 `legacy_balance` 选择生效，规则如下：

| 上报情况 | 主站当前处理 |
|---|---|
| requested（请求入口）为正 | 只接受冻结全局 `EntryGroupID`，或冻结节点下同一冻结入口镜像的正值本地编号；统一成冻结全局编号后不再交通用解析器解释，低值全局编号也一样 |
| 本地 requested 与全局 requested 编号碰撞 | 查冻结节点和冻结入口这一行的精确映射；正确本地编号恰等于其他节点/分组全局编号时仍归冻结入口，冻结全局编号恰等于另一组本地编号时也仍归冻结入口；不能误认碰撞组。不属于原冻结全局/本地入口的声明拒绝，不依赖区域或名称猜测 |
| 入口镜像缺失 | 已携带冻结全局编号的 requested 可保留；本地 requested 缺精确镜像映射拒绝，不回退区域解析 |
| requested 为 0 或省略 | 保持省略；主站专用规范化不补入口编号，网关 `omitempty`（零值不序列化）继续有效 |
| actual（实际承接）`gateway_group_id > 0` | 必须有冻结节点下的当前镜像，接受该节点的全局编号或正值本地编号；同时命中时优先全局编号。可同节点重路由至其他分组，不强制等于入口；未命中冻结节点镜像的声明拒绝，实际分组无缺镜像保留例外 |

主站先核原请求、Key（接口密钥）、真实用户、网关、非空请求模型和付款主体；不接受改绑后新入口替代冻结选择。倍率是否明确按主站 `usageEventHasExplicitRateMultiplier`（倍率证据判定）判断：有限正倍率保留；零倍率须有有效冻结零倍率审计，或 `image/video/per_request` 计费模式且图片或视频数量为正的证据。仅写 `rate_multiplier:0` 不保证取得明确零倍率资格；缺少上述证据且镜像默认值为正时仍会补默认倍率。名称只在去空白后为空时补值，不用镜像重算费用。旧余额金额仍以 `actual_cents` 为权威，原个人/企业钱包、租约与真实员工合同保持不变；资金事件仍按 `actual_micro_cents` 和原授权分摊。

主站 `/internal/usage/report` 和 `/internal/usage/report-batch` 的存储层错误返回 HTTP 500；本节拒绝产生 `ErrFundingConflict`（资金合同冲突）时，`error=funding_authorization_conflict`。不能套用资金管理端点的 HTTP 409；批次失败仍须按返回的 `applied_count`（已应用数量）与原事件身份安全重放。

无持久 selection（资金选择）的事件，以及 `time_card`（时效卡）/`forced_balance`（强制余额）原授权结算，仍走主站原 general resolver（通用分组解析器）；后两者仍先核原授权全局入口，不增加本次旧余额专用的镜像缺失拒绝。通用解析器的 gateway/region（网关编码/区域）OR（任一条件匹配）仍在，本次仅同步旧余额限定修补，不能宣称通用风险已修完。两类事件的完整区别见 [主站冻结入口合同](../control-plane/API.md#legacy_balance旧余额用量入口分组规范化静态合同已核)。

实现依据：[认证与基础用量字段](internal/server/middleware/gateway_lite_api_key_auth.go)、[资金选择与旧租约分流](internal/server/middleware/gateway_lite_funding.go)、[原入口及交付前恢复](internal/gatewaylite/context.go)、[用量事件省略字段](internal/gatewaylite/types.go)、[真实用量修正来源](internal/service/gateway_lite_usage_correction.go)、[资金全局入口附加](internal/gatewaylite/funding.go)、[主站冻结节点规范化](../control-plane/internal/store/time_card_settlement.go)、[主站单条/批次解析分支](../control-plane/internal/store/store.go)。

**验收状态**：静态源码合同与限定主站真实数据库回归已核。2026-10-13 相同 412 份输入的准确修前覆盖产生 5 顶层中 3 失败、24 子例中 10 失败；最终实现 5 顶层/24 子例全部通过、零失败/跳过，原混合批次、精度/原来源退款和卡/强制余额缺镜像合同 3 顶层/2 子例全部通过。28 个准确自建库完成原进程与外部收尾，待处理为 0，独立只读复核均不存在。旧建库准备超时不是业务红，记录保留。见 [余额修复证据](../docs/modules/tokenav-upgrade-20261012/余额用量入口分组身份修复计划.md)；[接口同步清单第 45 项](../docs/modules/tokenav-upgrade-20261012/接口文档同步计划.md#本轮新增收口清单)已完成本限定回归。该结果不代替真实双系统网关交付、界面或部署验收。

### OAuth O4 专用委托与可靠用量消费

主站已注册 `/oauth/device_authorization` 和 `/oauth/token` 标准设备流程，及七个内部 POST：`/internal/oauth/invocations/resolve`、`reserve`、`bind`、`resize`、`settle`、`release`、`status`。详细字段以 [主站合同](../control-plane/API.md) 为准，`status` 是 lookup（不确定结果恢复查询）。普通 API Key 及身份 access token（身份访问令牌）不接受为此内部协议的付费委托凭证。

`tokenav_oa_` 专用凭证已由网关真实认证入口分流并实时向主站核验，随后进入费用 reserve（预留）与真实资金授权 bind（绑定）；不走普通 API Key 热缓存。主站 `resolve.key.available_gateways` 仅投影当前已获准且与用户同意编号一致的固定节点；网关仍以该节点的编号、代码及区域核验准入，不把空列表或默认节点当作授权。首期类型限定个人付款，企业委托尚无完整费用同意合同，按不支持拒绝。每个 HTTP 请求/每个长连接轮次分别检查模型、期限、总消费上限、RPM 和并发，取得 `force_authorization=true` 的有界卡/钱包授权。

`OAuthInvocationEndpointSupported`（委托端点校验）当前接受 `/v1/responses`、`/responses`、`/backend-api/codex/responses`、`/openai/v1/responses`、`/v1/chat/completions`、`/chat/completions`、`/v1/messages`、`/messages`；这是准入路径名单，不自动新建别名路由。已有 Responses WebSocket（响应长连接）的三个 GET 入口 `/v1/responses`、`/responses`、`/backend-api/codex/responses` 使用相同逐轮委托准入；新增 Codex 精确别名不包含其 `/compact`、`/input_tokens`、`/ws-turn` 等子路径或近似前缀。已静态核对 OpenAI 服务的 Responses、Chat 和 Messages 转换入口初次准入，以及 Responses WebSocket 每轮准入；仅当最终上游保留有效输出上限且价格可界定时才允许发送。此次别名修正的定向测试不替代修后真实长连接及结算验收。独立 Anthropic/Gemini/Antigravity 服务及媒体/实时/向量等未接专用准入的路径不据端点名单宣称可用；无委托资金准入或不支持的执行协议拒绝。

当前可靠用量消费已通过 `NewOAuthInvocationUsageSink(clientRef,clientRef)`（委托消费包装器）挂在队列实际交付端：先确认原资金用量在主站成功，再确认委托消费；两项任一失败或响应不明不 ACK（确认消费），持相同原请求、修订与快照重放。真实认证、上述 HTTP 初次准入、长连接逐轮准入与交付包装器已接，但其他协议和真实上游端到端验收仍受上述边界约束。

委托操作与恢复合同：

- `bind/resize/settle/release` 以原预留及 `operation_id` 冻结操作种类、完整输入指纹，含 `expected_version`（期望版本）。同编号只原样重放，不能用旧编号改为最新版本或金额；原 `reserve` 输入指纹可跨扩容、撤销/到期和终态恢复原记录，不重新占预算，也不恢复执行权。
- 实际 `clientRef`（运行时主站客户端）已实现 `/status`。用量消费在结算前查原预留，核请求/网关/委托/原资金编号并取当前版本；编号精确为 `{request_id}:oauth-settle:{revision}:v{expected_version}`。新版本使用新编号，同编号保持完整原输入，不能在丢失响应后篡改其冻结版本。
- 新结算修订是绝对最终费用，原资金必须先以同修订/金额结算。主站对旧修订核历史原金额及原资金绑定，只确认当前持久状态，不重复扣费、不回退 `spent_micro_cents`（委托累计消费）；消费端确认返回修订不少于输入，同修订金额相同，且终态为 `settled/released` 才 ACK。
- 双扩容顺序为真实资金成功后再扩委托预算，两侧均确认前不能执行更贵路径。资金已经扩容而委托失败/不明，不伪造缩回资金上界；已执行或结果未知保留占用和原快照。只有确认尚未发送且失败明确才可释放零成本请求。恢复保留原 pending（待确认）操作和 `expected_version`，只处理账务不确定性，不重新发送上游或转到个人/新卡。
- 释放输入固定 `actual_micro_cents=0/revision=0/terminal_confirmed=true`；先确认原资金 `released` 且消费 0，再释放已绑定委托。同资金身份必须一致，`settled` 或未知状态不能代替零消费释放证据；连接取消、超时或 TTL（缓存有效期）不能证明未消费。

委托实际截止不晚于身份 refresh family（刷新族）期限，以主站 `resolve.expires_at_millis` 为准。主站批准 pending（待确认）申请时重新核应用 scope（允许范围），授权码申请还核精确 callback（回调）；撤销本应用 invocation token 会撤原 grant（应用授权）及 backing Key（内部关联密钥），不清除在途账务。`introspect.token_use=access_token|refresh_token|invocation_token` 区分身份访问、刷新和付费委托，身份有效不能替代付费同意。

`UsageEvent.oauth_invocation_reservation`（用量事件的委托预留）及 `funding_snapshot` 保存非秘密原身份、金额、版本和终态证据，供可靠交付和后台只读核验。预留结构不含 `invocation_token`、普通 Key 秘密或浏览器凭据；它不是 credential（鉴权凭证）或 execution authority（再次执行授权），后台不能编辑它来扩权或重新执行。本轮没有新增原始事件/预留读取 HTTP 端点，字段存在不表示全部后台日志已展示。

未知生产结果 outbox（持久恢复事件箱）使用迁移 `241_gateway_lite_oauth_invocation_recovery.sql` 的独立表，已由生产路由初始化并启动；数据库不可用时准入拒绝，不能绕过持久栅栏。每次变更前保存原操作种类、版本及完整输入，派发前以 SQL CAS（条件更新）写不可逆 `may_dispatch` 栅栏；活请求先设 `DispatchUncertain`（派发状态不确定），持久更新确认成功后才置 `ExecutionStarted`（已进入派发）并清不确定标记。栅栏回复丢失无论实际提交与否，活请求都不得据取消、超时或表面未发送按零成本释放。

恢复消费者核保存摘要和行身份，以 claim（认领）与 generation（所有权代次）隔离旧请求：认领原子清除旧所有者并增加代次，旧请求检查点/派发及旧消费者写入必须匹配所有者或认领令牌与代次，陈旧写入拒绝。只有消费者取得所有权且持久 `may_dispatch=false`、快照未开始执行时，才能清除派发不确定标记，按原资金/委托操作收尾；可能已派发的记录仅查询原终态，尚无确定终态则进入 `attention`（待人工核验）保留占用，不重新调业务上游。`pending/completed/attention`（待恢复/已恢复/需核验）是内部恢复状态，不是新公开读接口。HTTP 请求和长连接每轮结束归还恢复所有权；`wire.go/wire_gen.go`（依赖注入/生成停机代码）在应用任务停止后，以独立 5 秒截止调用恢复消费者 `Stop`（停止并等待退出），失败保留数据库和缓存连接，成功才清默认实例并关闭基础设施。这些能力已有定向真实存储回归，仍须真实双系统、界面与上线验收。

实现依据：[主站客户端](internal/gatewaylite/oauth_invocation_client.go)、[双授权扩容](internal/gatewaylite/oauth_invocation_admission.go)、[零成本释放](internal/gatewaylite/oauth_invocation_control.go)、[仅账务恢复](internal/gatewaylite/oauth_invocation_recovery.go)、[SQL 持久栅栏与认领](internal/gatewaylite/oauth_invocation_outbox.go)、[恢复消费者与退出等待](internal/gatewaylite/oauth_invocation_outbox_worker.go)、[生产启动](internal/server/router.go)、[停机接线](cmd/server/wire.go)、[生成停机代码](cmd/server/wire_gen.go)、[非秘密预留类型](internal/gatewaylite/oauth_invocation_types.go)、[用量事件字段](internal/gatewaylite/types.go)、[可靠结算](internal/gatewaylite/oauth_invocation_usage.go)。

### Seedance（视频任务）查询、原身份与 settlement（可靠结算）

正式任务路由的前缀为 `/api/v3`、`/v3`、`/v1` 或无前缀：`POST {prefix}/contents/generations/tasks` 创建，`GET/DELETE {prefix}/contents/generations/tasks/:task_id` 查询或删除。普通 OpenAI 分组和显式解析到 OpenAI 的 composite（聚合分组）都使用 Seedance 端点对应的 OpenAI 目标平台校验；共享媒体处理器不再把 Seedance 生成请求误套为 Grok 目标。此修补不新增路由、请求字段或严格卡媒体支持。AI 审查启用时，媒体创建继续由前置能力门返回 HTTP 503 `ai_unsupported_protocol`，不调用审查或业务上游。

已实现任务创建前登记和创建后快照，以及状态/删除的原归属检查。`GET/DELETE` 各版本 `/contents/generations/tasks/:task_id` 使用 read-only（只读资格检查），不新取额度租约、不消费 RPM/TPM，不能因用户当前零余额卡住旧任务查询；仍校验原用户、原 Key、原企业付款主体、分组与原账号归属，缺失快照或归属不匹配返回 404。

创建前冻结服务器请求编号、计费模型/倍率、真实员工、付款主体、Key、入口、原资金只读快照及包含数值费率的 `ExecutionPricing`（执行价）。普通余额创建按客户请求模型优先、账号映射模型兜底的候选顺序（各自含现有别名规范化并去重），选择首个成功冻结的有效价，持久 `BillingModel`（计费模型）与 `ExecutionPricing.Model`（执行价模型）写同一候选。全候选缺价或无有效冻结价时创建上游派发前拒绝，当前处理器返回 HTTP 503 `api_error`；未知 token 成本的严格卡 Seedance 仍拒绝。完成查询恢复原 `RequestMeta`（计费身份）并安装独立原价仓，按真实 `completion_tokens` 使用创建价结算，不重新选择候选或回读轮询时热价。已有原执行价快照时，`RecordUsage`（用量记账）只保留创建冻结的计费候选和倍率，包括没有 `RequestFunding`（请求资金授权）的 `composite`（聚合分组）任务；不被后来的客户价、映射价或响应模型热价回退覆盖，计价错误直接返回，不能生成零价终态。原价缺失、损坏或模型不匹配在 claim（认领）前拒绝，不能用当前价补账。创建预登记已经存在时阻止重复派发，需恢复原任务，不以缓存到期或客户端重试证明可以重发。状态查询本身免费。认领带随机所有权令牌，仅自身能确认/释放；原费用可靠入队成功后才确认已结算，重复查询不重复收费。明确失败保留快照并释放自身认领，跨数据库/Redis 模糊提交则保留占用，按原请求编号恢复，不能凭过期认领再次制造新账。该原价恢复合同不表示未知 token 费用已经具备严格卡执行上界，边界见上表。

普通余额的远程任务完成还须提交创建时保留的 lease（额度租约）占用。当前新增专属完成路径正在验收：恢复原请求、Key（接口密钥）、真实用户/企业、付款主体、区域和原租约编号后，以同一 Redis Lua（缓存原子脚本）换算原始微美分、累计原零钱、将该请求的预留转成已用，并按原标记可靠入队。完成查询不借用查询请求的新编号或新租约，不修改冻结的 `legacy/idempotent_v2`（既有/幂等）结算模式，也不把逐轮 `Final` 字段当作通用异步开关。缺原请求、原租约已替换、身份或金额证据冲突时拒绝完成记账；不能降级为只上报或进程内零钱累计。

同请求重试必须使用相同原始金额、付款主体、租约和结算模式；已有可靠冻结金额与入队标记时复用，不再次累计零头或追加事件。所有可预测的身份、类型、金额和队列编号耗尽错误均在财务写入前拒绝；队列消息被删除后仍检查原 `last-generated-id`（最后生成编号），已经入队的精确重放不需要新编号。没有可信原始金额的历史状态保留待核，不能凭已出现 `corrected_actual_cents`（修正金额）猜测完成。`idempotent_v2` 的可重试 Redis 失败可以由 PostgreSQL outbox（数据库待发送事件箱）持久接管完整提交意图和原始金额；此时可靠接管不代表 Redis 已完成提交，恢复消费者仍须完成原子操作。专属持久事件同编号冲突时读取原封套，仅财务事实一致才允许幂等成功；旧封套缺提交意图、金额/身份/租约/模式/缓存前缀不一致、记录消失或损坏均拒绝，不覆盖旧行。仅计时遥测不同不构成财务冲突。`legacy` 保留原任务认领到期后的同编号恢复边界，不暗切换为 v2。新增内部意图及原始金额不进入主站用量 JSON（结构化接口数据），不扩展外部请求字段、严格卡支持或其他媒体路径。

**本次验收状态**：正式路由增强断言先实证原缺陷——6000 美分事件已上报，但原请求仍为 `reserved`、原租约已用为零。专属修补后，四前缀普通／聚合分组全组 2 个顶层／20 个子测试同轮通过；八个生命周期均核原请求已提交6000、原租约已用6000／预留0，重建、重复查询及删除后保持单次费用。新增单元 21 顶层／112 子和旧余额、异步媒体回归也已通过。以上使用本机真实路由与内存缓存测试服务，记录型主站／计费仓储及 SQL 替身不证明真实主站钱包、数据库并发或生产验收。独立复审另发现的残缺原身份现已在认领前拒绝：远程普通余额及授权卡都要求原元信息存在、请求编号去空白非空且用户编号为正；不借用轮询身份补值，本地路径不增加远程身份要求。新增真实完成链测试修前四损坏反例准确失败、三正常对照通过；最小修补后2顶层／7子全部通过，红绿仅该生产文件变化且测试不变，独立限定复审通过。最新身份守卫下，原完成链另跑6顶层／3子全部通过。真实 PostgreSQL 并发与 Redis 队列编号边界另轮4顶层／11子全部通过：已实际证明插入阻塞后原财务封套不变、最大编号及删除后空队列在写前拒绝、两种结算模式的最后编号精确重放不再推进零钱。此前 legacy 子例在夹具前置检查失败的记录保留，原错误原因未定；本轮仅补安全诊断，未改预算、断言或生产代码。准确自建库与缓存键的原收尾及独立只读核验通过。完整双系统、界面与上线仍须分别验收。详见 [Seedance 正式路由闭环验收计划](../docs/modules/tokenav-upgrade-20261012/Seedance正式路由闭环验收计划.md)。

实现依据：[逐轮权限](internal/server/middleware/gateway_lite_api_key_auth.go)、[严格帧接入](internal/handler/openai_gateway_handler.go)、[原生透传帧校验](internal/service/openai_ws_v2_passthrough_adapter.go)、[资金上界](internal/service/gateway_funding_bound.go)、[最终发送](internal/service/gateway_funding_dispatch.go)、[资金计价失败](internal/service/openai_gateway_service.go)、[委托可靠消费](internal/gatewaylite/oauth_invocation_usage.go)、[Seedance 创建候选价与持久快照](internal/service/seedance.go)、[计费候选顺序](internal/service/openai_model_alias.go)、[数值执行价冻结与校验](internal/service/execution_pricing_snapshot.go)、[Seedance 创建前接点](internal/handler/grok_media.go)、[Seedance 结算](internal/handler/seedance.go)、[原预留原子提交](internal/gatewaylite/async_reservation_usage.go)、[写前校验与账务入队脚本](internal/gatewaylite/async_reservation_usage_script.go)、[失败持久接管](internal/gatewaylite/usage_retry_outbox.go)。

## 同步生图媒体中转

- 当 `gateway_lite.media_proxy_enabled=true` 且节点具备稳定签名密钥和对象存储时，OpenAI Images（图片接口）与 Grok Images（图片接口）的同步非流式 JSON 输出中的生成图片 `url` 会由网关立即下载、校验并写入 `image-sync/` 命名空间，然后改写为网关自身的 `/v1/media/:token` 地址。客户只需访问网关域名，不需要直接访问境外图片源。
- `b64_json`、`data:` URL、输入/参考图片和非图片链接保持原样；下载失败时保留原 URL并记录内部诊断，不把一次成功的生图重复提交到上游。当前同步桥接只处理非流式 JSON；SSE 图片事件仍按原协议返回，待单独完成事件级缓存后再开启。异步图片任务继续使用既有 artifact（任务产物）下载接口。
- `GET /v1/media/:token` 只接受节点签发的 AES-GCM（认证加密）短期令牌，默认有效期 15 分钟（可配置 60 秒至 24 小时）。当前令牌是短期 bearer（持有者凭证）：会校验签名、过期时间和对象路径，但读取路由不强制核对用户/API Key/任务 scope（归属范围），也不提供单个同步对象的撤销状态。令牌过期或无效返回 `404`，对象读取失败返回脱敏的 `502`；响应带 `nosniff` 和私有缓存头，不携带客户或上游凭据。
- 上游回源仅允许 HTTPS、公开 DNS 地址和受支持的 PNG/JPEG/GIF/WebP/AVIF 文件头，并限制 32 MiB、超时和重定向次数。客户端媒体链接的 `gateway_lite.public_url` 支持管理员显式配置的 HTTP/HTTPS（普通/加密连接）绝对基础地址，包括本机、内网主机及带端口地址；无需额外放行开关，保留原协议与基础路径。配置校验与媒体代理构造器使用同一规则：长度不超过 512 字节，主机非空，端口如提供须为 1–65535，拒绝账号密码、查询参数（含仅有 `?` 的空查询）、片段及非法地址；为空时使用同源相对地址，不从 `Host` 或转发头推断。该公开地址只用于生成客户端链接，上游抓取的 HTTPS 与 SSRF（服务端请求伪造）防护规则保持不变。
- 同步对象使用现有本地/S3 兼容对象存储，无需额外引入 MinIO SDK；无论本地还是 S3，当前都由网关的 `/v1/media/:token` 路由读取对象，S3 的 `SignedURL`（预签名地址）接口不用于同步响应。后台清理任务按 `image-sync` 前缀和 TTL 删除过期对象（本地文件及 `.meta` 一并删除，S3 按分页删除），不会触碰异步图片任务对象；自定义对象存储未实现清理扩展时安全跳过。

## 企业付款主体契约

- 企业 API Key（接口密钥）的 `user_id（用户编号）`始终是真实请求员工，`organization_id（企业编号）`与 `billing_subject_type=organization/billing_subject_id（付款主体类型/编号）`决定余额、lease（额度租约）、账本和用量归属；个人 Key 保持 `billing_subject_type=user`。
- KeySnapshot（密钥快照）同时携带企业总并发、企业 RPM/TPM、成员月预算、成员并发、成员 RPM/TPM，以及企业与成员允许的网关、分组和模型。企业 Key 不继承员工个人 T0/T1/T2 等级、个人余额或个人专属倍率。
- 最终允许范围是企业策略、成员策略、网关/分组有效状态、模型价格和 effective policy（最终生效策略）的交集；企业策略或成员授权过期、缺失、撤销时 fail closed（失败即拒绝）。
- 企业 Redis（缓存服务）原子门禁同时约束企业总量和真实成员独立限额；请求完成、失败或断开后的并发释放按请求编号幂等，不能释放其他请求占用。
- 用量上报必须原样携带真实员工、企业付款主体、Key、网关、分组、请求模型和计费模型。网关不能根据客户端字段改写付款主体，也不能在企业租约失败后回退员工个人钱包。

## 特殊协议 RPM/TPM 计数语义

- 普通 HTTP/SSE（普通请求/事件流）在关键词路由完成后只消费一次 RPM/TPM（请求与 Token 限额）；账号 failover（故障切换）、上游重试和 invalid-request fallback（无效请求兜底）不重复计数。
- Responses WebSocket（响应长连接）的每个真实 `response.create` 都是独立用户提交：首轮在入口计数，后续轮次在本地策略通过后各计一次；同一轮 Priority（优先）降级、上游重连和重发复用本轮门禁结果。
- OpenAI Live（实时会话）只在创建提交时按 `session（会话）`估算 Token 并计数一次；Sideband（旁路）事件、续租、断开和结算不重复计数。
- Grok 图片/视频生成、编辑与扩展各按提交计数一次；视频状态和内容查询、后台任务、完成结算不计数，只执行无副作用资格复核。
- 异步图片由用户创建任务时计数；worker（工作线程）执行、重试、对象存储和结算不再次消费 RPM/TPM。
- 企业请求使用 organization（企业）、member（成员）、group（分组）和 key（密钥）的 Redis（缓存服务）原子门禁；企业原子能力不可用时 fail closed（失败即拒绝），不会回退消费员工个人计数桶。

分组在 config-push（配置主动推送）、config snapshot（配置快照）和 KeySnapshot（密钥快照）三条链路中统一携带 `billing_model_source（计费模型来源）`、`max_reasoning_effort（最大推理档位）`、`reasoning_effort_mappings（推理档位映射）`与 `allow_live（实时接口开关）`。轻量网关反向应用主站快照或恢复远端 Key 时必须完整还原这些字段，避免缓存命中后计费、推理或 Live（实时）能力回退默认值；effective policy（最终生效策略）校验和也包含这些字段。

`GatewayAccountSnapshot（网关账号快照）`使用 `group_priorities` 映射传输每个 `group_id（分组编号）`对应的 `account_groups.priority（账号在当前分组内的绑定优先级）`，该字段作为绑定元数据继续保留，但不参与 `strict_priority（严格优先级）` 分层；严格模式只读取账号全局 `priority`。快照按节点可发布分组过滤时，`group_ids`、`group_priorities` 和 `group_model_mappings（分组专属模型映射）`必须同步删除未发布分组，不得留下错位绑定。`GatewayGroupSnapshot（网关分组快照）`使用 `scheduling_mode` 传输 `default`、`random` 或 `strict_priority`，缺失时按 `default` 兼容。

## 分组账号调度规则

管理员创建或编辑分组时可传 `scheduling_mode`：

- `default`：默认值，保留优先级、负载感知、LRU（最久未使用）和普通会话粘性。
- `strict_priority`：仅适用于 `openai`、`grok` 分组，分组只限定参与账号，调度始终按账号全局 `accounts.priority（账号优先级）` 从小到大逐层执行。最高层的全部合格账号都无法原子取得并发槽位后，才进入下一层；同层继续按负载、队列、错误率和首字延迟均衡，普通会话粘性仅作为同层偏好。其他平台提交该值会返回 HTTP `400`、`UNSUPPORTED_GROUP_SCHEDULING_MODE`，不会出现“保存成功但运行时无效”。
- `random`：普通请求在通过平台、模型、额度、RPM（每分钟请求数）、并发和运行时保护门禁的账号中完全随机；不按优先级、负载率或最近使用时间排序。

`random` 下不读取普通会话粘性或 `previous_response_id`（上次响应编号）账号绑定，每次请求均重新随机；若上游响应状态只存在于原账号，续链可能由上游返回找不到响应。随机模式不会绕过模型路由、关键词账号排除、额度保护或并发门禁。分组列表与详情响应也会返回 `scheduling_mode`。

`strict_priority` 不受高级调度总开关影响，避免管理员显式选择后静默回退为软优先级。`previous_response_id` 和已建立的 WebSocket（长连接）等协议强绑定仍保持原账号；普通 `session_hash（会话哈希）` 曾落到低优先级层时，高层恢复后会回到高层。并发可用性以 Redis（缓存服务）原子抢槽结果为准，批量负载快照只负责同层排序，不得作为提前降层依据。
## 时间与时区约定

- 数据库时间点、Unix 时间和 RFC3339（标准时间字符串）继续保存/传输绝对时间，避免跨节点排序、过期和幂等语义变化。
- 轻量网关后端默认 `timezone=Asia/Shanghai`，启动日志和运行日志也显式使用该时区；后台页面的人类可读时间、日期筛选、`datetime-local（本地日期时间控件）` 和导出文件名统一按 `Asia/Shanghai（中国上海时区）` 解释，不跟随管理员电脑或服务器所在国家漂移。数据库和 API（接口）的绝对时间点仍按 UTC/Unix 时间保存与传输。
- 渠道健康日聚合、Ops（运维）日指标、内容审计的纯日期筛选，以及余额/账号额度告警的“每日一次”去重键，均以中国上海 `00:00` 为日界线；RFC3339（标准时间字符串）筛选仍按其中携带的绝对时区解释。
- AI 运营审查每日金额/token 预算是明确例外：当前 SQL（数据库）桶按 UTC 00:00（上海 08:00）切换，见本文首部；展示时间仍按上海时区，不能把展示日期当预算日界线。
- 单个分组的 `peak_timezone（高峰倍率时区）` 是独立计费策略字段，仍按该分组显式配置执行，不会被后台展示时区覆盖。

## 对外用户接口

### 管理员分组平台契约

- `POST /api/v1/admin/groups` 与 `PUT /api/v1/admin/groups/:id` 的 `platform（平台）`支持 `anthropic`、`openai`、`gemini`、`antigravity`、`grok`、`kimi`、`zhipu`、`deepseek` 和 `composite`；未知值会在参数校验阶段返回 HTTP `400`，不会写入分组或账号绑定。
- Kimi、智谱 GLM、DeepSeek 分组的新建与编辑使用各自原生平台身份，默认展示类别和内置图标也按平台生成。三类国产平台不猜测静态模型全集；自定义 `/v1/models` 候选来自已绑定账号的实际模型声明、模型映射或管理员手工输入，避免错误展示 Claude 模型。

### 管理员分组快速绑定账号

- `POST /api/v1/admin/groups/:id/accounts/bind`：请求体为 `{ "account_ids": [1, 2], "confirm_mixed_channel_risk": false }`。服务端先一次性校验账号存在、平台匹配和混合渠道风险，再批量绑定；已有绑定不会重复插入，账号原有其他分组不会被移除。校验失败时不会写入任何绑定。

### 分组模型白名单

- 分组的 `models_list_config` 支持 `enabled` 与 `models`。启用且至少选择一个模型时，`GET /v1/models`、`/v1beta/models` 等目录响应只返回所选模型（支持精确名称和末尾 `*` 通配符）；关闭时返回全部可达模型，并包含目标账号可调度的分组模型映射源别名。管理端候选接口也会把已保存的分组映射源别名纳入可选模型。
- 同一规则也在请求入口强制执行：JSON（包括 `model` 和 Live `session.model`）、表单、Multipart（多段表单）、查询参数以及 Gemini 路径模型都会在额度租约、账号调度前校验；不在列表中的手工请求返回 HTTP `403`、错误码 `GROUP_MODEL_NOT_ALLOWED`，不会产生费用或上游请求。
- Responses WebSocket（响应长连接）握手后的首帧和每个后续 `response.create` 都再次校验模型；二进制 JSON 首帧与文本首帧规则一致，非 JSON 音频帧不被当作模型请求。开启白名单但未选择模型会被拒绝保存；历史上已存在的空白名单按“全部拒绝”处理，避免绕过限制。

### 账号成本模型

- 账号用量统计使用最终实际发往上游的 `upstream_model`（上游模型）查采购价；新记录不会用客户的 `requested_model`（请求模型）作为价格回退。已知上游模型但暂未解析到价格时按 `0/unknown（零值/未知）` 记录，只有历史记录缺少 `upstream_model` 时才兼容回退 `total_cost`。客户侧 `model` 与内部 `upstream_model` 分开保存，分组/渠道/账号映射不会让账号成本误用别名价格。
- 解析出的原始采购成本再乘 `account_rate_multiplier`（账号成本倍率）形成账号统计费用；该口径只用于账号采购成本、额度窗口和管理员统计，不改变客户侧 `total_cost`/`actual_cost`（销售计费与钱包扣费）。
- 用量上报可携带 `account_stats_cost`（原始账号采购成本，尚未乘账号倍率）。字段为 `null` 时表示没有新的成本证据，修正事件会保留数据库已有值；显式 `0` 表示已确认零成本，必须覆盖旧值。该字段不会写入客户账本或 `actual_cents`。
- 开启 `apply_pricing_to_account_stats`（将模型定价应用到账号统计）时，先按最终上游模型匹配当前分组的自定义价格（包括按次、图片、视频和分档价格），未命中再按该上游模型使用 LiteLLM/fallback 价格；不会直接复用用户侧已扣费用。

### 模型目录读取保护

`gateway.models_list_read_max_bytes`（默认 `8388608`，即 8 MiB）统一限制通用 `/v1/models`、Codex 模型清单和 Antigravity 可用模型接口的响应读取大小。超过上限时停止解析并返回上游模型同步错误，不会继续把完整响应读入内存；可通过 YAML（配置文件）或同名环境变量 `GATEWAY_MODELS_LIST_READ_MAX_BYTES` 调整，必须为正数。

gateway-lite（轻量网关）保留 sub2api 原有 AI 网关接口，例如：

- `/v1/messages`
- `/v1/chat/completions`
- `/v1/responses`
- `/v1/alpha/search`
- `/v1beta/*`

运行探针：

- `GET /health`：`liveness probe（存活探针）`，只表示 gateway-lite 进程仍能处理 HTTP 请求，固定返回 `200 {"status":"ok"}`。
- `GET /ready`：`readiness probe（就绪探针）`，并行检查 PostgreSQL（关系数据库）和 Redis（缓存服务）。全部可用时返回 `200` 和 `status=ready`；任一依赖不可用或 2 秒内未完成时返回 `503` 和 `status=not_ready`。响应只返回 `ok`、`unavailable` 或 `timeout`，不会暴露数据库连接串、密码或底层错误详情。

`/health` 与 `/ready` 含义不能混用：进程仍存活但数据库或 Redis 故障时，`/health` 继续返回 200，`/ready` 返回 503，供反向代理或容器编排停止向该实例分配新流量。

主站 runtime health（运行健康）心跳中的真实模型探针会按 OpenAI 账号认证形态分流：普通 OAuth（授权）账号使用现有 access token / refresh token（访问令牌/刷新令牌）链路，Agent Identity（代理身份）账号不读取或刷新 OAuth Token，而是为本次探针动态生成 `AgentAssertion（代理签名）`。普通 OpenAI OAuth 账号同时缺少两种授权材料时，本次探针会记录稳定错误并尝试下一个候选；不在健康监控中改写账号状态。合法 Agent Identity 即使没有这两个 Token 也保持可调度；只有私钥、却没有 `auth_mode=agentIdentity` 的不完整账号不会被猜测成 Agent Identity。探针错误和后台响应不会返回私钥、Token 或完整签名。

这些接口用于接收用户的 AI 请求，并转发到上游模型账号。

主站 API Key（接口密钥）的 `ip_whitelist/ip_blacklist（IP 白名单/黑名单）`随 `KeySnapshot（密钥快照）`下发；普通/未知用途进入 Redis（缓存服务）热缓存，精确 `token_use=agent_api_key` 的应用密钥按前文实时鉴权合同逐请求及逐轮回源、不使用成功快照缓存。网关在额度租约、并发占位和上游调度之前校验：黑名单优先，同一 IP 同时命中黑白名单仍拒绝；白名单非空时必须命中；两个名单都为空时不增加 ACL（访问控制名单）判断。规则只接受单 IP 或 CIDR（网段）。客户 IP 使用请求开始时冻结的 trusted proxy（可信代理）策略：默认只信任 TCP 对端，管理员开启转发 IP 后仍要求实际对端命中 `server.trusted_proxies（可信代理列表）`才接纳自定义转发头，公网直连客户端不能伪造 Header（请求头）绕过规则。ACL 修改复用 `key:snapshot（密钥快照）` outbox（事件箱）主动失效，轮询继续作为恢复通道。

### gateway-lite 容量保护与 HTTP 429

`run_mode=gateway-lite` 时，轻量网关默认启用实例级 overload protection（过载保护）：最多同时处理 1024 个用户模型请求，另允许 1024 个请求等待槽位，最长等待 10000ms。SSE（事件流）和 WebSocket（长连接）会占用槽位直到真实连接结束。`/health`、`/ready`、`/api/v1/*` 后台接口、静态资源和主站内部通信不进入该限制。

进入过载保护的模型请求会返回 `X-Gateway-Overload-Wait-Ms（网关过载等待毫秒数）` 响应头；直接取得槽位时为 `0`。该时间发生在鉴权前，应单独观察，不能归入 auth（鉴权）、routing（调度）或 upstream（上游）耗时。

等待区也已满或等待超时时返回：

```http
HTTP/1.1 429 Too Many Requests
Retry-After: 1
Content-Type: application/json

{"error":{"type":"gateway_overloaded","code":"gateway_overloaded","message":"网关当前请求过多，请稍后重试"}}
```

429 表示请求尚未进入账号调度、额度租约和上游调用，不产生 usage（用量）或费用。客户端应按 `Retry-After` 换其他同线路网关或稍后重试，不能无限立即重试。

### gateway-lite 管理 API 独立限流

`run_mode=gateway-lite` 时，`/api/v1/admin/*` 使用进程内滑动窗口限流：同一已登录管理员默认每分钟最多 `240` 次管理请求，其中 dashboard/usage（看板/用量聚合查询）最多 `60` 次。管理员可在后台选择“默认、宽松、严格”三档或关闭保护；自定义数字仍由服务端校验。配置读取使用 60 秒内存快照和 stale-on-error（失败保留旧值），数据库短暂异常不会突然放开或锁死后台。超过限制返回 HTTP `429`、`Retry-After（建议重试秒数）`，以及 `{"code":"RATE_LIMITED","message":"管理请求过于频繁，请稍后重试"}`。

该限流只挂载轻量网关管理后台，按管理员身份隔离；没有身份时按真实客户端 IP 隔离。它不访问 Redis（缓存服务），不挂载 `/v1/*`、`/responses` 等用户模型请求路径，因此不会给 AI 请求热路径增加锁、网络查询或计费影响。

后台运行配置与入口拒绝观测接口仅在 `run_mode=gateway-lite` 注册：

- `GET /api/v1/admin/ops/runtime/summary`：只读返回轻量网关当前进程真正生效的 PostgreSQL/Redis 连接池、计费关键/非关键队列、用量队列与批量上报、请求/流式超时、WebSocket（长连接）和高级调度组合值；顶层 `composite_groups_enabled（聚合分组总开关）`供管理员分组表单在保存前判断该能力是否可用。
- `GET /api/v1/admin/ops/runtime/panel-rate-limit`：读取最终生效的限流设置和当前进程命中统计。
- `PUT /api/v1/admin/ops/runtime/panel-rate-limit`：更新总开关或预设；请求字段为 `enabled`、`preset`，仅 `preset=custom` 时使用 `user_rpm`、`heavy_rpm`、`window_seconds`。
- `POST /api/v1/admin/ops/runtime/panel-rate-limit/reset`：删除数据库覆盖值并恢复默认档位。
- `GET /api/v1/admin/ops/runtime/openai-scheduler`：返回下一次 OpenAI 请求会冻结使用的高级调度不可变快照；包含修订号、预设、Top-K、订阅池偏好、九项软权重和固定只读硬约束，不含账号凭据。
- `PUT /api/v1/admin/ops/runtime/openai-scheduler`：使用 `expected_revision` 原子更新高级调度策略；同时提交 `preset`、`advanced_enabled`、`sticky_soft_scoring_enabled`、`top_k`、`prefer_subscription_pool`、五项基础权重、重置/额度/采购成本权重、`upstream_cost_enabled` 与会话粘性权重。权重范围 `0-2`，Top-K 范围 `1-1000`，修订冲突返回 `409`。
- `POST /api/v1/admin/ops/runtime/openai-scheduler/reset`：请求 `{ "expected_revision": n }`，原子恢复安全默认并关闭高级/实验开关；恢复同样生成新修订号，不删除审计历史。
- `GET /api/v1/admin/ops/runtime/billing-cache`：读取计费缓存当前档位、修订号、关键/非关键实际 worker（工作协程）、buffer（缓冲区）、实时队长、延后区和写入超时；不返回 Redis 地址或凭据。
- `PUT /api/v1/admin/ops/runtime/billing-cache`：使用 `expected_revision` 和 `preset=low|standard|high` 热切换计费队列。管理界面只提交档位，不提供数字输入；服务端仍对可选显式覆盖做严格范围校验。
- `GET /api/v1/admin/ops/ingress-rejections?minutes=60`：查询最近 `1-1440` 分钟入口请求数、拒绝数、拒绝率、Top 分类和一分钟趋势。
- `GET /api/v1/admin/settings/codex-client-admission`：读取 Codex client admission（Codex 客户端准入）内存生效策略和脱敏聚合。
- `PUT /api/v1/admin/settings/codex-client-admission`：保存固定客户端预设、版本边界和 audit/enforce（只审计/拦截）模式。
- `GET /api/v1/admin/ops/runtime/server-timing`：读取默认采样率、临时诊断截止时间和累计采样数。
- `POST /api/v1/admin/ops/runtime/server-timing/enable`：临时全量开启分段耗时；body（请求体）只接受 `{"preset":"5m|15m|30m|60m"}` 四个快捷档位，省略时为 `15m`。
- `POST /api/v1/admin/ops/runtime/server-timing/disable`：立即关闭临时全量诊断并回到默认采样率。

入口观测只保留节点、稳定拒绝分类、分钟时间桶和计数；不保存 path（请求路径）、IP、Header（请求头）、Body（正文）或完整 API Key（接口密钥）。前端运维页使用上述接口提供总开关、默认/宽松/严格三档快捷按钮、自定义数字、一键恢复和趋势卡片，不要求管理员编写限流表达式。`panel-rate-limit` 配置读取、修改和 `reset（恢复默认）`三个已鉴权路径不受自身限流影响，确保管理员即使命中 `429` 也能立即关闭保护或恢复默认；这类绕过只增加匿名 `recovery_bypass_total（恢复通道次数）`计数。普通模型请求的额外工作只有内存 atomic（原子）计数；只有真实拒绝才更新分钟聚合。

`runtime/summary` 只在已登录管理员路由组下开放，每次刷新复用 `Handlers.RuntimeMetrics（现有运行指标）`，只拷贝显式白名单队列字段；不会序列化完整 `SystemSettings`。响应不会包含密码、Token（令牌）、API Key（接口密钥）、Cookie（登录凭据）、代理地址、带凭据 URL 或完整数据库/Redis 连接串；后台读取设置失败时只标记 `advanced_setting_available=false`，不回显底层错误原文。连接池和请求超时保持只读；只有 `/runtime/openai-scheduler` 与 `/runtime/billing-cache` 的显式白名单允许在线修改。调度策略采用 PostgreSQL CAS（比较并更新）、结构化审计和进程内不可变快照：持久化失败不换快照，多实例最长约 5 秒后台收敛，请求开始后不受策略热更新影响。`previous_response_id` 始终硬绑定，账号健康/能力/额度/并发始终是硬门禁，采购成本只影响选账号而不参与客户销售计费。

计费队列热更新请求示例：

```json
{
  "expected_revision": 7,
  "preset": "high"
}
```

成功响应的 `data` 包含 `revision`、`preset`、`available_presets`、`critical_worker_count`、`critical_buffer_size`、`critical_queue_length`、`critical_queue_capacity`、`best_effort_worker_count`、`best_effort_buffer_size`、`best_effort_queue_length`、`best_effort_queue_capacity`、`critical_active_workers`、`best_effort_active_workers`、`best_effort_deferred_length`、`best_effort_deferred_capacity` 和 `timeout_milliseconds`。切换时先发布新工作池，再完整排空旧工作池；配置通过临时文件原子替换持久化。修订号过期返回 HTTP `409 billing_cache_revision_conflict`；持久化失败返回 HTTP `500`，并在返回错误前把运行中的工作池恢复到原档位，所以失败响应绝不代表新档位已经生效。

`gateway_lite.server_timing_sample_basis_points（分段耗时万分比采样率）`默认 `0`，`100=1%`、`10000=100%`，范围 `0-10000`。命中采样的客户模型请求会在首次响应中返回标准 `Server-Timing`，流式请求结束后的结算阶段通过 `Server-Timing-Final` trailer（尾部响应头）返回。公开阶段名固定为 `total（总耗时）`、`auth（鉴权缓存）`、`quota（额度租约）`、`route（调度）`、`firstbyte（上游首字）`、`settle（结算入队）`；任意自定义阶段名都会被丢弃，不暴露 PostgreSQL/Redis 名称、账号编号、上游主机、lease 编号或内部错误。临时开启最长 60 分钟，到期自动恢复；开启和关闭都会进入管理员操作审计。默认关闭路径不分配 collector（采集器），不增加数据库或 Redis 调用。

gateway-lite 的请求头和正文分别受限：`server.max_header_bytes（请求头上限）`兼容默认 `64 KiB`，允许配置 `8 KiB-1 MiB`，同时传给 Go `net/http（网络服务层）`的 HTTP/1 上限和 HTTP/2 header-list（请求头列表）上限；应用层也使用同一值提前检查。能够进入 Gin（路由框架）的超限请求会在鉴权、账号调度、lease（额度租约）和上游请求之前返回 HTTP `431`，错误码为 `request_header_too_large`，并只把 `header_too_large` 分类加一；计算过程不拼接、不复制、不记录 Header 值。明显超过网络层读取窗口的请求可能由 Go 在进入 Gin 前直接返回 `431`，因此不会进入应用聚合，这是运行指标的明确边界。

正文使用两档独立上限：`gateway.text_max_body_size（纯文本 JSON 正文上限）`兼容默认 `32 MiB`，只用于 `/v1/embeddings`、`/embeddings` 和三个 `/alpha/search` 入口；`gateway.max_body_size（多媒体正文上限）`兼容默认 `256 MiB`，继续用于 Responses/Messages、Gemini、图片、视频及其他可携带内联媒体或上传内容的路径。文本上限必须大于 `0` 且不得高于多媒体上限；`server.max_request_body_size（服务级请求体上限）`仍是所有路径的最终兜底。GET（读取接口）和 WebSocket（长连接）握手不携带正文，不受文本正文上限影响。正文超限仍由 `MaxBytesReader（限长读取器）`保持原协议的 HTTP `413` 响应，本地确认 `MaxBytesError（正文超限错误）`后只聚合一次 `body_too_large`；中间件与协议 handler（处理器）通过匿名错误标记避免同一请求重复计数，合法请求由上游返回的 `413` 也不会被误计为入口正文超限。

`invalid_path（非法路径）` 只在 Gin（路由框架）没有匹配到任何本地路由且最终返回 `404` 时记录。已注册的模型接口即使上游或本地能力判断返回 `404`，也保留原响应但不进入非法路径统计，避免把真实渠道错误误判成入口扫描。

### 当前 Key 倍率自省与上游探测

- `GET /v1/sub2api/billing`：使用当前用户 API Key 鉴权，只返回该 Key 的 Token 销售倍率声明。该请求不申请 lease（额度租约）、不扣款、不写 usage（用量），也不更新 Key 最近使用时间；响应使用 `Cache-Control: no-store`，每个 Key 默认每分钟最多 60 次查询。响应包含 `group_rate_multiplier（分组默认倍率）`、可选 `user_rate_multiplier（用户覆盖倍率）`、`resolved_rate_multiplier（覆盖后的基础倍率）`、`peak_rate_enabled/start/end/timezone（高峰策略）`、`applied_peak_multiplier（本次实际叠加值）`、`effective_rate_multiplier（最终 Token 倍率）`、`policy_version（冻结策略版本）` 和 `observed_at（解析时间）`。用户倍率是覆盖分组倍率，不是再次相乘；高峰倍率最后相乘。

高峰倍率契约必须在本地分组、scheduler Redis cache（调度缓存）、主动上报主站的 group snapshot（分组快照）、主站 Key snapshot（密钥快照）和请求鉴权之间完整保留 `peak_rate_enabled/peak_start/peak_end/peak_rate_multiplier/peak_timezone` 五个字段。请求热路径最终以 gateway-lite（轻量网关）本地调度缓存中的当前分组为权威；旧版主站只把字段放入 `group.config` 时仍兼容读取，但不得因任一 mapper（映射器）漏字段而静默退回标准倍率。
- `POST /api/v1/admin/accounts/:id/upstream-billing-probe`：对显式标记 `extra.upstream_provider=sub2api` 的 OpenAI、Kimi、智谱或 DeepSeek API Key 上游执行一次手动倍率探测。地址、代理或凭据变化会使旧快照立即失效；adaptive（自适应）国产账号使用其 Chat 协议地址访问账单端点。快照以 CAS（比较并交换）保存 last-good（最后成功值），失败不会把倍率猜成 0 或 1。
- `PUT /api/v1/admin/accounts/:id/upstream-billing-probe`：设置账号级自动探测开关，请求体为 `{"enabled":true|false}`。只有上述 OpenAI-compatible（OpenAI 兼容）API Key 且显式标记为 Sub2API 的账号允许开启；普通 API、OAuth（授权）账号和其他平台拒绝开启。

探测倍率只表示采购成本事实，不覆盖账号手工倍率，也不修改客户分组倍率、钱包、账本、lease（额度租约）或主站销售计费。管理员显式开启成本感知调度后，它可以作为健康、模型、额度和并发硬门禁之后的软排序信号；缺失、失败且 last-good 已过期、unsupported（不支持）或非法数据一律按中性分处理，不猜 0 或 1。OpenAI、Kimi、智谱和 DeepSeek 的创建/编辑账号页面均使用“普通 API / Sub2API”点击选项显式声明上游类型，不根据域名猜测。

自动任务默认关闭；全局开关和账号开关必须同时开启。默认每 30 分钟、单批最多 20 个账号、并发最多 4 个，并在周期上增加 ±10% jitter（随机抖动）。手动与自动探测共用 `singleflight（并发合并）`，同一账号不会因同时点击和定时任务而重复请求。多节点通过 Redis compare-token（令牌比较）Leader lock（主节点锁）选出单一执行节点，75 秒租期、20 秒续租；Redis 不可用、续租失败或失去所有权时 fail-closed（失败关闭），立即停止领取后续任务，不回退 PostgreSQL（数据库）锁或本地锁。

`GET /api/v1/admin/settings` 与 `PUT /api/v1/admin/settings` 新增：

- `upstream_billing_auto_probe_enabled`：全局自动探测开关，默认 `false`。
- `upstream_billing_probe_interval_minutes`：周期，范围 `5-1440`，默认 `30`。
- `upstream_billing_probe_batch_size`：单批账号数，范围 `1-20`，默认 `20`。
- `upstream_billing_probe_concurrency`：探测并发，范围 `1-4`，默认 `4`。
- `openai_upstream_cost_scheduling_enabled`：是否把可信采购倍率加入 OpenAI 调度软排序，默认 `false`。
- `openai_upstream_cost_weight`、`openai_quota_headroom_weight`、`openai_prefer_soonest_reset_weight`：三项软权重，均为 `0-2` 的有限数，默认 `0`。

请求热路径只读取进程内 atomic（原子）设置快照和 scheduler account snapshot（账号调度快照）中的 `extra`，不会为软权重新增数据库、Redis 或上游 HTTP 请求。成本只在同一账号优先级内至少存在两个可信且不同的倍率时归一化；仅一个已知值或全部相同时保持中性。额度余量取总/日/周和新鲜 Codex 5h/7d 窗口中最紧的剩余比例；重置偏好只读取可信未来时间。普通日/周固定重置时间同时携带 `quota_reset_updated_at（重置快照更新时间）`，缺失、来自未来或超过 8 天的快照按未知处理；任何未知或超过 2 小时的 Codex 快照也保持中性，不排除账号。

### v0.1.166 请求兼容与计费口径

- OpenAI passthrough（透传）账号切换到非透传账号时，会从本次派生请求体中整轮移除其他提供商生成的加密 `reasoning（推理）` 项；原始请求体保持不变，同模式重试不清理。
- Responses → Chat Completions（响应转聊天补全）桥接会把已完成 reasoning item（思考项目）的明文 summary（摘要）按 item ID 缓存到 Redis 7 天；后续历史只有 `encrypted_content（加密内容）`时按同一 ID 恢复 DeepSeek thinking（思考模式）要求的 `reasoning_content`。明文历史会自愈回写并刷新期限；缓存未命中、超时或 Redis 不可用时 fail-open（失败放行），不阻断客户请求。
- Responses WebSocket（响应长连接）每个 `response.create` 独立解析渠道映射、请求模型和最终上游模型；每轮用自己的计费模型写入用量日志。已建立连接的账号不支持新模型时，使用 WebSocket `1008` 提示重连，不把账号误记为故障。
- usage log（用量日志）始终保留非空 `upstream_model（最终上游模型）`，即使它与公开模型同名，便于审计渠道映射和账号映射。
- 经第三方代理替换 User-Agent（客户端标识）的 Claude Code 请求，只要仍携带合法 billing attribution（计费归因）system block（系统块）和 metadata user ID（元数据用户编号），仍按真实 Claude Code 处理，避免破坏 prompt cache（提示缓存）。
- Gemini pool mode（号池模式）的可重试错误恢复同账号重试/换号；名为 `web_search` 的普通 Hermes function（客户端函数）不会被误转为 Gemini 服务端搜索。
- composite（聚合分组）前缀路由的 `upstream_model` 留空时，透传本次具体请求模型；精确路由和显式上游模型行为不变。
- `CONFIG_FILE` 可显式指定 YAML（配置文件）绝对路径，并优先于 `DATA_DIR` 和默认搜索目录。
- 默认模型目录增加 `gemini-3.6-flash`；Codex Responses / Anthropic 协议转换支持 namespace（命名空间）工具 delta（增量）、数组形式函数结果、`additional_tools（附加工具）` 和空输入 schema（结构定义）。

### v0.1.169 Claude Code 与邮件兼容

- Claude Code validator（校验器）在保留 `User-Agent`、`X-App`、Beta、版本和 metadata（元数据）严格门禁的前提下，识别官方 `auto security-monitor classifier（自动安全监控分类器）`单块提示结构，并兼容分类器输出中插入 `<category>` 元素；普通用户正文不能仅凭关键词冒充官方客户端。
- SMTP（邮件发送）统一使用 RFC/MIME（标准互联网邮件格式）：信封地址由 `net/mail` 严格解析，中文显示名和主题使用标准编码，HTML 正文使用 quoted-printable（可打印字符编码），并自动生成 `Date` 与唯一 `Message-ID`。地址中的 CR/LF 直接拒绝，显示名和主题中的异常换行被净化，防止 header injection（邮件头注入）。

### v0.1.170 选择性升级兼容口径

本轮没有新增或删除公网 API（接口）路径。轻量网关选择性吸收官方 `v0.1.170` 中会影响请求、流式计费和客户端兼容的修复：

- Anthropic（协议）流在上游已给出 input/cache usage（输入/缓存用量）后中断时，保留已观测用量并交给既有幂等计费链路；可安全 failover（故障切换）的请求仍不携带部分用量，避免重试成功后重复计费。
- OpenAI 流内 `response.failed` 若语义为 `429 rate limit（限流）`，按账号 pool mode（号池模式）策略同账号重试或换号，并保留响应头；普通流内错误继续按 `502` 处理，HTTP 200 流的配额头不会被误写成账号冷却状态。首个语义输出前出现的 `response.failed` 或顶层 `error` 事件会先进入管理员 error passthrough（错误透传）规则；命中时按规则返回指定状态、类型和中文文案，未命中且错误可重试时才安全换号。Chat Completions 与 Messages 的普通流、缓冲流使用同一规则。
- OpenAI OAuth（授权账号）普通 Responses 请求默认保留 Codex namespace（工具命名空间）；仅 compact（上下文压缩）或账号显式开启 `extra.openai_responses_flatten_namespaces=true` 时使用旧摊平兼容路径。跨账号恢复会移除账号绑定的加密 `compaction/compaction_summary（压缩摘要）`。
- Codex 透传缺少 `instructions（系统指令）` 时补入模型对应默认指令；Claude Code 官方分类器可携带多个 system（系统）块；Responses 转 Chat Completions（聊天补全）时把工具输出图片安全移动到后续 user（用户）消息。
- Grok Responses（响应接口）流中的厂商 `event: ping` 计费/保活帧会转换为标准 SSE（事件流）注释，严格客户端不再因未知事件中断；正文、终止事件和 usage（用量）保持原样。
- Grok OAuth（授权账号）在 JWT（身份令牌）没有明确档位时，只使用 24 小时内由 `grok-4.5` Responses（响应接口）产生的配额窗口区分 SuperGrok 与 Heavy；`8300` 请求或 `53,000,000` Token（令牌）的窗口判为 Heavy。其他模型、缺失模型、过期快照和后续非 4.5 快照不会刷新这份证据；JWT 明确档位始终优先。
- Grok 上游返回明确的 model capacity（模型容量不足）、`engine_overloaded` 或 `server_busy` 时，普通账号只对“账号＋实际上游模型”安装 3 分钟内存冷却，同账号其他模型继续调度；即使状态码为 `429`，也不会写成整账号限流或团队级模型限流。pool mode（账号池模式）只保存脱敏配额快照并交给请求内重试，不写本地账号或模型健康状态。普通 `429`、无容量语义的 `5xx`、内容策略拒绝和客户端参数错误继续按原规则处理。
- 官方分组利润控制和账号倍率自动同步不回迁：主站继续作为价格、金额、租约与最终计费权威，避免出现两套计价来源。

### v0.1.171 选择性升级兼容口径

本轮没有新增或删除公网 API（接口）路径。OpenAI 请求的失败切换规则统一如下：

- 上游返回 `server_is_overloaded` 或 `slow_down` 时，HTTP（普通请求）、SSE（事件流）和 WebSocket（长连接）都会先在同一账号内执行有界重试；预算耗尽后才切换同分组其他账号。这类瞬时拥堵不会冷却账号或模型，也不会降低调度健康度。普通 SSE 即使已经提交心跳，仍可继续同分组和跨分组安全换号；全部候选耗尽后必须在已提交的 HTTP 200 流内返回 `response.failed`，不会追加 JSON 错误。Responses WebSocket HTTP bridge（长连接转普通请求桥接）会在首轮语义输出前暂存 `response.created/response.in_progress` 等前导帧，最多占用 8 MiB；随后出现顶层 `error` 或 `response.failed` 容量错误时可安全换号且不向客户端泄露旧账号帧。已经输出文本、工具参数或媒体后禁止拼接其他账号响应，网关保留真实错误用于内部审计，仅把客户端副本中的容量错误码规范为可重试的 `server_error`，并记录 `gateway.failover_suppressed_after_semantic_output` 告警。
- Responses、Messages 和 Chat fallback（聊天接口兼容回落）命中账号已配置的 temporary unschedulable（临时不可调度）规则时，会在尚未向客户端输出内容的前提下切换账号；重试和切号均受现有预算约束，不会重复输出或重复计费。
- OpenAI 返回的 `400/403` content safety（内容安全拒绝）保持对应协议的原始错误语义，不切换账号、不冷却账号或模型，避免把确定性的请求拒绝放大为多账号重复请求。

Codex OAuth（授权账号）的 `User-Agent`、`originator` 和 `version` 继续使用同一身份版本；自动版本同步、管理员覆写和紧急回滚配置见下文 Codex 模型清单说明。组合分组推理强度、额度重置缓存和后台恢复只改变内部执行规则，不改变公网请求或响应结构。

相关配置均支持同名大写下划线环境变量：

- `gateway_lite.overload_enabled` / `GATEWAY_LITE_OVERLOAD_ENABLED`：总开关，默认 `true`。
- `gateway_lite.overload_max_inflight`：正在处理上限，默认 `1024`。
- `gateway_lite.overload_max_waiting`：排队上限，默认 `1024`。
- `gateway_lite.overload_wait_timeout_ms`：最长等待毫秒数，默认 `10000`。
- `gateway_lite.usage_report_workers`：Redis Stream（持久用量消息流）回传 worker（工作协程）数，默认 `4`，范围 `1-64`。
- `gateway_lite.usage_report_batch_size`：每次回传条数，默认 `25`，范围 `1-500`。默认小批次用于缩短主站热点用户和租约行的事务持锁时间。

当管理员没有在 YAML（配置文件）或环境变量中显式设置通用连接池参数时，gateway-lite 使用 PostgreSQL `max_open_conns=128/max_idle_conns=32` 和 Redis `pool_size=256/min_idle_conns=32`。PostgreSQL 还支持 `database.conn_max_lifetime_minutes` 与 `database.conn_max_idle_time_minutes`（对应环境变量 `DATABASE_CONN_MAX_LIFETIME_MINUTES`、`DATABASE_CONN_MAX_IDLE_TIME_MINUTES`），默认分别为 `30` 和 `5` 分钟。显式设置的连接池参数始终优先；`max_idle_conns` 不能大于 `max_open_conns`，连接存活/空闲时间不能为负，非法配置会在启动时失败。standard（标准模式）仍保留原通用默认值。

### v0.1.175 选择性升级兼容口径

本轮没有新增或删除公网 API（接口）路径，也没有改变成功响应结构；主要收口上游故障切换、计费完整性、协议兼容与可观测性：

- 本地计费事务与 control-plane correction（主站计费修正事件）均未能持久化时，gateway-lite（轻量网关）仍保留本次 usage log（用量日志）的 Token、模型、理论成本和请求证据，但把 `actual_cost（实收金额）` 固定记为 `0`，并写入 `usage.billing_persistence_failed_recorded_unsettled` 结构化错误日志。该记录只表示“上游已经产生用量但尚未确认扣款”，不会伪报收入；已有任一权威计费链路成功时继续记录真实实收。

- OpenAI 上游返回 HTML 格式 `403` 时，只将其视为代理、CDN（内容分发网络）或端点故障并切换账号，不递增账号连续 `403`、不冷却或停用账号；结构化账号级 `403` 仍按原规则处罚。
- 原生 Responses 的确定性 `400` 保留真实 HTTP 状态及 `error.type/code/param/message`，不包装成可重试 `502`；管理员错误透传规则和可安全切换的瞬时 `400` 仍优先执行。
- OpenAI 流只收到空 `response.completed`、且没有语义输出、usage（用量）或错误时，在尚未写给客户端前触发 failover（故障切换）；有真实输出或用量的完成事件保持成功。
- OpenAI 兼容响应按 `usage → response.usage → data.usage → data.response.usage` 解析，避免嵌套网关少记用量；Grok 聊天成功响应缺少可计费用量时不先输出给客户端，而是安全切换账号。
- OAuth Images（授权图片请求）读取中断、compact（上下文压缩）空流和 pool mode（账号池模式）认证失败进入有界重试或切号；请求级容量错误采用最高 8 秒的指数退避，避免紧密重试放大上游压力。
- Chat Completions（聊天补全）接受 `reasoning` 别名，仍以 `reasoning_content` 为优先；Gemini 工具 schema（结构）将可安全表达的整数 `exclusiveMinimum` 转换为 `minimum + 1`。
- HTTP/SSE（普通与事件流）及 WebSocket（长连接）的 TTFT（首个可见输出延迟）只从真实文本、工具参数、图片等可见内容开始计算；只发送 `output_text.done` 或 `function_call_arguments.done` 的 WebSocket 上游仍保留首输出兜底。
- API Key（接口密钥）创建和更新会拒绝负数、NaN（非数字）和 Infinity（无穷值）的 quota（额度）、5 小时/1 天/7 天限额；创建时 `expires_in_days` 必须大于 `0`。非法输入返回 HTTP `400`，避免绕过前端直接写入异常限额。
- User-Agent（客户端标识）只有通过格式、长度和合理版本校验后才进入账号指纹缓存，历史污染值读取时自动修复；OpenAI 个人订阅到期时间不会被 workspace（工作区）权益覆盖。
- Priority/Flex（优先/弹性服务档次）进入账号实际成本统计；cyber policy（上游网络安全策略）事件严格遵守管理员配置的分组和模型审计范围。
- 聚合分组后台可直接配置生图权限；请求记录恢复 `request_id（请求编号）`，运营监控的大内存容量自动使用 GB 显示。两项只改变后台展示，不改变对应 API 字段。

截至 `v0.1.175`，按上游响应声明模型计费、Codex OAuth（授权账号）设备指纹收敛和大文件备份分卷已按本项目边界补齐；具体安全条件分别见“计费模型来源”“Codex OAuth 设备指纹收敛”和“大文件备份分卷”。官方完整后台仍不机械移植，避免破坏现有后台职责与交互边界。

### v0.1.178 协议可靠性与模型声明修复

本轮没有新增、删除或改变公网 API（接口）路径；只收口跨协议转发、流式终态、上游错误和调度的正确性：

- Responses WebSocket（响应长连接）的合法 JSON（数据格式）二进制帧与文本帧走同一模型映射、内容审计、推理/优先级策略、用量观察和响应模型改写链路；音频等非 JSON 二进制只在 passthrough（直通）模式按原字节转发。WebSocket 转 HTTP（长连接转普通请求）无法表达不透明媒体帧，会明确提示客户端改用直通模式，不会静默丢帧。
- `response_model_mode=request（跟随请求）` 和自定义响应声明覆盖 Responses、Chat Completions（聊天补全）、Anthropic Messages（消息协议）、Gemini（谷歌模型）及其 fallback（回退）生成的 JSON/SSE（流式事件）响应；本地审计仍记录真实上游声明模型，不会把对客户的改写误标为上游不一致。
- Responses SSE（流式响应）同时识别 JSON 的 `type` 与标准 `event:` 字段。部分兼容上游只在 `event: response.completed/failed/cancelled/canceled` 中声明终态时，不再被误判为断流；JSON 内的 `type` 始终优先。
- `custom/freeform（自定义/自由文本）`、`tool_search（工具搜索）` 与 namespace（命名空间）工具在 OpenAI API Key（接口密钥）、WebSocket 转 HTTP 和 Responses→Chat 回退链路中会安全降级为函数工具，并在普通/流式回包恢复原始客户端工具形态及 `tool_choice（工具选择）`。
- Codex OAuth（授权登录）的额度探针使用官方专用 `codex-auto-review` 模型；管理员手动测试和普通账号测试仍使用各自原有测试模型，避免把探针结果误作普通模型可用性。
- 同一 `chatgpt_account_id` 的 OpenAI OAuth（授权）账号仅在确定性的 `402 deactivated_workspace`（工作区已停用）时联动停止调度并落库，60 秒内去重；普通 `429`、API Key（接口密钥）和影子账号不会被扩大影响。
- Gemini（谷歌模型）原生与兼容入口会保留可确认的上游 `4xx`（客户端请求错误）状态和安全错误信息；`429/5xx`（限流/服务端错误）仍沿用请求级重试和换号。Google Search（谷歌搜索）与客户端函数混用时自动补 `includeServerSideToolInvocations=true`，并保留既有 `toolConfig（工具配置）`。
- Anthropic SSE（流式响应）在尚未向客户输出有效内容时识别 `overloaded_error（上游过载）`，按协议语义进入现有安全换号；已输出内容的流绝不跨账号拼接，避免重复工具调用、重复计费或会话内容混合。

### v0.1.168 选择性升级兼容口径

本轮没有新增、删除或改变公网 API（接口）路径及响应结构。版本号表示 gateway-lite（轻量网关）已经选择性吸收官方 `v0.1.168` 中适用于本项目的修复，不表示直接合并官方整包：

- OpenAI passthrough（透传）账号不再被遗留 `model_mapping（模型映射）`误过滤，跨平台账号隔离规则保持不变。
- Messages（消息协议）到 Responses/Chat Completions（响应/聊天补全）的桥接会依据最终上游模型保留 GPT-5.6 的 `max` reasoning effort（最大推理力度）。
- API Key（接口密钥）Codex 模型清单保留网页搜索声明；模型清单 JSON 结构不变。
- Claude OAuth（授权账号）模拟请求迁移 system（系统提示）时保留最后一个缓存断点与 TTL（有效期）。
- OpenAI Live（实时会话）在 Redis（缓存服务）短暂故障时仍竞争唯一 finalize（最终结算）并尽力落用量、释放租约；生产总开关继续默认关闭。
- 本地模拟 Anthropic（协议）响应统一使用原生 `msg_01...` 消息编号。
- 内置模型目录补齐 Kimi K3 的 `k3`、`k3-256k` 及 Moonshot 官方域名，`kimi-k3` 与 `kimi-k3-code` 继续作为独立模型，不做相互映射；状态页识别 Claude Sonnet 5 的 `CSon5` 别名。
- 用户与 API Key 更新只写调用方明确声明的字段；余额、累计充值、配额使用量和限流窗口继续由原子计费路径维护。管理员调账也使用单条数据库原子操作，旧页面快照不能覆盖并发扣费结果。

主站仍是金额、额度租约、用量幂等和 effective policy（最终生效策略）的权威来源；本轮不改变这些内部契约，也不引入官方 Passkey（通行密钥）、prompt audit（提示词审计）、安装向导、支付或推广模块。

用量回传继续先写入 Redis Stream，再由 4 个 worker 小批量上报主站。批量结算使用独立 10 秒 HTTP（网络请求）超时，但和普通控制请求共享同一个固定连接池；普通鉴权、租约请求仍使用较短超时。批量失败只做一次共享退避并通过 Redis pipeline（流水线）整体重入队；主站按唯一 `request_id（请求编号）` 幂等结算。内部 usage worker（用量工作池）队列溢出默认使用 `sync（同步兜底）`，不能抽样或静默丢弃计费事件。

### `GET /v1/usage`

返回当前 API Key（接口密钥）的额度和用量摘要。存在 Key 独立额度或速率限制时返回 `quota_limited（密钥限额）` 模式；没有 Key 独立限制时返回 `unrestricted（套餐或余额）` 模式。

订阅套餐的 `subscription（订阅信息）` 对象包含 `daily_usage_usd（每日用量）`、`weekly_usage_usd（每周用量）`、`monthly_usage_usd（每月用量）`、对应额度上限、`expires_at（到期时间）`，以及 `weekly_window_start（当前周额度窗口开始时间）`。`weekly_window_start` 没有激活时为 `null`；新增字段不改变既有字段和模式判断，旧客户端可以直接忽略。

Codex alpha/search（Codex 独立网页搜索）兼容入口：

- `POST /v1/alpha/search`
- `POST /alpha/search`
- `POST /backend-api/codex/alpha/search`

该接口只允许 API Key（接口密钥）绑定的 OpenAI 分组使用，并复用固定分组、模型映射、账号并发、失败切换、内容审计和额度资格校验。OpenAI OAuth（授权账号）转发到 ChatGPT Codex 搜索入口；OpenAI API Key（上游接口密钥）账号转发到该账号 Base URL（上游地址）的 `/v1/alpha/search`。账号显式 `openai_capabilities（OpenAI 能力集）` 可单独声明 `alpha_search`；旧账号声明 `chat_completions` 时仍隐含允许搜索，但只声明 embeddings 等无关能力不会误放行。只有最终上游 `2xx（成功状态码）` 会记录一次网页搜索费用；API Key 上游返回 `404/405（端点不存在或方法不支持）` 时，只在本次请求中排除该账号并切换同分组其他账号，不写凭据失效、停用、模型错误或冷却状态；OAuth 的 `404/405` 和普通模型端点的 `404/405` 保持原错误语义。其他 `4xx` 原样返回且不收费，`429/5xx` 和网络错误按现有规则换号。分组的 `web_search_price_per_call（网页搜索单次价格）` 为空时使用 `$0.01/次`，为 `0` 时免费，最终费用继续乘以 `rate_multiplier（分组费率倍率）`。

OpenAI OAuth 形态账号若明确标记为 Codex PAT（Personal Access Token，个人访问令牌），网关不会调用 PAT 无权访问的 standalone `/alpha/search`，而是把同一次搜索安全转换为 ChatGPT `/backend-api/codex/responses` 的 hosted `web_search（托管网页搜索）`。转换前会删除误带的 `prompt_cache_key`、`prompt_cache_retention` 等 Responses 私有字段；Responses SSE（服务器推送事件）的文本和 URL citation（网址引用）会还原成 Alpha Search JSON。账号缺少 `chatgpt_account_id` 时，网关先通过官方 `whoami` 校验并回填最小账号元数据；PAT 不进入 OAuth refresh（令牌刷新）或“缺少 refresh_token”停用逻辑。PAT 必须通过管理员独立导入接口安全建号，并带 `auth_mode=personalAccessToken`；普通 OAuth AT（访问令牌）导入不会自动猜成 PAT。成功回退仍只产生一次按次费用、一个网关 `request_id（请求编号）` 和一条 usage（用量）事实，usage 的实际上游端点记录为 `/v1/responses`；搜索链路单次 `401` 只允许本请求换号，不会据此永久置错账号。

`/v1/responses` 兼容说明：OpenAI OAuth passthrough（OpenAI 授权账号透传）路径会在请求缺少 `instructions（系统指令）` 或该字段为空字符串时自动补默认指令，避免客户请求和后台账号测活使用不同 body（请求体）导致上游拒绝。部分客户端会把未设置的可选字段序列化成字符串 `"[undefined]"` 或 `"undefined"`；gateway-lite（轻量网关）会在透传上游前删除这些顶层占位字段，再执行 `instructions` 默认值补齐。若 `instructions` 不是字符串，仍按非法/不兼容请求处理。

JSON（结构化请求体）兼容说明：文本接口会容忍 JSON 字符串内部的原始控制字节并转义为 `\\u00xx`，同时去除 UTF-8 BOM（字节顺序标记）。归一化后的请求体仍受当前端点选中的正文上限限制：纯文本 `/embeddings`、`/alpha/search` 使用 `gateway.text_max_body_size`，可携带内联图片/视频的接口继续使用 `gateway.max_body_size`；控制字节转义导致体积超过上限时返回 `413`，不会无限扩张内存。截断 JSON、字符串外控制字节等结构错误仍返回 `400`。

Gemini `/v1beta` 鉴权说明：

- API Key（接口密钥）读取顺序为 `x-goog-api-key`、`Authorization: Bearer`、`x-api-key`，最后才是在 `/v1beta` 和 `/antigravity/v1beta` 路径允许的 `key` 查询参数。
- `api_key` 查询参数已废弃并固定返回 `400`，避免同一请求存在两套含义不同的查询参数。
- standard（标准运行模式）会执行与其他文本入口一致的 IP 白名单/黑名单、Key 启停状态、到期时间、Key 独立额度、用户状态、绑定分组可用性、专属分组权限、订阅窗口或余额检查。
- simple（简易运行模式）仍会执行 Key、用户、IP 和分组权限检查，但按该模式约定跳过余额及订阅检查。
- 鉴权失败保持 Google API（谷歌接口）错误结构：`{"error":{"code":状态码,"message":"错误说明","status":"Google 状态名"}}`，便于 Gemini SDK（开发工具包）正确识别 `UNAUTHENTICATED`、`PERMISSION_DENIED` 和 `RESOURCE_EXHAUSTED`。

Codex compact（Codex 上下文压缩）支持两种入站形式：

- `POST /v1/responses/compact` 或 `POST /backend-api/codex/responses/compact`。
- 普通 `POST /v1/responses` / `POST /backend-api/codex/responses` 的 `input[]` 中包含 `{"type":"compaction_trigger"}`。

第二种 body signal（请求体信号）会在调度前提升为 compact 请求，并只选择支持 compact 的账号。若原请求携带 `stream:true`，上游仍使用 unary JSON（单次 JSON）compact 协议，gateway-lite（轻量网关）会把最终结果桥接为 Responses SSE（响应事件流），保留 `compaction` 输出项和 `response.completed` 终态。等待上游期间可按 `gateway.stream_keepalive_interval（流式心跳间隔）` 发送 SSE 注释心跳；心跳字节不影响账号 failover（失败切换）判断。心跳后发生的错误会以 `response.failed` 终态返回，并写入 ops error log（运维错误日志），即使实际 HTTP 状态码已经固化为 `200`。

Codex 续链兼容说明：当客户端回放 `function_call`、`custom_tool_call`、`tool_search_call`、MCP 或本地 Shell 工具调用时，若调用项的 `id` 是非法的 `item_*` 等非 `fc*` 形式，gateway-lite（轻量网关）会删除该调用项 `id`，同时保留合法 `call_id（调用编号）`。工具结果项和普通消息的 `id` 不受影响，避免上游返回 `Expected an ID that begins with 'fc'`，也不会破坏结果配对。

上游 URL path guard（路径护栏）说明：`/v1/responses/*subpath`、`/responses/*subpath` 和 `/backend-api/codex/responses/*subpath` 只接受由 ASCII 字母、数字、点、短横线和下划线组成的普通路径片段，并限制片段长度和层数；点路径、编码分隔符、查询符、控制字符和其他逃逸形式会在鉴权后、账号调度前返回 `404`。Gemini 原生模型路径、兼容请求体里的模型名和 Grok/xAI 视频 request ID（请求编号）使用同一类 ASCII 闭集；视频编号还会在查询任务、构造上游状态/内容地址和生成本地回放地址前重复校验，非法值返回 `400`。该规则只收紧恶意路径，不改变 `/responses/compact` 等合法接口。

`/v1/messages/count_tokens` 和 `/messages/count_tokens` 兼容说明：当 API Key（接口密钥）绑定的 group（分组）是 OpenAI 平台时，gateway-lite（轻量网关）会把 Anthropic-compatible（Anthropic 兼容）的 count_tokens（令牌计数）请求转换为 OpenAI `POST /v1/responses/input_tokens` 请求，并返回 Anthropic 兼容格式 `{"input_tokens": number}`。API Key（接口密钥）账号会使用该账号配置的 `base_url（上游地址）`；OpenAI OAuth（授权账号）如果上游因为缺少 `api.responses.write` 权限或不支持 `input_tokens` 端点返回 401/403/404，会使用本地 tokenizer（分词器）估算作为兜底结果，避免把账号临时下线。发送 Anthropic 上游前会删除 `max_tokens`、stream（流式开关）和其他生成参数，只保留计数接口接受的输入结构。当固定分组属于 Grok 平台时，网关直接使用本地 tokenizer 对规范化后的 Messages 请求做确定性估算，不请求 xAI，也不选择或修改上游账号。两种分支都只校验 Key 和使用资格，不申请 lease（额度租约）、不占用用户/账号并发、不写 usage（用量）或请求日志，也不触发账单、冷却、健康和调度副作用。

`POST /v1/responses/input_tokens`、`POST /responses/input_tokens` 和 `POST /backend-api/codex/responses/input_tokens` 提供原生 Responses（响应协议）Token 预检。请求必须包含非空 `model`；网关会执行 API Key（接口密钥）鉴权、内容审计、模型权限和余额/订阅资格检查，并应用分组与账号模型映射。官方 OpenAI 上游在支持该接口时透传真实计数；第三方 OpenAI-compatible（OpenAI 兼容）中转、Grok、Kimi、智谱、DeepSeek 或明确不支持该接口的上游返回本地 tokenizer（分词器）估算。成功结构为 `{"object":"response.input_tokens","input_tokens":number}`。该预检不创建生成任务、不扣费、不写 usage（用量）或账本，也不占用用户生成并发；仅在选择账号时使用短暂调度容量，选择完成后立即释放。

`/v1/messages` 的 Chat Completions（聊天补全）回退说明：当 OpenAI API Key（上游接口密钥）账号配置为不支持 Responses API（响应接口）时，gateway-lite（轻量网关）会把 Anthropic Messages 请求转换为 `/v1/chat/completions` 请求，并将普通或流式响应转换回 Anthropic 格式。该链路保留工具调用、停止原因、缓存读取/写入 Token、Priority（优先处理）策略、失败切换和真实用量结算；客户端中途断开时仍会继续读取上游用量，但不会伪造 `message_stop（消息结束）` 掩盖上游断流。明确支持 Responses API 的账号仍使用 `/v1/responses`，不会被错误降级。

`/v1/responses` 的高级工具回退说明：当请求转到仅支持 Chat Completions（聊天补全）的上游时，`custom/freeform tool（自由文本工具）` 会降级为单字符串参数的 function（函数工具），`tool_search（工具搜索）` 会使用代理函数，MCP namespace（命名空间）子工具会摊平成带命名空间前缀的函数名；网关在普通和流式响应中恢复原始工具类型。命名冲突、摊平后重名或无法安全表达的强制 `tool_choice（工具选择）` 会在本地返回明确错误，不会静默调用错误工具。

`/v1/responses` 原生 HTTP namespace（命名空间）兼容说明：OpenAI OAuth（授权账号）的 HTTP/SSE（事件流）上游不支持 Codex 私有 namespace 子工具时，gateway-lite（轻量网关）会把子 function（函数工具）安全摊平成 `<namespace>__<name>`，同步改写历史 `function_call（函数调用）` 和精确 `tool_choice（工具选择）`；选择整个 namespace 时降级为 `auto（自动选择）`。内置 `image_gen（生图命名空间）` 保持原样，OpenAI API Key（上游接口密钥）账号和原生 WSv2（第二版响应长连接）也不会被不可逆改写。普通 JSON、SSE 的 `response.output_item.added/done`、`response.completed` 和 `response.done` 会在返回客户端前恢复为原始 `namespace + name`。摊平名与顶层工具或其他 namespace 撞名时直接返回 `400 invalid_request_error（非法请求）`；账号故障切换时会清除上一账号的临时映射，避免跨账号错误还原。

Responses Lite（Codex 精简响应模式）兼容说明：HTTP 请求通过 `X-OpenAI-Internal-Codex-Responses-Lite: true` 标记；Responses WebSocket（响应长连接）每轮 `response.create` 通过 `client_metadata.ws_request_header_x_openai_internal_codex_responses_lite="true"` 标记。命中该标记后，gateway-lite（轻量网关）会把 `reasoning.context（推理上下文）` 固定归一为 `all_turns（全部轮次）`，保留现有 `reasoning.effort（推理强度）`，保证 Codex Desktop 多轮推理上下文一致。顶层 `namespace（命名空间）` 工具会幂等迁移到 `input.additional_tools（附加工具载体）`；顶层只允许 Lite 支持的 function/custom/tool_search（函数/自定义/工具搜索），不支持的 hosted tool（托管工具）、冲突定义和非法结构返回明确 `400 invalid_request_error`，不会静默删除客户工具。gateway-lite 不会再次注入托管 `image_generation（图片生成）` 工具、`tool_choice` 或图片桥接说明，避免与客户端自带的附加工具冲突。HTTP 普通转发、OpenAI passthrough（透传）、原生 WSv2 和 WebSocket HTTP bridge（长连接转 HTTP 桥接）使用同一归一规则并继续传递 Lite 标记；Grok 桥接不注入 OpenAI 私有请求头。该标记不会绕过分组 `allow_image_generation（允许生图）` 权限，也不会单独改变计费规则。除大小写不敏感的字符串 `true` 外，其他值均按非 Lite 请求处理。

Responses 图片意图说明：Codex 在普通文本请求中被动携带 `image_gen namespace（图片工具命名空间）` 只表示客户端声明工具目录，不会单独触发 `allow_image_generation` 权限拒绝、图片专用并发槽或 Responses capability（能力）过滤。图片模型、原生 `image_generation` 工具、专用图片接口，以及 `tool_choice` 明确选择 `image_generation` 或 `image_gen.imagegen` 时才属于显式生图意图。该规则统一用于 HTTP、SSE、Responses Lite、原生 WebSocket 首轮及后续轮次；被动 namespace 原文仍保留给后续工具转换，不会被粗暴删除。

Chat Completions 图片模型端点说明：OpenAI 分组的 `POST /v1/chat/completions` 如果客户端模型或渠道映射后的真实模型属于 `gpt-image-*` 图片模型，gateway-lite（轻量网关）会在并发、计费资格和账号调度前返回 `400 invalid_request_error`，提示改用 `/v1/images/generations`。该错误属于确定性的端点不兼容，不会进入普通文本账号 failover（失败切换）。

Responses 协议终止状态兼容说明：Anthropic 流式上游在 `message_delta.stop_reason=max_tokens` 后结束时，gateway-lite（轻量网关）输出 `response.incomplete`，并设置 `status=incomplete`、`incomplete_details.reason=max_output_tokens`；普通 `end_turn` 仍输出 `response.completed`。Responses 转 Chat Completions（聊天补全）时，非流式和流式结果中的 `incomplete_details.reason=content_filter` 都保留为 `finish_reason=content_filter`，不会错误降级成 `stop`。

Responses `created_at（创建时间）` 兼容说明：当 Chat Completions（聊天补全）结果被转换为 Responses（响应协议）结果时，网关优先沿用上游 `created` 时间戳；上游缺失或提供非正值时使用网关生成响应时的 Unix 秒时间。流式 `response.created` 与终态 `response.completed/response.incomplete` 使用同一创建时间，确保严格客户端不会因字段缺失或时间漂移拒绝响应。

Responses 请求体超限换号说明：原生或 API Key passthrough（接口密钥透传）的 JSON `POST /v1/responses` 请求，如果所选上游在尚未向客户端输出时返回 HTTP `413`，或返回带明确 `body_too_large / payload_too_large / request_body_too_large` 等结构化错误码的 `400`，gateway-lite（轻量网关）会把它视为当前账号/上游代理的请求体大小能力边界，在固定分组内排除该账号并切换到不同候选账号。上下文窗口超限、普通客户参数错误、已开始输出的响应、Images multipart（图片文件表单）和不可重放流不进入该规则；同账号 pool retry（池内重试）也被禁止。失败尝试不写账号健康故障、不生成 usage（用量）或重复消耗额度租约，最终成功仍只有一条计费事实。候选耗尽时，非流式固定返回脱敏 `413 invalid_request_error`，消息为 `Request payload is too large`；已进入 Responses SSE（响应事件流）格式时返回同一消息的 `response.failed` 终态，不透传可能包含供应商或代理细节的上游错误体。

Responses 图片终态兼容说明：部分上游会在 `response.output_item.done`、`response.completed` 或 `response.done` 已经携带非空图片 `result（结果）` 时，仍把 `image_generation_call（图片生成调用）` 状态保留为 `generating/in_progress（生成中）`。gateway-lite（轻量网关）会在 HTTP 透传、普通 SSE（事件流）、SSE 转 JSON（结构化响应）、OpenAI WSv2（第二版响应长连接）、客户端 Responses WebSocket（响应长连接）和 WebSocket HTTP bridge（长连接转 HTTP 桥接）路径把这类终态统一修正为 `completed（已完成）`。没有图片结果、状态为 `failed（失败）`、二进制帧或普通非终止事件时保持上游原值，避免把未完成或失败图片误报为成功。

OpenAI 图片意图与尺寸证据说明：HTTP 和客户端 Responses WebSocket（响应长连接）请求在进入调度前会缓存 canonical image intent（规范图片意图）的 true/false 判定，账号 failover（故障切换）复用同一结果；渠道映射后的图片模型也进入同一判定。OpenAI 图片意图只选择确实支持 `/v1/responses` 的账号：自动探测或 `force_chat_completions（强制聊天补全）` 已确认不支持 Responses 的 API Key 上游会被排除，避免在 forward（转发）阶段静默降级后丢失生图能力；普通文本请求仍可使用这些 Chat Completions 账号。compact（压缩）模型映射或 passthrough strip（透传剥离）只重算当前 attempt（尝试），不能污染原请求事实。OAuth（授权账号）转换结果以及 API Key（接口密钥）或第三方 Base URL（上游地址）的原生 Images 响应若包含 PNG、JPEG 或 WebP 内联字节，网关会解码真实宽高并回填 `ImageOutputSizes（实际输出尺寸集合）`；非流式 `data[].b64_json` 缺少 `size` 时，会在返回客户端前补入对应 `data[].size`，日志和图片分档计费复用同一响应证据。图片数量只统计 `data[]` 中真实含 `url` 或 `b64_json` 的结果；URL 不会为尺寸识别而主动下载，未知或无效 base64 仍计数但不伪造尺寸。该逻辑不改变现有按 Token、按图和按次价格规则。

OpenAI 图片工具 usage（用量）说明：HTTP 非流式、普通 SSE（事件流）、SSE 转 JSON（结构化响应）、客户端 Responses WebSocket（响应长连接）、原生 WSv2（第二版长连接）和 passthrough WS（透传长连接）共用同一解析规则。v0.1.163 hosted image generation（托管生图）返回的标准 `response.usage` 是文本/编排总 Token 的权威来源；`response.tool_usage.image_gen` 只补齐标准 usage 尚未提供的图片输入/输出 Token，不覆盖总输入/输出，也不与已经存在的图片 Token 重复相加。旧版 OAuth（授权）响应只在 `response.output[].image_generation_call.usage` 给出完整生图总量时，仍按稳定 `id` 去重聚合；图片调用缺少 `output_tokens_details.image_tokens` 时可把该调用的 `output_tokens` 作为图片输出 Token。字段必须是非负整数，畸形、负数、溢出或恶意指数记法的工具 usage 不会污染合法顶层 usage；`response.failed/incomplete/cancelled`、中间事件和重复终态不会把未完成图片重复计入。网关会把图片输入 Token 从总输入 Token 中拆出，按 `渠道 image_input_price（渠道图片输入价） > 模型目录 input_cost_per_image_token（模型图片输入价） > 文本输入价回退` 计费；`null（空值）` 表示继承，显式 `0` 表示免费。usage log（用量日志）和主站 correction（修正事件）携带 `image_input_tokens`、`image_input_cost`、`image_input_price`、`image_input_price_source`，但最终扣费仍只使用网关已经计算的 `actual_cost/actual_cents（实际费用）`。

OpenAI Codex 套餐模型门控说明：仅当 OpenAI OAuth（OpenAI 授权）账号收到上游明确的 `400`，且错误正文包含“该模型不支持通过 Codex + ChatGPT 账号使用”的稳定语义时，gateway-lite（轻量网关）会把当前 `account + mapped model（账号 + 映射后模型）` 冷却 30 分钟，并让本次请求切换同分组其他账号。该处理不会关闭整个账号，也不影响该账号上的其他模型。普通业务 `400`、OpenAI API Key（上游接口密钥）账号和不匹配的错误正文不会进入该模型冷却。

Anthropic 429（请求过多）兼容说明：上游返回 429 但没有 reset（重置时间）响应头时，gateway-lite（轻量网关）不会把账号误判为 5 小时或 7 天长期耗尽，而是应用管理员可配置的秒级兜底冷却，并继续当前请求的账号 failover（失败切换）。关闭 `RateLimit429CooldownSettings（429 兜底冷却设置）` 后不会写入本地冷却状态。

Web Search（网页搜索）历史兼容说明：客户端在后续 `/v1/messages` 或 `/v1/messages/count_tokens` 中回放网关模拟生成的 `server_tool_use`、`web_search_tool_result` 块时，gateway-lite（轻量网关）会在发送上游前删除这些本地伪造块，并保留文本搜索摘要。对于 GLM、Kimi、DeepSeek 等要求 thinking passback（思考内容回传）的第三方协议族，会删除所有上游不接受的网页搜索块；Anthropic 官方协议会保留非本地伪造的真实搜索块。

GPT-5.6 模型别名说明：`gpt-5.6`、`gpt-5.6-high`、`gpt-5.6-max` 和合法日期版本会在 OpenAI OAuth（授权账号）请求链路映射到 `gpt-5.6-sol`；`gpt-5.6-terra`、`gpt-5.6-luna` 仍保持各自型号。`/v1/models` 和轻量网关后台模型列表同时展示通用别名与三个明确型号；未知的 `gpt-5.6-*` 后缀不会被猜测映射。

Grok 4.5 模型目录说明：Grok 分组的 `/v1/models` 和轻量网关后台模型选择器统一展示 xAI 内置模型、媒体模型及兼容别名。`grok`、`grok-latest`、`grok-4.5-latest`、`grok-build-latest` 会映射到 `grok-4.5`；`grok-imagine-image-quality-20260403`、`grok-imagine-image-quality-latest`、`grok-imagine-image-pro` 映射到 `grok-imagine-image-quality`，`grok-imagine-image-2026-03-02` 映射到 `grok-imagine-image`，`grok-imagine-video-1.5-preview`、`grok-imagine-video-1.5-2026-05-30` 映射到 `grok-imagine-video-1.5`。支持推理档位的 Grok 4.5/Build 模型项会额外返回 `supportsReasoningEffort=true`、默认 `reasoningEffort=high` 和 `reasoningEfforts=[low,medium,high]`，供兼容客户端直接构建推理强度选择器。上游模型同步只会为账号仍然拥有的规范模型补回别名，不会把账号不支持的模型伪装成可用；账号显式配置的 `model_mapping（模型映射）` 始终具有更高优先级。明确的空模型集合会继续返回空，不会回退默认全集。模型列表和主站价格页保留客户可请求的实际别名。分组未开启 `allow_image_generation（允许媒体生成）` 时，`grok-imagine*` 图片、编辑和视频模型不会出现在模型列表中，真实媒体请求也必须通过相同权限校验。

Grok OAuth（授权账号）模型同步会优先读取上游条目的 `model`、`modelId`、`model_id` 和 `_meta` 中的协议模型编号，最后才使用展示名；重复模型会去重排序。`X-UserID/X-Email（账号身份头）` 只发送到精确的官方 `cli-chat-proxy.grok.com`，自定义中继地址不会收到这些身份字段。

Grok 账号上游路由说明：Grok OAuth（授权账号）和 API Key（接口密钥账号）都会保存并使用管理员明确确认的完整 `base_url（上游地址）`。后台提供 Build CLI、官方 API、`us-east-1`、`us-west-2`、`eu-west-1` 五个快捷模板，也允许符合全局 URL 安全策略的第三方地址；快捷按钮只负责填充，不替代最终保存值。文本、Chat Completions（聊天补全）、模型同步、额度/billing（账单）探测、账号测活、图片、编辑、视频提交和视频状态查询共用同一地址分类与校验规则。只有 Build CLI 地址上的媒体请求会改走官方 `https://api.x.ai/v1`，避免 CLI 请求体限制；管理员选择的官方、区域和第三方地址在媒体路径保持原值。官方及区域 `*.api.x.ai` 只接受 HTTPS、无 `userinfo（用户名密码段）`、`query（查询参数）`、`fragment（片段）`，且基础路径必须为 `/v1`；`api.x.ai.evil.example` 等伪造官方后缀会被拒绝。第三方地址禁止直接保存 `/responses`、`/models` 等具体接口路径，并继续服从 `security.url_allowlist（网址白名单）`。公共 HTTP 传输层只对 `cli-chat-proxy.grok.com` 注入经过版本校验的 Grok CLI 身份头。CLI 上可重放的 `POST /v1/responses` 或 `/v1/chat/completions` 如果明确返回 `403 Access denied`，或返回 `code=permission_denied` 且正文为官方聊天端点权限拒绝，会在尚未向客户端输出且请求体可安全重放时，仅使用同一凭据、请求编号和计费上下文回退一次官方 API；媒体请求、不可重放请求和其他错误不回退，官方回退失败时保留原始 CLI 403，失败尝试不会重复扣费。Composer 模型不支持的 `reasoning（推理）` 参数会在转发前删除；普通 Grok Responses 请求中，`reasoning` 输入项若携带 `content:null`，网关只删除这个空字段并保留 `summary（摘要）`、`encrypted_content（加密推理内容）` 和非空 content，避免 xAI 返回 `422`。如果 xAI 明确返回 `invalid-argument` 且说明无法解密 `encrypted_content`，网关只删除被拒绝的 reasoning 加密字段并在原账号重试一次，其他输入、摘要和大整数保持不变。

Grok 429 限流说明：上游返回 `429 Too Many Requests（请求过多）` 时，轻量网关会根据 `Retry-After（建议重试时间）`、请求额度窗口和 Token（文本计量单位）额度窗口计算最晚恢复时间，并将当前账号写入持久 `rate_limit（限流）` 状态。并发响应只能延长、不能缩短该时间；当前请求进入既有 failover（失败切换）调度后，可改用同分组其他可用账号。没有任何恢复头时默认冷却 2 分钟。成功响应若明确显示剩余额度已经归零，也会立即封锁该账号。内容审计或安全策略拒绝属于单次请求错误，不会冻结账号，也不会盲目切换账号。

Grok OAuth 凭据恢复说明：同一客户请求内的令牌刷新、并发恢复和跨账号 failover（故障切换）共享 15 秒预算。永久凭据错误、明确权益拒绝、代理失效、provider（服务商）故障和临时刷新失败使用不同状态；临时失败只安装运行时拉黑和限时不可调度。刷新成功写回使用 credentials（凭据）中的服务端 `_token_version + proxy_id` 做 CAS（比较并交换），管理员提交非空 Grok OAuth 凭据时由服务端严格递增版本，普通名称或优先级编辑不会制造冲突；CAS 失败时重新读取权威账号行，若其他并发已经写入有效新令牌则直接恢复请求。provider/request 级错误不会无意义地轮询整个账号池，也不会污染账号健康度；对客户端只返回脱敏的 `503`。Grok API Key（接口密钥）及第三方 Base URL（上游地址）不进入该 OAuth 恢复链。

OpenAI 与 Grok 使用严格的平台边界。`platform=openai（OpenAI 平台）` 账号不会自动补入 Grok 别名，也不会调度 Grok 文本、图片、视频或 Responses WebSocket（响应长连接）请求；历史 OpenAI 账号即使仍残留 Grok `model_mapping（模型映射）`，运行时也会过滤并返回明确 `model_not_found（模型不存在）` 或平台不支持错误。同一个第三方上游同时提供 OpenAI 和 Grok 能力时，管理员必须分别创建 `platform=openai + type=apikey` 和 `platform=grok + type=apikey` 两个账号，分别绑定对应分组。

OpenAI Images（OpenAI 图片接口）非流式保活说明：`POST /v1/images/generations` 和 `POST /v1/images/edits` 在等待较慢上游时，可以通过 `gateway.image_nonstream_keepalive_interval（图片非流式心跳间隔）` 或环境变量 `GATEWAY_IMAGE_NONSTREAM_KEEPALIVE_INTERVAL` 启用 whitespace heartbeat（空白心跳）。默认值 `0` 表示关闭；启用时只允许 `5-60` 秒。心跳写入 `" \n"`，JSON（结构化数据）协议允许最终文档前存在空白，因此 API Key（接口密钥）和 OAuth（授权账号）的生成、编辑结果仍是标准 JSON。首次心跳会把 HTTP 状态固定为 `200`；之后若上游失败，网关会停止心跳并返回合法的 OpenAI JSON 错误体，同时在运维日志保留真实上游状态。心跳字节不算业务响应字节，不会阻止同分组账号 retry/failover（重试/失败切换）。

### 图片异步任务

客户端可以复用 `POST /v1/images/generations`，增加请求头 `Prefer: respond-async` 把非流式 JSON 生图请求交给 durable queue（持久队列）。可选请求头 `Idempotency-Key` 最长 128 字符；同一所有者、相同幂等键和相同请求会返回原任务，不会重复预留额度或再次生成图片。成功提交返回 HTTP `202`、`Preference-Applied: respond-async`、`Location: /v1/images/tasks/:id`：

```json
{
  "object": "image.task",
  "replayed": false,
  "task": {
    "id": "c8f7...",
    "state": "queued",
    "settlement_state": "pending",
    "progress": 0,
    "model": "gpt-image-1",
    "image_count": 1,
    "reserved_amount": 0.04,
    "actual_amount": 0,
    "artifacts": []
  }
}
```

状态机固定为 `queued/running/succeeded/failed/cancelled/expired（排队/执行/成功/失败/取消/过期）`。`settlement_state` 为 `pending/settled/released（待确认/已结算/已释放）`；只有图片已保存且金额完成结算时才返回下载地址。上游已经生成成功但入账暂时失败时，worker（工作线程）只重试同一幂等用量入账，不会再次调用生图上游。

API Key（接口密钥）鉴权接口：

- `GET /v1/images/tasks?limit=50&cursor=...`：只列出当前 Key 所属用户且属于当前 Key 的任务。
- `GET /v1/images/tasks/:id`：查询当前 Key 的单个任务。
- `POST /v1/images/tasks/:id/cancel`：幂等取消排队/运行任务；额度最多释放一次。
- `GET /v1/images/tasks/:id/artifacts/:index`：下载已结算任务的指定产物。
- 无 `/v1` 前缀的 `/images/tasks...` 兼容路径行为相同。

登录用户接口：

- `GET /api/v1/image-tasks`
- `GET /api/v1/image-tasks/:id`
- `POST /api/v1/image-tasks/:id/cancel`
- `GET /api/v1/image-tasks/:id/artifacts/:index`

管理员接口：

- `GET /api/v1/admin/image-tasks?state=failed&limit=50&cursor=...`：按状态分页查看本节点任务。
- `GET /api/v1/admin/image-tasks/capabilities`：返回 `{"schema_version":1,"enabled":true|false}`，供主站在展示候选节点前确认本节点是否明确启用异步图片任务；接口始终返回 HTTP `200`，未启用时为 `false`。
- `GET /api/v1/admin/image-tasks/metrics`：返回队列深度、容量、worker 数量、当前执行数及脱敏后的生效配置。
- `GET /api/v1/admin/image-tasks/config`：读取页面可维护的期望配置和当前进程真实状态。响应包含 `revision`、`desired`、`effective_enabled`、`restart_required`、`config_writable`、`read_only`、`env_overrides` 和 `degraded_reason`；只返回 `encryption_key_configured`、`access_key_id_configured`、`secret_access_key_configured` 布尔值，绝不回显 TOTP 加密密钥或 S3 凭据。
- `PUT /api/v1/admin/image-tasks/config`：保存异步图片任务启动配置。请求结构为 `{"expected_revision":"...","desired":{...},"generate_encryption_key":true|false}`；`expected_revision` 用于阻止两个页面互相覆盖，S3 的 `access_key_id` / `secret_access_key` 留空表示保留现值。首次启用可用 `generate_encryption_key=true` 让服务端生成并直接持久化固定 AES-256（加密）密钥，已经配置后禁止通过接口轮换。所有修改保存后返回 `restart_required=true`，重启后才真正更换 worker（工作线程）、对象存储和加密器。
- `POST /api/v1/admin/system/restart`：gateway-lite（轻量网关）后台只开放这一项系统操作，用于应用进程级配置；不开放在线更新或回滚。接口响应后向当前进程发送 `SIGTERM（优雅终止信号）`，主流程会先停止接收请求并刷出用量、计费和后台 worker，再由 systemd（系统服务）或容器重启策略拉起；不会用 `os.Exit` 跳过清理。
- `GET /api/v1/admin/image-tasks/:id`：读取任务详情；响应不包含加密请求、策略快照、对象内部键或任何凭据。
- `POST /api/v1/admin/image-tasks/:id/cancel`：取消尚未派发的任务。
- `GET /api/v1/admin/image-tasks/:id/artifacts/:index`：下载已结算产物。
- `POST /api/v1/admin/image-tasks/:id/replay`：人工重放可恢复的失败任务，请求体必须为 `{"confirm":"REPLAY_IMAGE_TASK"}`；已成功产物只重试结算。
- `POST /api/v1/admin/image-tasks/cleanup?limit=100`：请求体必须为 `{"confirm":"CLEANUP_IMAGE_TASKS"}`；清理已经过期且财务终态的任务与对象，失败项保留并可重试。

本地 JWT（登录凭证）管理员默认查看当前节点全部任务，也可带 `user_id` 缩小范围。主站使用 `X-Gateway-Lite-Admin-Key` 同步密钥访问 Capabilities/List/Get/Cancel/Artifact；除 Capabilities（能力声明）外均必须携带合法正整数 `user_id`。所有任务查询和取消都在 PostgreSQL（数据库）SQL 条件内同时校验任务 ID 与 `user_id`，不匹配统一返回 `404`。同步密钥不能访问 Replay/Cleanup/Metrics。只有管理员视图返回 `user_id`，API Key 与普通用户视图不泄露该字段。

任务 ID 在进入 PostgreSQL 的 UUID 转换前先校验；非法 ID 统一按 `404 image_task_not_found` 处理。分页 `cursor` 解码后还会校验内部 UUID，非法或伪造 cursor 返回 `400 invalid_image_task_input`，不会变成数据库 `500`。分页 `limit` 最大为 200。

`claim_batch_size` 的真实语义是“每个 worker 每次唤醒最多连续领取并处理多少个任务”，不是一次预占多少个任务；worker 始终逐个 claim（领取），避免慢生图使后续 claim 过期后被其他节点重复执行。`max_attempts` 只限制上游派发前的处理尝试（包括请求解密）；一旦持久化为 `dispatched` 就绝不再次调用生图上游。已生成产物的结算重试与失败任务的额度释放重试不受 `max_attempts` 限制，直到进入财务终态。

任务创建或成功状态保存遇到 PostgreSQL（数据库）错误时，错误本身不能证明事务已回滚。创建路径会按 owner（所有者）与 `Idempotency-Key（幂等键）` 从主库重读：确认任务已提交时保留原 reservation（预留）并返回该任务，确认不存在时才释放，重读失败则返回可重试的 `503 image_task_persistence_uncertain` 并保留预留；客户端必须使用同一幂等键重试。worker 保存 `succeeded（成功）` 后也会按任务 ID 重读：确认成功态已提交时保留对象并继续幂等结算，只有确认数据库仍是本 worker 持有的 `running + dispatched（执行中且已派发）` 状态才删除对象并释放；重读失败或结果不确定时保留对象与预留，交给 claim（领取）恢复流程处理。

异步任务默认关闭。启用 `gateway.image_tasks.enabled=true` 前必须显式配置 `totp.encryption_key` 和对象存储；单节点开发可使用 `storage.type=local`，共享数据库的多节点部署必须使用 `storage.type=s3`，否则节点可能领取到另一个节点无法读取的本地产物。请求正文使用 AES-GCM（认证加密）保存，任务表不保存 API Key、OAuth Token、代理地址或上游凭据；远程图片下载会阻止私网、回环、链路本地、运营商内网和重定向绕过，并受单图/单任务大小上限约束。

后台保存前会执行完整配置校验；首次启用或更换存储时还会执行本地临时文件写删或 S3 Put/Get/Delete（上传、读取、删除）限时探测，失败时不写入配置。正在运行或仍有待结算任务时不能关闭模块，避免遗留 reservation（额度预留）。若任一 `GATEWAY_IMAGE_TASKS_*` 环境变量覆盖页面配置，接口会返回 `read_only=true` 和环境变量名称但不返回其值；`TOTP_ENCRYPTION_KEY` 单独使密钥生成按钮只读。配置文件不可写时返回明确错误，不会假装保存成功。

异步图片属于可选子系统：启动时发现图片任务参数、固定密钥或存储类型无效，会 fail-closed（失败关闭）为默认关闭并在后台返回 `degraded_reason`，普通文本请求仍可启动；本地对象存储初始化失败也只关闭图片任务并记录受保护日志，避免 systemd（系统服务）因可选图片配置反复重启。`SUB2API_CONFIG_FILE` 与 `CONFIG_FILE` 均可指定后台实际读写的同一个配置文件。

Grok 媒体接口：

- `POST /v1/images/generations`、`POST /images/generations`：Grok 图片生成。
- `POST /v1/images/edits`、`POST /images/edits`：Grok 图片编辑；兼容 JSON（结构化数据）和 multipart/form-data（文件表单），文件会在网关内转换为 xAI 接受的 Data URL（数据地址）。
- `POST /v1/videos/generations`、`POST /videos/generations`：Grok 视频生成，返回的 `request_id（任务编号）` 会绑定本次命中账号。
- `POST /v1/videos/edits`、`POST /videos/edits`：Grok 视频编辑。路由常驻注册，是否可用由节点热开关 `grok_video_transforms_enabled（视频变换开关）` 决定，关闭时返回 `404 not_found_error`（与未提供该能力对客户端等价）；开关保存后即时生效，不需要重启进程。请求必须携带 `Idempotency-Key（幂等键）`。
- `POST /v1/videos/extensions`、`POST /videos/extensions`：Grok 视频扩展。同样受上面的热开关约束，并且必须携带 `Idempotency-Key（幂等键）`。
- `GET /v1/videos/{request_id}`、`GET /videos/{request_id}`：查询视频任务；必须由创建任务的同一用户和 API Key（接口密钥）访问，并固定使用原任务账号。没有持久所有者记录的旧任务返回 `404`，不能把上游 `request_id（任务编号）` 当成跨用户访问凭证。状态响应中的上游视频地址会改写为下面的同源内容地址。状态查询本身不预扣、不产生单独费用，最终视频费用始终归入原始生成请求的请求编号。
- `GET /v1/videos/{request_id}/content`、`GET /videos/{request_id}/content`：代理读取受保护视频内容。接口复用上述用户、Key 和原任务账号所有权校验，支持客户端 `Range（分段下载）` 并透传 `206/416`、`Content-Range`、`Accept-Ranges` 等下载头。上游状态返回的签名地址只接受 `https://vidgen.x.ai`（端口只能省略或为 `443`）；状态查询和内容下载都禁止重定向，避免 Bearer Token（访问令牌）或签名地址被带到其他域名。该接口不创建新任务、不预扣额度，也不产生独立媒体费用。

Grok 媒体图片输入兼容 `image`、`images`、`reference_images（参考图片）` 和图片编辑 `mask（蒙版）`；每项可以使用官方 `{"url":"..."}`、旧版 `{"image_url":"..."}` 或字符串形式，数组顺序和重复项都会原样保留。网关在内容审计、图片输入判断、模型路由及上游转发中复用同一组有序媒体对象，最终发往 xAI 的 JSON（结构化数据）只包含规范 `url`，不会混发 `image_url`。同一对象的 `url` 与 `image_url` 都非空且不相等时返回 `400 invalid_request_error（非法请求）`；两者相同或一方为空时合并为一个 `url`。图片编辑 multipart（文件表单）中的主图与蒙版也进入相同集合并转换为 `url` Data URL，主图、参考图和蒙版角色不会互换。

单个媒体请求最多包含 16 张主图/参考图；远程 URL 最长 8 KiB，单个 Data URL 最长 28 MiB、解码图片最大 20 MiB，全部 URL 合计最大 64 MiB，multipart 单文件最大 20 MiB。Data URL 只接受非空 base64 PNG、JPEG、WEBP 或 GIF；远程地址只接受无用户名密码段、无 fragment（片段）的公共 HTTP/HTTPS URL，并拒绝 localhost、回环、私网和链路本地字面量地址。域名会在内容审计/计费前和构造上游请求前分别解析，任一 A/AAAA 结果命中回环、RFC1918 私网、链路本地、CGNAT、IPv6 ULA 或云元数据范围即 fail-closed（失败关闭）；第二次解析用于阻断从公网地址切换到内网地址的 DNS rebinding（DNS 重绑定），单阶段解析最多等待 2 秒。空值、错误字段类型、非法 base64、协议、解析失败或超限请求在选择账号和预占计费前返回稳定 400，错误信息不会回显域名、解析出的内网地址或带签名参数的原始 URL。

媒体请求只允许 Grok 分组使用；OpenAI 分组的图片继续走标准 OpenAI Images（图片）处理器，视频接口直接返回 `404`。Anthropic、Gemini 等其他平台同样不能访问 Grok 媒体接口。图片、编辑和视频生成统一受分组 `allow_image_generation（允许媒体生成）` 开关、内容审计、用户并发及账号并发限制。`grok-imagine` 在图片接口规范化为 `grok-imagine-image-quality`；纯文本 `grok-imagine-video-1.5` 及其 `-preview` 等 1.5 系列后缀请求会按 xAI 能力回退到 `grok-imagine-video`，携带输入图片时保持客户端请求的 1.5 系列模型。Grok OAuth 媒体请求遇到上游 `401` 时强制刷新令牌并仅重试一次；Grok API Key 账号使用自身 Base URL（上游地址）和错误处理规则。

视频编辑与扩展的 `video（源视频）` 必须是对象，可使用官方 `{"url":"..."}`、兼容 `{"video_url":"..."}` 或 xAI Files API（文件接口）的 `{"file_id":"..."}`；`url` 与 `video_url` 不一致，或 URL 与 `file_id` 同时存在时返回 `400`，不会静默覆盖。发往 xAI 的远程/Data URL 最终统一为 `video.url`。远程地址必须是无凭据、无 fragment（片段）、路径以 `.mp4` 结束的公共 HTTP/HTTPS 地址；Data URL 只接受非空 `data:video/mp4;base64`。源视频 URL 与图片 URL 使用相同的两阶段 DNS 公网解析校验，阻断私网、回环、云元数据和 DNS rebinding（DNS 重绑定）。视频只把 prompt（提示词）送入现有内容审计，不会把 MP4 伪装成图片交给图片审核节点。

视频编辑不接受 `duration/resolution/aspect_ratio（时长/分辨率/宽高比）` 覆写，输出继承源视频且最高 720p；网关提交前按 15 秒上界预留额度，任务成功后使用终态返回的真实输出时长做差额结算。视频扩展的 `duration` 只表示新生成片段，范围为 2–10 秒，默认 6 秒；兼容输入 `seconds` 会规范化为 `duration`，终态返回的“源视频 + 扩展片段”总时长不会被错误用于收费。`expired（已过期）` 与 failed/cancelled（失败/取消）一样按 0 元终态释放预留额度。

视频编辑和扩展使用现有 `idempotency_records（幂等记录）`，同一个 API Key（接口密钥）、操作、`Idempotency-Key` 和规范请求体只会创建一个上游收费任务；重放响应带 `X-Idempotency-Replayed: true`。幂等存储不可用时 fail-closed（失败关闭），请求不会到达上游。上游已接受但 durable task（持久任务）登记失败时返回稳定 `503 grok_video_task_registration_failed`，相同幂等键只重放该结果，不会再次创建收费任务。外部副作用处于不确定状态时处理锁覆盖完整 7 天幂等窗口，禁止短锁超时后自动重试上游。

新的图片生成、图片编辑和视频生成要求账号具备 `grok_media_generation（Grok 媒体生成）` capability（能力）。所有候选账号都被媒体资格隔离时返回 `503`，错误类型为 `grok_media_no_eligible_account`，请求不会到达上游、不会产生 usage（用量）、费用或额度租约消费。Grok 媒体提交如果收到非内容安全类 `403`，会为 OAuth 和 API Key 账号保存 `source=media_request` 的不合格证据并只隔离后续媒体请求，不会把账号全局冷却或破坏仍然健康的文本调度；首次请求仍可安全换到其他媒体账号。Grok 文本请求不使用该能力；`GET /v1/videos/{request_id}` 历史任务查询也不要求新媒体资格，仍优先使用创建任务时绑定的账号。

Grok 官方媒体默认价格：`grok-imagine-image` 为 `$0.02/张`，`grok-imagine-image-quality` 为 `$0.05/张`；默认价格不随 1K/2K 分辨率变化。`grok-imagine-video` 为 `$0.05/生成秒`，`grok-imagine-video-1.5` 为 `$0.08/生成秒`；默认价格不随 480p/720p/1080p 分辨率变化。分组 `image_price_*`、`video_price_*` 或模型价格 override（覆盖价）仍具有更高优先级；清除覆盖价后恢复上述官方默认值。基础视频模型的 1080p 价格保持为 `0`，表示该能力不支持，而不是免费。

Grok 视频请求的 `duration（时长）` 只允许 1 至 15 的整数，未传时明确按 8 秒发送并预占额度；`resolution（分辨率）` 只允许 480p、720p、1080p 及其兼容别名，未传时明确按 480p 发送。基础 `grok-imagine-video` 不接受 1080p，单次请求只允许一个视频输出。非法值返回 `400 invalid_request_error（非法请求）`，不会静默压缩参数后按较低金额计费。提交前会按冻结价格扩展余额或套餐额度预占；成功后按最终结果做差额调整，失败、取消和 48 小时超时会通过同一 `request_id（请求编号）` 上报 0 元纠正事件并释放额度。


### Seedance 原生视频任务（Ark 协议）

火山方舟 Ark 的异步视频任务协议原样透传，不把 `content[]` 转换成 OpenAI `messages` 或 Grok `prompt`。路由同时提供 `/v1`、`/api/v3`、`/v3` 和无版本前缀四种别名。

- `POST /contents/generations/tasks`：创建任务。请求体按 Ark 协议原样转发（`model`、`content[]`、`duration`、`resolution`、`ratio`、`generate_audio` 等），网关只按账号 `model_mapping（模型映射）` 改写 `model`，响应保持上游原生格式。`Content-Type` 必须是 `application/json`，否则返回 `415`；`model` 必须是非空字符串、`content` 必须是非空数组，否则返回 `400`。`type=text` 的内容会拼成 prompt（提示词）进入内容审计，`type=image_url` 的输入进入统一媒体输入校验。
- `GET /contents/generations/tasks/{task_id}`：查询任务，必须由创建任务的同一用户、同一 API Key（接口密钥）和同一分组访问，并固定使用创建时的账号；查不到持久归属记录返回 `404 not_found_error`。不开放上游任务列表接口，避免共享账号的任务互相泄露。
- `DELETE /contents/generations/tasks/{task_id}`：按上游语义删除，不自动退款。
- 分组与鉴权：只允许 `openai` 或 `composite（聚合）` 分组，其他平台返回 `403 permission_error`；分组仍需开启 `allow_image_generation（允许媒体生成）`。
- 账号要求：OpenAI 平台 API Key 账号，凭据 `openai_capabilities（端点能力）` 必须包含 `seedance`，并配置自定义 `base_url（上游地址）`（可填源站、代理前缀或完整的 `.../api/v3`，缺少版本段时网关补 `/api/v3`）。没有合格账号时返回 `503 seedance_no_eligible_account`，请求不会到达上游，也不产生用量、费用或额度租约消费。
- 调度缓存：精简候选元数据必须保留 `openai_capabilities`，与完整账号使用同一能力判断。缺失能力声明不能授予 Seedance；显式空集合仍拒绝全部所测端点，仅 Seedance 的声明不会变成默认允许聊天或向量请求。修补不放宽账号、分组或图片能力检查，不新增请求／响应字段；已有候选缓存需要经过正常快照重建才能包含该字段。修前四反例失败、修后五子例及17顶层／17子扩展回归已通过，真实钱包和部署另验，见[调度快照能力一致性计划](../docs/modules/tokenav-upgrade-20261012/调度快照能力字段一致性修正计划.md)。
- 计费口径：**不使用** Grok 的按秒视频价，按上游返回的 `usage.completion_tokens（补全 token 数）` 与创建前持久的原数值 token 价结算（分组专属定价或目录价）；普通余额创建按客户模型优先、账号映射兜底选首个有效价，`BillingModel` 与 `ExecutionPricing.Model` 同值，完成只用原冻结候选和倍率（包括无资金授权的聚合分组），不重选热价或按缺价生成零价终态，全部候选缺价创建前拒绝；详见[原价冻结与可靠结算](#seedance视频任务查询原身份与-settlement可靠结算)。严格卡 Seedance 仍不开放。创建请求本身不计费；首次观察到 `succeeded（成功）` 时结算一次，重复查询由共享 claim（认领）键与持久化任务去重共同保证不会重复收费；排队、运行中、失败和取消均不计费。创建请求按提交计一次 RPM/TPM，查询与删除不计。
- 上游异步创建的错误不自动重试，避免重复创建已收费任务。

> **源码接入状态（2026-10-12 核对）**：创建前后原归属/计费/资金快照、查询/删除免费资格检查，以及按原请求可靠入队后确认结算已接。认领使用随机所有权令牌，模糊提交保留原占用恢复；不再沿用 2026-10-03 的“仅创建”结论。未知 `completion_tokens` 的严格卡费用上界尚不支持，源码接入不表示真实上游或部署验收通过。

### Codex models manifest（Codex 模型清单）

- `GET /backend-api/codex/models`
- `GET /models`
- `GET /v1/models?client_version=0.144.1`

Codex CLI（Codex 命令行工具）和 Codex Desktop（Codex 桌面版）携带 `client_version（客户端版本）` 查询模型时，gateway-lite（轻量网关）会从当前 Key 固定的 OpenAI 或 Composite（聚合）分组内选择 OpenAI OAuth（OpenAI 授权）或 OpenAI API Key（OpenAI 接口密钥）账号获取实时 models manifest（模型清单）；聚合分组复用同一套 OpenAI 账号选择和失败切换。根路径 `GET /models` 和 `GET /backend-api/codex/models` 始终返回 Codex models manifest（模型清单），用于兼容 Codex 客户端的探测路径。上述三条 Codex 模型清单路径均为非计费请求：只做鉴权和分组校验，不调用 `AcquireLease（额度租约预占）`，也不写入 `usage event（用量事件）`；gateway-lite 热路径对相应 `GET/HEAD` 请求使用同一豁免口径。OAuth 账号请求 ChatGPT Codex 后端；API Key 账号只允许请求经过 URL 安全校验的自定义 Base URL（上游基础地址）的 `/v1/models`。普通客户端不携带 `client_version` 时，`GET /v1/models` 仍返回原 OpenAI-compatible（OpenAI 兼容）模型列表。

Codex OAuth 出站的 `User-Agent`、`originator` 和 `version` 使用同一身份版本。后台系统设置可填写 `openai_codex_client_version` 固定版本，留空时采用每 6 小时自动同步的 `openai_codex_client_version_synced`，再回退到程序内置稳定版；`openai_codex_version_auto_sync_enabled` 可关闭自动同步。配置文件 `gateway.disable_codex_identity_enforcement=true` 仅用于紧急回滚身份归一化，默认必须保持 `false`。

OpenAI OAuth（授权账号）的 `extra.codex_fingerprint_mode` 支持四档，并由账号创建、编辑和批量编辑接口维护：

- `off（关闭）`：保持客户原始设备、会话和线程标识；历史账号没有该字段时按 `off` 处理，升级不会静默改变身份。
- `device（设备级）`：只收敛 `installation_id（安装编号）`，不改会话和线程。
- `session（会话级）`：稳定设备和账号会话；按客户 `session-id` 派生线程，每次请求生成新的 turn（轮次）。新建 OpenAI OAuth 账号默认使用此档。
- `full（完全收敛）`：设备、会话和线程全部按账号稳定收敛，适合明确需要减少上游身份数量的场景，但多个客户会话会共享线程身份，管理员应按业务风险主动选择。

收敛后的 `installation_id/session_id/thread_id/turn_id/window_id（安装/会话/线程/轮次/窗口编号）` 会同时写入出站 Header（请求头）、`x-codex-turn-metadata` 和请求体 `client_metadata`，同一请求保持一致。HTTP（普通请求）与 WebSocket（长连接）的 `response.create` 均执行同一策略；WebSocket 握手头与首轮元数据一致，后续轮次在不能更新握手头的既有连接上通过 `client_metadata` 刷新轮次字段，重连时同步新一轮握手头。该逻辑不修改连接池粘性键、`prompt_cache_key（提示缓存键）` 或 `previous_response_id（上次响应编号）`，同一轮重试也复用原轮次标识。该设置只允许 OpenAI OAuth 账号，普通 API Key（接口密钥）或其他平台提交会返回 HTTP `400`。

隔离规则：

- `fixed_group（固定分组）` Key 只允许从绑定的本地分组选择账号，不会为了获取模型清单跨分组回退。
- API Key 账号必须显式配置自定义 Base URL；缺少地址、地址不合法或使用官方 `api.openai.com` 时返回配置错误。官方 OpenAI API 的普通 `/v1/models` 不能伪装成 ChatGPT Codex 模型清单来源。
- API Key 原始模型清单使用 30 秒 fresh cache（新鲜缓存）和 5 分钟 stale cache（过期兜底缓存），并用 `singleflight（并发合并）` 避免多个 Codex 客户端同时刷新时重复请求上游。仅缓存不超过 1 MiB 的响应，最多保存 64 个上游身份；完整响应读取上限仍为 8 MiB。
- 客户端在首次刷新期间取消请求时，已启动的共享后台刷新会继续在 15 秒上游超时内完成，后续客户端可以复用结果。过期缓存会触发后台重新验证，但在 handler（处理器）层只作为 last-known-good（最后有效）候选：网关继续尝试同一固定分组的其他账号；没有更新鲜的有效清单时才返回该旧结果。
- 网络错误、超时、`429` 和 `5xx` 会排除失败账号，并按网关账号切换上限在同一固定分组继续选择其他账号；普通 `4xx`、Base URL 和凭据配置错误不会盲目遍历全部账号。
- 上游即使返回 HTTP `2xx`，响应也必须是 JSON object（结构化对象），包含顶层 `models` 数组；每个模型项必须带非空 `slug` 或 `id`，同一清单内模型编号不能重复。空 `models` 数组是合法的明确空能力，不会回退默认全集。普通 OpenAI `{"object":"list","data":[]}`、畸形 JSON、错误字段类型、缺少模型编号、重复模型或超过 8 MiB 的响应会标记为可重试上游错误 `OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST` / `OPENAI_CODEX_MODELS_BODY_TOO_LARGE`，在固定分组内换号，且不会写入 fresh/stale cache（新鲜/过期缓存）或覆盖最后有效清单。
- 返回模型会按账号 `model_mapping（模型映射）` 转成客户实际可请求的模型名。
- 返回模型同时受分组自定义模型列表、API Key（接口密钥）的 `allowed_models（允许模型）` 和分组生图权限限制。
- 受限清单使用 gateway-lite（轻量网关）重新生成的 `ETag（缓存标识）`；不同分组或不同权限不能复用彼此的缓存结果。
- 响应头 `X-Sub2API-Codex-Models-Source` 标记安全来源：`upstream（上游实时）`、`fresh-cache（新鲜缓存）` 或 `last-known-good（最后有效兜底）`。返回 `last-known-good` 时同时写入管理员运维日志，包含 API Key、固定分组和本地账号编号，但不包含上游密钥或完整凭据。
- 上游模型清单不可用且没有未超过 5 分钟最大陈旧期的 last-known-good 时返回 `502`；当前分组没有可用 OpenAI 账号且也没有可用兜底时返回 `503`，不会伪造静态清单。

### 请求来源观测字段

gateway-lite（轻量网关）的 access log（访问日志）会记录脱敏后的客户端来源摘要，便于排查慢请求、流式超时和客户端兼容性问题。`client_ip（客户端地址）` 与 API Key ACL（接口密钥访问控制）、hybrid auth（混合鉴权）、后台会话和操作审计共用 `security.trust_forwarded_ip_for_api_key_acl + server.trusted_proxies` 的安全解析口径，不会单独信任客户端伪造的 `CF-Connecting-IP`、`X-Real-IP` 或 `X-Forwarded-For`。字段会写入标准日志，并同步进入 `ops_system_logs（运维系统日志）`：

为避免高并发成功流量同步争抢标准输出和日志文件锁，2xx 成功模型请求的 access log（访问日志）固定保留 1% 运行样本，样本带 `sample_every=100`；4xx/5xx、Gin error（框架错误）和非模型接口仍逐条完整记录。该采样不影响 usage（用量）、request log（请求日志）、额度租约、账本和错误审计。

- `user_agent（客户端标识）`：来自 `User-Agent`，最长保留 512 字符。
- `content_length（请求体长度）`：来自 HTTP `Content-Length`，未知时为 `-1`。
- `host（请求域名）`：来自 Host，最长保留 255 字符。
- `referer（来源页面）`：来自 `Referer`，最长保留 512 字符。
- `x_request_id_header（请求编号头）`：客户端传入的 `X-Request-ID`。
- `x_client_request_id_header（客户端请求编号头）`：客户端传入的 `X-Client-Request-ID`。
- `effective_client_request_id（实际链路编号）`：网关生成或保留后的链路请求编号。

安全说明：访问日志不会记录 `Authorization（鉴权头）`、Cookie（浏览器凭据）或完整 request body（请求体）。

## 后台登录接口

gateway-lite（轻量网关）保留最小后台 auth（认证）接口，方便管理员登录区域网关后台：

- `POST /api/v1/auth/login`
- `POST /api/v1/auth/login/2fa`
- `POST /api/v1/auth/refresh`
- `POST /api/v1/auth/logout`
- `GET /api/v1/auth/me`
- `POST /api/v1/auth/revoke-all-sessions`
- `GET /api/v1/settings/public`

gateway-lite（轻量网关）不注册客户侧注册、找回密码、OAuth（第三方登录）注册、邀请码和优惠码校验接口。

首次启动管理员：

- gateway-lite（轻量网关）主服务启动后会检查 users（用户表）。
- 发行二进制不内置固定后台账号或密码。
- 当 users（用户表）为空且不存在 admin（管理员）时，必须通过 `ADMIN_EMAIL`、`ADMIN_PASSWORD` 环境变量，或 `config.yaml（配置文件）` 的 `admin.email`、`admin.password` 显式提供首次管理员凭据。
- 空库未提供完整凭据时拒绝启动并给出配置提示，避免自动创建公开弱口令账号。
- gateway-lite（轻量网关）模式下，如果已经存在任意 admin（管理员），启动时不会覆盖密码，也不会同步默认账号。
- standard（标准）模式如果已经存在任意用户或管理员，则跳过默认账号创建，避免覆盖真实账号。
- `AUTO_SETUP`（自动初始化）模式同样要求显式设置 `ADMIN_EMAIL` 和 `ADMIN_PASSWORD`。
- gateway-lite（轻量网关）后台页面默认随二进制嵌入。执行 `make build` 会先构建 `frontend`，再生成带后台页面的轻量网关二进制；普通 `go build ./cmd/server` 会嵌入当前 `internal/web/dist` 中已有的后台页面，不需要额外 `-tags embed`。

`config.yaml（配置文件）` 示例：

```yaml
server:
  max_header_bytes: 65536
admin:
  email: admin@example.invalid
  password: REPLACE_WITH_A_STRONG_ADMIN_PASSWORD
gateway:
  max_body_size: 268435456
  text_max_body_size: 33554432
gateway_lite:
  region: local
  gateway_code: local-gateway
  redis_prefix: jnm:gateway-lite
  media_proxy_enabled: true
  # 留空返回同源相对媒体地址；显式填写支持 HTTP/HTTPS 基础地址，含本机和内网节点。
  # 该地址仅用于客户端链接，上游媒体抓取仍要求安全 HTTPS。
  public_url: ""
  media_proxy_ttl_seconds: 900
  admin_sync_key: dev-gateway-admin-key
  control_plane_url: http://127.0.0.1:8088
  control_plane_token: dev-internal-token
  control_plane_timeout_ms: 1000
  runtime_health_interval_seconds: 15
  runtime_active_window_seconds: 300
  config_sync_interval_seconds: 30
  cache_invalidation_interval_seconds: 5
  legacy_snapshot_scheduler_authority: false
  usage_queue_pending_alert_threshold: 1000
  usage_queue_dead_alert_threshold: 1
  invalid_auth_abuse_enabled: true
  invalid_auth_abuse_threshold: 120
  invalid_auth_abuse_window_seconds: 60
  invalid_auth_abuse_block_seconds: 60
  invalid_auth_abuse_capacity: 16384
  invalid_auth_abuse_redis_timeout_ms: 25
redis:
  host: 127.0.0.1
  port: 6379
  username: "" # Redis 6+ ACL 用户名；默认用户留空
  password: ""
  db: 0
security:
  # 默认关闭；开启后敏感后台操作要求一次性 TOTP 授权。
  step_up_enabled: false
  step_up_ttl_seconds: 300
```

`gateway_lite.media_proxy_enabled`（生图媒体中转开关）默认开启。开启后，
OpenAI/Grok 生图返回的远程图片地址会改为轻量网关签发的短期地址，客户无需
直接访问上游域名；`b64_json` 和输入图片地址保持不变。`gateway_lite.public_url`
为空时返回同源相对地址，否则必须是无用户名密码、无查询参数和无片段的
`https://` 地址。`gateway_lite.media_proxy_ttl_seconds` 控制签名有效期，允许
范围为 60--86400 秒，默认 900 秒。媒体令牌密钥由已配置的 `jwt.secret` 派生，
不会在配置文件或接口中单独暴露；如果启动阶段没有 JWT 密钥，媒体中转会暂时
跳过并记录告警，不影响普通文本请求。

`redis.username（Redis 访问控制用户名）` 也可通过环境变量 `REDIS_USERNAME` 设置。空值保持原先只使用密码的连接行为；用户名与密码都会传给 Redis 客户端，但不会通过健康接口、后台状态或日志回显。

`security.step_up_enabled（敏感操作二次验证开关）` 默认是 `false`，升级后不会因为管理员尚未配置 TOTP（动态验证码）而锁死后台。设为 `true` 后，敏感路由继续执行现有强校验：只接受已启用 TOTP 的浏览器 JWT（登录会话），一次性授权绑定操作范围、会话、来源地址和浏览器标识，并在 `security.step_up_ttl_seconds` 内有效；机器管理密钥不能绕过该校验。

`GET /api/v1/settings/public` 会返回 UI capabilities（界面能力）字段，前端可以用它隐藏不适合当前运行模式的菜单：

```json
{
  "run_mode": "gateway-lite",
  "ui_surface": "gateway-lite-admin",
  "gateway_lite_admin_enabled": true,
  "customer_self_service_enabled": false
}
```

字段说明：

- `run_mode`：run mode（运行模式），gateway-lite（轻量网关）节点返回 `gateway-lite`。
- `ui_surface`：UI surface（界面形态），gateway-lite（轻量网关）节点返回 `gateway-lite-admin`。
- `gateway_lite_admin_enabled`：gateway-lite admin（轻量网关后台）是否启用。
- `customer_self_service_enabled`：customer self-service（客户自助功能）是否启用；gateway-lite（轻量网关）返回 `false`，前端应隐藏充值、注册、邀请、用户中心等客户侧入口。

## 后台管理接口

gateway-lite（轻量网关）保留 admin（后台管理）里的运维功能，方便管理区域网关：

- `/api/v1/admin/dashboard/*`：dashboard（仪表盘）和统计概览。
- `/api/v1/admin/accounts/*`：accounts（账号池）管理、测试、刷新和批量维护。
- `/api/v1/admin/groups/*`：groups（分组）管理和容量统计。

### Kimi、智谱 GLM、DeepSeek 账号协议

管理员创建、编辑或批量维护 Kimi、智谱 GLM、DeepSeek API Key（接口密钥）账号时，`credentials（凭据）`支持以下字段：

- `account_mode`：`payg（按量付费）`或 `coding（编程套餐）`；DeepSeek 只使用 `payg`。该字段决定供应商默认地址和账号连接测试方式，不改变主站销售计费口径。
- `api_protocol`：`chat_completions（聊天补全）`、`anthropic（消息协议）`、`responses（响应协议）`或 `adaptive（自适应）`。Kimi、智谱不支持固定 `responses`，非法组合安全回退 `chat_completions`；DeepSeek 支持四档。
- `base_url`：固定协议或历史账号使用的基础地址。旧账号缺少新增字段时继续按 Chat Completions 和供应商默认地址工作。
- `api_base_urls`：仅自适应模式使用，结构为 `{ "chat_completions": "...", "anthropic": "...", "responses": "..." }`；每个入站协议选择自己的地址，缺失项回退供应商与套餐对应的官方默认地址。

连接测试与“同步上游模型”复用同一套协议解析、API Key 和安全请求头覆写，不会把密钥、完整请求头或带凭据 URL 写入响应与日志。自适应账号可分别承接 `/v1/chat/completions`、`/v1/messages` 和 `/v1/responses`：Kimi、智谱没有原生 Responses 时由网关转换到 Chat Completions；DeepSeek 原生 Responses 强制 `store=false` 并删除 `previous_response_id（上次响应编号）`，符合其无状态约束。国产原生 Anthropic 流同时兼容 `cached_tokens（缓存令牌）`及 5 分钟/1 小时缓存创建明细，避免缓存用量漏计。

管理员可按需刷新国产供应商账号的官方额度或余额；查询只在管理员点击时访问上游，不加入用户请求热路径，也不启动额外的周期探测：

- `GET /api/v1/admin/cn-providers/accounts/:id/quota`：查询 Kimi、智谱 GLM `coding（编程套餐）`账号的 5 小时与周额度窗口，返回 `provider`、`tiers[].window/used_percent/reset_at`、`plan_level`、`fetched_at` 和探测状态；成功结果保存到账号 `extra（扩展快照）`，页面刷新后仍可展示最近一次有效数据。
- `GET /api/v1/admin/cn-providers/accounts/:id/balance`：查询 Kimi、DeepSeek `payg（按量付费）`账号余额，返回 `balances[].currency/balance`、`available`、`fetched_at` 和探测状态；智谱按量账号没有稳定官方余额端点，接口会明确拒绝而不会伪造余额。

两类探测均复用账号代理、请求头覆写和 URL allowlist（网址允许清单）安全校验；同一账号的并发点击通过 `singleflight（并发合并）`只发起一次上游请求。HTTP `401/403` 只返回凭据失效结果，不覆盖最近成功快照；接口错误不会泄露 API Key、代理凭据或完整上游响应。

Kimi 上游将单账号并发达到上限时返回固定的 `403` 文案
`You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again.`。
网关会把该响应识别为可恢复的账号容量限制：当前请求切换其他账号，并将原账号临时停调约 10 分钟；不会增加通用 `403` 永久禁用计数，也不会把有效账号标记为认证失效。只有完全匹配该文案时才使用此专用策略，其他 `403` 仍按常规账号错误策略处理。

Composite（组合分组）的 `target_platform（目标平台）`新增 `kimi`、`zhipu`、`deepseek`。模型前缀识别、显式模型路由、渠道定价、账号模型映射、审计、故障切换和调度快照均沿用现有组合分组链路；`/v1/messages` 与 `/v1/messages/count_tokens` 可进入国产目标。WebSocket（长连接）目前仍只支持 OpenAI/Grok 目标，若组合路由解析到国产平台会明确拒绝，不会静默错发到其他平台。数据库 migration（迁移）为 `224_composite_routes_add_cn_providers.sql`。

### `GET /api/v1/admin/groups/capacity-summary`

返回所有启用分组的容量和实时入口请求统计。除既有 `concurrency_*`、`sessions_*`、`rpm_*` 字段外，新增 `ingress_requests_used`（入口实时请求数）：请求通过鉴权及用户/企业并发门禁后，从该分组进入且仍在处理中的请求数量。该字段按 `RequestMeta.OriginalGroupID`（原始入口分组）计数，即使请求后续故障转移到其他分组，仍归原入口分组；它不参与限流、计费或账号调度。

入口统计使用 Redis 有序集合并带 TTL（过期时间）清理；长请求会低频续租，请求完成、取消或超时后立即释放。主站快照密钥从 `RequestMeta.OriginalGroupID`（原始入口分组编号）读取入口分组；本地 `glk_` 密钥使用独立的 request context（请求上下文）入口标记，两类请求都参与统计，但本地密钥不会因此获得主站额度租约或远程计费身份。Redis 暂不可用时，容量汇总接口返回错误，后台显示 `—`（未知），不能把未知伪装成 `0`；请求热路径仍继续放行（fail-open，统计失败不阻断业务）。后台分组列表约每 5 秒刷新一次；容量字段仍保持账号槽位口径，不能与入口请求数混用。

分组创建、编辑、复制和查询对象包含两个仅用于用户界面展示的字段：

- `display_brand（展示类别）`：最长 40 个字符，例如 `OpenAI`、`Kimi`、`GLM`。为空时按 `platform（调度平台）` 使用默认类别。
- `display_icon（内置图标）`：只允许 `openai/claude/gemini/grok/kimi/glm/deepseek/qwen/doubao/minimax/universal`，不接受外部 URL（网址）或任意 SVG（矢量图）代码。

这两个字段不会改变 `platform（调度平台）`、账号筛选或请求协议。例如 Kimi、GLM 的 OpenAI-compatible（OpenAI 兼容）分组仍保持 `platform=openai`，只通过展示字段改变主站卡片身份。数据库 migration（迁移）为 `171_group_display_identity.sql`；字段变化会进入 config push（配置主动推送），由主站镜像后展示。

分组创建、编辑、复制和查询对象还包含 `local_only（仅轻量网关本地使用）`：

- 类型为 boolean（布尔值），默认 `false`。`false` 保持现有行为；`true` 表示该分组不发布给 control-plane（主站控制面），只允许当前轻量网关创建的 `glk_` 本地 API Key（接口密钥）绑定和请求。
- `local_only=true` 的分组不会进入主动配置推送的 `groups[]（分组快照）`、`effective_policies[]（最终生效策略）`，账号快照也会移除对该分组的 `group_ids（分组编号）`引用。主站完成下一次全量同步后，不再展示该分组，也不能新建或切换主站 Key 到该分组。
- `GET /api/v1/admin/groups` 和 `GET /api/v1/admin/groups/all` 默认不按该字段过滤，保证轻量网关本地后台仍可维护本地专用分组。主站实时分页列表使用 `GET /api/v1/admin/groups?publishable_only=true`，权威完整回拉使用 `GET /api/v1/admin/groups/all?include_inactive=true&publishable_only=true`；过滤在数据库计数前生效，分页内容、总数以及 active（启用）/inactive（停用）列表都不会泄露 `local_only=true` 分组。
- 主站读取单项详情和有效模型时分别使用 `GET /api/v1/admin/groups/:id?publishable_only=true` 与 `GET /api/v1/admin/groups/:id/effective-models?publishable_only=true`。即使主站知道本地编号，`local_only=true` 分组也按不存在返回；不带参数的轻量网关本地后台仍可正常维护。
- 已缓存旧快照的主站 Key，以及主站尚未配对时回退鉴权的普通本地 Key，都不能绕过：任何未经轻量网关本地 `glk_` 鉴权的请求都会在 lease（额度租约）和上游调度之前返回 HTTP `403`，错误码为 `GROUP_LOCAL_ONLY`。fallback（兜底）、关键词重路由、ForcePlatform（强制平台）和 Gemini 原生协议同样执行该边界；`glk_` 本地 Key 继续执行分组状态、模型、账号、内容审计和本地额度规则。

分组创建、编辑、详情、复制和查询对象还包含 `per_user_concurrency（分组每用户并发）`：

- 只接受 `0..100000` 的整数，默认 `0` 表示该分组不额外限制。管理员后台提供“不限、1、2、3、5、10”快捷选项，也允许输入精确数字。
- 设置为正数后，同一用户在该分组中所有 API Key（接口密钥）的活跃请求合并计算，不能通过新建或轮换 Key 绕过；不同用户、不同分组的计数互相隔离。
- 该上限与用户 T0/T1/T2 等级并发同时生效，任一上限达到都不能放行。申请由一次 Redis Lua（缓存原子脚本）同时检查用户等级、最终分组和 API Key 活跃槽位，失败不会残留部分槽位。
- 内容审计把请求切换到允许分组后，按本次请求最终实际分组执行限制。普通 HTTP/SSE（服务端事件流）覆盖完整请求生命周期；OpenAI Responses WebSocket v2 按每个活跃 turn（轮次）占槽、空闲连接不占槽；Live（实时通话）在创建成功后把普通槽位原子替换成持续会话租约，并在续租、断开、过期和结算时维护同一分组槽位。流式结束、普通请求结束、客户端断开和超时都进入幂等释放；Redis 无法确认分组槽位时失败关闭。
- 主站快照 API Key 在轻量网关没有本地数据库 ID，限流与活跃统计使用公开 `key_id` 派生的稳定负数命名空间；不会保存或散列 API Key 原文。本地 Key 继续使用正数数据库 ID，二者不会碰撞。
- `per_user_concurrency=0` 保持原有用户并发热路径，不增加分组槽位或 Redis 往返。数据库 migration（迁移）为 `198_add_group_per_user_concurrency.sql`。

分组创建、编辑、详情、复制和查询对象新增 `model_mapping（普通分组模型映射）`，类型为 `{客户请求模型: 分组目标模型}`。创建与编辑请求示例：

```json
{
  "model_mapping": {
    "gpt-public": "gpt-5.6-sol",
    "gpt-5.*": "gpt-5.6-sol"
  }
}
```

分组对象还支持 `billing_model_source（计费模型来源）`，后台创建、编辑页提供三项点击选择：

- `requested`：按客户原始请求模型查价。
- `channel_mapped`：按分组与渠道映射完成后的模型查价。
- `upstream`：按最终发送给上游账号的实际模型查价。
- `response_model`：尝试按上游成功响应中声明的模型查价。该模式的语义是“上游确实降级时把优惠让给用户”，不是允许上游自报更贵模型抬高账单。它只用于普通 Token（令牌）请求，并且必须同时满足：同一请求没有冲突声明、模型能通过管理员显式渠道价格或本地精确价格目录识别、响应模型重算金额不高于原计费金额、不会把原本非零费用降为零。若原模型采用渠道定价，响应模型也必须命中渠道定价，禁止绕过管理员加价后退回全局价格；任一条件不满足都会静默回退原计费模型。图片、视频、网页搜索、Grok 搜索附加费和语音等按次/按量请求不采用该来源，`CostOverride（异步任务固化成本）` 也不会被响应声明覆盖。

新建分组默认 `requested（按客户原始请求模型计费）`，对应 database migration（数据库迁移）`194_group_billing_model_source_requested_default.sql`；分组明确设置后优先于渠道的旧 `billing_model_source`。历史分组字段为空时继续继承渠道，避免升级后静默改变既有账单。该字段会进入分组详情、复制、API Key（接口密钥）鉴权缓存和 scheduler cache（调度缓存）；鉴权快照版本为 `18`，旧版本缓存自动回源重建，避免 Redis 命中后丢失计费来源。gateway-lite（轻量网关）运行时若发现本地分组的 scheduler cache 条目丢失，只在该异常路径通过本地权威数据库读取一次，并立即写回 Redis（缓存服务）；相同并发请求由 singleflight（单次合并）合并，正常热路径不增加数据库查询。首次增加字段的 database migration（数据库迁移）为 `193_group_billing_model_source.sql`；`214_group_billing_model_source_response_model.sql` 扩展数据库约束以允许 `response_model`，没有新增数据库列。

- 精确匹配优先；源模型只允许一个末尾 `*`，多个通配符、非末尾通配符和单独 `*` 会被拒绝；目标必须是具体模型，不能包含通配符。
- 源模型按去空格、小写规范化，同一个源模型不区分大小写且只能出现一次；空源、空目标、直接或间接循环映射返回 HTTP `400`。
- 普通分组拒绝明显跨厂商映射；Composite（聚合分组）继续使用其显式跨平台路由能力。映射不会递归执行，单个请求只解析一次确定目标。
- 生效顺序为“客户请求模型 → 分组目标模型 → 渠道映射 → 账号实际模型”。账号选择和可用模型检查使用分组目标模型；计费使用该目标模型价格，日志保存完整映射链。
- `effective policy（最终生效策略）`仍以客户请求模型作为用户可见 `model`，同时上报 `upstream_model/price_model（实际调度模型/计价模型）`和 `route_source=group_mapping`；目标模型没有真实可调度账号时，不公开源模型别名。
- 普通客户端读取 `/v1/models（模型列表）`时展示映射左侧的客户别名；只有映射目标存在真实可调度账号时才展示。管理员禁用目标计价模型后，对应别名也会同步隐藏，避免通过未知别名绕过模型禁用。
- 即使管理员显式启用 legacy snapshot scheduler authority（旧主站快照调度权威兼容开关），轻量网关仍以本地数据库中全部未删除分组（包括停用分组）的 `local_only` 为安全边界；本地分组查询或拒绝态 Redis（缓存服务）写入失败时整次更新失败关闭，不允许旧快照覆盖或清理本地专用账号。
- 复制分组会保留 `local_only`。数据库 migration（迁移）为 `190_group_local_only.sql`。

### 智能分流映射（weighted_model_mapping）

分组创建、编辑、详情、复制和查询对象新增 `weighted_model_mapping（智能分流映射）`，类型为 `{公开模型: [{model, weight}]}`，仅 `platform=openai` 分组可用。请求示例：

```json
{
  "weighted_model_mapping": {
    "pro-fast": [
      {"model": "gpt-5.6-sol", "weight": 70},
      {"model": "gpt-5.6-terra", "weight": 30}
    ]
  }
}
```

- 语义：客户请求命中公开模型时按权重平滑轮询选一个目标模型完成调度与转发；计费与用量按实际转发的目标模型查价，客户端可见模型名保持公开模型。
- 键必须精确（不支持通配符），且与 `model_mapping` 键互斥：同一公开模型只能出现在其中一边；精确分流优先于 `model_mapping` 的通配符规则。
- 语义边界（单跳）：分流目标是终端上游模型——命中后直接按目标选账号与转发，不会再套用任何 `model_mapping` / `weighted_model_mapping` 规则。因此目标允许与其他映射的公开键同名（同名只是"公开模型"与"上游模型"撞名，两条路径互不叠加）；仅禁止目标等于自身源（等于没分流）。
- 校验：每个公开模型 2-8 个目标、单分组总目标 ≤64；`weight` 取 1-100（相对比例，自动归一化，不要求和为 100）；目标必须是具体模型、同平台、不得等于自身源模型；违反返回 HTTP `400 INVALID_GROUP_WEIGHTED_MODEL_MAPPING`。
- 自动禁用：目标级健康按 `(分组, 公开模型, 目标模型)` 维护在网关进程内——上游 404/套餐不含该模型（plan-gated）时冷却 30 分钟；无法归因账号的连续失败渐进冷却（2 次 10 秒、5 次 45 秒、8 次 5 分钟）；401/403/429 等账号级信号不记目标健康。冷却到期后半开探测（只放行 1 个请求），成功立即恢复满权重，失败冷却翻倍（上限 30 分钟）。全部目标同时冷却时 fail-open（宁可试降级目标不直接拒绝）。
- 兜底顺序：分流首选目标 → 其余健康目标（权重降序）→ 分组 `fallback_model`（保持原语义）→ 真实错误。
- 健康 API：`GET /api/v1/admin/groups/:id/model-route-health` 返回 `{group_id, targets:[{public_model, target_model, state(healthy/failing/cooling/recovering), consecutive_failures, last_reason, cooldown_until, cooldown_remaining_ms, probe_until}]}`；`DELETE /api/v1/admin/groups/:id/model-route-health`（body `{"public_model":"...","target_model":"..."}`）手动清除冷却立即恢复分流。健康状态是进程内存观测，网关重启即清零。
- 该字段随分组复制、配置快照（`GatewayGroupSnapshot.weighted_model_mapping`）与鉴权热路径透传；数据库 migration（迁移）为 `229_group_weighted_model_mapping.sql`。

分组对象同时包含 `minimum_tier（分组最低等级）`，只接受 `null/0/1/2`：

- `null` 表示继承主站 gateway node（网关节点）的等级；`0/1/2` 分别表示 T0/T1/T2。
- 最终规则为 `max（节点等级，分组最低等级）`，所以分组只能提高门槛，不能把 T1 节点降成 T0。
- 创建、编辑、详情、复制、配置主动推送和 effective policy（最终生效策略）均保留该字段；复制分组不会丢失等级配置。
- 请求热路径会在模型检查和 lease（额度租约）申请前校验用户等级。等级不足返回 HTTP 403 和 `GROUP_TIER_REQUIRED`；关键词允许分组、fallback（兜底）和自动调度也会排除等级不足的候选，不改变倍率或计费价格。

数据库 migration（迁移）为 `177_add_group_minimum_tier.sql`。主站快照还会携带只读 `effective_required_tier（最终所需等级）`；gateway-lite（轻量网关）运行时取主站节点门槛与本地最新分组门槛中的较高值，避免同步窗口绕过。

OpenAI 分组还可携带以下 reasoning effort（推理力度）策略字段：

- `max_reasoning_effort（推理力度上限）`：空字符串表示不限制；非空值只允许 `minimal/low/medium/high/xhigh/max`。
- `reasoning_effort_mappings（推理力度精确映射）`：数组元素为 `{"from":"minimal","to":"low"}`。请求先执行精确映射，再应用上限；同一 `from（来源档位）` 不能重复。

策略只改写客户端明确提交的 `reasoning.effort` 或兼容入口的明确推理字段；客户端没提交时不会由网关猜值。HTTP Responses、Chat Completions、WebSocket（长连接）和 WSv2（第二版长连接）共用同一规则。非 OpenAI 分组保存这两个字段会被拒绝；策略不会改变模型映射、长上下文、Priority（优先处理）或价格。数据库 migration（迁移）为 `172_group_reasoning_effort_policy.sql`。后台使用固定下拉选择，前端无需手工输入档位。
- `/api/v1/admin/proxies/*`：proxies（代理）管理和质量检测。
- `/api/v1/admin/ops/*`：ops（运维监控）、告警、日志、实时流量和请求错误。
- `GET /api/v1/admin/ops/dashboard/overview` 与 `GET /api/v1/admin/ops/dashboard/snapshot-v2` 的 `system_metrics（系统指标）` 会返回 `redis_pool_wait_count（本分钟连接池等待次数）`、`redis_pool_wait_duration_ms（本分钟累计等待毫秒）` 和 `redis_pool_timeouts（本分钟等待超时次数）`。三项来自 go-redis 进程内累计计数的窗口差值，首次采样为 `0`，Redis 客户端重启或计数回绕后自动重建基线；它们只用于容量诊断，暂不影响健康评分。数据库 migration（迁移）为 `178_add_redis_pool_window_metrics.sql`。
- `/api/v1/admin/usage/*`：usage（用量记录）查询和清理任务。
- `/api/v1/admin/settings/*`：settings（系统设置）中和网关运维相关的配置。
- `GET /api/v1/admin/gateway-lite/config`：gateway-lite config（轻量网关配置）读取当前主站控制面连接配置；不会返回完整 `control_plane_token（主站控制面令牌）`。
- `PUT /api/v1/admin/gateway-lite/config`：gateway-lite config（轻量网关配置）写入 `config.yaml（配置文件）` 并刷新允许热更新的运行参数；托管绑定身份不能由普通配置接口改写，主站换绑必须走 `/api/v1/admin/gateway-binding` 的排空、断开及重新登记流程。端口、数据库连接等启动级配置仍属于重启范围。
- `/api/v1/admin/gateway-binding/*`：主站能力探测、对接密钥验证、登记/启用状态及安全排空解绑管理，具体方法、字段和 202（异步受理）语义见下文绑定合同。
- `GET /api/v1/admin/settings/web-search-emulation`：web search emulation（网页搜索模拟）配置；如果尚未配置，返回默认关闭配置 `{"enabled":false,"providers":[]}`，避免后台首次打开账号池或渠道页时出现 404。
- `/api/v1/admin/model-prices/*`：model prices（模型价格）统一管理，读取默认价格目录并维护本地覆盖价。
- `/api/v1/admin/channels/*`：channels（渠道）管理。
- `/api/v1/admin/channel-monitors/*`：channel monitors（渠道监控）管理。
- `/api/v1/admin/channel-monitor-templates/*`：channel monitor templates（渠道监控模板）管理。
- `/api/v1/admin/error-passthrough-rules/*`：error passthrough（错误透传）规则。
- `/api/v1/admin/tls-fingerprint-profiles/*`：TLS fingerprint（TLS 指纹）模板。
- `/api/v1/admin/scheduled-test-plans/*`：scheduled tests（定时测试）计划。
- `/api/v1/admin/risk-control/*`：risk control（风控）配置和日志。
- `/api/v1/admin/openai/*`、`/api/v1/admin/gemini/*`、`/api/v1/admin/antigravity/*`、`/api/v1/admin/grok/*`：上游账号 OAuth（授权）维护能力。

### 本轮关键词、语种、Skill 与允许分组路由

`GET/PUT /api/v1/admin/risk-control/config` 的以下字段只描述 `keyword_only`（本地关键词）路径；`ai_only`（纯 AI）完整覆盖、外发授权和能力边界见本文首部，不套用尾部新增内容或本地不外发规则。

- `local_keyword_only=true`：关键词路径使用本地字符标准化、语种识别和关键词自动机，不为关键词路由调用第三方接口；读取纯 AI 配置不能据此字段推断审查资料未外发。
- `allowed_languages`：管理员允许的语种代码数组，后台以标签多选提交；当前内置 `zh/en/ja/ko/es/fr/de/pt/it/ru/ar/hi/th/tr/vi/id/he/el`，空数组回退为中文和英文。
- SQL（数据库查询语句）、代码围栏和可确认的结构化代码只跳过语种门禁，原文仍完整进入关键词自动机；代码前后的自然语言说明继续参与语种识别。葡萄牙语等非英语拉丁语种至少需要两个独立证据，`a/as/o/os` 这类 SQL 关键字或表别名不再用于定性。
- `scan_latest_user_input`、`scan_top_level_instructions`、`scan_new_skill_content`、`scan_new_tool_output`：分别控制本轮最后用户输入、客户端系统指令、本轮新读取 Skill（技能内容）和本轮新工具结果，默认全部开启。客户端系统指令在上游协议转换前统一覆盖：Responses 的顶层 `instructions` 与 `input[] role=system/developer`；Chat Completions 的顶层 `instructions` 与 `messages[] role=system/developer`；Anthropic Messages 的顶层 `system`；Gemini 的顶层 `systemInstruction`；OpenAI Live 创建通话时初始 Session 的 `instructions`；以及 Live Sideband（旁路控制连接）中 `session.update.session.instructions`、`response.create.response.instructions` 和兼容的事件顶层 `instructions`。Responses WebSocket（响应长连接）的每个 `response.create` 继续按同一 Responses 口径检查。用户、Skill 和工具只提取请求尾部新增内容，不重复扫描普通历史会话；控制指令只要仍存在于进入预审的请求副本中就按当前值检查。普通本轮内容与全部客户端控制指令分别保留 256K、512K 审查预算，不能互相用超长安全文本挤掉；普通内容和控制指令之间设置仅本地可见的匹配边界，而多个控制指令按真实上游语义拼接检查，拆分在相邻控制片段中的短语仍可命中。
- 关键词模式下，上述客户端系统指令只进入本地语种识别和关键词自动机，不新增第三方资料外发；纯 AI 在管理员明确外发同意后审查全部角色和控制指令，未同意时返回 error（审查不可用）。
- 开启客户端系统指令扫描且请求进入配置的分组/模型范围时，同一个 JSON object（结构化对象）重复出现顶层 `instructions/input/messages`、Anthropic `system`、Gemini `systemInstruction` 或 Live 控制事件 `instructions`（含大小写变体）固定返回 `400 invalid_request_error`；系统/开发者消息内的 `role/content/type/text`、Anthropic system block（系统片段）的 `type/text`、Gemini `parts/text` 同样拒绝重复，避免不同 JSON 解析器分别采用不同值。全部客户端控制指令合计在 JSON 解码及 Unicode NFKC（兼容规范化）后超过 512 KiB 固定返回 HTTP `413`；JSON 引号和 Unicode 转义不计入有效指令大小，网关不会只检查前半段却把未检查尾部继续发送上游。关闭该扫描开关或请求不在审查范围内时，不额外施加这两条审查封装限制。
- Anthropic `x-anthropic-billing-header` 只有在整块内容均为合法 `cc_version/cc_entrypoint/cch` 机器归因字段时才跳过本地指令审查；只要后面追加自然语言、未知字段、重复字段或换行尾文，就作为普通客户端系统指令完整扫描。这样 API Key（接口密钥）直通 Anthropic 的原始 system block 不能借合法前缀夹带未审查指令。
- Anthropic `system[]` 和 Gemini `systemInstruction.parts[]` 的相邻文本片段同时按“带分隔连接”和“原样连续连接”两种上游可理解口径扫描，例如 `forbid` 与下一块 `den directive` 仍会命中 `forbidden directive`。该补充口径不用于 Responses/Chat 的独立控制字段；命中贡献块定位与历史隔离使用同一口径，只删除真正参与危险短语的片段，保留相邻安全规则。
- `keyword_group_routing_enabled`：仅在 `keyword_block（强拦截）` 命中后查找允许分组。`keyword_audit（只审计）` 写入一条日志后立即使用原分组放行，绝不调用允许分组解析器；语种不允许时直接拒绝，也不能借切组绕过。

分组创建、编辑、详情、复制、配置推送和 effective policy（最终生效策略）对象新增 `allow_blocked_keyword_requests`。默认 `false`；设为 `true` 表示该分组允许全部强拦截关键词，不区分关键词类别。候选仍必须同时满足分组有效、协议和模型支持、API Key（接口密钥）套餐分组权限、专属分组权限、计费资格以及存在可调度账号。

命中后的成功切组只作用于当前请求，不修改 Key 的持久分组。请求后续模型映射、账号选择、分组/账号倍率、长上下文、Priority（优先处理）、图片和最终用量都使用实际分组；下一次未命中请求仍从 Key 原始分组开始。放行、切组和拒绝响应都会按实际决定增加以下可用响应头；没有有效分组编号时省略对应编号头：

- `X-Gateway-Original-Group-ID`：原始分组编号。
- `X-Gateway-Effective-Group-ID`：本次实际分组编号。
- `X-Keyword-Policy-Route`：稳定原因，目前成功切组为 `rerouted`。

`GET /api/v1/admin/risk-control/logs` 支持 `page/page_size/result/endpoint/search/from/to/group_id/original_group_id/effective_group_id/content_source/detected_language/route_reason`。`page_size` 最多 100，按创建时间、编号倒序；`from/to` 接受既有时间格式，纯日期按上海时区解释，`to` 包含该日结束。分组编号必须为正整数。`search` 对请求编号、用户邮箱、密钥名称、业务模型、输入预览、两份全文及错误字段作 `ILIKE`（不区分大小写模式匹配），搜索到全文不代表列表响应包含全文。

`content_source=user|instructions|skill|tool|mixed` 中，`instructions` 表示任一已接入客户端系统指令来源，不只 Responses 顶层字段。日志同时返回原始/实际分组、`detected_languages/keyword_policy_rerouted/keyword_policy_reason` 与仅管理员可见的 `matched_keyword`。关键词预览围绕真实命中位置截取，跨 Anthropic/Gemini 片段命中使用相同连接表示；无法定位时留空，不回退无关的最新用户消息。

| `result` 筛选 | 当前口径 |
|---|---|
| `pass/allow` | `action=allow/ai_shadow_allow`、`flagged=false` 且错误为空；含无已选内容的跳过，须查看 AI `phase/provider_called` 区分 |
| `hit/flagged` | `flagged=true`，或 `keyword_audit/keyword_allow_original/keyword_reroute/ai_shadow_block`；影子违规属于观察命中，不表示已处罚 |
| `blocked/block` | 实际拒绝动作 `block/keyword_block/hash_block/language_block/cyber_policy/error/ai_shadow_error/shadow_error`；影子有效 `ai_shadow_block` 不在其中 |
| `error` | 审查错误动作，以及非有效违规阻断动作的非空错误；有效 `block` 后处罚投递不确定不改算审查 error |

命中和阻断可以重叠；仅审计、原组放行、切组放行不能混入真正未命中。纯 ASCII 单词关键词按完整 word/token（单词/词元）匹配，例如 `carding` 不命中 `discarding`；安全研究包删除低上下文独立词，仅保留带动作和对象的高置信短语。接口不把 `matched_keyword` 写入用户响应头，不因语种拒绝自动封号、扣款或锁定整个会话。

从 migration（数据库迁移）`183_content_moderation_language_evidence.sql` 起，管理员风控日志还返回：

- `content_fingerprint`：`v1:` 加 128-bit HMAC（带密钥哈希）摘要。摘要以 `user_id + api_key_id` 作为隔离作用域，密钥为进程启动时随机生成；只能在同一用户、同一 API Key（接口密钥）和同一进程生命周期内识别重复内容，不能跨客户关联、跨重启跟踪或离线反查原文。
- `language_evidence`：包含 `detector_version`、输入/样本/自然文字数量、各文字系统计数、语种计数、`language_ratio_bps`（语种占比万分比）、拉丁语系独立证据数和本次门槛。这里只保存数字，不保存触发字符或证据词。

从迁移 `242_content_moderation_ai_review_detail.sql` 与 `243_content_moderation_ai_full_text.sql` 起，AI 审查日志使用以下管理员复核字段：

| 字段 | 当前含义 |
|---|---|
| `input_excerpt` | 脱敏后的有界列表预览，不等于全文；没有可提取文本时为空，不能拿错误码充当原文 |
| `full_input_text` | 当前请求固定已知字段中提取的完整脱敏文本，每字段带 `[父路径|角色]`；不按 4000 字符截尾，不是原始请求 JSON，不包含未解析扩展、远程历史或图片字节。配置/授权/图片编码失败或严格传输拒绝时，已提取文本仍可保留 |
| `review_text_excerpt` | 保留字段名，现值是实际用于本次模型调用的完整脱敏文本，不再是截断摘录。只有 `provider_called=true` 才填写；未调用、跳过、准备阶段失败或严格帧拒绝时为空/省略。图片独立送审而无文本时也可为空；不能由它单独推定未调用 |
| `ai_review_detail` | 整体判定证据，包含 `verdict/categories/reason/error_code/phase/provider_called/provider_http_status/latency_ms/provider_latency_ms/input_hash/coverage`、审查配置 `model/adapter/policy_version`、`uncovered_fields`，以及下述来源元数据。非 AI 日志通常省略 |

`GET /api/v1/admin/risk-control/logs` 返回分页预览和元数据，SQL 列表查询不读取两份全文列，响应也不返回全文。`GET /api/v1/admin/risk-control/logs/:id` 复用管理员鉴权、返回 `{code:0,message:"success",data:ContentModerationLog}`，在同一日志对象中按持久记录给出两份全文；非法/非正编号为 400，不存在为 404，存储失败按错误响应返回。空全文因 `omitempty`（空值省略）可以不出现，旧日志未保存的全文不能重建，也不能把旧截断摘录称为新完整证据。

`ai_review_detail.fragments` 仅有每个完整文本字段的 `id/parent/role/source/start_rune/end_rune`；`images` 仅有 `id/parent/role/media_type/bytes`，不包含图片字节、Data URL（内嵌数据地址）或远程图片地址。两数组均无逐段/逐图 `verdict/categories`，整体模型结论不能反推独立片段判定。`phase=preparation/provider/completed/skipped` 分别表示准备失败、调用中或调用失败、取得有效整体结论、无已选内容跳过；`provider_called` 表示实际进入 HTTP 调用，不承诺供应商已收齐请求或成功审查。`provider_http_status=200` 与 `verdict=error` 可以同时成立，表示已收到响应但正文读取、完成状态或判定合同不合格；未收到响应时没有可报告状态。

`latency_ms` 是整轮审查处理耗时，`provider_latency_ms` 是实际供应商调用耗时；无调用时后者为 0，日志 `upstream_latency_ms`（审查上游耗时）不填。`queue_delay_ms` 若存在，是既有审计记录等待落库的时间，不是 AI 等待模型队列。纯 AI 不给出概率；`highest_score/category_scores` 的旧零值或空值不能展示为“0% 安全/违规概率”。正文与原因按密钥规则脱敏，图片只记录元信息；`content_source=mixed` 表示多个文本来源，不把它误当作整体判定已经覆盖所有未知内容。

`language_block（语种拦截）` 的 `input_excerpt` 固定为空；migration（数据库迁移）`183_content_moderation_language_evidence.sql` 也会幂等清空旧版本已经保存的语种拦截摘要，后台只展示上述统计和指纹。关键词审计继续脱敏，但摘要改为实际命中位置附近的有界窗口。local detector v2（本地语种识别器第二版）按自然语言片段判断：单个假名、少量外文文件名、URL、模型名、SQL、代码和结构化 JSON 字段不再使整轮请求变成未允许语种；真实且达到独立字符、词证据与占比门槛的语种仍稳定拦截。关键词自动机继续扫描本轮完整文本，不因语种视图剔除结构化内容而漏检关键词。编号 181/182 继续为本项目明确不采用的上游 prompt audit（提示词审计）保留，避免后续运维把两类迁移混为一谈。

WebSocket（长连接）首帧可在连接上游前正常切组；上游连接建立后的新帧不能透明迁移分组，命中时返回 `retryable=true`、`route_reason=websocket_reconnect_required` 的策略错误并结束当次连接，由客户端重新连接。被拒帧不会发送给上游，并显式退回当轮额度预留，因此 Token（令牌）费用和最低占位费用均为 0。Responses WebSocket 后续 `response.create`、OpenAI Live 初始 Session 以及 Sideband 的 `session.update/response.create` 都把审查或历史隔离后的同一份 payload（请求载荷）交给真实上游，不会重新使用原始未隔离内容。

强关键词请求最终被拒绝、且没有通过原分组或允许分组发送给上游时，gateway-lite（轻量网关）只为实际包含或共同组成命中关键词的用户/Skill（技能）/工具/客户端控制指令片段保存带进程随机盐的 SHA-256 fingerprint（指纹），不保存原文。若只有系统指令命中，不会把无辜的最新用户消息加入隔离缓存。第三方客户端下一次原样重发时，网关会在审查和上游转发共用的请求副本中删除相同旧轮次及其孤立助手/工具结果：Responses/Chat 的相同顶层 `instructions` 和 Live/Sideband 对应单值指令会删除该字段；Responses/Chat 的相同 `system/developer` 项只删除自身；Anthropic `system[]` 与 Gemini `systemInstruction.parts[]` 按 text block/part（文本片段）逐项删除，仅在删除后数组为空时移除整个顶层字段，因此合法计费归因块和其他安全系统规则会继续发往上游。变化后的指令仍按新内容检查。隔离不会修改客户端本地会话；允许原组或成功切组、已经真实发给上游的内容不会登记。

fingerprint 按 API Key（接口密钥）隔离，默认有效期 7 天，每个 Key 最多 32 条、单进程最多 100000 条。缓存仅存在于当前网关进程内，进程重启后自动清空，不写 PostgreSQL（数据库）、Redis（缓存服务）或日志。主站快照 Key 没有本地数据库编号时，使用非秘密 `KeyID` 派生的稳定负数命名空间；本地 Key 使用正数数据库编号，两者不会串线。没有 fingerprint 的普通 Key 只执行一次内存空查询，不解析历史；Images（图片）端点没有多轮历史，不执行该逻辑。已经通过允许分组真实发给上游的内容不会被登记或隔离。

数据库 migration（迁移）包括 `174_group_allow_blocked_keyword_requests.sql`、`175_content_moderation_remote_user.sql`、`176_usage_log_keyword_policy_evidence.sql`、`183_content_moderation_language_evidence.sql` 和 `184_content_moderation_matched_keyword.sql`。前 3 个分别增加分组开关、内容日志证据，以及用量日志的原始/实际分组和关键词路由证据列及查询索引；183 增加 `content_fingerprint`、`language_evidence` 两列，并幂等清空旧 `language_block（语种拦截）` 记录中的原文摘要；184 持久化仅管理员可见的实际命中关键词，便于复核误判。迁移均可重复执行。

本轮内容策略失败使用稳定错误码：`unsupported_language`（语种未允许）、`keyword_allowed_group_unavailable`（没有可用的关键词允许分组）和 `keyword_group_reconnect_required`（WebSocket 已建连，需重连后重新选组）。重复或超限顶层指令属于请求结构错误，使用 `invalid_request_error`。错误响应不会返回命中的敏感关键词原文。

### OpenAI Agent Identity 与 Codex PAT 导入

以下两个接口只位于管理员鉴权路由，均要求 `Idempotency-Key（幂等键）`。同一管理员以同一幂等键重试会返回第一次结果，不会重复创建账号；响应为 `{"action":"created|updated","account":{...}}` 的统一数据包装，不回显私钥或完整令牌。

#### `POST /api/v1/admin/accounts/import/agent-identity`

上传或粘贴官方 Agent Identity `auth.json（身份文件）`。请求字段：

- `content`：必填，完整 JSON 文本。根对象必须声明 `auth_mode=agentIdentity`，并包含 `agent_identity.agent_runtime_id`、`agent_private_key`、`account_id` 和 `chatgpt_user_id`。
- `name`、`notes`、`group_ids`、`proxy_id`、`concurrency`、`priority`、`rate_multiplier`、`load_factor`：可选账号配置；新账号默认并发数为 3、优先级为 50。
- `credential_extras`：可选的非敏感账号凭据扩展，用于继承 `model_mapping（模型映射）`、compact 映射、Base URL（上游基础地址）等既有配置。服务端会删除 `auth_mode`、Agent 私钥、runtime ID、task ID、OAuth Token、API Key、Cookie 和客户端密钥等鉴权字段，不能用它覆盖身份文件中的权威凭据。
- `extra`、`skip_default_group_bind`、`confirm_mixed_channel_risk`：沿用普通账号创建语义。

服务端只接受 PKCS#8 编码的 Ed25519 私钥。相同 `chatgpt_account_id` 重复导入会更新原账号，并删除旧 OAuth Bearer（承载令牌）、refresh token（刷新令牌）、Cookie 和 API Key。运行时会按当前 UTC 时间动态生成 `AgentAssertion（代理身份签名）`；没有 task ID 时自动注册并持久化，只有上游 `401` 明确返回 `invalid_task_id`、`task_not_found` 或 `task_expired` 等 task 失效语义时才重新注册一次。普通签名错误、时钟偏差、`403`、`429`、`5xx` 和网络错误不会触发 task 无限重试。Responses、Chat Completions（聊天补全兼容）、Messages（消息兼容）、compact（上下文压缩）、模型清单、Token 计数、图片、搜索、额度查询、账号检测、渠道健康和 WebSocket（长连接）共用同一签名规则。

#### `POST /api/v1/admin/accounts/import/personal-access-token`

导入 Codex PAT（Personal Access Token，个人访问令牌）。请求字段：

- `access_token`：必填，完整 PAT；只能出现在本次 HTTPS 管理请求体中。
- 其余账号配置字段与 Agent Identity 接口相同；`credential_extras` 同样不能覆盖鉴权模式和令牌字段。

服务端先调用官方 `whoami（身份确认）` 验证令牌并读取最小账号身份，再固定保存 `auth_mode=personalAccessToken`。同一账号或令牌重复导入会更新原账号，替换后立即清理旧 token cache（令牌缓存），并删除 `refresh_token`、`id_token`、客户端密钥和 OAuth 过期时间；PAT 永不进入 OAuth refresh（令牌刷新）流程。模型清单、普通 Responses、compact 和 Hosted Search（托管搜索）使用现有 PAT 权限边界，不会把无权访问的独立端点伪装成成功。

安全规则：`agent_private_key`、PAT、AT、RT、Cookie 和 API Key 属于敏感凭据，数据库新写入使用现有 AES-GCM 字段级加密；账号列表、错误、审计、日志、主站配置快照和备份证据不得出现明文。导入错误分别使用稳定码 `OPENAI_CODEX_AGENT_IDENTITY_IMPORT_INVALID` 和 `OPENAI_CODEX_PAT_IMPORT_INVALID`。

Antigravity OAuth（Antigravity 授权）令牌保护规则：

- `POST /api/v1/admin/antigravity/oauth/refresh-token` 用于校验管理员手工提交的 `refresh_token（刷新令牌）`。如果上游刷新响应没有返回新的 `refresh_token`，后台创建账号页面会继续使用管理员本次提交的原始令牌，避免创建出只能短期使用、无法续期的账号。
- Antigravity 自动刷新路径构建新 credentials（凭证）时，只在上游明确返回非空 `refresh_token` 时才写入新值；为空或全空白时不写入该字段，再由 credentials merge（凭证合并）保留账号原有的手工令牌。
- `PUT /api/v1/admin/accounts/:id` 编辑 Antigravity OAuth 账号时，如果请求体没有提交 `refresh_token`、`access_token` 等敏感凭据，后端会保留现有敏感字段；只有管理员明确提交非空新值时才覆盖。

### 分组一键复制

`POST /api/v1/admin/groups/:id/duplicate` 为管理员创建一个独立、默认停用的分组副本：

- 必须携带合法 `Idempotency-Key（幂等键）`；服务端把它与管理员身份、源分组 ID 绑定。同一管理员用同一个 Key 重试只返回原副本，不会再次创建。
- 请求体为空。展示类别、内置图标、所有计价、图片/视频价格、Web Search（网页搜索）价格、长上下文开关、模型路由、有效模型清单、Messages（消息接口）映射、RPM、fallback（兜底分组）和账号绑定都由服务端读取，浏览器不回传内部配置。
- 副本名称依次使用 `<源名称> (Copy)`、`<源名称> (Copy 2)` 等不冲突名称，最多 100 个 Unicode 字符；副本状态固定为 `inactive（停用）`，管理员确认后才可启用。
- 账号绑定保留原 `priority（优先级）`。源绑定引用软删除/不存在账号、跨平台账号、与 `require_oauth_only（仅允许授权账号）` 冲突，或模型路由引用不在有效绑定中的账号时，返回明确错误且整个事务回滚。
- 分组记录、账号绑定和 `scheduler_outbox（调度事件箱）` 在同一 PostgreSQL 事务提交；成功后发送精确 `groups/duplicate` config-changed（配置变化）通知，使 control-plane（主站控制面）分组镜像和价格缓存主动更新。
- `duplicate_operation_id（复制操作标识）` 只保存在数据库用于响应不确定时恢复结果，不出现在任何 API 响应。迁移为 `169_group_duplicate_operation_id.sql`。
- 操作写入 `admin.groups.duplicate` 审计记录，包含管理员、源分组、结果和请求编号，不保存秘密配置。

成功响应仍使用后台 `AdminGroup（管理员分组）` 对象，例如：

```json
{
  "id": 102,
  "name": "OpenAI 高级组 (Copy)",
  "platform": "openai",
  "status": "inactive",
  "account_count": 12
}
```

稳定错误包括 `IDEMPOTENCY_KEY_REQUIRED（缺少幂等键）`、`IDEMPOTENCY_KEY_INVALID（幂等键非法）`、`GROUP_NOT_FOUND（源分组不存在）`、`GROUP_EXISTS（名称或操作标识冲突且无法恢复）` 和 `GROUP_DUPLICATE_BINDING_INVALID（源账号绑定无效）`；绑定完整性错误返回 `400`，不会留下半成品副本。

OpenAI 分组创建、编辑和查询对象新增 `web_search_price_per_call（网页搜索单次价格，美元/次）`。该字段允许 `null（未设置）`、`0（免费）` 或非负数；创建/编辑成功后会进入 API Key 鉴权快照、scheduler cache（调度缓存）和主动上报主站的分组快照。数据库 migration（迁移）为 `160_group_web_search_price_per_call.sql`，新增 nullable（可为空）的 `groups.web_search_price_per_call DECIMAL(20,8)`，回滚时可在确认没有旧版本读取该字段后删除该列。

### 分组模型兜底

管理员可在 `POST /api/v1/admin/groups` 和 `PUT /api/v1/admin/groups/:id` 传入 `fallback_model（分组兜底模型）`；`GET /api/v1/admin/groups`、`GET /api/v1/admin/groups/:id`、分组复制、API Key（接口密钥）鉴权快照、config push（配置主动推送）与 config snapshot（配置快照）都会返回或携带该字段。该字段只对 OpenAI 分组生效，最长 100 字节，空字符串表示关闭；非 OpenAI 分组提交非空值返回 `INVALID_GROUP_FALLBACK_MODEL`。

`fallback_model` 仅在原请求模型经过分组/账号映射后没有任何可用账号时，作为同分组的一次性调度候选。它不改写客户请求模型、正常响应声明或默认按用户请求模型的计费口径；日志中的真实上游模型继续记录最终实际发送的模型。参数错误、鉴权失败、内容策略拦截、已经开始输出后的上游失败，以及兜底模型本身无可用账号时都不会无限重试。

普通文本分组还支持可选的 `failover_group_id`（跨分组可用性兜底分组）和 `failover_strategy`（跨分组顺序）。策略可取 `original_model_first`（默认：先尝试兜底分组原模型，再回到当前分组兜底模型）或 `current_group_first`（先尝试当前分组兜底模型，再转移到兜底分组原模型）。跨分组最多一次，仅用于账号失败预算耗尽、无可用账号、408、429、5xx 和超时等可恢复上游故障；400、鉴权/余额/并发拒绝、内容拦截、已输出首字节、`previous_response_id`（上次响应编号）、WebSocket、compact（压缩）、图片/视频请求不会跨组。目标分组重新执行自身模型映射、账号调度、授权、账务资格和每用户并发门禁；计费、API Key、入口分组倍率和客户端响应模型声明仍按原请求分组口径执行。用量记录通过 `requested_gateway_group_*` 保存入口分组，通过 `gateway_group_*` 保存最终承接分组。

`POST /api/v1/admin/groups` 与 `PUT /api/v1/admin/groups/:id` 都可读取/写入这两个字段。创建或更新时，`failover_group_id` 为正数才启用；更新传入 `0` 或负数会清空跨分组兜底，省略字段则保留既有配置。目标必须是不同的、启用中的 OpenAI 分组，且不能形成 A→B→…→A 环路；不满足时返回 `400 INVALID_GROUP_FAILOVER_TARGET`。未知顺序返回 `400 INVALID_GROUP_FAILOVER_STRATEGY`。非 OpenAI 分组不会保留跨分组兜底配置。

OpenAI 分组创建、编辑和查询对象还包含以下长上下文字段：

- `allow_openai_long_context_billing（启用长上下文计费）`：分组级开关，默认 `false`。只有分组内至少存在一个已绑定、未删除、状态正常、可调度，并且账号 `extra.openai_long_context_billing_enabled=true（厂商长上下文计费开启）` 的 OpenAI 账号时才允许开启。
- `openai_long_context_billing_account_count（长上下文成本账号数量）`：符合当前分组 OAuth only（仅 OAuth）、privacy（隐私账号）等持久规则，且账号厂商长上下文开关开启的数量。
- `openai_standard_billing_account_count（标准成本账号数量）`：符合相同持久规则，但账号厂商长上下文开关关闭的数量。
- `openai_long_context_billing_status（长上下文计费状态）`：`enabled（已开启独立长上下文计费）`、`standard_only（长上下文同标准价）`、`unavailable（没有匹配账号）` 或 `not_applicable（非 OpenAI 分组）`。分组关闭独立长上下文计费时，只要标准成本账号与厂商长上下文成本账号合计大于 0 就返回 `standard_only`；不能因为普通账号为 0 而误报 `unavailable`。

管理员单分组详情 `GET /api/v1/admin/groups/:id` 还会返回完整 `account_groups（账号绑定关系）`及 `account_bindings_loaded=true（绑定已完整读取）`。空 `account_groups`在该标记为真时才代表该分组确实未绑定任何账号；分组列表接口仍是轻量摘要，不保证带出完整绑定。该约定防止主站编辑器将“未加载”误判成“全部解绑”。当前版本不支持分组 `allow_priority（Priority 优先处理）`字段，调用方不得把未知字段的 HTTP 200 当成开关已经生效。

`GET /api/v1/admin/groups/:id/effective-models`返回该分组当前真实有效模型：只读取已绑定、状态正常、可调度账号及其已保存的模型映射，响应含 `models/effective_models（有效模型）`、`configured_models（配置模型）`、`reported=true（已完成权威读取）`和`version（读取版本）`。该只读接口不扫全量账号，也不会将未知账号能力伪装成可用模型；主站分组编辑器优先使用它。

### 分组自定义定价

- 两个接口均位于管理员鉴权路由组。`GET /api/v1/admin/groups/:id/pricing` 读取分组配置弹窗所需的完整定价合同；分组不存在返回统一的分组不存在错误，非法编号返回 `400`。
- GET 响应字段为 `enabled/source/channel_id/shared/model_pricing/restrict_models/features_config/apply_pricing_to_account_stats/account_stats_pricing_rules`。没有自定义价时返回 `enabled=false`、`source=inherited`、`channel_id=null`和空数组/空对象；独享渠道返回 `source=dedicated`；历史共享渠道返回 `source=shared`、`shared=true`。成本规则只返回当前分组范围内的规则和账号编号，共享渠道中的其他分组及其账号编号不会返回。
- `PUT /api/v1/admin/groups/:id/pricing` 请求字段为 `enabled/model_pricing/restrict_models/features_config/apply_pricing_to_account_stats/account_stats_pricing_rules`，成功后返回与 GET 相同结构并记录 `admin.groups.pricing.update` 审计动作。`model_pricing`及成本规则内价格支持 `billing_mode=token|per_request|image|video`、默认价格和 `intervals（区间/分档价格）`。
- `model_pricing[]` 的 Token（令牌）价格支持可选 `fast_multiplier` / `flex_multiplier`（必须大于 0）；留空分别沿用模型目录 Fast/Priority（快速档）价格与默认 Flex（弹性档）倍率。`intervals[]` 支持 `input_multiplier/output_multiplier/cache_write_multiplier/cache_read_multiplier`，按渠道基础价换算；同一项同时设置绝对价格和倍率时，绝对价格优先。账号成本统计规则不接受这些服务档位倍率，避免重复计算用户已经完成的计费。
- 原生 Anthropic Messages 请求的 `speed: "fast"` 仅在实际发往 Anthropic 原生上游的 Claude Opus 4.8/5 生效，统一记录为 `service_tier=fast` 并参与计费；Bedrock、其他模型和映射后不支持 Fast 的模型不会误加价。
- PUT 会原子替换当前分组的完整定价合同。`enabled=false` 表示关闭自定义定价并恢复继承全局价格；关闭历史共享渠道时只解绑当前分组。修改历史共享渠道时在一个数据库事务内拆出 `group-pricing-{group_id}` 独享渠道，其他分组继续使用原渠道，任一步失败都会回滚。账号成本规则只能选择当前分组账号；普通分组每条 `platform（平台）`必须等于分组平台；`composite（聚合）`分组每条必须显式使用真实目标平台，防止同名模型跨平台串价。渠道层的旧 `model_mapping（模型映射）`和 `billing_model_source（计费模型来源）`不会继承到新合同，统一由分组原生字段控制，避免隐藏的双重配置。
- 分组创建接口 `POST /api/v1/admin/groups` 新增可选 `status=active|inactive`。自定义定价创建流程先创建 `inactive（停用）`草稿，定价原子保存成功后再启用，避免价格保存失败时产生可被客户使用的半成品分组。
- 保存成功会失效渠道定价内存快照和当前分组 Key（接口密钥）鉴权缓存，并通过既有 `mixed/group_pricing_update（混合配置/分组定价更新）`配置变更通知推动主站刷新匿名价格合同；用户请求热路径不增加 SQL（数据库语句）。
- 后台侧边栏不再提供独立渠道定价菜单；旧 `/admin/channels` 与 `/admin/channels/pricing` 前端网址跳转 `/admin/groups`。`/admin/model-prices（全局模型价格）`与`/admin/channels/monitor（渠道监控）`继续保留，迁移不删除历史渠道数据或运维能力。

### composite（聚合分组）路由管理

管理员分组创建、修改接口的 `platform（平台）`支持 `composite（聚合）`。聚合分组可以绑定 Anthropic、OpenAI、Gemini、Antigravity、Grok、Kimi、智谱 GLM 和 DeepSeek 八类具体平台账号；普通分组仍只允许绑定自身平台账号。

- `GET /api/v1/admin/groups/:id/composite-routes`：读取全部路由，包含停用项。
- `POST /api/v1/admin/groups/:id/composite-routes`：创建路由。
- `PUT /api/v1/admin/groups/:id/composite-routes/:route_id`：完整修改一条路由。
- `DELETE /api/v1/admin/groups/:id/composite-routes/:route_id`：软删除路由。
- `PUT /api/v1/admin/groups/:id/composite-routes/batch-status`：批量启用或停用，body（请求体）为 `{"route_ids":[1,2],"enabled":true}`；所有编号先校验，再在同一数据库事务中更新。
- `POST /api/v1/admin/groups/:id/composite-routes/preview`：只预览、不保存。只查现有规则时 body 为 `{"model":"用户模型名","endpoint":"responses"}`；保存前可额外传 `candidate（待保存规则）`和`editing_route_id（正在修改的规则编号）`，服务端会把候选规则临时合入同一优先级算法，返回真正会胜出的规则但不写数据库。响应额外返回 `price_model/price_source/price_available（真实计价模型/价格来源/价格是否可用）`；价格查找按本次 `target_platform（目标平台）`严格隔离，同名的其他平台价格不能被采用，`price_available=false` 表示启用后请求会失败关闭。

创建和修改路由的 body（请求体）字段为：`public_model（用户模型）`、`match_type=exact|prefix（精确或前缀匹配）`、`target_platform（真实平台）`、`upstream_model（上游模型）`、`endpoint（接口范围）`、`priority（优先级，数字越小越优先）`、`enabled（启用状态）`和`notes（备注）`。接口范围可选 `any/messages/count_tokens/responses/chat_completions/embeddings/images/gemini`。

preview（预览）按“精确规则 > 接口专用 > 更长前缀 > 更小 priority > 更小规则 ID”返回唯一决定；没有显式规则时才使用明确的官方模型前缀。未知模型返回 `matched=false`，不会猜平台。规则保存后会原子替换内存只读快照，正常请求不查询数据库。

`gateway_lite.composite_groups_enabled（聚合分组总开关）`默认关闭。关闭时不能创建或启用聚合分组、不能启用路由，已有聚合 Key 请求返回 `503 COMPOSITE_GROUPS_DISABLED`；普通分组不会进入聚合解析。开启前必须确认目标平台价格、账号绑定和主站最终策略镜像完整。复制聚合分组会在同一数据库事务内复制账号绑定优先级和全部未删除路由，副本保持停用；删除分组会在同一事务内软删除全部聚合路由，并在提交后立即清除运行时只读快照。

异步 Grok 视频任务会把公开模型、目标 Grok 平台和 `route_source（路由来源）`写入非敏感任务快照，任务完成后的延迟结算会恢复同一聚合路由上下文；因此创建请求与最终用量修正使用同一公开模型和路由审计字段，不会因后台 worker（工作协程）丢失请求 context（上下文）。

创建分组时，如果同时使用 `copy_accounts_from_group_ids（从来源分组复制账号）`，后端会对实际待复制账号做校验；编辑分组并替换账号绑定时也使用替换后的真实账号集合校验。前端禁用只能提供即时提示，后端会重复强校验，直接绕过页面提交无效配置会返回：

- `OPENAI_LONG_CONTEXT_BILLING_PLATFORM_INVALID`：非 OpenAI 分组尝试开启。
- `OPENAI_LONG_CONTEXT_BILLING_ACCOUNT_UNAVAILABLE`：没有符合条件的长上下文成本账号。

最后一个符合账号被解绑、删除、关闭调度、停用、过期自动暂停或关闭账号厂商长上下文开关时，gateway-lite（轻量网关）会原子关闭分组开关，失效 scheduler cache（调度缓存），并通过 config push（配置主动推送）把新状态上报主站。临时 `429/529`、过载和短期冷却只影响当前请求候选，不会改写持久化分组开关。数据库 migration（迁移）为 `164_add_group_openai_long_context_billing.sql`。

OpenAI 文本请求进入账号调度前，会从当前模型结构化价格读取 `long_context_input_token_threshold（长上下文输入阈值）`，并对 Responses、Chat Completions、Messages 和 WebSocket `response.create` 的输入 Token 做同一套本地估算：

- 估算值小于或等于阈值时不缩小账号池，账号厂商长上下文开关不影响普通请求调度。
- 估算值超过阈值且分组开关开启时，只允许调度 `openai_long_context_billing_enabled=true` 的账号；长上下文标准价格字段不完整时在请求上游前拒绝。
- 估算值超过阈值且分组开关关闭时，不按账号厂商长上下文成本开关过滤候选；标准成本账号和已开启厂商长上下文成本的账号都可照常调度。此时分组开关只表示“不向用户应用长上下文附加价格”，不能把超过阈值的请求挡住。
- 分组开关开启时，粘性会话绑定到成本类型不匹配的账号会立即失效，再按当前请求重新调度；WebSocket 后续轮次跨过阈值且当前连接账号未开启厂商长上下文计费时，会在转发前关闭该轮并提示客户端重新连接。分组关闭时不做这类成本账号匹配拦截。
- 实际计费同时要求“账号厂商长上下文开关开启”和“分组允许长上下文计费”；即使估算与上游最终 Token 存在偏差，分组关闭时也不会向客户误收长上下文附加费用。

分组图片固定价格说明：`allow_image_generation（允许图片生成）` 只控制媒体权限，`image_rate_independent（图片独立倍率）` 只决定使用图片倍率还是普通倍率；真正把图片从图片 Token 改为按张结算，需要给当前规格配置 `image_price_1k/image_price_2k/image_price_4k（各规格单张价格）`。OpenAI 图片请求命中已配置规格时，分组固定价格优先于模型/渠道图片 Token 价格，费用为“规格单张价格 × 返回图片数量 × 图片有效倍率”，用量日志写入 `billing_mode=image（按图计费）`；没有配置的规格继续使用原模型或渠道价格，不猜测覆盖价。主站需要在一个模型行里给出唯一计费单位，因此只有三档价格都已配置时才整体展示为“按图”；三档不完整时保留模型目录单位，避免把未配置规格误标成按图。

新增稳定错误码：

- `OPENAI_LONG_CONTEXT_INPUT_ESTIMATE_FAILED`：无法可靠估算输入 Token，为避免错误路由和计费而拒绝请求。
- `OPENAI_LONG_CONTEXT_PRICING_UNAVAILABLE`：模型长上下文价格矩阵不完整。
- `OPENAI_LONG_CONTEXT_BILLING_ACCOUNT_UNAVAILABLE`：分组允许长上下文计费，但当前没有可用的长上下文成本账号。

### Grok 渠道监控

渠道监控和监控请求模板接口的 `provider（服务商类型）` 支持 `openai`、`anthropic`、`gemini` 和 `grok`：

- `/api/v1/admin/channel-monitors/*`：创建、编辑、删除、立即检测和查询历史。
- `/api/v1/admin/channel-monitor-templates/*`：维护可复用的请求头和请求体模板。

`POST /api/v1/admin/channel-monitors/:id/duplicate` 创建独立、默认停用的渠道监控副本：

- 请求体为空，必须携带合法 `Idempotency-Key（幂等键）`；幂等身份绑定管理员、源监控和 Key，相同操作的重试只返回同一个副本。
- 服务端复制 provider（服务商）、`api_mode（接口协议）`、endpoint（上游地址）、主/附加模型、目标分组、检测间隔、随机抖动、模板关联、Header 快照和 Body 快照。
- `api_key_encrypted（加密密钥）` 只在服务端数据库加密域内复制，不解密后转交浏览器；响应继续只有 `api_key_masked（脱敏状态）`，不返回明文、密文或 `duplicate_operation_id（复制操作标识）`。
- 副本强制 `enabled=false（停用）`，`last_checked_at（最后检测时间）` 为空；历史明细、错误记录、日聚合和现有调度任务均不复制。管理员确认真实模型和目标分组后才能启用。
- 数据库 migration（迁移）`170_channel_monitor_duplicate_operation_id.sql` 增加 SHA-256 operation identity（操作身份）部分唯一索引，保证多节点并发和幂等协调器降级时仍只创建一个副本。
- 操作写入稳定的 `admin.channel_monitors.duplicate` 审计动作；审计摘要不保存 API Key 或请求模板秘密。

`provider=grok` 使用 xAI OpenAI-compatible Chat Completions（xAI 的 OpenAI 兼容聊天补全）协议，固定请求 `POST /v1/chat/completions`，使用 `Authorization: Bearer <xAI API Key>` 鉴权。Grok 不允许 `api_mode=responses`；直接调用后端提交该组合会返回 `CHANNEL_MONITOR_INVALID_API_MODE（渠道监控接口模式无效）`，不会向上游发送请求。

创建 Grok 监控时，`primary_model（主检测模型）` 可以省略或传空字符串，后端会保存为 `grok-4.5`。从其他 provider（服务商）切换为 Grok 且未同时指定模型时，也会重置为 `grok-4.5`，并把接口模式重置为 `chat_completions（聊天补全）`。其他服务商仍必须显式提交非空 `primary_model`。

后台新建表单选择 Grok 后会自动填入默认 endpoint（上游地址）`https://api.x.ai` 和模型 `grok-4.5`；管理员仍可改为真实可访问的第三方 xAI-compatible（xAI 兼容）地址。编辑已有 Grok 监控时保留数据库中的地址和模型，不会被页面默认值覆盖。

检测结果继续使用现有状态：正确回答随机算术 challenge（挑战题）的 `2xx` 请求为 `operational（正常）`，超过慢响应阈值为 `degraded（缓慢）`，回答错误或空内容为 `failed（内容失败）`，网络超时、`429` 和 `5xx` 为 `error（请求错误）`。错误正文和 URL 中出现的 `xai-*` 密钥会在写入历史前替换为脱敏标记。

数据库 migration（迁移）`161_channel_monitor_grok_provider.sql` 会幂等更新 `channel_monitors` 与 `channel_monitor_request_templates` 的 provider check constraint（服务商检查约束），允许 `grok`，重复执行不会反复重建已经包含 Grok 的约束。

### OpenAI OAuth 额度与额度重置券

以下接口仅允许轻量网关管理员调用：

- `GET /api/v1/admin/openai/accounts/:id/quota`：读取 OpenAI OAuth（OpenAI 授权账号）的 5 小时、7 天等额度窗口和可用额度重置券。
- `POST /api/v1/admin/openai/accounts/:id/reset-quota`：消耗一张可用额度重置券，重置上游允许重置的额度窗口。
  - 重置券成功消耗后，账号状态恢复使用脱离浏览器连接且最多 8 秒的后台上下文；后续恢复、快照刷新或账号刷新失败时仍返回成功主体，并通过 `warning_code` 告知部分成功，避免管理员重复消耗重置券。
- `POST /api/v1/admin/openai/accounts/:id/quota/refresh`：查询上游额度并把安全的重置券快照写入账号 `extra.codex_reset_credit_snapshot`。
  - 正数额度必须携带尚未过期且可解析的 RFC3339 到期明细；过期项会被剔除，无法安全老化的快照不会覆盖旧缓存。响应中的 `cache_persisted` 表示快照是否成功保存。

额度查询先调用上游 `/backend-api/wham/usage` 获取基础用量，再尽力调用 `/backend-api/wham/rate-limit-reset-credits` 获取额度券详情。详情接口的优先级如下：

1. 详情明确返回 `available_count` 或 `availableCount` 时，以该值为准，包括明确的 `0`。
2. 详情没有明确计数但返回额度券列表时，以状态可用、类型兼容的记录数量为准；没有到期时间的可用记录仍计入数量。
3. 详情请求失败、返回空对象、`null`、非法结构或没有任何有效计数/列表字段时，保留基础用量接口中的原值，不让辅助接口失败影响整个额度页面。

详情兼容顶层数组以及 `credits`、`rate_limit_reset_credits`、`items`、`data` 容器，并兼容 snake_case（下划线命名）与 camelCase（驼峰命名）。对后台只暴露额度券 `expires_at（到期时间）`，不返回上游额度券 ID、令牌或其他敏感元数据。

### Grok OAuth（xAI 授权）管理接口

以下接口仅位于管理员鉴权路由，不对普通 API Key（接口密钥）开放：

- `POST /api/v1/admin/grok/oauth/auth-url`：生成带 PKCE（授权码防劫持）的 xAI 授权地址，返回一次性 `session_id（授权会话编号）` 和 `state（状态校验值）`。
- `POST /api/v1/admin/grok/oauth/exchange-code`：使用 `session_id`、授权码和 `state` 兑换令牌；授权会话兑换后立即失效，不能重复使用。
- `POST /api/v1/admin/grok/oauth/refresh-token`：校验并刷新管理员提交的 Grok `refresh_token（刷新令牌）`，可选使用指定代理。
- `POST /api/v1/admin/grok/oauth/reconcile`：分页扫描 Grok OAuth（授权）账号的凭据与身份一致性。请求默认执行 `dry_run=true（只扫描）`；只有先取得该页响应的 `preview_token（预览指纹）`，再同时显式提交 `apply=true`、`dry_run=false`、原 `preview_token` 和本次选中的 `account_ids（账号编号，1-500 个）` 才会修复。支持 `after_id（游标）`、`limit（每页数量，最大 500）` 和 `refresh_window_seconds（临近过期窗口，最大 24 小时）`。账号状态、令牌版本、代理、身份归属或计划动作在预览后发生任何变化，执行会返回 `409` 并要求重新扫描；因此不能绕过预览，也不会把管理员刚重新授权的账号按旧结果封禁。扫描会识别缺少访问/刷新令牌、非法或临近过期时间、缺少稳定身份、保存邮箱与身份令牌不一致、刷新后身份变化及跨分页重复身份；重复身份保留 ID 最小的账号，其余账号停止调度但不删除，历史用量、分组、代理、倍率和凭据原样保留供人工复核。写入前使用 `updated_at + _token_version + proxy_id` 执行 CAS（比较并交换），并在同一数据库语句中写入 scheduler outbox（调度变更队列）。响应只返回账号 ID、原因、计划动作、结果、预览指纹和必要的保留账号 ID，不返回邮箱、Token（令牌）、凭据或原始上游错误；管理操作审计只保存模式和扫描数量。
- `POST /api/v1/admin/grok/oauth/create-from-oauth`：兑换授权码后创建 Grok OAuth 账号，可同时指定代理、并发数、优先级和分组。OAuth 默认保存 Build CLI 地址 `https://cli-chat-proxy.grok.com/v1`；请求明确提交的官方、区域或合法第三方 `base_url`、`header_overrides（请求头覆写）` 和 `model_mapping（模型映射）` 会保持原值。创建成功后进入并发数为 3 的异步额度探测队列，不阻塞创建响应；探测失败不会回滚已创建账号。
- `POST /api/v1/admin/grok/sso-to-oauth`：把一个或多个 Grok Web SSO Cookie（网页登录凭据）转换为 Build OAuth（授权）账号。请求可使用 `sso_tokens` 数组或兼容单项 `sso_token`，每项也可直接粘贴 `sso=<value>` Cookie 片段；服务端去重后最多使用 3 个 worker（工作线程）并发执行 xAI 设备授权转换，只跟随可信 `x.ai` 重定向。响应始终分别返回 `created（已创建）` 和 `failed（失败）` 数组，单项失败不会回滚已成功账号。SSO Cookie 只用于本次转换，不写入账号、响应或日志；创建结果保留请求明确提交的 Base URL、Header 覆写、模型映射、代理、分组和调度参数。
- `POST /api/v1/admin/grok/accounts/:id/refresh`：刷新已有 Grok OAuth 账号令牌，并通过账号更新流程同步清理令牌缓存和调度快照。
- `GET /api/v1/admin/grok/accounts/:id/quota`：先并发读取 xAI CLI proxy 的 `/billing?format=credits` 周账单和 `/billing` 月账单。账单包含权威使用比例或付费计划时直接返回，不消耗模型额度；免费账号账单缺少使用比例时，才追加一次 `grok-4.5` 最小 Responses（响应接口）真实请求，读取请求数、Token 和重置时间响应头。响应 `source（来源）` 为 `billing_probe（账单探测）`、`active_probe（主动模型探测）` 或 `hybrid_probe（混合探测）`，并可返回 `billing（账单摘要）`、`snapshot（限额头快照）`、`local_usage_24h（滚动 24 小时本地用量）`、`local_usage_7d（账单周窗口本地用量）`、`local_usage_monthly（月账单窗口本地用量）`、`persisted（是否保存成功）` 和 `probe_error（补充主动探测错误）`。`billing.weekly_status_code（周探测状态）` 与 `billing.monthly_status_code（月探测状态）` 分别保存两次 HTTP 结果，不能用成功窗口覆盖另一窗口的 403。
- `POST /api/v1/admin/grok/accounts/:id/reset-quota`：返回 xAI 当前不支持主动重置订阅额度的明确错误，不伪造重置成功。
- `GET /api/v1/admin/grok/runtime-sanity`：返回 Grok 运行时公开配置摘要，用于诊断 OAuth 地址、模型和媒体接口路径；不得返回 Client Secret（客户端密钥）或账号令牌。

`GET /api/v1/admin/accounts/:id/usage` 对 Grok OAuth 账号会自动刷新过期的 billing（账单）快照，但只调用不消耗模型额度的周/月账单接口；一分钟内的并发或重复失败重试会合并/限频。Free（免费）账号使用真实网关请求日志计算滚动 24 小时用量，付费账号按上游返回的有效周/月窗口计算本地请求数、Token 和费用。周、月任一账单接口临时失败时，成功窗口会更新，失败窗口保留上一次有效值并标记 `partial（部分数据）`。轻量网关另外保存 `grok_media_eligibility_snapshot（Grok 媒体资格快照）`：任一周/月端点明确 403 时记录不合格；429、5xx、超时或解析失败只标记 `stale（证据陈旧）`，保留最后一次确定资格，首次失败则保持 unknown（未知）。资格快照及必要账单状态会进入 scheduler cache（调度缓存），但不会修改账号文本健康、冷却、客户价格、余额、套餐或账本；billing（账单）接口返回 429 也不会暂停模型账号。

令牌只允许通过管理员授权流程写入受管理员鉴权保护的账号凭据字段。服务日志、运行时诊断和上游错误摘要必须经过脱敏，不记录完整 `access_token（访问令牌）`、`refresh_token（刷新令牌）` 或 `id_token（身份令牌）`。数据库敏感凭据新写入使用 `enc:tokenav:v1:` 字段级密文；迁移期 `enc:tokeav:v1:` 和历史 `enc:aiceo:v1:` 密文仍可解密，并会在账号凭据迁移任务中重加密为新前缀。

Grok 账号接入通用账号管理接口后的行为：

- `GET /api/v1/admin/accounts/:id/models`：没有显式模型映射时返回 xAI 内置模型目录；存在 `model_mapping（模型映射）` 时只返回映射左侧的客户请求模型名。
- `POST /api/v1/admin/accounts/:id/test`：Grok OAuth（授权账号）和 API Key（接口密钥账号）都使用各自真实凭据请求 xAI Responses API（响应接口），默认模型为 `grok-4.5`，并保存真实响应中的额度头；测试失败不会伪造健康状态。
- `POST /api/v1/admin/accounts/:id/models/sync-upstream`：Grok OAuth 与 API Key 账号都使用实际请求凭据读取上游 `/v1/models`。OpenAI API Key 使用账号密钥读取配置的 `/v1/models`；普通 OpenAI OAuth、Codex PAT（个人访问令牌）和 Agent Identity 使用各自真实鉴权读取官方 Codex 模型清单，并解析清单中的 `slug（模型标识）`。普通 OAuth 复用可刷新的令牌 provider（提供器），PAT 明确不进入 refresh（令牌刷新），Agent Identity 每次动态签名且 task（任务标识）失效时单次操作最多恢复一次。保存同步结果后，只补齐上游真实存在的模型和对应官方别名；返回结果与错误不会回显 API Key、AT、PAT、Agent 私钥或 `AgentAssertion（代理签名）`。
- `PUT /api/v1/admin/accounts/:id`：修改 Grok OAuth 凭据后立即删除旧令牌缓存，并主动刷新调度账号快照，使下一次请求使用新配置。
- `POST /api/v1/admin/accounts`、`PUT /api/v1/admin/accounts/:id`、`POST /api/v1/admin/accounts/bulk-update` 和账号 JSON 导入会统一校验并持久化 Grok Base URL、Header 覆写和模型映射；重新打开创建/编辑页面不得把官方、区域或第三方地址静默清空或替换。
- 上述创建、编辑、批量编辑、OAuth/SSO 创建和 JSON 导入统一接受 `extra.grok_media_eligible（Grok 媒体资格覆写）`：`true` 强制允许、`false` 强制隔离、`null` 或缺失使用自动探测；其他类型返回 `GROK_MEDIA_ELIGIBILITY_INVALID`，非 Grok 账号返回 `GROK_MEDIA_ELIGIBILITY_PLATFORM_INVALID`。客户端不能写入服务端管理的账单或资格快照。
- Grok OAuth（授权账号）可在 `extra.grok_client_tool_cache_enabled（客户端工具缓存）` 保存管理员开关。明确 `false` 是永久关闭边界，请求 Header（请求头）不能重新开启；请求级关闭始终优先。字段缺失时仅对账单证据已确认的 Free（免费档）OAuth 账号默认开启，付费、API Key（接口密钥）和未知档位保持关闭。该设置只影响工具与缓存协议转换，不改变分组权限、账号调度、模型价格或计费规则。
- Grok OAuth 账号并发数未填写或小于等于 0 时归一为 1；管理员仍可显式设置更高并发。
- 开启分组 `require_oauth_only（仅允许授权账号）` 后，Grok API Key（接口密钥）账号不会进入该分组调度集合。

`GET /api/v1/admin/accounts` 和单账号响应对 Grok 账号增加 `grok_media_eligibility（Grok 媒体资格）`：`state` 为 `eligible/ineligible/unknown（可用/不可用/未知）`，`eligible` 在未知时为 null，`effective_eligible` 表示当前调度兼容行为，`source` 区分 `override（手工覆写）`、`billing_probe（账单探测）`、`media_request（真实媒体请求 403）` 和 `non_oauth_default（非 OAuth 默认）`；同时返回 `reason`、`override`、`observed_at（最后确定证据时间）`、`probed_at（最近尝试时间）`、`stale`、周/月状态和失败窗口。管理员覆写与自动证据始终分开显示。

OAuth token refresh pool（授权令牌刷新池）配置：后台刷新使用稳定 ID cursor（游标）分页，不一次性加载全部账号，并只读取 `schedulable=true（允许调度）` 的账号；管理员明确关闭调度的账号不会继续消耗 OAuth 刷新请求。每个 provider（服务商）独立限制并发与 QPS（每秒请求数），一个服务商连续临时失败达到阈值时只停止该服务商本轮刷新，不影响其他服务商。单周期超时或进程取消不会把尚未实际失败的账号写入冷却。默认值为 `token_refresh.candidate_page_size=200`、`provider_concurrency=4`、`provider_qps=2`、`provider_failure_threshold=3`、`cycle_timeout_seconds=240`、`attempt_timeout_seconds=30（单账号单次刷新超时）`；配置上限分别为 1000、32、100、100、3600 秒和 300 秒。单账号超时只结束该次真实 OAuth 请求，不会占满整轮 4 分钟周期。

### Grok 4.5 文本请求接口

当 API Key（接口密钥）固定绑定的分组平台为 `grok` 时，下列 OpenAI-compatible（OpenAI 兼容）入口由原生 Grok 请求链路处理。分组平台为 `openai` 时只允许 OpenAI 模型，不会因为账号映射或上游模型列表中出现 Grok 名称而跨平台调度：

- `POST /v1/responses`、`POST /responses`、`POST /backend-api/codex/responses`：普通或流式 Responses（响应）请求，默认别名 `grok`、`grok-latest`、`grok-4.5-latest` 映射到 `grok-4.5`。
- `POST /v1/chat/completions`、`POST /chat/completions`：普通或流式 Chat Completions（聊天补全）请求。Grok API Key（接口密钥）、Grok OAuth 自定义 Base URL（上游地址），以及带工具、停止词等无法无损转换的请求继续直接转发到 Chat Completions；只有使用官方 CLI proxy（命令行代理）的 Grok OAuth 纯文本或 `image_url（图片链接）` 安全请求会桥接到 xAI Responses（响应接口），其中图片内容转换为 `input_image（图片输入）`，避免视觉输入被原生 Chat 端点静默丢弃。桥接采用严格字段白名单，不能安全表达的请求自动回退原生 Chat。
- `POST /v1/messages`：Anthropic Messages（消息）兼容入口，网关转换为 xAI Responses 请求，再转换回 Anthropic 响应。
- `GET /v1/responses`、`GET /responses`、`GET /backend-api/codex/responses`：客户端 Responses WebSocket（响应长连接）入口；Grok OAuth 和 Grok API Key 账号均使用 HTTP/SSE，由网关逐轮桥接。OpenAI 分组继续遵守 OpenAI 账号自己的原生 WebSocket 能力配置。

客户端 Responses WebSocket（响应长连接）保护规则：

- API Key 分布式 ingress lease（入站连接租约）在 WebSocket upgrade（协议升级）之前获取。连接数达到上限时直接返回 HTTP `429` 和 `Retry-After: 5`；Redis（缓存服务）租约后端不可用时返回 HTTP `503`，不会先升级再发送模糊的 WebSocket 关闭帧。主站快照 Key 没有本地数据库 ID 时，会从非秘密 `KeyID` 生成稳定的负数租约命名空间，本地 Key 继续使用正数数据库 ID；原始 API Key 不参与哈希且不会写入 Redis。协议升级失败和首包早退路径都会释放已取得的租约。
- `gateway.openai_ws.client_first_message_timeout_seconds` 限制客户端 WebSocket（长连接）升级成功后读取并解压首个 `response.create` 消息的秒数，默认 `30`。首帧接受文本或可识别的 JSON object（结构化对象）二进制帧，`type` 必须为 `response.create` 且必须携带模型；超时或非法结构使用 close code `1008`，不透明二进制帧在 HTTP bridge（长连接转普通请求）模式使用 `1003`，超过 `client_read_limit_bytes` 的帧使用 `1009`。关闭前会先发送明确 close frame（关闭帧），再关闭 transport（传输连接）并等待唯一读取任务退出。
- `gateway.openai_ws.ingress_inter_turn_idle_timeout_seconds` 限制两个已完成 turn（请求轮次）之间允许空闲的秒数，默认 `300`；设置为 `0` 可关闭空闲保护。等待下一轮期间，gateway-lite 会按不超过 `45` 秒的间隔向客户端发送协议级 ping（保活帧），避免 CDN / reverse proxy（反向代理）先于业务空闲阈值断开；ping 不会重置业务空闲计时。超时会向客户端发送 WebSocket close code `1000` 和 reason `websocket idle timeout`，再释放上游连接、请求槽位和 API Key 分布式租约，不会把上游账号误记为失败；客户端不应收到表示异常断开的 `1006`。
- 原生 WSv2（第二版响应长连接）的后续轮次接受合法 JSON 文本或 JSON 二进制 `response.create`；通用 passthrough（透传）模式额外只允许已知的 `response.cancel`、`session.update`、音频缓冲和 conversation item（会话项）事件，无法解析为 JSON 的音频等二进制按原字节透传。**资金逐轮准入、付费委托或纯 AI 配置启用的严格模式优先**，按[逐轮模型权限与严格帧合同](#企业范围websocket长连接逐轮模型权限)执行：原生严格透传只接受完整文本 `response.create` 和合法文本 `response.cancel`，不允许会话/音频事件或二进制透传。缺失 `type`、未知事件和畸形 JSON 会按客户端协议错误关闭，不能污染上游账号健康 EWMA（指数移动平均错误率）、触发账号冷却或伪装成账号耗尽。
- 上游 WebSocket JSON 事件必须是带非空 `type` 的 object（对象）文本或二进制帧；不透明二进制媒体在 passthrough 模式原样转发。畸形 JSON、缺失类型、同一 response（响应）重复终止或终止后继续发送会丢弃当前上游连接；本轮尚未向客户端输出时允许在新连接安全重试一次，已有业务输出后禁止重试或换号，避免把两个上游响应拼接到同一轮。
- Responses WebSocket 的 HTTP bridge（长连接转普通请求）会在第一轮真实语义输出前暂存 `response.created/in_progress` 等前导帧，合计上限 8 MiB；随后收到 capacity overloaded（容量过载）的 `error/response.failed` 时可丢弃整次尝试并安全换号。已经输出文本、工具参数或媒体后绝不换号，而只把发给 Codex 客户端副本中的 `server_is_overloaded/slow_down` 改为可重试的 `server_error`；账号健康、调度判断和运维日志仍使用未修改的上游原文。
- `gateway.openai_ws.max_ingress_connections_per_api_key` 限制同一 API Key（接口密钥）在所有网关实例上同时存活的客户端 WebSocket 连接数，默认 `64`；设置为 `0` 可关闭该上限。
- 分布式连接上限使用 Redis（缓存服务）短租约实现。上限开启时，首次获取租约失败必须 fail-closed（失败关闭）；续租成员明确丢失时立即断开，Redis 临时错误连续达到一个完整租约周期后也会断开，不能在无法确认租约所有权时无限保留连接。
- API Key 连接租约与每轮用户/账号并发槽位相互独立：两轮请求之间会释放 turn（请求轮次）槽位，但整条客户端连接仍占用一个 API Key 连接租约。

OpenAI Responses HTTP/SSE（服务端事件流）首输出保护规则：

- `gateway.openai_first_output_timeout_seconds` 限制 OpenAI Responses 流式请求在收到上游响应头后，等待首个语义输出的秒数，默认 `0` 表示关闭。命中超时且尚未向客户端写出语义内容时，网关返回 `UpstreamFailoverError（上游故障切换错误）` 给调度层继续换账号，不会先向客户端写入错误 SSE。
- `gateway.openai_first_output_high_reasoning_timeout_seconds` 是 `reasoning.effort=high/xhigh/max` 的专用首输出超时，默认 `0` 表示沿用普通配置。该值用于给高推理强度更长等待窗口，避免正常长推理被普通超时误切断。
- 首输出保护开启时，响应头和完整 SSE 事件先留在当前 attempt（尝试）的暂存区；空 reasoning metadata（思考元数据）、协议 preamble（前导事件）和注释心跳不会被当成语义输出，也不会泄露失败账号的 `x-request-id` 或 `x-codex-turn-state`。只有文本、工具参数、媒体或非空加密思考等真实输出完成一个 SSE 事件边界后才整体提交。
- 暂存区前 64 KiB 使用内存，Unix（类 Unix 系统）随后转入已 unlink（取消文件名）的临时文件，Windows 保持内存模式；单次尝试总上限 8 MiB。首输出前 Scanner（扫描器）也使用相同有界限制和单事件反压，超限会丢弃本次尝试并进入安全换号，避免异常上游放大内存占用。
- `gateway.openai_proxy_stream_circuit.disabled=false` 默认启用 proxy stream circuit（代理断流熔断）；同一代理 3 秒内的并发断流合并为一次故障。健康代理始终优先；只有所有可用账号都因代理隔离被隐藏时，调度才对该次请求 fail-open（故障放行）并重选一次，分组、模型、账号状态、并发和计费规则仍全部保留。设为 `true` 可整体关闭该熔断，不清除其他账号健康与调度限制。

Grok 4.5 prompt caching（提示词缓存）规则：

- Responses、Messages 和 WebSocket 桥接请求使用请求体 `prompt_cache_key`，并同步设置 xAI 会话请求头；Chat Completions 使用 xAI 官方的 `x-grok-conv-id` 请求头。
- 显式会话种子依次读取 `session_id`、`conversation_id`、`x-grok-conv-id`、请求体 `prompt_cache_key` 和协议桥显式传入值。`x-grok-conv-id` 只在当前 API Key（接口密钥）绑定 Grok 分组时参与粘性会话，不能污染 OpenAI 分组。
- 没有显式会话种子时，网关从模型、工具定义、system/developer 提示、instructions（指令）和首个用户输入生成可复用前缀；追加后续对话不会改变身份。
- 最终身份使用版本前缀 `grok-prompt-cache:v1`、当前 API Key 编号、映射后的上游模型和会话种子生成不可逆 UUID。相同 Key、模型和对话保持稳定；不同 Key 或不同映射模型不会共享上游缓存身份。
- 缺少有效 API Key 上下文、无法生成稳定种子或调用 `/v1/responses/compact` 时不生成缓存身份，并删除客户原始 `prompt_cache_key`，禁止内部探测或不同租户误共享缓存。
- 对没有显式工具意图的 Grok OAuth（授权）请求，网关加入 `web_search`、`x_search` 且设置 `tool_choice=none`，只选择 xAI 可缓存的免费路由，不执行搜索；客户已经传入 `tools` 或 `tool_choice` 时保持原样。
- Grok OAuth（授权）免费账号通过 `/v1/messages` 桥接、原生 `/v1/responses` 或 Responses WebSocket HTTP bridge（长连接 HTTP 桥接）发送 function tools（函数工具）时，纯客户端函数（例如 `read_file`、`view_image`）不会再触发原生搜索工具注入。只有请求已经明确携带 `web_search` 或 `x_search` 搜索意图，且 `tool_choice` 为空或 `auto` 时，网关才在隔离缓存身份存在时补齐另一种搜索工具。客户把 `web_search`/`x_search` 声明成 function tool 时会转换为对应原生工具并去重，其他函数的名称、参数结构和顺序保持不变。该逻辑只对已知 Free（免费档）OAuth 账号生效；账单或订阅信号明确为付费、状态未知或 API Key（接口密钥）账号时不会启用。
- Chat Completions 请求体中的兼容字段 `prompt_cache_key` 会转换为 `x-grok-conv-id` 后删除，避免 xAI Chat Completions 因未知请求体字段拒绝请求。
- Grok OAuth 与 API Key 账号走 `/v1/messages` 时都会把隔离后的缓存键注入上游 Responses 请求，不依赖 ChatGPT Codex 专属转换。

调度与错误规则：

- 分组平台严格隔离：`platform=grok` 分组只查询 Grok 账号，`platform=openai` 分组只查询 OpenAI 账号。OpenAI 账号上的历史 Grok 映射不会进入模型列表、账号调度或后台连接测试；同一上游的 Grok 能力必须通过独立 Grok API Key 账号接入。
- Grok OAuth（授权）账号首次收到上游 401 时，网关会删除旧令牌缓存，在统一刷新锁内强制刷新，并对同一账号重试一次；第二次仍失败才进入其他账号失败切换，禁止无限重试。
- `grok-imagine*` 图片生成模型若误发到 `/v1/responses`，网关会在请求上游前返回明确错误并提示改用 `/v1/images/generations`；视频模型仍由视频接口自己的路由处理。Grok Responses 会删除 Codex/Responses Lite 私有的 `input[].type=additional_tools（附加工具载体）`，但保留受支持的顶层工具，避免 xAI 在推理前因私有输入类型返回反序列化错误。
- xAI 返回非法参数、内容策略拒绝、模型或任务不存在、请求状态冲突等不适合切换账号的请求级 `4xx` 时，Responses（响应接口）保留上游 HTTP 状态码和 JSON 错误体，不改写为 `502`。鉴权失败、限流、可重试上游故障仍按账号刷新、冷却和失败切换规则处理。
- Grok OAuth（授权账号）上游返回 `429` 时，`team_id + mapped model（团队编号 + 映射后模型）` 冷却会写入现有 Redis（缓存服务）命名空间，多个轻量网关副本立即共享；一次调度只批量读取一次共享冷却。Redis 不可用或超时时安全回退当前进程的本地冷却，不放行本节点刚命中的限流，也不因缓存故障阻断全部请求。冷却键只保存不可逆团队指纹、规范模型和到期时间，不保存令牌或完整团队编号。
- `POST /v1/messages/count_tokens` 和 `/messages/count_tokens` 对 Grok 分组使用本地 tokenizer（分词器）返回确定性估算，不请求 xAI，也不产生额度、并发、账单、健康或调度副作用。embeddings（向量）接口当前仍不属于 Grok 能力，返回明确的不支持错误。
- xAI 返回的请求数、Token、重置时间和 `Retry-After（建议重试时间）` 响应头会写入账号额度快照；密钥和完整令牌不得写入日志。

Grok 文本、图片、图片编辑、视频、固定分组调度、计费、日志和用量回传已通过本地完整链路测试，116 节点也已完成无真实 Grok 账号的灰度部署。真实 xAI OAuth、真实 Grok 请求与实际账单仍按升级清单验收，验收前不得对外宣称生产可用。

### Grok Voice、Realtime 与网页搜索

以下用户接口只接受绑定 Grok 分组的有效 API Key（接口密钥），复用固定分组、内容审计、用户/账号并发、账号故障切换、用量幂等和本地请求日志；非 Grok 分组返回不支持错误：

- `POST /v1/tts`、`POST /tts`：转发 xAI TTS（文字转语音）请求；按上游实际音频用量结算。
- `POST /v1/stt`、`POST /stt`：转发 xAI STT（语音转文字）请求；支持音频表单或上游兼容请求体，按实际上游识别用量结算。
- `GET /v1/realtime`、`GET /realtime`：仅 WebSocket（长连接）升级请求可用。会话持续期间占用用户和账号并发，连接结束后以实际时长记录 Realtime（实时语音）用量；正常关闭不会误记为账号故障。
- `POST /v1/web_search`、`POST /web_search`：请求体为 `query` 和可选 `max_results`（范围 `1-20`，默认 `5`）。网关使用 Grok 原生 `web_search` 工具，结果只保留真实上游来源的 URL（网址）、标题和摘要；`429/5xx` 或网络故障可在同分组内换号，最终成功才写一条 `SearchCount=1` 用量事实。搜索单价读取分组 `search_price_per_1k（每千次搜索价格）`；显式 `0` 表示免费。
- `POST /v1/x_search`、`POST /x_search`：仅 Grok 分组可用，接受 `query`（亦兼容 `input`）、`max_results`、`allowed_x_handles`、`excluded_x_handles`、`from_date`、`to_date`、`enable_image_understanding` 和 `enable_video_understanding`。网关使用当前 Grok 运行时默认文本模型和原生 `x_search` 工具，并只返回 `x_search_call.action.sources`确认过的真实来源。该入口与 `web_search` 复用 API Key（接口密钥）鉴权、额度资格、用户/账号并发门禁、内容审计、同分组 failover（故障切换）和搜索按次计费；只有最终成功才写入一条 `SearchCount=1` 用量事实。

Chat Completions（聊天补全）与 Responses（响应）协议之间转换时，`x_search` 的账号过滤、日期过滤、图片/视频理解开关和 `tool_choice（工具选择）` 原样保留；上游返回的 `x_search_call.action.sources` 会被统一抽取，不会在协议回退时丢失来源。

Grok custom voices（自定义音色）继续使用 `/v1/custom-voices` 系列接口。语音、实时会话、搜索和音色接口均不因调用方提供的模型或账号标识跨分组、跨租户调度；后台账号测试可使用相同真实上游能力返回媒体预览，但不会扣客户余额或写客户用量。

### Grok runtime（运行时）设置与上游配额门禁

`GET/PUT /api/v1/admin/settings` 返回并保存以下 Grok 设置，保存后通过进程内不可变快照立即生效：

- `grok_default_text_model`：未显式映射 Grok 账号的默认文本模型，空值回退 `grok-4.5`。
- `grok_cross_client_model_map_enabled`：是否启用跨客户端模型映射，默认关闭，避免静默改写客户模型名。
- `grok_scheduling_threshold_percent`：范围 `1-100`，默认 `100`。`1-99` 会在真实 xAI 请求数或 Token 配额窗口的已用比例达到阈值时，暂停该账号至上游给出的重置时间；`100` 表示关闭此提前停调门禁。

阈值只读取真实 xAI 请求或 Token 配额响应头投影出的 `limit/remaining/reset`，不使用本站消费金额、本地 Token 统计或猜测值。没有完整真实配额头、没有未来重置时间或账号不是 Grok 时不暂停；暂停原因只保存脱敏的已用比例和重置时间。

### Channel Monitor V2（渠道监控第二版）

Channel Monitor V2（渠道监控第二版）从真实请求与用量记录进行被动聚合，不向被监控账号主动发测试请求。全局 `channel_monitor_mode` 在 V1/V2 间互斥；未切到 V2 时 V2 读取接口拒绝，避免两套口径同时展示。

- 管理员配置：`GET/PUT /api/v1/admin/channel-monitor-v2/config`，设置总开关、刷新间隔、平台/分组/模型范围、健康阈值及忽略错误类别；更新使用版本号并发控制。
- 管理员数据：`GET /api/v1/admin/channel-monitor-v2/{dimensions,snapshot,models,matrix,errors,users}`，支持 `range`、`platform`、`model`、`group_id` 和矩阵 `group_by` 筛选，返回 KPI（关键指标）、趋势、脉冲矩阵、错误分类与健康评分。
- 普通用户数据：`/api/v1/channel-monitor-v2/...` 使用相同只读聚合，但移除请求量、RPM（每分钟请求数）、TPM（每分钟 Token 数）及其他管理员流量规模字段，只保留其可见范围内的状态和质量信息。

### 轻量网关零钱结算开关

#### `GET /api/v1/admin/settings/billing-settlement`

读取 gateway-lite（轻量网关）当前使用的零钱结算模式。设置不存在或启动预热失败时固定按关闭处理：

```json
{
  "idempotent_enabled": false,
  "mode": "legacy",
  "legacy_warning": true
}
```

- `idempotent_enabled=false`：继续使用 `legacy（旧版）`结算逻辑。
- `idempotent_enabled=true`：使用 `idempotent_v2（防重复结算）`逻辑，同一 `request_id（请求编号）` 的重复完成或重复回调复用首次计算出的整数美分，不再重复推进零钱累计器。
- `mode` 和 `legacy_warning（是否展示旧版风险提示）`由服务端根据开关派生，客户端不能单独修改；警告文案由前端本地化。

#### `PUT /api/v1/admin/settings/billing-settlement`

保存结算开关。请求必须明确携带布尔值：

```json
{
  "idempotent_enabled": true
}
```

成功响应与 GET 结构相同：

```json
{
  "idempotent_enabled": true,
  "mode": "idempotent_v2",
  "legacy_warning": false
}
```

数据库保存成功后会立即更新 atomic（原子内存快照）；保存失败不会发布新状态。请求进入网关时固定本次使用的模式，切换开关只影响之后进入的新请求。此开关不追溯或补偿启用前已经结算的记录。

### 聚合分组总开关

#### `GET /api/v1/admin/settings/composite-groups`

读取本节点当前真正生效的 Composite（聚合分组）能力，响应为：

```json
{
  "enabled": false
}
```

首次没有数据库设置时沿用 `gateway_lite.composite_groups_enabled` 的 YAML（配置文件）或环境变量启动值；管理员保存后以数据库设置为准。

#### `PUT /api/v1/admin/settings/composite-groups`

请求必须明确携带布尔值，省略或传入 `null` 返回 `400`：

```json
{
  "enabled": true
}
```

保存成功后立即更新 atomic（原子内存快照），无需重启。分组创建、分组启用、聚合路由启用、OpenAI/Gemini 聚合请求和运行摘要读取同一状态；关闭时保留已有配置，但已有聚合 Key 的新请求返回 `503`。数据库写入失败不会改变当前运行状态。普通分组不受影响。

### 视频变换热开关（Grok 视频编辑/扩展）

#### `GET /api/v1/admin/settings/grok-video-transforms`

读取本节点当前真正生效的 Grok 视频编辑（`POST /v1/videos/edits`）与视频扩展（`POST /v1/videos/extensions`）开关：

```json
{
  "enabled": false
}
```

首次没有数据库设置时沿用 `gateway.grok_video_transforms_enabled` 的 YAML（配置文件）或环境变量启动值；管理员保存后以数据库设置为准。

#### `PUT /api/v1/admin/settings/grok-video-transforms`

请求必须明确携带布尔值，省略或传入 `null` 返回 `400`：

```json
{
  "enabled": true
}
```

保存成功后立即更新 atomic（原子）内存快照，**无需重启**：两条路由始终注册，请求处理器每次读取该快照，关闭时返回 `404 not_found_error`（与“路由不存在”对客户端完全等价）。数据库写入失败时不改变当前运行状态并返回错误，不会出现页面已开、实际仍 404 的半状态。视频生成（`POST /videos/generations`）与状态/内容查询不受该开关影响，始终可用。

### Codex client admission（Codex 客户端准入）

#### `GET /api/v1/admin/settings/codex-client-admission`

读取当前全局准入策略、固定客户端预设和本进程启动后的脱敏统计。响应示例：

```json
{
  "settings": {
    "mode": "audit_only",
    "min_version": "",
    "max_version": "",
    "allowed_clients": ["desktop", "cli", "vscode", "app_server"],
    "denied_clients": [],
    "app_server_enabled": false
  },
  "stats": {
    "started_at": "2026-08-01T08:00:00Z",
    "inspected": 120,
    "observed": 20,
    "detected": 18,
    "audit_violations": 2,
    "denied": 0,
    "clients": [{"key": "cli", "count": 18}],
    "reasons": [{"key": "allowed", "count": 16}],
    "signals": [{"key": "user_agent", "count": 18}],
    "versions": [{"key": "0.144.1", "count": 18}],
    "profiles": [{
      "client": "cli",
      "version": "0.144.1",
      "signals": ["user_agent", "originator", "codex_path"],
      "count": 18
    }]
  },
  "known_clients": ["desktop", "cli", "vscode", "app_server"],
  "version_presets": ["", "0.144.1", "0.150.0"]
}
```

`stats（统计）`只保留客户端粗分类、规范化版本、信号类别、原因和计数；`profiles（脱敏指纹组合）`把客户端类别、Codex 引擎版本和实际命中的信号组合关联起来，便于确认某条规则依据的不是互不相关的总数。两类统计均使用固定有界槽，不保存原始 `User-Agent`、`originator`、IP、API Key、请求头、查询串或正文，恶意构造不同版本号或包装器名称不会让内存无限增长；服务重启后计数重新开始。

#### `PUT /api/v1/admin/settings/codex-client-admission`

请求体就是完整 `settings` 对象。`mode` 只能为 `audit_only（只审计）`或 `enforce（正式拦截）`；默认和设置缺失时固定为 `audit_only`。`allowed_clients/denied_clients` 只能选择 `desktop/cli/vscode/app_server` 四个内置预设，不能提交自由请求头表达式。`deny（拒绝）`优先于 `allow（允许）`，响应会返回去重后的最终规则。最低/最高版本只能选择或提交三段/四段数字版本，空字符串表示不限制。

准入采用至少两个独立信号组合：官方客户端家族 `User-Agent`、已知 `originator`、`/responses`、`/v1/responses`、`/models`、`/v1/models`、`/backend-api/codex/*` 等 Codex 路径、`client_version` 和 `x-codex-*`/Responses Lite（精简响应）标志。只有一个可伪造请求头或只有普通路径时只记为 `insufficient_signals（信号不足）`，不会据此拒绝；因此官方 UA + `/v1/responses` 可以执行版本门禁，普通 `curl` 请求 `/v1/responses` 不会被误认成 Codex。官方桌面、CLI（命令行）和 VS Code 家族复用账号级 `codex_cli_only` 的同一身份注册表。

`app_server` 表示直接使用 Codex App Server 的未列名集成，不是 Claude Code 的别名。真实 App Server 的外层 `User-Agent` 和 `originator` 仍会声明 `Codex Desktop`，因此不能只看外层前缀；网关还会校验 `clientInfo` 派生的末尾 wrapper（包装器）名称/数字版本与 `x-codex-*`/Responses Lite 引擎头。只有“成对的 Desktop 外层身份 + 非官方 Desktop wrapper + 引擎头”同时成立才归为 `app_server`，第一方 Desktop 的 `(Codex Desktop; build)` wrapper 保持 `desktop`。官方家族之外的旧式候选仍必须同时命中引擎头和另一个独立信号；`app_server_enabled=false` 时在正式拦截模式拒绝，开启后还要通过 `allowed_clients`。Claude Code 的完整或部分身份声明都会返回 `account_policy_client` 聚合原因，并继续交给既有账号级命名客户端与 `codex_cli_only` 规则判断；即使同时携带 `x-codex-*` 也不会被冒充成 App Server。wrapper 和请求头仍属于可伪造的 admission signal（准入信号），不是客户端认证，所以新规则继续默认 `audit_only`。当前入口层为了保持热路径不读取正文，只使用请求头级引擎信号；官方可配置 body fingerprint（正文指纹）仍属于部分能力，不能宣称完整等价。

执行优先级如下：

1. `audit_only` 只记录，不拦截；`enforce` 下的全局 deny、App Server 开关、指纹冲突和版本边界在 API Key 解析、`lease（额度租约）`、扣款、上游调度和正常用量写入之前执行。
2. 全局规则允许只代表通过入口检查，不会绕过账号规则；选择上游账号后，账号级 `codex_cli_only` 仍可继续收紧。
3. 全局拒绝不会取得 lease、不扣款、不写正常用量，并只向 ingress rejection（入口拒绝）聚合增加 `codex_client_denied` 分类。

请求热路径只 `atomic.Load（原子读取）`预编译不可变策略和 bitmask（位掩码），不访问 PostgreSQL（数据库）、Redis（缓存服务）、文件或第三方接口。管理员保存成功后原子替换策略；进程启动时同步预热，预热失败保持默认只审计，不能因临时数据库故障意外封禁客户。

### Settings（系统设置）里的网关转发行为字段

`GET /api/v1/admin/settings` 和 `PUT /api/v1/admin/settings` 会包含 gateway forwarding（网关转发行为）相关字段：

- `gateway_custom_error_message`：客户网关错误的统一说明文案，纯文本、最多 500 个 Unicode 字符；留空即关闭。保存后立即刷新进程内快照，新请求生效。
- `enable_fingerprint_unification`：是否统一 OAuth（授权）账号的指纹头，默认开启。
- `enable_metadata_passthrough`：是否透传客户端原始 metadata（元数据），默认关闭。
- `enable_cch_signing`：已废弃字段。新版 Claude Code CLI（Claude 代码命令行客户端）取消 CCH 签名后，该开关保留但无实际效果。
- `enable_claude_oauth_system_prompt_injection`：是否对 Claude OAuth mimic（模拟 Claude 授权客户端）路径注入 system prompt（系统提示），默认开启。
- `claude_oauth_system_prompt`：Claude OAuth mimic 路径注入的扩展 system prompt，空值使用内置默认。
- `claude_oauth_system_prompt_blocks`：Claude OAuth mimic 路径注入的 system blocks（系统块）JSON 配置，空值使用内置默认。
- `enable_anthropic_cache_ttl_1h_injection`：是否对 Anthropic OAuth/SetupToken（授权/安装令牌）请求体注入 1h cache_control ttl（缓存控制时间），默认关闭。
- `enable_client_dateline_normalization`：是否对 Anthropic OAuth/SetupToken 请求体里的客户端 dateline（日期线指纹）做归一化，默认开启；API Key（接口密钥）账号、Bedrock（AWS 托管模型）账号和非 Anthropic 平台不受影响。
- `rewrite_message_cache_control`：是否改写 `messages[*].content[*].cache_control`，默认按网关配置决定。
- `openai_fast_policy_settings`：OpenAI Fast/Flex Policy（优先/灵活处理规则）配置。新版不再使用 `openai_priority_mode（旧三态策略）`，也不读取 API Key（接口密钥）的独立 Priority 偏好。

自定义错误文案作用于 `RegisterGatewayRoutes` 注册的全部客户 API（接口）入口，包括 `/v1`、`/v1beta`、`/responses`、`/backend-api/codex`、`/chat/completions`、`/messages`、`/embeddings`、`/images`、`/videos`、`/alpha/search` 与 `/antigravity`。OpenAI、Anthropic、Gemini 的普通 JSON 错误、完整 SSE（流式事件）错误帧，以及 Responses WebSocket/Live（长连接/实时）错误帧和异常关闭原因会替换为管理员文案；HTTP 200 返回的异步图片任务和 Grok 视频任务失败状态也会替换已有的客户可读错误文案。HTTP 状态码、WebSocket close status（关闭状态）、`code/type/status/state/error_code/error.code`、`Retry-After` 及其他机器字段不变。请求开始时固定读取一份内存快照，热路径不访问 PostgreSQL（数据库）或 Redis（缓存服务）。后台管理接口、健康检查、内部请求日志、内容审计证据、账号冷却和 failover（故障切换）仍使用真实错误。Nginx/CDN/TLS 在请求进入 Go 服务之前生成的错误、客户端已断开或没有合法错误帧的 WebSocket 断线不在应用可改写范围内。

当 `enable_claude_oauth_system_prompt_injection（Claude OAuth 系统提示注入）` 开启且网关需要把非 Claude Code 客户端模拟成 Claude Code 时，Anthropic OAuth（授权账号）的 Haiku 请求与 Sonnet/Opus 使用同一套完整 Claude Code 线协议：注入 billing/system blocks（计费与系统块）、规范化模型编号，并发送完整 `anthropic-beta` 集合（包含 `claude-code`、`oauth`、`interleaved-thinking`、`context-management` 等当前受支持能力）。该规则只作用于 Anthropic OAuth mimic（模拟）路径；真实 Claude Code 客户端继续透传其 beta，Anthropic API Key（接口密钥）账号继续使用不含 OAuth/Claude Code 标记的 API Key beta，OpenAI/Grok 等其他平台不会进入此改写函数。

保存示例：

```json
{
  "openai_fast_policy_settings": {
    "version": 1,
    "priority_detection_by_request_enabled": true,
    "priority_detection_by_response_enabled": true,
    "rules": [
      {
        "id": "filter-flex-for-apikey",
        "service_tier": "flex",
        "action": "filter",
        "scope": "apikey",
        "user_ids": [],
        "model_whitelist": ["gpt-5.6-*"]
      }
    ]
  }
}
```

生效规则：

- `rules（规则）` 按页面顺序匹配；指定 `user_ids（用户编号）` 的用户专属规则先于全局规则，同一层只执行第一条命中规则。`id（规则编号）` 会稳定写入管理员日志，显式重复编号会拒绝保存。
- `service_tier` 支持 `all（全部已识别模式）`、`priority（优先处理）`、`flex（灵活处理）`；客户兼容值 `fast` 会在发送上游前归一为 `priority`。
- `action（动作）` 支持 `pass（放行）`、`filter（删除 service_tier）`、`block（拒绝）`、`force_priority（把已存在且合法的模式改成 priority）`。没有 `service_tier` 的普通请求不会被规则凭空注入 Priority。
- `scope（账号范围）` 支持 `all`、`oauth`、`apikey`、`bedrock`；`model_whitelist（模型白名单）` 不匹配时执行 `fallback_action（兜底动作）`。
- `priority_detection_by_request_enabled（根据请求判断）` 使用规则处理后最终发送给上游的值；`priority_detection_by_response_enabled（根据响应判断）` 使用上游明确返回的值。两个开关至少开启一个，前后端都会拒绝同时关闭。
- 仅开启请求判断时完全忽略上游返回；仅开启响应判断时响应缺失或非法按标准处理；两个都开启时响应明确值优先，响应缺失或非法才回退最终发送值。
- 请求发送 `priority` 而响应明确为 `default` 时，双开关模式按标准计费；请求发送标准而响应明确为 `priority` 时按 Priority 计费。响应非法原值会保留在管理员证据字段中，不会被猜测为合法模式。
- 图片生成意图、Claude 和 Gemini 请求不应用 OpenAI Fast/Flex 规则。HTTP、SSE（流式事件）、Chat Completions、Responses、Messages 和 WebSocket 每个 `response.create` 使用同一规则函数。
- 模型不支持 Priority 或 Priority 价格字段不完整时 fail-closed（失败关闭），本地返回明确错误，不按固定倍率猜价；超过长上下文阈值时也必须具备对应组合价格。
- 上游在输出内容前明确拒绝 `service_tier` 时，网关移除该字段并按标准模式重试一次；开始输出后禁止重试，避免重复内容和重复计费。
- 设置保存后立即刷新轻量网关本地运行时缓存；WebSocket 会话在建立时固定读取一份规则快照，避免每个帧都查询数据库。

### `GET /api/v1/admin/usage`

- 轻量网关管理员接口新增可选查询参数 `upstream_model_mismatch=true|false（上游响应模型是否不一致）`。`true` 只看不一致，`false` 只看明确一致；上游未声明模型的历史记录不会被归入任一项。
- 管理员日志 DTO（数据传输对象）新增 `upstream_response_model（上游响应原文声明模型）` 和 `upstream_model_mismatch（是否与实际发送模型不一致）`。这两个字段只存在轻量网关本地管理员 API（接口），普通用户 API、用量上报、control-plane（主站控制面）及主站前端均不包含。
- `GET /api/v1/admin/usage/search-users?q=...` 同时按本地影子用户保存的主站用户名/邮箱搜索，结果新增 `username（用户名）`；远程影子账号对管理员展示主站真实邮箱，不展示内部 `@local.invalid` 合成地址。身份来自 Key snapshot（密钥快照）并在快照变化时更新，不会逐条日志回查主站。
- `GET /api/v1/admin/usage/stats` 同样接受 `upstream_model_mismatch=true|false`，保证统计卡与列表筛选口径一致。

### v0.1.172 选择性升级兼容口径

- 合入 OAuth（授权登录）补全流程接管漏洞、金额八位小数精度、10 秒 TCP/TLS/SOCKS5 建连上限、Responses→Anthropic 坏历史块清理、Codex Desktop 空工具类型修复。
- OpenAI/Grok 的 `405` 只触发 failover（故障切换），不触发账号级下线；图片模型误投 Codex 文本端点时不写模型冷却。
- 系统日志落库连续失败采用 `2s → 4s → ... → 60s` 指数退避，退避期丢弃尽力而为日志，保护 PostgreSQL（数据库）业务连接池。低流量瞬时失败串保留 30 分钟，成功请求立即清零。
- 上游响应模型审计覆盖 OpenAI、Anthropic 和 Antigravity/Gemini 的普通与流式响应，只用于本地诊断，不改变路由、映射或计费。

### v0.1.173 选择性升级兼容口径

- Gemini 原生、Messages（消息兼容）与 Chat Completions（聊天补全兼容）生图均按上游实际返回的 `inlineData/inline_data（内联图片）` 数量计费；流式响应只保存图片 SHA-256 fingerprint（指纹）及单片出现次数，不保存图片原文，因此不同增量片段的多张图会累计、累积快照中的同一图片不会重复计费。无法从响应识别图片时，才回退请求模型或映射后模型的生图能力判断。
- Gemini `pool_mode（账号池模式）` 的普通 `429` 由请求级重试和 failover（故障切换）处理，不再把整个账号写为账号级限流；管理员显式启用并命中的 `custom_error_codes（自定义错误码）` 仍按原规则处理。
- OpenAI API Key（接口密钥）与 OAuth（授权账号）图片请求即使客户端中途断开，也会继续读取上游终态并记录真实图片用量，避免上游已经收费但网关漏记；两条路径共用 30 分钟独立硬超时，Agent Identity（代理身份）恢复重试沿用原截止时间，异常上游不会永久占用协程和账号并发。
- OpenAI OAuth（授权账号）的普通、passthrough（透传）HTTP 请求、账号测试、额度探测、运行时模型探测和 Alpha Search（搜索）回退不再注入废弃的 `OpenAI-Beta: responses=experimental`。WebSocket（长连接）仍保留协议必需的 `responses_websockets=...` Beta（测试功能）值。网关会根据最终发送的 `model` 与合法 `service_tier` 生成受控的 `x-codex-routing-hint`；调用方提供的同名头会被清理，API Key 账号不会获得该提示。
- Grok OAuth 客户端缺失时，授权码交换、刷新令牌和 SSO 转换统一返回 `GROK_OAUTH_CLIENT_NOT_CONFIGURED`，不会触发空指针崩溃；授权码交换在确认客户端可用前不会消费一次性会话。
- Grok 批量导入后的自动探测最多保留 64 个待处理任务，并按账号合并等待中和执行中的重复任务，保护本机与上游。队列满时只放弃本次后台自动探测，账号仍可通过后续导入或管理员手动探测恢复。
- 官方 xAI 视频状态只有顶层 `status=done` 且存在 `video.url` 时才视为成功并结算；自定义第三方上游继续兼容既有的 `completed/succeeded` 和常见 URL 字段，避免破坏第三方协议。
- 上游响应模型观察只在响应片段出现候选模型字段后才执行完整 JSON 校验，减少无模型流式增量的扫描；Gemini 同时识别 `response.response.modelVersion` 嵌套声明。

用途：分页查询 gateway-lite（轻量网关）本地 usage logs（用量日志）。

- 查询参数 `model（模型）` 按 `requested_model（客户请求模型）` 精确筛选；历史记录没有该字段时回退 `model`。分页列表、汇总卡片、模型分布、入口端点和上游端点统计使用同一表达式，不再出现列表已过滤但图表仍混入其他映射后模型的情况。

Priority（优先处理）相关规则：

- 查询参数 `service_tier` 支持 `priority（优先处理）`、`standard（标准处理）`、`flex（灵活处理）`；`fast` 会兼容归一为 `priority`，`default/auto/scale` 会归一为 `standard`。
- 每条管理员日志返回 `requested_service_tier（客户原始请求值）`、`sent_service_tier（规则处理后最终发送值）`、`response_service_tier（上游明确返回值）`、`service_tier（最终计费模式）`、`priority_detection_source（最终判断来源）`、`priority_policy_action（规则动作）`、`priority_policy_rule_id（命中规则编号）` 和 `priority_policy_fallback（是否使用规则兜底动作）`。`priority_policy_mode（旧三态来源）` 仅用于历史日志兼容，新请求不再写入。
- 每条管理员日志可返回 `rate_policy_audit（倍率策略审计）`，保存请求开始时冻结的分组倍率、用户覆盖倍率、高峰配置、本次是否命中高峰、最终 Token 倍率、时区、解析时刻和策略版本。只接受 `schema_version=1`、`billing_scope=token`、`v1:<16位十六进制>`版本以及有限非负且相互一致的倍率；无效可选证据会丢弃，但不会阻断本地用量日志或远程计费事件。流式结束、重试及重复回调继续沿用同一份首次合法证据；字段不包含账号采购成本倍率。普通用户日志不返回这份内部审计。数据库 migration（迁移）为 `187_usage_log_rate_policy_audit.sql`，字段是可空 JSONB（JSON 二进制），可重复执行。
- `priority_detection_source` 可能是 `request`、`response`、`request_response_invalid（响应非法后回退请求）`、`standard_default（没有有效证据）` 或 `standard_response_invalid（响应非法且不能回退请求）`。
- `service_tier` 是真实计费依据，具体优先级由两个判断开关决定；不能再简单假定“响应永远优先”。发送值与响应值不一致时，运维日志会记录 `openai_service_tier.upstream_override`。
- 上述规则证据字段只用于管理员审计，不参与二次计费；普通用户日志只返回最终 `service_tier`。
- 每条日志返回 `long_context_billing_applied（是否实际应用厂商长上下文价格）`。只有分组允许长上下文计费、命中账号开启厂商长上下文计费、请求超过模型阈值且长上下文价格确实改变本次费用时才为 `true`；历史记录、分组关闭和普通价格请求为 `false`。
- Token（令牌）字段使用拆分口径：`input_tokens（普通输入）` 不包含 `cache_read_tokens（缓存读取）` 和 `cache_creation_tokens（缓存写入）`；三者分别计费，避免缓存 Token 重复按普通输入收费。
- 图片输入证据字段为 `image_input_tokens（图片输入 Token）`、`image_input_cost（图片输入基础费用）`、`image_input_price（采用的单位价格）` 和 `image_input_price_source（价格来源）`。`image_input_tokens` 是 `input_tokens` 的子集；来源支持 `channel`、`model_catalog`、`static_model`、`text_input_fallback`，显式免费时价格仍为 `0` 并通过非空来源与“未配置”区分。
- `/v1/chat/completions` 转发到 Responses 上游，或 Responses 结果转换回 Chat Completions 时，会保留 `cache_write_tokens（缓存写入 Token）`；本地计费、下游 usage（用量）和主站上报使用同一数值。
- Responses 与 Anthropic Messages（消息协议）双向转换时，会单独保留 `cache_creation_input_tokens（缓存写入 Token）`；该数值同时计入 Responses 的总输入 Token，但转换回 Anthropic 时会从普通输入中扣除，避免缓存写入重复按普通输入计费。
- HTTP 请求使用网关生成的规范请求编号：客户端请求编号统一保存为 `client:<client_request_id>`，本地请求编号统一保存为 `local:<request_id>`，无上下文时使用 `generated:<uuid>`。额度预扣占位、真实 Token/媒体费用、本地日志和向主站上报的 `request_id（请求编号）` 必须使用同一个规范值，禁止再生成“原始 UUID 占位记录 + `client:` 前缀真实用量记录”两条数据。若真实费用修正先于 HTTP 中间件收尾到达，中间件必须读取 Redis（缓存服务）中已经修正的最终金额再上报，不能用一美分占位值覆盖真实费用。WebSocket（长连接）仍按每轮上游 `response id（响应编号）` 记账。
- 费用字段包括 `input_cost`、`output_cost`、`cache_creation_cost`、`cache_read_cost`、`total_cost` 和应用分组倍率后的 `actual_cost（实际费用）`。这些字段单位为 USD（美元），上报主站时再通过统一的亚分累计规则转换为 `actual_cents（实际费用分）`。
- 如果上游返回的缓存读取与缓存写入 Token 之和大于总输入 Token，普通输入会归零而不会出现负数，同时写入 `openai_usage.cache_tokens_exceed_input_tokens` 管理员系统警告日志。
- 管理员日志的 `account（账号摘要）` 会额外返回 `platform（平台）`、`temp_unschedulable_until（临时暂停截止时间）`、`temp_unschedulable_reason（临时暂停原因）` 和 `grok_quota_snapshot（Grok 额度快照）`。额度快照只包含允许保存的请求数、Token、重置时间、订阅层级和授权状态，不包含 OAuth Token（授权令牌）或 API Key（接口密钥）；普通用户日志不返回这些管理员诊断字段。

### `GET /api/v1/admin/ops/system-logs`

用途：查询 ops system logs（运维系统日志），供轻量网关后台排查请求链路、上游错误、日志写入状态和运行时配置。

常用查询参数：

- `page`、`page_size`：分页。
- `time_range`：时间范围，支持 `5m`、`30m`、`1h`、`6h`、`24h`、`7d`、`30d`。
- `start_time`、`end_time`：自定义时间范围，RFC3339 时间格式。
- `level`：日志级别，例如 `debug`、`info`、`warn`、`error`。
- `component`：组件名，例如 `http.access`。
- `request_id`、`client_request_id`：请求链路编号。
- `user_id`：用户编号。
- `api_key_id`：API Key（接口密钥）编号。用于按某个 Key 排查请求日志和系统日志。
- `account_id`：上游账号编号。
- `platform`：平台或供应商，例如 `openai`、`anthropic`。
- `model`：模型名。
- `q`：关键词搜索。

返回字段包含 `api_key_id（接口密钥编号）`、`account_id（账号编号）`、`request_id（请求编号）`、`client_request_id（客户端请求编号）`、`platform（平台）`、`model（模型）` 和 `extra（额外结构化字段）`。后台页面会把这些字段合并展示，方便直接从日志定位到具体 API Key（接口密钥）和上游账号。

### `POST /api/v1/admin/ops/system-logs/cleanup`

用途：按筛选条件清理 ops system logs（运维系统日志）。请求体支持与查询接口相同的过滤字段，包括 `api_key_id（接口密钥编号）`。该接口不可恢复，后台页面必须要求管理员确认后再调用。

### Ops error logs（运维错误日志）查询接口

用途：查询 gateway-lite（轻量网关）记录的请求错误和上游错误，供后台用量分析、错误排查、账号调度分析使用。

接口：

- `GET /api/v1/admin/ops/errors`：统一错误列表。
- `GET /api/v1/admin/ops/request-errors`：请求错误列表。
- `GET /api/v1/admin/ops/upstream-errors`：上游错误列表。

`upstream_errors（上游错误事件数组）` 中每个事件可包含 `proxy_host（代理主机）`。该字段只保存代理 Host（主机名或 IP），不会保存代理用户名、密码、完整代理 URL、query（查询参数）或 fragment（片段）。OpenAI failover（失败切换）、OpenAI passthrough（透传）、Responses WebSocket（响应长连接）拨号失败和 WebSocket HTTP bridge（长连接转 HTTP 桥接）失败都会尽量记录该字段，供管理员判断同一批错误是否集中在某个代理节点；普通用户接口不返回该字段。

常用查询参数：

- `page`、`page_size`：分页，`page_size` 最大为 `500`。
- `time_range`：时间范围，支持 `5m`、`30m`、`1h`、`6h`、`24h`、`7d`、`30d`。
- `start_time`、`end_time`：自定义时间范围，RFC3339 时间格式。
- `view`：视图，支持 `errors（错误）`、`excluded（排除项）`、`all（全部）`。
- `q`：关键词搜索，匹配 `request_id（请求编号）`、`client_request_id（客户端请求编号）` 和错误信息。
- `status_codes`：状态码列表，逗号分隔，例如 `429,500,502`。
- `status_codes_other`：其他状态码筛选，支持 `1/true/yes` 开启；开启后匹配不在常用状态码列表里的错误。
- `phase`：错误阶段，例如 `request`、`auth`、`routing`、`upstream`、`network`、`internal`。
- `category`：面向管理员的稳定错误分类，支持 `auth（鉴权）`、`rate_limit（限流）`、`quota（额度或套餐）`、`invalid_request（请求参数）`、`service_unavailable（路由不可用）`、`upstream（上游）`、`internal（内部）`、`cyber（内容策略）`。
- `error_owner`：错误归属，例如 `client（客户端）`、`provider（上游供应商）`、`platform（平台）`。
- `platform`：平台或供应商，例如 `openai`、`anthropic`。
- `group_id`、`account_id`、`user_id`、`api_key_id`：按分组、账号、用户、API Key（接口密钥）过滤。
- `model`：模型名；管理端按 `COALESCE(requested_model, model)` 精确匹配。
- 错误详情中的 `requested_model（用户请求模型）` 与 `upstream_model（实际发送模型）` 分别表示客户请求体模型和完成分组/账号/协议映射后真正发往上游的模型；两者不同表示发生了映射，管理端表格显示为 `requested_model → upstream_model`。尚未选出账号或尚未发出上游请求的错误不会伪造 `upstream_model`。普通用户错误接口仍只返回客户请求模型。
- `sort`：排序，支持 `created_at_desc（最新优先，默认）`、`created_at_asc（最早优先）`、`status_code_desc（状态码高到低）`、`status_code_asc（状态码低到高）`、`severity_desc（严重度高到低）`、`severity_asc（严重度低到高）`、`category_desc（分类降序）`、`category_asc（分类升序）`。

安全说明：`sort（排序）` 参数只接受固定枚举，后端会转换为固定 SQL `ORDER BY` 片段，不会把客户端输入直接拼入 SQL。

### `GET /api/v1/admin/model-prices`

用途：读取当前轻量网关生效的 model prices（模型价格）目录，供轻量后台和主站 control-plane（控制面）同步展示。

返回字段：

- `billing_mode`：计费模式，支持 `token（按令牌）`、`image（按图片张数）`、`per_request（按请求次数）`、`video（按视频秒数）`。
- `input_cost_per_1m_tokens` / `output_cost_per_1m_tokens`：token 价格，单位为 USD / 1M tokens（美元/百万 token）。
- `input_cost_per_1m_tokens_priority` / `output_cost_per_1m_tokens_priority`：Priority（优先处理）输入、输出价格。
- `cache_read_cost_per_1m_tokens` / `cache_creation_cost_per_1m_tokens`：缓存读取和缓存写入 token 价格。
- `cache_read_cost_per_1m_tokens_priority` / `cache_creation_cost_per_1m_tokens_priority`：Priority（优先处理）缓存读取和缓存写入价格。
- `*_above_272k`：超过长上下文阈值后的标准处理输入、输出、缓存读取和缓存写入价格。
- `*_priority_above_272k`：兼容保留字段，当前官方规则没有可用价格，接口返回时为 `0` 或省略。gateway-lite（轻量网关）会拒绝“Priority + 超过 272K”请求，不会读取这些字段猜价。
- `long_context_input_token_threshold`：长上下文价格触发阈值；`0` 表示该模型没有单独长上下文价格。
- `output_cost_per_image`：生图按张计费价格，单位为 USD / image（美元/张）。
- `input_cost_per_image_token`：图片输入 token 价格，单位为 USD / image token（美元/图片 token）；`null` 表示继承文本输入价，显式 `0` 表示免费。
- `output_cost_per_image_token`：生图按图片 token 计费价格，单位为 USD / image token（美元/图片 token）。
- `per_request_price`：按次计费价格，单位为 USD / request（美元/次）。对 LiteLLM（模型价格目录）里只有 `output_cost_per_image` 的生图模型，会用每张图片价格作为默认按次展示价。
- `supports_prompt_caching`：是否支持 prompt caching（提示词缓存）。
- `supports_service_tier`：模型是否支持 OpenAI `service_tier`，前端只在该值为 `true` 时展示 Priority 价格。
- `disabled`：本地模型禁用状态。禁用后不再对客户暴露或放行请求。

本轮模型卡片及筛选使用以下新增只读元数据，管理目录、运行健康上报和配置推送中的 `GatewayModelPrice`（网关模型价格）传输一致保留：

| 字段 | 类型与真实来源 |
|---|---|
| `model_vendor` | 可选字符串；优先目录显式 `model_vendor`。为空或仅空白时，管理目录响应与节点价格上报统一回退到去除首尾空白的原始 `litellm_provider`（目录供应商）；两者均空时保持未知，不从模型名称推测。节点在分组改写 `provider` 为兼容协议平台之前固定该值，分组继续继承 |
| `input_modalities` | 可选字符串数组；优先目录 `input_modalities`，其次 `supported_modalities`，只接受 `text/image/audio/video/embedding`；没有保留下来的合法模态且 `supports_vision=true` 时仅确认 `image`，不按聊天模式补文本或其他媒体 |
| `max_input_tokens` | 可选正整数；目录显式输入令牌上限。缺失/非正数不展示为已知上下文，也不替代实际请求可执行输出上限 |
| `metadata_source` | 可选字符串；能力元数据的证据来源。有显式厂商、输入模态或输入上限且目录未标来源时为 `pricing-catalog`；厂商回退沿用原值，包括原本为空，不因此补写能力来源。价格 `source` 与能力来源是不同字段 |
| `price_fields` | 字符串数组或 `null`；列出来源明确提供的价格字段，使用本接口每百万令牌等返回字段名。数组内字段即使数值为 0，也有显式配置证据；已知数组不包含的零值为未配置，不能显示免费。`null` 或旧响应缺字段表示存在性未知，不从序列化默认 0 反推免费；空数组表示未声明价格字段 |

`provider` 保留价格目录提供来源/渠道分类的含义，分组价格中也可能表示兼容协议平台；`model_vendor` 仅按上述优先级在节点目录阶段选定，不能用分组平台覆盖已选厂商。主站前端对旧快照或自定义价缺少 `model_vendor` 的条目，显示与筛选回退读取已有 `provider`，两者均空才显示“未知”；该展示回退不改变后端合同或能力。`source` 保留 `catalog/override/static_fallback` 等价格来源。管理目录现有 `provider` 查询仍按该价格提供来源过滤，没有新增 `model_vendor/input_modalities` 服务端筛选参数。厂商回退不补造输入能力、上下文或计价字段。

能力只从精确模型目录取得，不沿用价格别名、模糊匹配或家族兜底作为自定义模型的能力事实。只覆盖价格时可保留同一精确目录条目的能力来源；后台价格写入请求仍使用原字段，本轮未开放手填这些只读能力元数据。价格存在性由原始目录键或明确 `price_fields` 保留；原目录解析遇到显式数组（含空数组）或 `null` 时服从该声明，不用序列化残留数值扩充。尚未声明该合同的目录，解析器可把按真实倍率派生的正长上下文价加入证据，但不会据默认 0 猜免费。分组覆盖合并遇到旧 `null`（存在性未知）目录时只可补入已有正价证据，仍不得推断零价免费；显式空数组不会被旧残价扩充。管理员价格请求在转成数值前记录非空价格指针；分组覆盖价同样保留显式 0 的字段证据，切换计费方式不会把无关默认零误标免费。这组展示字段不改费用计算、计价优先级或冻结数值价格，也不扩大 AI（人工智能）媒体审查支持。

说明：全局 `model-prices（模型价格）` 目录来自 LiteLLM price catalog（模型价格目录）和本地 override（覆盖价）。渠道 `channels（渠道）` 里的 `intervals（分层价格）` 属于账号/渠道级计费配置，不会被虚构进全局模型价格目录；真实请求计费仍以请求命中的渠道和分组配置为准。

实现依据：[目录返回类型及处理器](internal/handler/admin/channel_handler.go)、[价格请求存在性](internal/handler/admin/model_price_display_metadata.go)、[元数据证据与精确目录](internal/service/model_price_display_metadata.go)、[网关传输类型](internal/gatewaylite/types.go)、[模型价格上报](internal/server/gateway_lite_model_prices.go)、[分组有效价格](internal/server/gateway_lite_group_model_prices.go)、[覆盖价字段证据](internal/server/gateway_lite_model_price_metadata.go)。

### `GET /api/v1/admin/model-prices/sync/status`

返回公开基础价格目录的同步状态，包括 `catalog_version（目录版本）`、`catalog_sequence（防回滚序号）`、`remote_model_count（远程模型数）`、`model_count（最终生效模型数）`、`override_count（本地覆盖数）`、`source（当前来源）`、`last_success_at（最近成功时间）`、`last_error（最近错误）`、`local_hash（当前摘要）`、`remote_url（远程清单地址）` 和 `refreshing（是否正在刷新）`。响应不包含代理凭据或本地密钥。

价格生效顺序固定为：本地管理员覆盖 > 最近一次校验成功的 GitHub（代码托管）公开目录 > 程序内置兜底。网关启动时先验证磁盘缓存的 manifest（目录清单）、摘要、模型数量和价格语义；当前正文与清单不匹配时会再读取上一组已验证配对，二者都无效才使用内置快照。远程检查在后台进行，不因 GitHub 超时阻塞启动。远程下载拒绝网址重定向、旧序号、同序号不同内容、异常大幅缩容、大小写重复模型、负数/非有限价格和 `disabled（模型禁用）`字段，全部通过后才切换；远程仓库只能更新基础价格，只有本地管理员覆盖可以禁用模型。

### `POST /api/v1/admin/model-prices/sync`

管理员立即同步公开基础价格目录。成功返回与 status 接口相同的状态并通知主站刷新模型价格快照；失败返回 `503 / MODEL_PRICE_SYNC_FAILED`，当前生效价格保持不变。

### `PUT /api/v1/admin/model-prices`

用途：保存单个模型的本地 override（覆盖价）或禁用状态。

请求体可包含：

- `model`：模型编号，必填。
- `provider` / `mode` / `billing_mode`：供应商、模型模式和计费模式。
- `input_cost_per_1m_tokens`、`output_cost_per_1m_tokens`、`cache_creation_cost_per_1m_tokens`、`cache_read_cost_per_1m_tokens`。
- `input_cost_per_1m_tokens_priority`、`output_cost_per_1m_tokens_priority`、`cache_creation_cost_per_1m_tokens_priority`、`cache_read_cost_per_1m_tokens_priority`。
- 标准和 Priority 的 `*_above_272k` 长上下文价格，以及 `long_context_input_token_threshold`。
- `output_cost_per_image`、`input_cost_per_image_token`、`output_cost_per_image_token`、`per_request_price`。
- `supports_prompt_caching`、`supports_service_tier`、`disabled`。

至少需要提供一个价格字段或 `disabled=true`。本接口维护的是全局模型覆盖价；如果需要给某个上游账号配置 `1K/2K/4K` 生图分层价格，应在 `channels（渠道）` 的 `model_pricing.intervals（模型定价分层）` 中配置。

### 渠道模型图片输入价格

`GET /api/v1/admin/channels`、`GET /api/v1/admin/channels/:id`、`POST /api/v1/admin/channels` 和 `PUT /api/v1/admin/channels/:id` 的 `model_pricing[]` 增加 `image_input_price（图片输入 Token 单价）`，单位为 USD / image token（美元/图片 token）。字段为 nullable（可空）：缺失或 `null` 表示继承模型目录的 `input_cost_per_image_token`，显式 `0` 表示该渠道图片输入免费，大于 `0` 表示渠道覆盖价。保存、清空或修改后沿用现有渠道价格事件刷新运行时价格快照、调度证据和主站缓存；旧节点不认识该字段时会忽略，不影响其它价格字段。

gateway-lite（轻量网关）不注册客户侧和主站运营类后台接口，例如：

- `/api/v1/user/*`
- `/api/v1/payment/*`
- `/api/v1/admin/payment/*`
- `/api/v1/admin/redeem-codes/*`
- `/api/v1/admin/promo-codes/*`
- `/api/v1/admin/subscriptions/*`
- `/api/v1/admin/affiliates/*`

### 主站代理账号池鉴权

gateway-lite（轻量网关）后台接口默认仍使用管理员 JWT（登录令牌）或 admin API key（管理员接口 Key）鉴权。为了让 control-plane（主站控制面）能统一管理区域网关账号池，以下账号池接口在 gateway-lite（轻量网关）模式下额外接受 `X-Gateway-Lite-Admin-Key: <gateway_lite.admin_sync_key>`：

- `GET /api/v1/admin/accounts`
- `POST /api/v1/admin/accounts`
- `PUT /api/v1/admin/accounts/:id`
- `POST /api/v1/admin/accounts/bulk-update`
- `DELETE /api/v1/admin/accounts/:id`
- `POST /api/v1/admin/accounts/:id/test`
- `POST /api/v1/admin/accounts/:id/ws-probe`
- `POST /api/v1/admin/accounts/:id/models/sync-upstream`

这个 Key 只用于主站代理账号池管理，不开放完整后台权限。请求必须命中以上账号池路径；其他后台接口仍需要正常管理员登录或 admin API key。

#### `GET /api/v1/admin/accounts` 默认排序

账号列表未传 `sort_by（排序字段）` 和 `sort_order（排序方向）` 时，默认使用 `priority asc（优先级从小到大）`，并以账号 ID 作为稳定次序；数字越小代表优先级越高。`priority` 始终表示账号全局 `accounts.priority（账号优先级）`，选择具体分组或 `group=ungrouped（未分组）`只过滤账号范围，不改变展示值和数据库分页排序口径。该口径与 `strict_priority（严格优先级）` 调度一致。显式传入 `sort_by=name` 等其他既有白名单字段时，继续按请求参数排序。

#### OpenAI 账号 WS 支持探测与筛选

gateway-lite（轻量网关）对 OpenAI 平台账号提供上游 WebSocket（WS，双向长连接）握手探测能力，用于判断账号上游是否真的能走 WS 转发路径（可显著降低首字延迟）。探测只做一次 WS 握手、不发送 `response.create`、不产生推理消费，也**不会自动开启任何 WS 转发行为**；是否启用 WS 仍由账号 `extra` 的 `openai_*_responses_websockets_v2_mode`（批量编辑或单账号编辑设置）决定。

- `POST /api/v1/admin/accounts/:id/ws-probe`：手动触发一次探测。请求体为空；返回 `{ account_id, status, detail, checked_at, account }`。`status` 取值 `supported（支持）`、`unsupported（不支持）`、`unknown（未知）`——握手成功即 `supported`；上游返回 400/404/405/410/426/501 判 `unsupported`；401/403/429/5xx/超时等无法证明支持与否的情况一律 `unknown`，等待下次复检。非 OpenAI 账号返回错误。
- 探测结论写入账号 `extra`：`openai_ws_probe_status`（结论）、`openai_ws_probe_checked_at`（unix 秒）、`openai_ws_probe_detail`（简短原因，如握手 HTTP 状态），账号列表接口按原样返回。
- `GET /api/v1/admin/accounts` 新增 `ws_probe` 筛选参数，取值 `supported`、`unsupported`、`unknown`、`__unset__`（从未探测过）；非法值返回 400 `INVALID_WS_PROBE_FILTER`。
- `POST /api/v1/admin/accounts/bulk-update` 的 `filters` 同样新增 `ws_probe` 字段，语义与列表筛选一致，用于“只对支持 WS 的账号批量开启 WS 模式”这类操作。
- 后台自动复检：服务启动 30 秒后开始，每 15 分钟扫描一次 OpenAI 平台 active 账号；`supported/unsupported` 结论 12 小时内不重测，`unknown` 与未探测的账号每轮补测。账号间串行并间隔 2 秒，避免集中握手触发上游风控。
- 探测握手头与真实 Codex CLI 转发对齐（`originator: codex_cli_rs`、`OpenAI-Beta: responses_websockets=...`、Codex CLI User-Agent），指纹头跟随账号的 `codex_fingerprint_mode` 配置：合成模式（device/session/full）用账号种子合成，off 则不带指纹头，保证探测结论与实跑形态一致。凭影子账号借母账号凭据与实时代理握手，结论落在影子自己的 `extra` 上。

#### HTTP 客户端 WS 上游桥接（http_client_ws_bridge_enabled）

配置项 `gateway.openai_ws.http_client_ws_bridge_enabled`（默认 `false`）。默认行为保持「仅 WS 入站请求走 WS 上游」：普通 HTTP/SSE 客户端即使账号开启了 WS 模式，也会按原逻辑降级为 HTTP 上游（决策原因 `client_protocol_http`）。

开启后，客户端入站协议不再限制上游协议：只要账号 WS 开关（`openai_*_responses_websockets_v2_mode`）放行，网关会把 HTTP `POST /v1/responses` 请求在内部转换为上游 WS（`ctx_pool`，上下文池）转发——客户端无需支持 WebSocket，也无需修改任何接入方式；网关负责 HTTP body → `response.create` 帧、WS 事件流 → SSE/JSON 响应的双向协议转换。

可用性保护：

- WS 转发失败且尚未向客户端写出任何字节时，自动回落同账号 HTTP 重放原始请求体（日志 `ws_http_bridge_fallback`，含失败原因），客户端无感知；回落的记录按 HTTP 模式入账。
- 已经向客户端写出流式字节（响应中途失败）时无法安全重放，保持原 WS 错误返回，不会拼接两个协议的响应。
- 原生 WS 客户端不受此开关影响，失败仍按原行为返回错误。
- 用量日志按真实转发协议记录：走 WS 上游成功的请求 `request_type=3`（WS）并在后台使用记录显示 WS 标注；回落 HTTP 的请求按普通 HTTP 记录。

#### `POST /api/v1/admin/accounts/:id/duplicate`

安全复制一个静态凭据账号。该接口只允许正常 gateway-lite 管理员 JWT（登录令牌）或 admin API key（管理员接口 Key）调用，不接受 `X-Gateway-Lite-Admin-Key` 主站代理权限。请求体为空，必须携带 `Idempotency-Key（幂等键）`；相同管理员、源账号和幂等键的重试返回同一个副本，不会重复创建。除通用幂等记录外，数据库还通过活动账号 `extra.duplicate_operation_id` 的部分唯一索引兜底，多节点并发或幂等协调器降级时，输掉唯一键竞争的请求会读取并返回已提交的副本。

仅支持 `apikey`、`upstream`、`bedrock`、`service_account` 类型。凭据在服务端深复制，响应仍使用现有脱敏 DTO（数据传输对象），浏览器不会取得或回传真实密钥；嵌套的 `header_overrides（请求头覆写）` 也只返回 header 名称，value（值）固定为空字符串占位。副本保留账号配置、分组关系、优先级、倍率和原始代理配置，但清除限流、错误、临时不可调度、用量、探测快照、CRS（远端同步）身份等运行态；新账号初始 `status=active`、`schedulable=false`，必须由管理员复核后再启用调度。

#### OpenAI Spark 影子账号

- `POST /api/v1/admin/accounts/:id/shadow`：从路径中的 OpenAI OAuth（OpenAI 授权）母账号一键创建 Spark 影子。请求体可选传 `name`、`priority`、`concurrency`、`group_ids`；默认继承母账号的分组、代理、并发、优先级和账号倍率。同一母账号最多一个影子。
- `GET /api/v1/admin/accounts/:id/shadows`：查看母账号关联的影子列表。
- `POST /api/v1/admin/accounts/:id/shadow/sync`：以影子 ID 主动重新同步母账号的分组和代理，不覆盖影子自己的并发、优先级和模型映射。
- 移除影子复用 `DELETE /api/v1/admin/accounts/:id`；删除母账号时，母账号、全部 Spark 影子、分组绑定和调度事件在同一数据库事务内删除，任一步失败均整体回滚，不会留下半删除账号树。

影子返回 `parent_account_id`和 `quota_dimension=spark`。影子自身不保存 access token、refresh token 或 API Key（接口密钥），每次请求从母账号解析凭据；但并发、日志、用量、计费和 `codex_bengalfox` Spark 额度仍记在影子 ID。母账号禁用、过期或进入凭据冷却时，影子不可调度；母账号的 global 429/overload（全局限流/过载）不连坐 Spark 独立额度。影子的 401、403 和凭据级 400 写回母账号，普通请求参数 400 不修改账号状态，429 只写入影子额度维度。

通用编辑只允许影子保存 `model_mapping（模型映射）`，并拒绝独立凭据、代理、重授权、Token 刷新、隐私设置和额度券重置。账号备份会跳过影子并返回 `skipped_shadows`，避免生成无法安全还原母子关系的坏备份。

#### Codex 额度健康摘要

`GET /api/v1/admin/accounts` 返回的 OpenAI OAuth（OpenAI 授权）账号对象可包含只读字段 `codex_quota_health`。它根据已有真实上游响应头中的 `x-codex-*` 额度快照推导，用于后台判断 5h / 7d 滚动窗口的观测状态；不会请求上游、不会返回凭据、不会新增数据库写入，也不改变计费、自动暂停、恢复或调度门禁。

```json
{
  "codex_quota_health": {
    "status": "near_limit",
    "trigger_window": "5h",
    "reason": "near_threshold",
    "source": "upstream_headers",
    "observed_at": "2026-08-19T00:00:00Z",
    "next_reset_at": "2026-08-19T02:00:00Z",
    "stale": false,
    "threshold_percent": 95,
    "five_hour": {
      "status": "near_limit",
      "used_percent": 96,
      "reset_at": "2026-08-19T02:00:00Z"
    },
    "seven_day": {
      "status": "healthy",
      "used_percent": 42,
      "reset_at": "2026-08-22T00:00:00Z"
    }
  }
}
```

- `status` 和每个窗口的 `status` 为 `healthy（正常）`、`near_limit（接近上限，当前阈值 95%）`、`exhausted（已耗尽）`、`recovered_pending_refresh（重置时间已过，等待下一次真实响应刷新）` 或 `unknown（未知）`。
- `trigger_window` 表示当前最严格状态命中的窗口，可能是 `5h`、`7d` 或 `5h,7d`；`unknown` 时可为空。
- 快照超过现有两小时有效期、时间异常或字段不完整时返回 `unknown` 并标记 `stale=true`（若为过期）。前端必须将其当作观测缺失，不能据此主动恢复或停用账号。
- 字段只向管理员账号池接口返回；普通用户接口和请求日志均不展示此内部状态。
- 管理员主动查询既有额度快照时，多实例会使用 Redis（缓存服务）的短时账号锁合并同一账号的探测；未抢到锁的实例短暂等待下一次页面查询读取新快照。该锁只减少重复上游查询，不影响客户请求、账号可调度状态或额度判断。

#### Codex Tail-Burst（超额尾部放行）

Tail-Burst 是在额度窗口即将耗尽或即将重置时的可选尾部放行策略，配置由根级 YAML（配置文件）`codex_tail_burst` 提供静态默认值，管理员也可在系统设置的功能开关中运行时切换：

- `GET /api/v1/admin/ops/advanced-settings` 和 `PUT /api/v1/admin/ops/advanced-settings` 的 `codex_tail_burst.enabled` 表示当前运行时总开关。旧版客户端省略该字段（或发送 `null`）时，服务端保留数据库中管理员此前明确保存的值；数据库没有明确值时回退根级 `codex_tail_burst.enabled`，不会被一次旧页面保存意外覆盖。显式发送 `false` 才会关闭并持久化关闭状态。
- 系统设置页使用窄权限接口 `GET /api/v1/admin/ops/codex-tail-burst` 和 `PUT /api/v1/admin/ops/codex-tail-burst`（请求体 `{\"enabled\":true|false}`）读取或切换该开关。窄接口把显式值独立保存到 `ops_codex_tail_burst` 设置键，只更新该开关，不读取、重写或触发其它高级设置副作用；即使运维监控总开关关闭，也仍可读取和保存 Tail-Burst。读取时兼容旧版 `ops_advanced_settings.codex_tail_burst.enabled`，再回退根级 YAML；完整高级设置接口仍受运维监控开关保护。
- 根级 `codex_tail_burst` 不属于 `gateway` 或 `gateway_lite` 节；示例默认 `enabled=false`、`trigger_remaining_ratio=0.02`、`expiry_window=10m`、`snapshot_ttl=90s`、`collector_interval=45s`、`collector_concurrency=4`、`collector_timeout=8s`。无效或缺失的阈值采用程序安全默认值。
- 只有活跃、非影子、带 `chatgpt_account_id` 的 OpenAI OAuth（授权）账号参与；API Key、其他平台和影子账号不会参与。账号 `extra.codex_tail_burst_disabled=true` 可单独退出采集、放行和会话结算，缺失或 `false` 时跟随全局开关。
- 命中 5h/7d 剩余比例阈值、窗口即将重置或账号订阅即将到期时，网关只跳过本地“预防性配额停调”，继续按原有模型、分组、并发、健康和失败切换规则调度。硬冻结、过载、凭据失效和其他临时不可调度状态仍然生效；上游返回权威配额拒绝后才冻结该账号并结束尾部会话。
- 采集器按 `collector_interval` 查询官方 `/backend-api/wham/usage` 并刷新 `codex_*` 快照。快照过期不会继续尾部放行。全局开关关闭时不再请求额度探针，但仍周期性清理并结算遗留的 `codex_tail_burst_active` 会话；账号级开关关闭时也会清理该账号遗留标记。数据库/用量仓储暂时不可用时保留标记等待下轮重试；账号失去资格或状态变化后，标记会在全局关闭清理、窗口/订阅退出判定或后续权威拒绝结算路径中收敛，不把失格状态伪装成新的额度快照。
- 尾部会话的增量请求数、Token（令牌）和窗口百分点仅写入账号扩展字段供管理员观察，不改变客户销售计费、钱包、额度租约或公网响应。正常请求热路径只读取内存运行时开关和账号扩展快照；未开启时不会新增上游探针流量。
- 每次尾部会话进入时都会生成 `codex_tail_burst_session_id`（UUID 会话编号）并参与原子结算校验；这样旧采集器不会误清理同一秒内新开启的会话。升级前没有该字段的历史会话仍按原有时间、窗口、原因和基线字段兼容结算。

#### `GET /api/v1/admin/ops/codex-tail-burst/stats`

读取 Codex 超额（Tail-Burst）历史会话统计，仅管理员可用且受运维监控开关保护。会话在进入超额放行时写入，采集器刷新增量，窗口重置或上游权威拒绝时结算；历史写入是限时同步的尽力而为观测数据，失败不会影响放行结果，但数据库变慢时单次写入最多可能增加约 2 秒等待。

查询参数：

- `start_time`、`end_time`：RFC3339（含时区）时间范围；也可使用 `time_range`（默认 `30d`，最大 30 天）。
- `account_id`：按账号 ID 筛选。
- `window`：`5h` 或 `7d`。
- `reason`：`used_ratio`、`reset_soon` 或 `account_expiry`。
- `status`：`active` 或 `settled`。
- `page`、`page_size`：分页参数，单页最多 100 条。

返回 `data`：

```json
{
  "summary": {
    "sessions": 2,
    "active_sessions": 1,
    "settled_sessions": 1,
    "accounts": 2,
    "requests": 18,
    "tokens": 42000,
    "delta_percent": 1.4
  },
  "trend": [{
    "date": "2026-08-30",
    "sessions": 2,
    "requests": 18,
    "tokens": 42000,
    "delta_percent": 1.4
  }],
  "items": [{
    "session_id": "uuid",
    "account_id": 42,
    "account_name": "Team OAuth",
    "started_at": "2026-08-30T01:00:00Z",
    "ended_at": "2026-08-30T03:00:00Z",
    "window": "5h",
    "reason": "used_ratio",
    "entry_percent": 98.2,
    "delta_percent": 1.4,
    "requests": 18,
    "tokens": 42000,
    "status": "settled"
  }],
  "total": 2,
  "page": 1,
  "page_size": 20,
  "pages": 1
}
```

如果历史表迁移尚未执行，接口返回 `OPS_TAIL_BURST_HISTORY_UNAVAILABLE`；升级时应用启动会自动执行 `228_codex_tail_burst_sessions.sql`。

#### `GET /api/v1/admin/accounts/:id/model-health`

OpenAI（OpenAI 平台）账号的只读模型健康摘要。接口不会探测上游、不会读取或返回凭据，也不会消耗上游额度；它把管理员配置声明和当前网关进程观察到的真实请求结果分开返回。

- `runtime_available`：账号当前是否满足本地 active/schedulable（有效/可调度）门禁；它不是模型已验证可用的保证。
- `declarations`：账号分组专属映射和账号全局映射的声明，`scope` 为 `group` 或 `account`；分组声明带 `group_id`。声明支持不等于最近成功。
- `observed_models`：当前进程按“账号 + 实际上游模型”记录的最近成功、连续失败、冷却和单探测恢复状态。`state` 为 `healthy`、`failing`、`cooling` 或 `recovering`。
- `runtime_observer_available=false` 或 `observed_models` 为空时，表示当前进程没有真实观测证据（例如刚重启或尚未请求），页面必须显示“暂无真实请求证据”，不能显示为健康。
- `health_scope=process_local`：状态只代表本节点进程，不是多网关聚合结论；`last_success_at`、`last_failure_at`、`cooldown_until` 和 `probe_until` 使用 RFC3339 时间。

#### `POST /api/v1/admin/accounts/validate-group-model-mappings`

在创建或编辑 OpenAI（OpenAI 平台）账号、保存 `group_model_mappings（账号分组专属模型映射）`前调用。它只读取当前配置和可调度账号，不发起上游请求、不消耗额度，也不写入健康状态；结果是保存前风险提示，不能替代真实请求验证。

请求体：

```json
{
  "platform": "openai",
  "account_id": 42,
  "group_ids": [7],
  "group_model_mappings": {
    "7": {
      "gpt-5.6-sol": "gpt-5.6-luna"
    }
  }
}
```

- `account_id`：编辑已有账号时传入；创建账号时省略。校验会把待保存的专属映射视为该账号的最新声明。
- `items[]`：每条映射返回 `group_id`、`requested_model`、`mapped_model`、`declared_accounts（分组声明账号数）`、`effective_accounts（当前可调度账号数）`、`recently_used_accounts（近 24 小时实际使用账号数）` 和 `single_point_risk`。
- `single_point_risk=true` 表示当前只有 0 或 1 个可调度账号可承接这条映射。管理员可以确认后继续保存，但后台必须明确提示单点风险。
- `health_scope=configured_and_schedulable_accounts`：此校验仅依据配置和本地调度门禁；模型最近成功、失败、冷却和恢复探测状态只在 `model-health` 接口中按当前进程展示，避免把历史使用时间误当作模型真实可用。

创建账号时可传 `status` 和 `schedulable` 控制初始调度状态：`status` 支持 `active`、`inactive`、`error`；`schedulable` 支持布尔值。不传时保持历史默认值：`status=active` 且 `schedulable=true`。主站如果要先下发一个不参与调度的账号，应显式传 `status=inactive` 或 `schedulable=false`。

`GET /api/v1/admin/accounts` 支持 `lite=true&include_credentials=true`。该组合仅供 control-plane（主站控制面）使用 `X-Gateway-Lite-Admin-Key` 同步账号池镜像时调用，会返回完整 `credentials（凭证）`。主站镜像只用于展示、搜索、审计和 Key（接口密钥）绑定候选，不再通过 config snapshot（配置快照）反向覆盖 gateway-lite（轻量网关）本地账号池。普通后台页面不要传 `include_credentials=true`，默认仍返回脱敏凭证和 `credentials_status（凭证存在状态）`。

账号必须显式绑定 group（分组）才会参与 gateway-lite（轻量网关）本地分组调度。这个判断发生在轻量网关本机数据库和 scheduler cache（调度缓存）里，不再依赖 control-plane（主站控制面）下发的 `accounts（账号快照）`。

#### scheduler cache（调度缓存）生命周期语义

gateway-lite（轻量网关）本地分组、账号和模型变化会先写入本机数据库，再刷新 scheduler cache（调度缓存）。Redis（缓存服务）里的调度桶现在带有 bucket epoch（调度桶世代）、retired marker（退休标记）和 group lifecycle lease（分组生命周期租约），用于避免旧重建任务把已经停用或删除的分组重新写成可调度状态。

- `CaptureBucketWriteToken（捕获调度桶写入令牌）` 只在调度桶未 retired（已退休）时返回当前 epoch（世代）；后续 `SetSnapshotWithToken（带令牌写入快照）` 必须使用同一个 bucket 和 epoch，否则写入会被 fencing（写屏障）拒绝。
- `RetireBucket（退休调度桶）` 会移除当前 active/ready 指针、让旧快照只保留短暂 grace TTL（宽限时间），并写入 retired marker。被 retired 的桶不会接受迟到的快照写入，也不会继续出现在可用调度桶集合里。
- `ReopenBucket（重新打开调度桶）` 只在分组重新 active/schedulable（有效/可调度）时使用，会清理 retired marker 并返回新的写入令牌；随后必须重建该分组所有标准平台桶。
- `TryAcquireGroupLifecycleLease（尝试获取分组生命周期租约）` 用于同一分组的创建、停用、删除和恢复事件串行化。拿不到租约的进程会跳过本次生命周期处理，等待 outbox（事件箱）下一轮修复，避免并发事件交错。
- 调度 outbox rebuild（事件箱重建）进入 degraded（退化）状态时会使用锁存和冷却窗口，避免 Redis、数据库或事件队列短暂异常造成全量重建风暴。
- 同一批次重建同时包含 `single（普通）` 和 `forced（强制）` 调度桶时，会复用同一次账号查询结果，减少数据库压力。当前调度平台集合包含 `anthropic`、`gemini`、`openai`、`antigravity` 和 `grok`。

这些机制不新增对外 API（接口），但会影响后台改分组、删账号和主站展示的生效口径：分组被停用或删除后，旧 Key snapshot（密钥快照）即使仍然存在，也不能让 retired bucket（已退休调度桶）继续调度账号。

#### 账号 credentials（凭证）请求头覆写字段

`POST /api/v1/admin/accounts`、`PUT /api/v1/admin/accounts/:id` 和 `POST /api/v1/admin/accounts/bulk-update` 的 `credentials（凭证）` 可包含请求头覆写配置：

```json
{
  "credentials": {
    "header_override_enabled": true,
    "header_overrides": {
      "user-agent": "my-client/1.0",
      "anthropic-beta": "max-tokens-3-5-sonnet-2024-07-15"
    }
  }
}
```

字段说明：

- `header_override_enabled`：boolean（布尔值），是否启用账号级请求头覆写。
- `header_overrides`：object（对象），key（键）是 header（请求头）名称，value（值）是发送给上游的请求头值。
- 普通后台账号响应会保留已配置的 header 名称，但把每个 value 固定脱敏为空字符串，并通过 `credentials_status.has_header_overrides=true` 表示已有配置；只有 `lite=true&include_credentials=true` 的主站同步接口会按前述受限权限返回完整值。
- 编辑账号时，回传已有名称和空 value 表示保留该名称当前的服务端值；填写非空 value 表示覆盖；删除该名称表示删除对应配置；提交空 `header_overrides={}` 表示清空全部请求头覆写。这样浏览器无需持有真实值，也不会因全对象 PUT（整体更新）误删未修改的配置。
- `platform=openai/anthropic（OpenAI/Anthropic 平台）` 仅 API Key（接口密钥账号）生效；`platform=grok（Grok 平台）` 的 API Key 和 OAuth（授权账号）都生效；Gemini 等其他账号忽略该能力。
- 创建和编辑账号时，关闭该能力会删除或清空对应字段；批量编辑关闭时会显式写入 `header_override_enabled=false` 和空 `header_overrides={}`，避免 JSONB merge（JSONB 合并）保留旧配置。
- 空 value（值）不会发送到上游；空 name（名称）会被过滤或拒绝。
- 禁止覆写认证头、连接控制头和会话隔离头，包括但不限于 `authorization`、`x-api-key`、`x-goog-api-key`、`host`、`content-type`、`content-length`、`cookie`、`session_id`、`conversation_id`、`x-client-request-id`、`x-codex-turn-state`、`x-claude-code-session-id`、`x-grok-conv-id`。
- 单个账号最多保存 64 个覆写项；header name（请求头名称）最长 200 字符；header value（请求头值）最长 8192 字节。

安全说明：该功能只允许补充或调整上游客户端兼容性 header（请求头），不能用于替换账号认证信息，也不能覆盖网关链路追踪和会话隔离字段。

#### OpenAI OAuth Codex namespace 兼容开关

`POST /api/v1/admin/accounts`、`PUT /api/v1/admin/accounts/:id` 和 `POST /api/v1/admin/accounts/bulk-update` 的 `extra（额外配置）`支持：

```json
{
  "platform": "openai",
  "type": "oauth",
  "extra": {
    "openai_responses_flatten_namespaces": true
  }
}
```

- `openai_responses_flatten_namespaces` 只对 OpenAI OAuth（授权账号）生效，缺失或 `false` 表示普通 Responses 请求原样保留 Codex namespace（工具命名空间），默认关闭。
- 创建页面关闭时不写入该字段；单账号编辑关闭时删除该字段，避免 `extra` 堆积默认值。
- 批量编辑使用独立“是否修改”控制：未勾选时不提交字段并保留每个账号原值；勾选后可明确提交 `true` 或 `false`。批量接口使用 JSONB merge（JSONB 合并），因此明确关闭必须提交 `false` 才能覆盖旧值。
- Compact（上下文压缩）请求始终沿用既有摊平兼容逻辑，不受该开关影响。

#### OpenAI 账号厂商长上下文计费

`POST /api/v1/admin/accounts`、`PUT /api/v1/admin/accounts/:id` 和 `POST /api/v1/admin/accounts/bulk-update` 的 `extra（额外配置）` 支持：

```json
{
  "platform": "openai",
  "extra": {
    "openai_long_context_billing_enabled": true
  }
}
```

- `openai_long_context_billing_enabled` 必须是 boolean（布尔值），且只允许用于 `platform=openai（OpenAI 平台）` 的账号。
- 新建 OpenAI 账号默认写入 `false`；旧账号缺少字段、字段非法或账号不是 OpenAI 时，运行时都按关闭处理，避免升级后意外提高费用。
- 开启表示该上游账号会被厂商按长上下文价格收费，它是 provider cost（上游成本）事实，不等同于分组是否允许客户使用长上下文计费。
- 关闭后，即使请求 Token 超过模型阈值，也使用该模型标准价格；开启后只有真实超过阈值才应用长上下文价格。
- 编辑账号未提交该字段时保留原值；批量编辑只允许目标全部是 OpenAI 账号，否则返回 `OPENAI_LONG_CONTEXT_BILLING_PLATFORM_INVALID`。
- Codex session import（Codex 会话导入）、后台 OAuth 创建、数据导入和普通账号创建统一经过同一默认值校验；Spark 影子保留自身的该配置，不从母账号凭据中复制。

#### 账号级关键词策略

`POST /api/v1/admin/accounts`、`PUT /api/v1/admin/accounts/:id` 和 `POST /api/v1/admin/accounts/bulk-update` 的 `extra（额外配置）`支持：

```json
{
  "extra": {
    "keyword_policy_mode": "inherit"
  }
}
```

- `keyword_policy_mode` 只接受 `inherit/protect/allow（跟随分组/保护账号/允许关键词）`；缺失或空值按 `inherit`，显式非法值返回 HTTP `400`。
- `protect（保护账号）`表示强关键词命中后，本轮调度、retry（重试）和 failover（故障切换）都必须绕开该账号；`allow（允许关键词）`表示该账号可承接强关键词请求，但仍需满足模型、分组、额度、并发和健康门禁。
- `inherit（跟随分组）`在严格分组中不承接强关键词请求；成功切到 `allow_blocked_keyword_requests=true（允许关键词）`分组后，可按目标分组规则承接。
- `keyword_audit（只审计）`不触发账号切换或允许分组路由，仍使用原分组和原调度规则直接请求。
- 创建、编辑页面使用三态点击选择；批量编辑只有管理员主动勾选该字段时才覆盖所选账号，未勾选时保留现值。该字段不增加关键词扫描次数，也不新增请求热路径远程 I/O（输入输出）。

#### Anthropic API Key 上游认证方式

`POST /api/v1/admin/accounts`、`PUT /api/v1/admin/accounts/:id` 和 `POST /api/v1/admin/accounts/bulk-update` 的 `extra（额外配置）` 可为 Anthropic API Key（Anthropic 接口密钥）账号指定上游认证方式：

```json
{
  "platform": "anthropic",
  "type": "apikey",
  "extra": {
    "anthropic_apikey_auth_scheme": "authorization_bearer"
  }
}
```

字段说明：

- `anthropic_apikey_auth_scheme`：可选值为 `x_api_key` 和 `authorization_bearer`。
- 不传、传空或传非法值时，保持历史默认行为：上游请求使用 `x-api-key: <api_key>`。
- 传 `authorization_bearer` 时，gateway-lite（轻量网关）会在真实请求、`/v1/messages/count_tokens`、账号测试和上游模型同步请求中使用 `Authorization: Bearer <api_key>`。
- 该字段只对 `platform=anthropic（Anthropic 平台）` 且 `type=apikey（接口密钥账号）` 生效；OAuth（授权账号）、Bedrock、Vertex service account（Vertex 服务账号）、OpenAI、Gemini、Antigravity 不受影响。
- 该字段是专用认证策略字段，不属于 header override（请求头覆写）。`header_overrides（请求头覆写）` 仍禁止覆写 `authorization` 和 `x-api-key`。

### `POST /api/v1/admin/accounts/:id/test`

用途：测试指定上游账号是否可用。该接口只用于后台管理员主动测活，不应改变真实请求调度状态、账号分组绑定或客户 API Key（接口密钥）的可用范围。

请求体：

- `model_id`：测试使用的模型编号。普通文本、图片和视频测试必填；Grok 搜索、TTS（文字转语音）、STT（语音转文字）可为空，Realtime（实时语音）为空时使用默认语音模型。
- `prompt`：测试提示词。图片/视频使用生成描述，Grok 搜索使用查询内容，TTS 使用朗读文本。
- `mode`：OpenAI 账号可选 `default（默认测试）`、`compact（压缩上下文探测）`；Grok 账号可选 `text`、`image`、`video`、`search`、`tts`、`stt`、`realtime`。
- `image_data_url`：Grok 图片编辑或图生视频的可选 Base64 Data URL（数据地址）。仅接受图片，解码后最大 4 MB。
- `audio_data_url`：Grok STT 测试音频的 Base64 Data URL（数据地址），解码后最大 8 MB。

`compact（压缩上下文探测）` 用于检查 OpenAI `/responses/compact` 或同类压缩上下文能力是否可用，只影响本次后台测试请求，不作为调度健康状态的唯一依据，也不改变客户真实请求的路由策略。

Grok 账号测试补充规则：

- OAuth（授权账号）通过 Grok Token Provider（令牌提供器）读取或刷新访问令牌。
- API Key（接口密钥账号）直接使用该账号的 `api_key`，不要求配置 OAuth 服务。
- 两类账号均使用账号的 `base_url` 和模型映射。`text` 使用 Responses（响应）接口；`image/video/search/tts/stt/realtime` 分别使用真实媒体、搜索和 Voice（语音）接口。图片、音频和视频结果通过 SSE（服务器推送事件）返回 `image/audio/video` 事件供后台直接预览；视频任务最多轮询 60 秒。
- 后台测试只验证账号真实能力，不写客户用量、不扣余额，也不会把本次测试结果作为唯一的调度健康状态。
- 启用 `security.url_allowlist（网址白名单）` 时，默认允许官方 `api.x.ai` 与 `cli-chat-proxy.grok.com`；自定义 Grok 兼容上游仍需管理员显式加入白名单。

### Grok Custom Voices（自定义音色）

Grok 分组支持官方 `POST/GET/PATCH/DELETE /v1/custom-voices` 及 `GET /v1/custom-voices/:voice_id/audio`。创建接口使用 multipart/form-data（文件表单）；列表与资源返回结构保持 xAI 兼容。

安全与调度规则：

- 创建成功后，gateway-lite（轻量网关）会把 `voice_id（音色编号）` 与创建者 `user_id + API Key（接口密钥）+ account_id（上游账号）` 持久绑定。主站远程密钥只保存非敏感 `Key ID（密钥编号）`，不保存完整密钥。
- `GET/PATCH/DELETE` 和音频下载必须由原用户的同一 API Key 发起，并固定使用创建音色的原上游账号；不会重新随机调度，也不会因分组内账号并发或权重变化漂移。
- `GET /v1/custom-voices` 只列出当前 API Key 在本网关创建并登记的音色。即使多个客户共享同一个 xAI team（团队）账号，上游返回的其他音色也不会暴露。
- 无归属记录、归属不匹配或猜测其他客户的 `voice_id` 均返回 `404`。归属数据库不可用时返回 `503` 并停止访问上游，禁止 fail-open（失败放行）。
- 上游创建已经成功但本地归属保存失败时，接口返回明确 `503` 并记录管理员错误；不会向客户返回一个之后无法安全管理的成功结果。
- migration（数据库迁移）`215_grok_custom_voice_bindings.sql` 创建持久归属表和本地/远程 Key 查询索引。

### 模型价格统一管理

### 风控内容审计与关键词规则包

`/api/v1/admin/risk-control/config` 读取和保存 content moderation（内容审计）配置。关键词能力包含两层：

- 保存 `enabled=true（启用内容审计）` 时，后端会同步打开 `risk_control_enabled（风控中心总开关）`。请求热路径先检查这个总开关，再检查内容审计配置；这样可以避免后台显示“内容审计已开启”但实际被外层总闸跳过。
- `all_groups/group_ids（全部分组/指定分组）` 按 API Key（接口密钥）绑定的 `group_id（分组编号）` 生效。`all_groups=true` 时所有 Key 都会审计；`all_groups=false` 时只有 Key 绑定分组命中 `group_ids` 才会审计，未绑定分组的 Key 不会命中指定分组规则。当前不做每个分组一套独立审计规则。
- `blocked_keywords（自定义拦截关键词）`：管理员手动维护，命中后始终按 `keyword_block（关键词拦截）` 处理。
- `keyword_rule_packs（关键词规则包）`：内置或自定义的分类规则包，字段如下：

```json
{
  "id": "platform_abuse",
  "name": "平台滥用",
  "description": "拦截绕过套餐、薅羊毛、接码、批量注册和绕过验证码等请求。",
  "action": "keyword_block",
  "enabled": true,
  "keywords": ["绕过套餐限制", "批量注册账号"],
  "built_in": true
}
```

`action（动作）` 支持：

- `keyword_block`：强拦截，返回配置里的 `block_status（拦截状态码）` 和 `block_message（拦截提示）`。
- `keyword_audit`：只记录 `content_moderation_logs（内容审计日志）` 后立即按原分组放行；不会调用允许分组解析器、远程 moderation API（内容审计接口），也不累计封禁次数。

`keyword_blocking_mode（审查引擎）` 显式接受 `keyword_only`（本地关键词）或 `ai_only`（纯 AI）。关键词模式执行本节本地语种、关键词和规则包；纯 AI 跳过这些规则与关键词切组，按本文首部完整输入和三态合同执行。`PUT /api/v1/admin/risk-control/config` 新提交旧 `keyword_and_api/api_only` 返回 `INVALID_CONTENT_MODERATION_ENGINE`，不会保存成功后偷偷改为关键词；保存仍关闭旧 `pre_hash_check_enabled`。AI 客户资料外发必须显式 `external_data_consent=true`，默认关闭，不能继承关键词模式的本地声明。

#### `GET /api/v1/admin/risk-control/keyword-catalog/status`

返回公开关键词目录的 `catalog_version（目录版本）`、`sequence（防回滚序号）`、`pack_count（规则包数）`、`keyword_count（生效短语数）`、`source（embedded / disk / remote）`、`hash（生效规则摘要）`、最近检查/更新时间和最近错误。远程目录会更新相同稳定 ID 的官方规则包，也允许追加新的稳定 ID 规则包；管理员本地保存的启停、`keyword_block / keyword_audit（强拦截 / 只审计）`动作以及自定义规则包始终优先。

内置包的本地覆盖只保存管理员真正改过的 `enabled/action（启停/动作）`差异，不再把整套默认规则写回配置。这样远程目录以后调整默认动作时，未被管理员明确修改的规则会正常跟随更新；恢复为目录默认值会自动删除对应本地差异。

#### `POST /api/v1/admin/risk-control/keyword-catalog/sync`

管理员立即同步关键词目录。网关拒绝 HTTP redirect（网址重定向），并先校验同源 HTTPS（加密网址）、manifest（目录清单）、防回滚序号、大小、SHA-256（文件摘要）、版本、规则包和关键词上限，再在后台构建完整候选快照；成功后一次性替换，失败返回 `503 / KEYWORD_CATALOG_SYNC_FAILED` 并继续使用最近有效或内置规则。同步期间不会清空旧运行时匹配器，该操作不进入用户请求热路径。

内置和公开 stable（稳定版）目录的强拦截只保留“动作 + 对象/目的 + 可执行结果”的高置信完整短语。`跑分、炸药、后门、木马、远控、开盒、人肉、接码、ransomware` 等歧义裸词不再直接强拦截；命中多个规则时固定选择 `keyword_block（强拦截）` 优先，其次选择更长、更具体的证据短语。

请求热路径会把 `risk_control_enabled（风控总开关）`、`content_moderation_config（内容审计配置）` 和编译后的 keyword matcher（关键词匹配器）放入 runtime snapshot（运行时快照）。自定义关键词与规则包按旧优先级合并到 Aho-Corasick 自动机中匹配：自定义关键词优先，其次按规则包顺序、包内关键词顺序命中。后台保存 `/api/v1/admin/risk-control/config` 成功后会立即替换当前进程快照；非本接口写入的配置最多在缓存 TTL 后异步刷新，刷新失败时继续使用旧快照并短暂退避，避免每个请求都打设置表。

默认内置规则包覆盖账号密钥盗取、违法犯罪、网络攻击与恶意代码、未成年人和性剥削、自残具体方法、隐私泄露、人肉、平台滥用、安全研究审计、提示词攻击审计和高风险建议审计。安全研究审计只保留“逆向分析程序、漏洞复现研究、授权渗透测试”等带动作与对象的完整短语；低上下文工具名和缩写不再触发。`反沙箱`、`反调试` 及内置英文同义表达不再属于强拦截词；管理员自己保存的自定义关键词不受影响。内容审计总开关未开启时，这些规则不会生效。

#### 用户处置策略

`/api/v1/admin/risk-control/config` 同时支持保存 user policy（用户处置策略）字段：

- `deduct_balance_on_block_enabled`：强拦截扣余额开关，默认 `false`。
- `deduct_balance_on_block_amount`：每次强拦截扣减的用户余额金额，默认 `0`，小于 0 会归零。
- `permanent_ban_enabled`：永久封禁开关，默认 `false`。
- `permanent_ban_threshold`：每个用户累计强拦截触发上限，默认 `20`，最小为 `1`。

这些字段只作用于 hard block（强拦截）：

- 会计入并触发处置：`block`、`keyword_block`、`cyber_policy`。
- 不会计入也不会扣余额：`keyword_audit`、`allow`、`error`、`hash_block`。

`CountFlaggedByUserSince（窗口违规计数）` 使用同一处置白名单，只累计 `block`、`keyword_block` 和未配置排除的 `cyber_policy`；标记为 `cyber_log_only` 的记录（见下节白名单）**永不**计入封号计数。`language_block` 和 `hash_block` 即使日志为 `flagged=true`，也不会间接推高后续封号、扣款或永久封禁计数。

#### cyber 用户白名单（只记录不拦截）

`GET /api/v1/admin/settings` 与 `PUT /api/v1/admin/settings` 新增 `cyber_policy_user_allowlist`（字符串，默认空）：

- 内容：平台用户 ID 列表，逗号或空白分隔；**任一非法项整表拒绝**（绝不部分生效）。
- 语义（对齐官方 v0.2.13）：列表内用户命中 `cyber_policy` 时**只记录不拦截**——
  1. 已有的会话屏蔽键对其不生效：HTTP responses、chat completions 与 WebSocket 握手三条准入路径一致放行；
  2. 新的 `cyber_policy` 命中不参与自动封号判定，风控日志 `mode` 记为 `cyber_log_only`；
  3. 即使之后把用户移出白名单，这些 `cyber_log_only` 历史行也不会被追溯计入封号计数。
- 生效方式：与 `cyber_session_block_enabled` / TTL 在同一次进程内刷新中加载（约 60 秒），请求热路径零数据库往返。
- 降级：读取失败时沿用上一次的白名单并把缓存 TTL 缩短到 5 秒；配置非法时**不豁免任何用户**（fail-closed）。
- 后台入口：轻量网关 Win12 控制台「设置 → 功能开关 → 风控与会话阻断 → 只记录不拦截（用户 ID）」。

个人 Key 命中强拦截后，独立部署的网关会在 `content_moderation_user_penalties（内容审计用户处置状态）` 中累计该用户的强拦截次数和已扣余额。达到 `permanent_ban_threshold` 后，用户状态会被置为 `disabled（禁用）`，并记录为 permanent ban（永久封禁）。永久封禁用户不能通过风控日志的普通“解封”接口恢复，需要管理员按数据库/专项工具明确处理。管理员仅在使用个人 Key 时跳过余额扣减和永久封禁，避免审计测试或后台调试误伤管理账户；管理员使用企业 Key 时仍按企业付款主体执行处罚。

当 gateway-lite（轻量网关）已配置 control-plane（主站控制面）客户端时，强拦截处罚会异步上报到主站 `POST /internal/content-moderation/penalty`。请求字段包括：

- `request_id`：处罚幂等编号，同一编号只执行一次；相同编号携带不同处罚参数会被主站作为冲突拒绝。
- `user_id`：实际发起请求的员工或个人用户编号。
- `key_id`：主站 API Key（接口密钥）的稳定编号，主站据此反查权威付款主体；不能用可编辑的 Key 名称代替。
- `organization_id`、`billing_subject_type`、`billing_subject_id`：企业付款主体声明。网关字段只用于一致性校验，不能伪造或改写主站数据库中的 Key 归属。
- `deduct_balance_cents`、`permanent_ban_enabled`、`permanent_ban_threshold`：个人 Key 的扣款与封禁策略。
- `organization_suspension_enabled`、`organization_suspension_threshold`、`violation_window_hours`：企业 Key 的成员风控暂停阈值和滚动统计窗口。
- `action`、`category`、`reason`、`region`、`gateway_id`、`created_at_millis`：触发动作、类别和审计归因信息。

企业 Key 的处罚始终由主站扣企业钱包，并按“企业 + 实际员工”在 `violation_window_hours（违规窗口小时数）`内滚动累计次数；事件发生时间随 durable outbox（可靠投递箱）保留，延迟重投不会让已经过期的违规重新进入当前窗口。达到阈值时只暂停该员工在当前企业的 API 权限。网关不会扣员工个人余额，不会封禁个人账号，不会撤销个人登录，也不会影响该员工的个人 Key 或其他企业。企业 owner/admin（所有者/管理员）可显式解除该成员的风控暂停。网关本地个人扣款、个人处罚累计和个人封禁逻辑不会作为企业处罚的 fallback（兜底），避免主站暂时不可用时误伤员工个人账户。

处罚上报使用 PostgreSQL durable outbox（可靠投递箱）：主站首次调用成功时热路径不写 outbox；网络断开、超时、`408/425/429` 或 `5xx` 等瞬时故障时，以同一 `request_id` 持久化为 `pending（待重试）`，后台线程按退避间隔重试，多个网关进程使用 `FOR UPDATE SKIP LOCKED` 安全领取且不会重复执行。`400/401/403/404/409`、主站返回 `ok=false`、事件 JSON 无法解码等永久业务错误会保存为 `dead（死信）` 并停止自动重试，保留错误信息供管理员排查；不会无限请求主站。服务关闭时会先停止处罚重试线程，再关闭数据库连接。migration（数据库迁移）`217_content_moderation_penalty_outbox.sql` 创建该可靠投递表、状态约束和仅针对待重试记录的索引。

当且仅当网关为真正独立部署、完全没有主站控制面客户端时，个人 Key 才继续使用本地个人处罚逻辑；企业 Key 不允许降级为本地个人扣款或封禁。

模型价格由两层组成：

- default catalog（默认价格目录）：轻量网关启动时从远程/本地 LiteLLM 价格目录加载，新服务器随二进制自带 MiMo（小米）文本模型价格。
- override（覆盖价）：管理员在轻量网关后台“模型价格”页面保存的本地价格和 `disabled（模型禁用）` 状态，持久化到 `data/model_pricing_overrides.json`，优先生效；删除覆盖价后回退默认目录或内置兜底价，并清除本地禁用状态。

#### `GET /api/v1/admin/model-prices`

用途：分页查询当前生效的 model prices（模型价格）。

查询参数：

- `page`、`page_size`：分页。
- `search`：按模型名或 provider（供应商）搜索。
- `provider`：按 provider 过滤，例如 `xiaomi`、`openai`、`anthropic`、`google`、`custom`。
- `source`：按来源过滤，支持 `catalog`（默认目录）、`override`（覆盖价）、`static_fallback`（内置兜底）。

返回字段中的价格单位为 USD / 1M tokens（美元/百万 tokens），图片价格为 USD / image（美元/张），视频价格为 USD / generated second（美元/生成秒数）。文本模型可分别返回标准、Priority（优先处理）、超过 272K 的标准长上下文及“长上下文 + Priority”输入、输出、缓存读写价格。`supports_service_tier=true` 表示模型支持 Priority，`long_context_input_token_threshold>0` 表示存在长上下文计费；请求命中的价格矩阵不完整时，网关不会猜价：Key 偏好会退回标准模式，全局强制 Priority 会返回明确错误。

视频模型返回 `billing_mode=video（按视频计费）`，并按模型能力返回 `video_price_per_second_480p`、`video_price_per_second_720p`、`video_price_per_second_1080p`。不适用的分辨率保持为 `0` 或省略，不混入 token（按 Token）、图片或按次价格。runtime health report（运行时健康上报）和 config push（配置主动推送）使用同一组字段同步到主站，保证主站价格页使用轻量网关当前生效价格，而不是自行猜测 xAI 价格。

返回字段包含 `disabled（是否禁用）`。禁用模型不会出现在 `/v1/models`，真实请求该模型会返回 `403 model_disabled（模型已禁用）`，不会进入并发、内容审计、额度扣减和上游调度。

#### `PUT /api/v1/admin/model-prices`

用途：新增或更新某个模型的 override（覆盖价）。保存后网关热路径、账号统计价格和健康上报价格都会优先使用覆盖价。

账号统计成本说明：账号统计、成本投影和模型价格展示共用同一套 BillingService（计费服务）。当 GPT-5.6 等模型命中 `long_context_input_token_threshold（长上下文输入阈值）` 时，账号统计不再用普通输入、输出、缓存读取和缓存写入单价直接相乘，而是调用统一计费器计算标准长上下文或 Priority（优先处理）长上下文价格，确保账号统计、请求日志、主站价格和真实扣费口径一致。

请求体示例：

```json
{
  "model": "mimo-v2.5-pro",
  "provider": "xiaomi",
  "mode": "chat",
  "input_cost_per_1m_tokens": 0.435,
  "output_cost_per_1m_tokens": 0.87,
  "cache_read_cost_per_1m_tokens": 0.0036,
  "cache_creation_cost_per_1m_tokens": 0,
  "supports_prompt_caching": true,
  "supports_service_tier": false,
  "long_context_input_token_threshold": 0,
  "disabled": false
}
```

说明：`disabled=true（禁用）` 可与价格字段一起保存。仅禁用模型时，价格可保持当前值；恢复启用时保存 `disabled=false`。视频模型可保存 `billing_mode=video` 和 `video_price_per_second_480p/720p/1080p（各分辨率每秒价格）`；删除 override 会同时移除本地禁用状态。

#### `DELETE /api/v1/admin/model-prices?model=mimo-v2.5-pro`

用途：删除某个模型的 override（覆盖价），不会删除默认目录价格；删除后返回当前回退后的生效价格。

说明：control-plane（主站控制面）“网关模型定价”页面会通过 `X-Gateway-Lite-Admin-Key（轻量网关管理员 Key）` 代理调用上述 `GET/PUT/DELETE` 接口。gateway-lite（轻量网关）仍是模型价格的唯一写入点，主站不保存第二份价格规则。

### `PUT /api/v1/user/password`

用途：已登录的 gateway-lite（轻量网关）管理员修改当前账号密码。接口需要后台 JWT（登录令牌），会校验旧密码；修改成功后提升 `token_version（令牌版本）`，使旧登录令牌全部失效。

请求体：

```json
{
  "old_password": "old-password",
  "new_password": "new-password"
}
```

边界：新密码至少 6 个字符。前端修改成功后应清理当前登录态，并返回当前节点配置的私有登录入口。

### Gateway-local API Key（轻量网关本地接口密钥）

以下接口只允许已登录管理员访问：

- `GET /api/v1/admin/gateway-lite/local-api-keys`：列出当前管理员创建的本地 Key，只返回脱敏值。
- `POST /api/v1/admin/gateway-lite/local-api-keys`：创建本地 Key；完整 `secret（密钥原文）` 在创建成功响应中返回。
- `POST /api/v1/admin/gateway-lite/local-api-keys/:id/reveal`：管理员显式再次读取自己创建的完整本地 Key。只接受 JWT（后台浏览器登录态），管理员机器密钥返回 `403`；越权、普通 Key、不存在或已删除 Key 统一按未找到处理。成功响应为 `{"secret":"glk_..."}`，并包含 `Cache-Control: no-store`、`Pragma: no-cache`。
- `PUT /api/v1/admin/gateway-lite/local-api-keys/:id`：修改名称、绑定分组、启停状态及 IP（网络地址）白名单/黑名单。
- `DELETE /api/v1/admin/gateway-lite/local-api-keys/:id`：撤销并删除本地 Key。
- 本地 Key 的列表、创建和更新响应中，`ip_whitelist` 与 `ip_blacklist` 无论数据库字段为空、缺失还是没有规则，都固定返回 JSON 空数组 `[]`，禁止返回 `null`。前端同时兼容升级前节点的 `null` 响应，避免列表、编辑或状态切换时整页崩溃。
- 更新接口对 `ip_whitelist` 和 `ip_blacklist` 使用三态语义：字段缺失表示保留原规则，显式空数组 `[]` 表示清空，非空数组表示完整替换。只修改名称、状态或分组时不会误清空现有 IP 规则。
- reveal（查看原文）属于 critical operation audit（关键操作审计），只记录管理员、路由、Key ID 和结果；密钥原文不得进入列表对象、URL、请求体、浏览器持久化存储或审计摘要。

创建请求示例：

```json
{
  "name": "本机自动化",
  "group_id": 2,
  "ip_whitelist": ["127.0.0.1", "10.0.0.0/8"],
  "ip_blacklist": []
}
```

创建响应数据示例：

```json
{
  "id": 12,
  "name": "本机自动化",
  "key_masked": "glk_123456...cdef",
  "secret": "glk_1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
  "group_id": 2,
  "group_name": "默认分组",
  "platform": "openai",
  "status": "active",
  "ip_whitelist": ["127.0.0.1", "10.0.0.0/8"],
  "ip_blacklist": []
}
```

鉴权与计费边界：

- 本地 Key 固定使用 `glk_` 前缀，并且必须绑定当前节点一个存在且启用的本地 group（分组）。
- `glk_` 请求只查询当前轻量网关的数据库和 Redis（缓存服务），不会调用主站 `key resolve（密钥解析）`、lease（额度租约）、rebalance（额度再平衡）或 usage report（用量回传）接口。
- 本地 Key 不扣主站套餐、用户余额、API Key 额度或周期额度；仍执行本地分组状态、账号调度、模型能力、内容审计、IP ACL（网络地址访问控制）和故障切换。
- 请求成功后仍写入当前轻量网关的 usage log（请求日志），更新上游账号最近使用时间，并在上游账号配置了本地额度上限时累计该账号消耗。
- 停用、修改绑定分组或删除后会立即清理鉴权缓存，下一次请求直接使用新状态。
- 非 `glk_` Key 继续走原有主站鉴权、套餐、额度租约和用量回传链路，不会在主站异常时自动降级为本地 Key。

### IPv6 出口自动管理（热更新配置）

以下接口只允许已登录管理员访问，用于管理本机 IPv6 出口自动管理器：
自动识别网卡全局 IPv6 → 以各地址为源对配置目标做真实 TLS 握手测通 →
测通地址注册为本地 HTTP CONNECT 代理（单端口复用，代理 URL 的 username
携带 IPv6 地址）→ 写入 proxy 表供账号代理池使用。

- `GET /api/v1/admin/ipv6-egress`：返回当前生效配置、运行状态与地址测通列表
  （`config` + `enabled` + `running` + `last_scan` + `entries[]` + `ok_count`）。
- `PUT /api/v1/admin/ipv6-egress`：热更新配置，无需重启服务。请求体各字段均可选，
  仅提交变更项；保存即持久化到网关 `data/ipv6_egress.json`（优先级高于
  config.yaml 的 `gateway.ipv6_egress` 段）并按新配置重启监听与扫描循环。
  字段：`enabled`、`listen_host`、`listen_port`、`probe_target`、
  `sync_interval_seconds`、`name_prefix`、`max_addresses`。
- `POST /api/v1/admin/ipv6-egress/rescan`：立即执行一次"识别 → 测通 → 注册"
  全流程（同步执行）；未启用时返回 400。响应同 GET。

配置校验边界：`listen_port` 须在 0-65535；`probe_target` 非空时必须为
`host:port` 形态；`sync_interval_seconds` 与 `max_addresses` 不能为负。

账号代理池（多出口）配合说明：账号编辑接口（`PUT /api/v1/admin/accounts/:id`）
的 `extra.proxy_pool` 接受 `[{id,name,concurrency}]` 数组（前端只提交 id 与名称，
后端保存时按 id 对账补全完整凭据 URL 并剔除已删除代理；`concurrency` 为该代理
独立并发上限，1-1000000 合法，缺省跟随账号并发）；`extra.proxy_main_concurrency`
可覆盖主代理并发上限（缺省同样跟随账号并发）。调度层按"熔断（最近连续 5 次
失败）与满载端点避开 + 最小在途"在主代理与池内代理间选择出口，Claude/Gemini
等平台热路径与 OpenAI 共用同一语义；账号列表与详情响应的 `proxy_stats` 返回
每个出口的实时在途数、独立并发上限与最近请求成败状态。

### 伪造流式首字（总开关，热生效）

- `GET /api/v1/admin/ops/runtime/fake-first-token`：返回当前配置
  `{"enabled":bool,"min_seconds":float,"max_seconds":float}`。
- `PUT /api/v1/admin/ops/runtime/fake-first-token`：更新配置，保存即热生效
  （进程内缓存立即刷新），无需重启。开启后，流式请求（Anthropic 与 OpenAI
  两条链路）在等待上游首字节期间按 `[min, max]` 秒随机延迟先向客户端发送一条
  SSE 注释行（`: fake-first-token`，对 EventSource 与各 SDK 无害），客户端在
  上游真实返回前即收到字节（伪装 TTFT 节奏）。OpenAI 链路在 handler 层真正
  请求上游之前武装定时器：上游响应头经常要十几秒才返回，等进入流式写入函数
  再排程就晚了。载荷按入站协议区分：chat completions 入站发一条真实的
  role 空首帧（`data:` 行，无正文、客户端无感）——对接网关（new-api /
  sub2api 等）的「首字」统计只认 `data:` 行，注释行骗不过它们的报表；
  `/v1/responses` 与 Anthropic 入站保持 SSE 注释行（responses 协议事件
  顺序严格，伪造协议帧会导致严格客户端报错）。中转链路（网关串网关）下若
  上级网关先发了伪造首字载荷（注释行），本节点以其实际到达时刻记账，不再
  重复发送。用量接口
  （`GET /api/v1/admin/usage` 与用户侧用量列表/详情）
  的每行记录新增 `fake_first_token_ms`（伪造首字发出时刻，未触发为 `null`），
  与 `first_token_ms`（上游真实首字）并列展示。
  范围约束：0.01-30 秒，min>max 自动交换，非法值回退默认（0.3-1.2）。

### 账号出口时区改写（credentials.`upstream_timezone`）

用途：消除「客户端本地时区 + 网关出口 IP 地理位置」不一致的降智风控矛盾。
Codex 等客户端会在请求体里注入 `<environment_context><timezone>Asia/Shanghai</timezone>...`
环境声明；账号配置出口时区后，网关在转发 OpenAI 请求（Responses 主链路 /
passthrough / raw chat completions）前把 body 中所有 `<timezone>` 标签统一
改写为配置值，使环境声明与出口 IP 地理对齐。

- 配置位置：账号 `credentials.upstream_timezone`（IANA 时区名，如
  `America/Los_Angeles`）；账号编辑接口的 `credentials` 字段透传保存。
- 未配置（空）时**原样透传**，行为与历史版本一致；
- 非法时区名（`time.LoadLocation` 校验失败）时原样透传，绝不改坏请求体；
- 只替换 `<timezone>` 标签，正文与其他环境字段（cwd / current_date 等）不动；
- 账号编辑弹窗暂未提供输入框，可通过管理接口修改账号 `credentials` 生效。

### 主站绑定与安全改绑（2026-10-14 源码合同）

管理员登录后，首次主站对接按“主站 URL（网址）→ 主站 `ge_` 对接密钥 → 节点资料”三步操作。该流程只处理网关与主站身份，不替代首次管理员私有配置、登录及权限校验。实际管理路由统一为 `/api/v1/admin/gateway-binding`，复用轻量网关管理鉴权；绑定处理器响应带 `Cache-Control: no-store`。管理包装成功使用 `{code:0,message:"success",data:...}`，异步受理见下表。

| 方法与路径 | 请求及实际响应 |
|---|---|
| `GET /status` | 无正文；HTTP 200，`data` 为 `BindingStatus`（绑定状态） |
| `POST /probe` | `{url}`；查询主站公开绑定能力，核协议为 `tokenav-binding-v1`；HTTP 200，`data={verified:true}`，只证明地址/能力可用 |
| `POST /verify` | `{url,token}`；向该主站验证对接密钥；HTTP 200，`data={verified:true}`，尚不登记或绑定节点 |
| `POST /register` | `{url,token,name,public_url,region}`；节点登记成功后持久保存待启用身份，再受理有序重启；HTTP **202**，`data` 为 `phase=activating` 的绑定状态，消息为“节点已登记，正在重启并验证实际连接。”。请求内不热切换运行客户端，不以 202 表示已 `bound` |
| `POST /disconnect-check` | 无必需正文；只读检查原主站未完成结算，有待确认操作时先查询其远端提交结果。HTTP 200，`data` 为 `BindingDrain`（排空检查）；检查故障返回 503，仍附检查结果。检查本身不更改绑定状态，`ready=true` 不能绕过正式断开的收尾与持久提交 |
| `POST /disconnect` | 必须 `{confirm:true}`；先持久排空，再核结算与旧主站确认；已有断开操作则先恢复原操作结果。成功 HTTP 200，`data` 为 `phase=unbound` 的状态；失败 409 或 503，`data` 为排空结果，保留原目的地和账务，按 `disconnect_pending/can_cancel` 判断恢复操作 |
| `POST /cancel` | 无必需正文；只允许有原绑定的 `draining/blocked` 且尚未冻结断开操作时取消排空。HTTP 200 返回原绑定状态；当代握手或快照尚未完成则恢复为 `activating`，均已有证据才为 `bound`。远端已脱离或 `disconnect_pending=true` 时拒绝，不取消首次待登记/待启用身份 |
| `POST /retry` | 无必需正文；仅 `phase=activating` 且重启可用时请求再次重启，HTTP 202，`data` 为当前状态；否则 409 `binding_restart_unavailable` |

表中 `/register` 三步输入采用实际 `BindingInput` 字段：

| 字段 | 类型与约束 |
|---|---|
| `url` | 字符串；主站绝对 HTTP/HTTPS 地址，支持基础路径，去首尾空白和尾斜杠，拒绝账号密码段、查询参数及片段 |
| `token` | 字符串；主站提供的 `ge_` 对接密钥，验证/登记使用；节点运行不复用该密钥，网关持久记录不保存该对接密钥 |
| `name` | 字符串；去空白非空，原字符串长度不超过 100 字节 |
| `public_url` | 字符串；管理员明确填写、客户可访问的绝对 HTTP/HTTPS（普通/加密连接）基础地址，支持本机、内网及端口。绑定向导原值提交，后台登记在创建 `Pending`（待绑定目的地）前执行共享配置校验，超长、账号信息、查询/空查询、片段或非法端口均返回 HTTP 400 `message=binding_public_url_invalid`，不会保存不可初始化的公开地址。登记成功后作为主站节点访问地址；`ApplyStartup`（应用启动身份）同时将其用于 `MediaProxyPublicURL`（媒体返回地址基址），媒体构造器沿用统一配置校验，无需额外开关、不自动改协议。媒体启用后签发链接形如 `{public_url}/v1/media/:token`，上游媒体抓取仍独立强制 HTTPS 并执行地址安全检查；不是仅供页面展示的名称字段 |
| `region` | 字符串；去空白非空，原字符串长度不超过 64 字节 |

`binding_id/gateway_code/node_scope/redis_prefix/admin_sync_key` 和 `gb_` 节点运行凭据由网关生成并在首次登记调用前持久化；管理员不在请求中选填这些身份。登记响应丢失或失败后，重试复用同一冻结身份，`url/name/public_url/region` 必须与已持久的待登记目的地一致，否则 409 `binding_registration_pending_retry_same_destination`；已有当前绑定、排空或阻塞状态拒绝直接登记新主站。首次登记已经进入 `activating` 时再次提交登记返回 400 `binding_activation_pending`，应轮询状态或调用 `/retry`；已有 `Current` 身份的登记冲突仍为 409 `binding_state_conflict`。主站核完整登记输入的摘要；同一 `binding_id` 只接受相同输入幂等重放，不允许覆盖节点、凭据或管理同步密钥。

`BindingStatus` 的实际 JSON（结构化响应）字段为：

| 字段 | 类型与语义 |
|---|---|
| `phase` | 字符串；`unbound/registering/activating/bound/draining/blocked`（未绑定/正在登记/待重启启用/已绑定/正在排空/排空受阻） |
| `binding_id/url/name/public_url/region/gateway_code` | 字符串；存在待启用 `Pending`（待绑定目的地）时显示待启用资料，否则显示 `Current`（当前目的地）；无目的地时为空 |
| `node_id` | 整数；已取得的主站节点编号，无目的地或尚未完成登记时为 0 |
| `first_binding_required` | 布尔；当前持久 `Current` 不存在即为 true，待登记/待启用阶段仍为 true，不能仅靠它判断登记失败 |
| `restart_available` | 布尔；当前运行进程是否具备绑定受控重启能力 |
| `can_cancel` | 布尔；当前为 `draining/blocked`，且没有远端已脱离标记或持久断开操作时为 true；前端依此展示可直接取消的状态，不能只看 `phase`。取消后的 `bound/activating` 仍取决于当代握手与快照证据 |
| `disconnect_pending` | 布尔；已有持久断开操作正在恢复确认，需继续按原操作重试/核实；此阶段禁止取消恢复原连接，不等于已经脱离 |
| `last_error` | 可选字符串；当前步骤的安全失败反馈 |
| `handshake_at/snapshot_at` | 可选整数；当前运行实例握手和完整快照成功后的 Unix 毫秒时间；正常 `bound` 重启先清为 0 并省略，重新完成验证后才返回 |

状态不返回 `token`、`credential/admin_sync_key` 等秘密或整个 `Current/Pending` 持久对象。数据库保存独立修订号、当前与待绑定目的地；每次状态变更通过 CAS（按原修订比较并提交）更新，冲突拒绝，提交失败不更改内存中的当前状态。启动先加载数据库的绑定身份，再构建主站客户端及后台工作任务；已脱离的身份不会被旧 `config.yaml` 或环境变量重新恢复。地址、节点凭据、网关编号、区域、缓存前缀及管理同步密钥固定在对应运行实例内，旧实例先停止接收 HTTP 请求、完成业务清理并等待后台任务退出后才重建；关停或等待失败时不并行启动新目的地，不把旧用量、租约、缓存或恢复事件改投新主站。

登记 202 只是持久保存后的异步受理，当前处理器不执行热重接。新运行实例的激活必须针对其启动时冻结的同一 `binding_id`，真实完成 `handshake`（节点握手）及 `SyncFull`（完整配置快照同步），再复核持久身份仍匹配且状态仍为 `activating`，最后提交才把 `Pending` 转为 `Current`、清空待启用对象并置 `bound`。旧实例或旧身份的迟到握手/同步不能把另一个待绑定目的地标为成功。失败保留 `activating` 和安全错误，后台继续尝试；前端应轮询 `/status` 并核同一 `binding_id`，不能因收到 202、连接瞬断、仅验证密钥或仅登记节点就显示已绑定。

正常 `bound` 节点重新启动也先持久回到 `activating`，保留原 `Current` 身份但清空 `handshake_at/snapshot_at`，当前实例重新完成握手及完整快照后才再次 `bound`；持久的上次成功不是本次已连接的证据。此时 `first_binding_required=false`，应显示原节点重新验证状态，不要求再登记节点。新节点在激活前不接受新的付费请求；导入旧配置的 `Legacy`（旧身份）保留既有激活期准入兼容，但仍不能宣称未完成的握手/快照已经成功。

`BindingDrain` 返回 `{counts,unknown,reachable,ready}`：`counts` 是字符串到整数的计数映射，失败且尚无计数时可为 `null`；`unknown` 为无法确认项的字符串数组，无项时可为 `null`；`reachable` 为原主站核验/结算查询是否可达，`ready` 为本次检查是否取得安全脱离或原操作已脱离的证据。仍绑定且没有已脱离回执时，必须本地检查无错误、原主站可达、没有未知项且全部计数为 0；未绑定时返回可直接继续的空计数，不表示已经检查过一个远程主站。已持久断开操作时先查询原操作，查询确认已脱离可返回 `ready=true/reachable=true/counts={}`，仅表示可继续本地收尾，不自动更新 `/status`。当前计数可含 `inflight/sql_retry/oauth_outbox/penalty_outbox/image_tasks/grok_video/batch_image/seedance_registry/redis_stream/redis_dead/redis_pending/reserved_requests/reserved_leases/pending_funding`，主站未结清计数加 `control_plane_` 前缀；这些值既有事件/任务数量，也有未清登记数量，不是统一货币余额。

正式断开先持久进入 `draining`，保持原目的地供在途结算，再复核本地、原主站及所有资金登记；未清、未知或查询失败均不能换站，没有 `force`（强制切换）参数。失败保留原凭据、原缓存命名空间、账务与队列，处于 `blocked` 或尚未完成远端确认的 `draining`，新登记仍拒绝；原主站不可达时保留排空状态，未启动远端断开操作且 `can_cancel=true` 可取消恢复原绑定，普通网络故障不会自动解绑。

远端断开前先持久冻结非空 `operation_id`（断开操作编号），以内部请求 `{binding_id,gateway_code,node_scope,operation_id}` 向原主站提交；回复丢失或本地提交失败后保留同一操作，`disconnect_pending=true`。重新 `/disconnect`、后台恢复任务及 `/disconnect-check` 通过原主站内部 `/internal/gateway-binding/disconnect-status`（断开结果查询；导入旧身份用 `legacy-disconnect-status`）取得 `{ok,detached,operation_id,binding_id}`，必须核返回操作与绑定身份一致，`detached=true` 才能完成本地收尾。未脱离仍按原操作及原结算证据继续，不新造操作、不跨站重投；查询失败或身份不符保留阻断。主站断开事务锁定实际节点并核冻结节点、网关编码、`binding_id/node_scope` 与原操作，将租约释放、节点下线及原操作回执一起持久化；新绑定同时停用节点凭据，导入旧身份则保存独立的旧身份断开回执。相同冻结身份和操作幂等重放，不同操作不能覆盖首份回执；错误操作查询返回 `detached=false`，已完成后用不同操作重放返回主站 HTTP 409 `error=binding_disconnect_conflict`，原操作重放为 HTTP 200。`disconnect` 与 `disconnect-status` 两类内部请求均拒绝空白操作编号（HTTP 400 `error=binding_operation_required`），旧身份还要求非空绑定编号（HTTP 400 `error=binding_identity_required`）；`detached=true` 必须来自这份精确持久回执，节点被管理员设为 `maintenance`（维护中）不算断开成功。已停用凭据不再允许普通运行调用，但原节点仍可按专用 `disconnect-status` 取回原操作的脱离证据。网关最后的清空身份也须提交成功才成为 `unbound`。

内部断开及断开状态查询的身份错误按主站实际路径区分：旧身份按网关编码查不到节点为 HTTP 409 `error=binding_scope_mismatch`；已找到节点但 `node_scope` 不符为 HTTP 403，同码。`gb_` 已认证节点的 `binding_id/gateway_code/node_scope` 任一不符为 HTTP 403，同码。以上是主站内部响应，网关管理接口不会原样沿用其 HTTP 状态：通信层保留安全错误标识，管理验证按原 `message` 映射，正式断开失败仍返回管理合同的 409/503 并保留账务，不可据内部错误误判已脱离。

已持久冻结断开操作后，`can_cancel=false`，即使某次查询返回尚未脱离也**禁止取消恢复**，因为超时的原调用仍可能随后提交。`/cancel` 返回 HTTP 400、`message=binding_disconnect_pending_cannot_cancel`；已经取得远端脱离标记则为 HTTP 400、`message=binding_remote_detached_cannot_cancel`。此阶段只能恢复原操作的确认；不能仅根据错误提示、连接恢复或前端关闭弹窗重新接受付费请求。操作编号和远端已脱离标记为内部持久事实，不在公开状态响应中暴露。全部确认后才清本地当前身份、置 `unbound` 并请求重启，不删除网关账号、上游配置或请求历史。

Seedance（视频任务）的新派发在鉴权、请求校验与资金准入之后、真实上游发送之前按原绑定与请求编号持久登记，未完成结算计入 `seedance_registry`；登记失败停止派发，结果未知保留未清状态。明确未被上游接受可释放，成功结算后才确认已清。未进入实际派发的 400/401 不生成媒体未清账。旧节点缺少完整历史媒体证据时，`unknown` 提示核对原供应商完整任务目录、最终结算与主站资金；没有管理员勾选或强制按钮能代替证据，缓存到期同样不能当作结清。记录读取/表缺失或状态未知仍阻止改绑，不把存储故障解释成零条。

排空期间新增费用准入使用绑定门禁：实际分类器覆盖标准 `/v1`、无版本、`/backend-api/codex`、`/antigravity/v1` 与 `/antigravity/v1beta` 的对应模型写请求、Gemini（原生生成）动作，以及四前缀 Seedance 创建；普通读请求、下载、任务查询/取消和 DELETE（删除/取消）保留用于旧工作收尾。WebSocket（长连接）握手也先核绑定，已建立连接计入 `inflight` 直到结束；Responses（响应接口）每个新轮在资金准入及真实发送前再次读取该连接原绑定管理器，排空或阻塞时以 `1013`（稍后重试）关闭，不以握手旧状态新增费用。已准入轮次继续按原资金快照结算，门禁不改写或释放原在途账务。本节按实际接点描述，不将静态覆盖声明为完整资金恢复、界面或部署已验收。

绑定管理错误沿用 `{code,message,data?}` 管理包装，成功 `code=0`，错误 `code` 为数字 HTTP 状态码；下面的字符串标识位于 `message`，不是新增的字符串 `code`。管理器不可用为 HTTP 503 `binding_state_unavailable`；非法 JSON 为 400 `binding_request_invalid`，未确认断开为 400 `binding_confirmation_required`；地址、能力及节点资料错误为 400，首次登记待激活且尚无 `Current` 时重复登记为 400 `binding_activation_pending`；`binding_node_conflict/binding_state_conflict/binding_registration_pending_retry_same_destination` 为 409；`binding_control_plane_unreachable/binding_restart_unavailable/binding_response_invalid/binding_control_plane_rejected` 为 503。主站对接密钥验证的 HTTP 401 `error=unauthorized` 在网关 `/verify` 返回 HTTP **400**、`message=unauthorized`，避免把主站对接失败当作管理员会话失效。取消已派发断开或已确认脱离分别返回 400 `binding_disconnect_pending_cannot_cancel/binding_remote_detached_cannot_cancel`。断开未清或状态冲突返回 409，其他断开检查/通信错误返回 503，并附检查结果；`disconnect_pending=true` 时错误提示明确要求重试确认、禁止取消恢复，不能把失败包装解释为已脱离。

对接密钥和运行密钥分离：主站管理员通过 `POST /admin/api/settings/control-plane-token/reveal` 可再次读取/复制 `ge_` 对接密钥，实际要求 `settings:write`（系统设置写权限）、同源校验、读取审计及禁止缓存；秘密不进入日志或文档。`ge_` 只用于主站 `/internal/gateway-binding/verify`、`register` 的新节点对接，不授予运行或管理权限。新生成的 `gb_` 关联主站持久绑定记录，用于本节点 `handshake/disconnect/disconnect-status` 及明确开放的内部运行接口，不取得管理接口或另一节点登记权限。网关 `/probe` 不带凭据，携凭据的主站绑定调用均拒绝重定向；主站密钥读取完整合同仍以主站自己的 API（接口）文档为准。

节点运行权限已接入实际 `authorizeBindingRuntime`（节点运行权限守卫），以凭据散列反查持久节点身份，不以自报 `gateway_code/region` 代替资源归属。请求只可使用处理器真正消费的目的地字段：通常为 `gateway_code`，用量/处罚为 `gateway_id`；额外补写处理器忽略的 `node_scope`、另一目的地字段，或仅使用其他节点同一区域都不能扩权。重复 JSON 成员、大小写变体身份字段、冲突节点声明及无效形状拒绝；新鉴权、租约和资金选择还核主站节点活动状态。归属/范围不满足在主站运行入口返回 HTTP 403 `error=binding_scope_mismatch`，后续业务校验仍按各接口自己的合同执行。

| 内部操作 | 主站实际权威归属核验 |
|---|---|
| `key/resolve`（密钥核验） | 读取持久 Key 的默认节点、网关编码与分组，必须属于当前节点；不会因自报本节点编码读取其他节点的 Key |
| `quota/acquire-lease/refill-lease/rebalance-lease`（申请/补充/重分配租约） | 新分配读取本节点 Key，核真实用户、企业及付款主体声明；带 `observed_lease_id` 时还核持久租约的节点专属 `node_scope` 与原主体。无 Key 的用量后补租只能继续本节点已有租约，不能选择新用户 |
| `billing/authorize`（资金选择） | 新请求核本节点 Key、实际 `entry_gateway_node_id/entry_group_id` 与付款主体；已存在请求读取原资金记录，按原节点/身份恢复，不重新选择当前 Key 的新归属 |
| 资金扩容、释放及 `GET /internal/billing/authorizations/:id` | 读取原资金授权的节点、网关与请求编号；变更须匹配原 `request_id`，读取不允许借其他节点的授权编号越权 |
| `usage/report/report-batch`（用量上报） | 已有用量修正以原持久用量为准；新事件依原资金记录或节点租约核身份，真实用户、Key、企业、付款主体及关联资金/租约不能串换。无占用的派发前零费用失败只能用当前分配给本节点的 Key 记录；批次逐项核验，不因一项合法放过另一节点事件 |
| `content-moderation/penalty`（审查处罚） | 优先读取已有处罚、用量或资金原归属；没有旧记录才读取本节点 Key，均核实际用户/付款主体，不凭节点凭据任意扣另一用户费用 |
| `oauth/invocations/*`（受限委托调用） | 解析核委托固定节点；预留核原委托/预留的节点、Key 和入口分组；绑定、扩容、结算、释放与状态查询核原请求/预留，关联资金还须属于同节点、同请求、同 Key 与同入口 |

这些检查保留原账务恢复资格：Key 后续撤销或改绑不把原租约、资金和已经持久的用量改投另一节点，也不赋予再次业务派发权限。新租约准入将已认证节点身份带入真实付款主体事务，在取得付款主体和 Key 锁之后再次核节点、绑定活动状态、Key 默认节点与原租约主体；等锁期间发生改绑不能使用此前中间件读到的旧归属新占额度。原资金/用量历史结算不套用这项新消费守卫，继续按原持久身份收尾。节点自身凭据有效不是用户权限或余额授权，原处理器还会继续执行应用、模型、额度和资金状态校验。

配置快照使用专用 `GatewayBindingConfigSnapshot`（绑定节点快照），只返回本节点与对应分组、账号镜像；没有镜像时返回空集合，不回退主站共享账号或其他节点。返回前清除账号 `credentials`（凭据），节点字段采用运行白名单，嵌套配置/元数据清除秘密、管理同步密钥等敏感项，平台跨节点规则不进入该快照；不能读出本节点或其他节点的 `AdminSyncKey`（管理同步密钥）。健康/配置上报的嵌套节点声明同样核范围。

缓存失效读取与 ACK（确认收到）使用持久 `audience`（事件接收范围）：限定目标节点的事件只到对应节点；全局 Key/租约事件按既有派发记录、曾经真实解析的节点记录、当前 Key 节点归属或原租约范围判断接收资格。Key 撤销/改绑后，曾缓存原 Key 的旧节点仍能接收自身应得的失效通知，当前新节点不取代原接收方。无节点目标且非 Key/租约的全局事件保留广播语义；这不放宽节点专属事件。读取和确认用同一受众条件，另一节点或不存在的 `ack_ids` 被拒绝，不能跨节点取走、确认或消除对方待收通知；事件载荷返回前同样去除秘密。

实现依据：[实际管理路由](internal/server/routes/admin.go)、[管理处理器](internal/handler/admin/gateway_binding_handler.go)、[请求/状态/排空类型与状态变更](internal/gatewaylite/binding_manager.go)、[持久目的地与激活](internal/gatewaylite/binding_state.go)、[主站通信](internal/gatewaylite/binding_remote.go)、[本地结算证据检查](internal/gatewaylite/binding_drain.go)、[启动激活及恢复](internal/gatewaylite/binding_runtime.go)、[有序重启](cmd/server/main.go)、[绑定准入](internal/server/middleware/gateway_binding.go)、[长连接新轮守卫](internal/gatewaylite/binding_turn.go)、[媒体派发结清登记](internal/gatewaylite/binding_media.go)、[媒体返回地址](internal/service/media_proxy.go)、[旧后台任务退出](internal/gatewaylite/binding_workers.go)、[主站绑定及断开处理器](../control-plane/internal/httpapi/gateway_binding_handlers.go)、[主站节点运行归属守卫](../control-plane/internal/httpapi/gateway_binding_scope.go)、[持久资源/快照/缓存受众查询](../control-plane/internal/store/binding_scope_query.go)、[付款主体事务内再核归属](../control-plane/internal/store/organization_billing_subject.go)、[快照秘密剔除](../control-plane/internal/httpapi/gateway_binding_snapshot.go)。本节仅同步源码合同，不代表桌面/手机界面、完整资金恢复端到端或部署已经验收。

### `GET /api/v1/admin/gateway-lite/config`

用途：读取 gateway-lite config（轻量网关配置），用于后台设置页展示主站控制面连接信息。

返回示例：

```json
{
  "region": "local",
  "gateway_code": "local-gateway",
  "redis_prefix": "jnm:gateway-lite",
  "admin_login_path": "/gateway-admin",
  "login_title": "轻量网关管理后台",
  "login_subtitle": "仅限授权管理员访问",
  "admin_sync_key_configured": true,
  "control_plane_url": "http://127.0.0.1:8088",
  "control_plane_token_configured": true,
  "control_plane_timeout_ms": 1000,
  "runtime_health_interval_seconds": 15,
  "runtime_active_window_seconds": 300,
  "config_sync_interval_seconds": 30,
  "cache_invalidation_interval_seconds": 5,
  "legacy_snapshot_scheduler_authority": false,
  "usage_queue_pending_alert_threshold": 1000,
  "usage_queue_dead_alert_threshold": 1,
  "config_path": "./config.yaml",
  "restart_required": false
}
```

字段说明：

- `control_plane_url（主站控制面地址）`：轻量网关访问主站后端的地址。
- `admin_login_path（管理员私有登录路径）`：当前节点唯一允许显示登录表单的单段路径。该字段只通过管理员鉴权接口返回，不进入公开设置接口。
- `login_title/login_subtitle（登录标题/说明）`：仅在请求命中正确私有路径时注入 HTML（网页）；错误路径不会拿到这些文案。
- `admin_sync_key_configured（管理员同步 Key 是否已配置）`：只返回是否已配置，不返回完整 Key。
- `control_plane_token_configured（主站控制面令牌是否已配置）`：只返回是否已配置，不返回完整令牌。
- `legacy_snapshot_scheduler_authority（旧快照调度权威开关）`：回滚开关。正常应为 `false`，表示账号、分组和模型价格以 gateway-lite（轻量网关）本地为调度权威；临时设为 `true` 时会恢复旧 snapshot（配置快照）写入调度缓存行为。
- `restart_required（是否需要重启）`：读取时为 `false`。

### `PUT /api/v1/admin/gateway-lite/config`

用途：保存 gateway-lite config（轻量网关配置）中允许更新的运行参数到 `config.yaml（配置文件）`，并尝试一次 full sync（全量同步）和 health report（健康上报）。托管绑定中的主站 URL（网址）、节点运行凭据、网关编号、区域、Redis prefix（缓存前缀）和管理员同步密钥必须与冻结运行目的地一致，改动被拒绝；本接口不承担主站首次绑定或换绑。

请求体示例（身份字段须从当前读取结果原样保留；秘密字段省略保留）：

```json
{
  "region": "local",
  "gateway_code": "local-gateway",
  "redis_prefix": "jnm:gateway-lite",
  "admin_login_path": "/gateway-admin",
  "login_title": "轻量网关管理后台",
  "login_subtitle": "仅限授权管理员访问",
  "control_plane_url": "http://127.0.0.1:8088",
  "control_plane_timeout_ms": 1000,
  "runtime_health_interval_seconds": 15,
  "runtime_active_window_seconds": 300,
  "config_sync_interval_seconds": 30,
  "cache_invalidation_interval_seconds": 5,
  "legacy_snapshot_scheduler_authority": false,
  "usage_queue_pending_alert_threshold": 1000,
  "usage_queue_dead_alert_threshold": 1
}
```

说明：

- `admin_sync_key（轻量网关管理员同步 Key）` 留空或不传时，会保留原配置文件里的旧 Key。主站同步网关、主站代理账号池管理都依赖这个 Key。
- `admin_login_path（管理员私有登录路径）` 为空时使用 `/gateway-admin`；只允许一个由字母、数字、下划线和连字符组成的 URL（网页地址）段，并拒绝 `/login`、`/admin`、`/api`、`/v1`、`/setup` 等保留路由。
- 私有登录路径、标题和说明保存后立即生效。旧路径随即只显示中性 404 页面；正确路径由服务端注入一次性登录许可标记。允许页和拒绝页使用不同 `ETag（页面缓存标识）`，避免浏览器 304 缓存串页。
- 私有路径只用于防止误入和减少扫描噪声，不替代密码、登录限流、Turnstile（人机验证）、JWT（登录令牌）或 2FA（双因素认证）。
- `control_plane_token（主站控制面令牌）` 留空或不传时，会保留原配置文件里的旧令牌。
- `control_plane_url（主站控制面地址）` 如果不为空，必须是 `http（普通连接）` 或 `https（加密连接）` 绝对地址；托管绑定运行中仍须与原目的地一致，不能用本字段改绑。
- 托管身份任一不一致返回 HTTP 409 `message=managed_binding_identity_requires_disconnect`，写文件、切换主站客户端及同步前即拒绝。`region/gateway_code/redis_prefix/control_plane_url` 是普通字符串请求字段，省略不是部分更新保留；修改非身份参数时需提交当前身份值，不能把本接口当作任意字段的补丁保存。秘密留空/省略保留规则不允许改写已有绑定秘密。
- `legacy_snapshot_scheduler_authority（旧快照调度权威开关）` 只有显式传入时才改变；未传时保留旧值。该字段只用于灰度回滚验证，正常生产应保持 `false`，避免主站旧 mirror（镜像数据）覆盖轻量网关本地调度。
- 返回体和 `GET /api/v1/admin/gateway-lite/config` 一致，并附 `sync_triggered`（是否尝试同步）及可选 `sync_error`（同步错误）。允许的运行参数保存不通过本接口更换主站身份，不能用 `restart_required=false` 推断改绑已经生效。

### `POST /api/v1/gateway-lite/sync`

用途：供 control-plane（主站控制面）触发 gateway-lite（轻量网关）的 full sync（全量同步）和 health report（健康上报），可附当前托管身份下允许的通信运行参数。首次未绑定不能借本接口写入新主站绕过三步登记；修改主站地址、凭据、区域或网关编号等托管身份同样返回 HTTP 409 `managed_binding_identity_requires_disconnect`。本接口不把一次快照同步变成绑定 `bound` 状态，启用须完成原绑定的真实握手及完整快照。

鉴权：

- `Authorization: Bearer <key>` 或 `X-Gateway-Lite-Admin-Key: <key>`。
- `<key>` 可以是 `gateway_lite.admin_sync_key（管理员同步 Key）`，也可以是后台“管理员 API Key”页面生成的 `admin-...` Key。主站网关节点页面填写的是后者时，同步接口也会接受。

请求体示例：

```json
{
  "region": "local",
  "gateway_code": "us-1",
  "control_plane_url": "https://tokenav.ai",
  "control_plane_timeout_ms": 3000,
  "config_sync_interval_seconds": 30
}
```

成功返回会包含 `synced=true`、`health_reported=true` 和本次同步用时。接口会把配置持久化到实际启动配置文件；本机 systemd 安装优先写 `/etc/sub2api/config.yaml`，Docker 安装优先写 `/app/data/config.yaml`。

## 鉴权方式

用户可以通过以下方式传 API Key（接口密钥）：

- `Authorization: Bearer <api_key>`
- `x-api-key: <api_key>`
- `x-goog-api-key: <api_key>`

API Key（接口密钥）格式：

```text
tokenav_sk_<key_id>_<secret>
```

gateway-lite（轻量网关）只把 `key_id` 发给 control-plane（主站控制面），本地用 `sha256(secret)` 校验密钥。

兼容规则：新建密钥使用 `tokenav_sk_`；迁移期 `tokeav_sk_` 和历史 `aiceo_sk_`、`fm_sk_` 前缀继续可解析，避免品牌切换导致已有客户端失效。

### API Key 当前并发字段

以下 API（接口）返回的 API Key（接口密钥）对象会包含 `current_concurrency（当前并发数）`：

- `GET /api/v1/keys`
- `GET /api/v1/keys/:id`
- `GET /api/v1/admin/users/:id/api-keys`
- `GET /api/v1/admin/groups/:id/api-keys`

字段含义：

- `current_concurrency` 来自 gateway-lite（轻量网关）本地 Redis（缓存服务）中的 `concurrency:api_key:{apiKeyID}` stats-only（仅统计）槽位。
- 普通请求、Responses（响应接口）、Anthropic/Gemini 兼容请求和 OpenAI WebSocket 每个活跃 turn（轮次）都会在拿到用户并发槽位后记录该统计槽位。
- 该字段只用于后台展示当前活跃请求数，不参与限流、计费、调度或额度判断。
- Redis（缓存服务）不可用、统计写入失败或读取失败时，接口返回 `0`，真实请求继续执行，不会因为统计失败而拦截客户请求。

### 用量统计中的缓存 Token 字段

以下统计接口会分别返回缓存写入和缓存读取 Token，不再只返回合并后的缓存总量：

- `GET /api/v1/usage/stats`
- `GET /api/v1/admin/usage/stats`

相关响应字段：

| 字段 | 含义 |
| --- | --- |
| `total_cache_creation_tokens` | 当前筛选范围内的 `cache write（缓存写入）` Token 总量 |
| `total_cache_read_tokens` | 当前筛选范围内的 `cache read（缓存读取）` Token 总量 |
| `total_cache_tokens` | 前两个字段之和，用于兼容旧版缓存总量展示 |
| `total_tokens` | 普通输入、输出、缓存写入和缓存读取 Token 的总和 |

统计接口直接分别聚合数据库中的 `cache_creation_tokens` 和 `cache_read_tokens`。前端的缓存写入、缓存读取卡片必须使用对应拆分字段，不能再从 `total_cache_tokens` 猜测或拆分。

## 内部依赖接口

gateway-lite（轻量网关）会调用 control-plane（主站控制面）的内部接口：

- `POST /internal/key/resolve`
- `POST /internal/quota/acquire-lease`
- `POST /internal/quota/refill-lease`
- `POST /internal/quota/rebalance-lease`
- `POST /internal/usage/report`
- `POST /internal/usage/report-batch`
- `POST /internal/config/snapshot`
- `POST /internal/cache/invalidate`
- `POST /internal/gateway/config-changed`
- `POST /internal/gateway/config-push`

模型校验规则：

- `POST /internal/key/resolve（内部密钥解析）` 返回的 `allowed_models（允许模型）` 对普通/未知用途仍进入 gateway-lite（轻量网关）热路径缓存；精确 `token_use=agent_api_key` 的应用密钥完全跳过成功快照缓存，每个 HTTP 请求和 Responses（响应接口）新轮次以主站实际解析结果检查，见前文实时鉴权合同。
- 用户请求体里存在顶层 `model（模型名）` 时，gateway-lite（轻量网关）会在预留额度前校验该模型是否命中允许列表；不命中时返回 `403 MODEL_NOT_ALLOWED（模型不允许）`，不会占用本地 lease（额度租约）。
- `["*"]` 或空列表表示不限制模型。模型缺失的接口不会被这个规则误拦截。

额度调用规则：

- gateway-lite（轻量网关）优先使用本地 Redis（缓存服务）里的 quota lease（额度租约）完成热路径 reserve（预占）。
- 本地 lease（额度租约）不存在、过期或余额不足时，先调用 `POST /internal/quota/acquire-lease（申请额度租约）`。
- 申请或重分配时会附带 `observed_lease_id/observed_version/observed_allocated_cents/observed_reserved_cents/observed_spent_cents/observed_expires_at（网关本地观察到的租约状态）` 和 `local_exhausted（本地已耗尽）`。其中 `spent/reserved（已消费/已预留）` 以 Redis 实时计数为准，主站观察值只用于识别延迟，不直接改账。
- 主站返回的租约包含 `target_cents（当前网关目标份额）` 和 `refill_at_cents（低水位阈值）`。所有活跃网关共同分享套餐目标池，默认总额为最严格套餐额度的 10%；网关数量和 API Key（接口密钥）数量不会把总池重复放大。
- 本地可用金额低于 `refill_at_cents` 时，gateway-lite（轻量网关）使用 Redis `SET NX（仅不存在时写入）` 短锁合并并发信号，并在后台发起一次低水位预取。预取失败不阻塞当前请求；本地真正耗尽后仍走同步申请和重分配兜底。
- 请求结束时，真实费用如果把本地 lease（额度租约）推入低水位，结算 Lua（原子脚本）会随结算结果返回低水位信号；usage event（用量事件）成功写入 Redis Streams（Redis 消息流）后，gateway-lite（轻量网关）才异步触发一次补租。普通未到低水位的请求不增加 Redis 读取或主站调用；同一用户、同一网关的并发信号继续由进程内合并和 Redis 短锁抑制，避免补租风暴。
- 即使 acquire lease（申请额度租约）返回 `ok=true`，写回 Redis 后仍无法预留，也会再执行一次 `POST /internal/quota/rebalance-lease（额度租约重分配）`。每次客户请求最多强制重分配一次，禁止无限重试。
- Redis 写入使用 Lua CAS（Lua 原子比较更新）：旧版本不能覆盖新版本，同一租约的迟到快照不能降低已分配金额，本地 `spent/reserved（已消费/已预留）` 不会被主站旧快照清零；不同租约编号不能覆盖仍有效或存在预留的本地租约。
- request（请求）的 reserve/resize/commit/refund/adjust（预留/调整/提交/退款/修正）都会核对请求记录里的 `lease_id（租约编号）` 与当前租约一致。旧租约请求迟到时仍保存请求最终状态并上报真实 usage（用量），但绝不修改替换后的新租约，也不为新租约误触发补租；`reserved_cents/spent_cents（预留/已用金额）` 在重复或迟到操作下均保护为非负数。
- rebalance（重分配）成功后，gateway-lite（轻量网关）重新 reserve（预占）。只有主站明确返回 `usage_limit_exceeded/insufficient_balance（套餐额度不足）` 时才向客户返回 429；`lease_state_conflict（租约状态冲突）`、主站超时、空响应或补额后仍无法预留均返回 503 `QUOTA_UNAVAILABLE（额度服务不可用）`。
- control-plane（主站控制面）返回 `error=usage_limit_exceeded` 时，可能携带 `error_detail（错误详情）`：`period_quota（套餐周期总额度不足）`、`window_5h（本 5 小时周期额度不足）`、`window_day（本日额度周期不足）`、`window_week（本 7 天周期额度不足）` 或 `lease_unallocated（可分配额度租约不足）`。gateway-lite（轻量网关）会把该详情转换为更明确的 429 错误消息。
- rebalance（重分配）不在每个请求都调用，只有当前网关本地额度不足且普通申请失败时进入慢路径，避免影响正常客户访问。

用量上报规则：

- 用户请求完成后，gateway-lite（轻量网关）先把 usage event（用量事件）写入本地 Redis Streams（Redis 消息流）`gl:usage:stream`，不在用户请求热路径同步等待主站入账。成功请求使用一个原子 Lua 脚本同时确认本地额度消费并持久入队，避免 `CommitResolved（确认费用）`、`AdjustCommitted（重复校正）` 和 `XADD（消息入队）` 三次串行 Redis 往返；多个并发请求会用有界微批 pipeline（流水线）发送各自独立的原子脚本，每个请求仍须等自己的 Redis 持久确认后才能结束，不会退化成先响应后记账。真实费用 correction（修正）也在一个 Lua 脚本内同时调整租约、持久入队并判断低水位，进程退出不能落在“额度已改、用量未入队”的中间状态；脚本会先校验消息流键类型，确定无法 `XADD（消息入队）`时不提前改动金额。真实费用修正先到达时，消息流额外保存脚本解析出的最终金额，后台 worker（工作协程）以上述金额覆盖预估值后再上报主站。
- 额度预留在选择上游账号前失败时，也会写入费用为 0 的 usage event（用量事件）：`upstream_id（上游账号）` 为空，`status（状态码）` 为 429 或 503，`error_message（错误信息）` 记录额度耗尽或租约同步失败阶段，避免失败请求从主站日志中消失。
- usage event（用量事件）会携带 `latency_millis（总用时毫秒）`、hot-path breakdown（热路径分段耗时）字段 `auth_latency_millis（鉴权耗时）`、`quota_latency_millis（额度耗时）`、`routing_latency_millis（调度耗时）`、`routing_breakdown_millis（调度子阶段耗时）`、`upstream_latency_millis（上游耗时）`、`response_latency_millis（响应写出耗时）`、`report_latency_millis（用量上报入队耗时）`，以及 `gateway_group_id/gateway_group_name（网关分组编号/名称）`、`rate_multiplier（分组倍率）`、`account_rate_multiplier（账号倍率）`、`requested_reasoning_effort（策略处理前的客户原始思考档次）`、`reasoning_effort（最终请求体中明确发送给上游的思考档次）`、`requested_service_tier（客户原始请求值）`、`sent_service_tier（最终发送值）`、`response_service_tier（上游响应值）`、`service_tier（最终计费模式）`、`priority_detection_source（判断来源）`、`priority_policy_action（规则动作）`、`priority_policy_rule_id（规则编号）`、`priority_policy_fallback（是否使用兜底动作）` 和 `long_context_billing_applied（是否实际应用厂商长上下文价格）`；真实响应记录产生首个 token 时，还会随 correction（用量修正）携带 `upstream_id（实际命中的轻量网关本地账号编号）`、`requested_model/upstream_model/model_mapping_chain（客户请求模型/实际上游模型/模型映射链）`、`first_token_millis（首字用时毫秒）`、真实账号倍率、`billing_mode/billing_tier（计费模式/计费档位）`、`image_count/image_size/image_output_tokens/image_output_cost（图片张数/尺寸/图片 token/图片费用快照）`，以及 `video_count/video_resolution/video_duration_seconds（视频数量/分辨率/生成秒数）`，供 control-plane（主站控制面）保存真实账号、模型映射、TTFT（首字用时）与计费证据快照。两个思考档次字段独立保存：主站页面只展示客户原始值，轻量网关后台只展示实际上游值；`reasoning_effort` 不得回退到客户原始值、模型后缀或厂商默认值。Gemini 原生请求只读取最终请求体明确存在的 `generationConfig.thinkingConfig.thinkingLevel`，不把 `thinkingBudget` 换算成档次。任一字段缺失时页面显示“—（未指定）”，重复 correction 的空值也不得清空先前证据。`routing_breakdown_millis` 当前 `version=1`，顶层记录 `user_slot（用户并发占槽）`、`billing_recheck（请求后限流复检）`、`session_policy（会话策略）`、`account_selection（账号选择，包含选择器内部占槽）`、`post_selection_account_slot（选择器返回后的补充占槽）` 和 `other（调度总耗时扣除已测阶段后的剩余值）`；账号选择内部继续记录 `selection_group_validation（分组校验）`、`selection_sticky_lookup（粘性账号读取）`、`selection_account_resolve（账号解析）`、`selection_account_slot（账号占槽）`、`selection_sticky_refresh（粘性期限刷新）`、`selection_candidates（完整候选池解析）`、`selection_settings（调度设置读取）` 和 `selection_other（选择器剩余耗时）`。这些指标只观察，不改变调度和计费。调度前失败没有实际账号，`upstream_id` 和模型映射字段保持为空。页面默认只展示分组和分组倍率，不展示账号倍率。
- `account_rate_multiplier（账号采购倍率）`未携带表示调度前未知，显式 `0` 表示采购成本为零。合法 `0×` Token 策略只把请求开始的 1 分 safety placeholder（安全占位）按零提交；图片/视频独立倍率产生的真实 correction（用量修正）无论先于或晚于 finalize（请求收尾），都会保留在本地 lease（额度租约）并上报主站，不能被 Token 免费策略整笔退款。媒体 correction 的 `rate_multiplier`保存实际图片/视频倍率，不会被 Token 审计中的 `0×`覆盖。
- usage event（用量事件）的 `session_id（会话标识）` 只从客户端明确发送的 `session_id/conversation_id/X-Session-Id/X-OpenCode-Session/X-Conversation-ID/X-Claude-Code-Session-Id（会话请求头）`读取；最大 255 个字符，控制字符、非法 UTF-8 或超长值会被丢弃。它只用于审计关联，不读取请求体 `prompt_cache_key（提示词缓存键）`，不改变 sticky（粘性调度）、`request_id（请求幂等标识）`、额度租约或最终扣费。
- 隔离性能测试可在请求中显式发送 `X-Gateway-Perf-Diagnostics: 1`；网关会返回 `X-Gateway-Handler-Started-At-Unix-Nano（网关进入处理器时间）`和 `X-Gateway-Response-Write-At-Unix-Nano（网关首次写响应时间）`响应头，并声明 HTTP Trailer（响应尾字段）`X-Gateway-Finalize-Ms` 和 `X-Gateway-Access-Log-Ms`。它们用于拆分 TCP 写完到进入 Gin（Web 处理器）的排队、网关开始处理到首次写出、响应写出后客户端调度、本地额度确认与用量持久入队、以及最外层 access log（访问日志）同步写出耗时。普通请求不发送诊断头时不会计算、包装 ResponseWriter（响应写入器）或返回这些字段；它们只用于隔离压测，不参与调度、费用或用户日志。
- 图片计费快照只用于 evidence（证据）和 request log（请求日志）追溯，不是新的扣费来源；实际扣费仍以 gateway-lite（轻量网关）当次请求已经计算出的 `actual_cents（实际费用分）` 为准。
- usage worker（用量后台任务）使用 consumer group（消费者组）读取 `gl:usage:stream`，默认每批最多 50 条。
- control-plane client（主站客户端）优先调用 `POST /internal/usage/report-batch（批量用量上报）`；如果下游不支持 batch（批量），代码仍可回退到单条 `POST /internal/usage/report（单条用量上报）`。
- 批量上报成功后执行 `XACK（消息确认）`，随后执行 `XDEL（删除已处理消息）`；同时通过 `XTRIM MAXLEN ~ 10000` 防止旧版本遗留消息或异常路径让 stream（消息流）无限增长。失败时重新写回 `gl:usage:stream` 并按次数退避重试。
- 重试耗尽或事件无法解析时写入 `gl:usage:dead` dead stream（死信消息流），后续运维可单独排查。
- runtime health monitor（运行时健康监控）会定期探测 control-plane（主站控制面）`/health（健康检查）`，同时读取 `gl:usage:stream（用量消息流）`、consumer group pending count（消费者组待确认数量）和 `gl:usage:dead（用量死信消息流）`，再合并上报到 `POST /internal/gateway/health-report（网关健康上报）`。
- `gateway_code（节点代码）`和 `region（区域）`必须分别上报真实配置值；节点代码用于唯一定位，区域用于主站核对身份。不得用节点代码覆盖区域，否则主站会按身份不一致拒绝健康、配置推送和配置变更上报。
- runtime health monitor（运行时健康监控）使用防重入的单轮采集：主站探测、可选运行指标与最终心跳上报都有有界 timeout（超时）。队列与进程指标优先于低频真实模型探针采集；某个 provider（指标提供器）超时或 panic（异常）不会终止后续心跳。可选采集超时时会上报 `runtime_health_enrichment_timed_out=true` 和 `runtime_health_enrichment_timeout_stage`，最终心跳使用新的超时预算，不继承已过期 context（请求上下文）。
- scheduler snapshot（调度快照）的 `sched:meta（脱敏候选元数据）` 只用于筛选，不再写入 full-account hot cache（完整账号热缓存）。候选账号被选中后才从 `sched:acc（完整账号快照）` 补全凭据；真实模型探针另有最多 2 秒的本地 repository（数据仓库）重载兜底，仅在选中对象明确缺少认证材料时触发，不加入用户请求热路径。
- runtime health monitor（运行时健康监控）还会按 `GATEWAY_LITE_MODEL_PROBE_INTERVAL_SECONDS（真实模型探测间隔秒数）` 执行 real model probe（真实模型探测），默认每 `300` 秒使用本地 OpenAI-compatible（OpenAI 兼容）账号池发起一次随机算术 challenge（挑战题）请求。实际间隔会被限制在 `15` 到 `3600` 秒之间，并在每次真实探测后加入 `±10%`、最多 `30` 秒的 jitter（随机抖动），避免多台网关同时打到上游。同一模型存在多个可调度账号时，探针先按正式调度顺序收集本轮候选（最多 `16` 个），再在整轮总 deadline（截止时间）内为每个账号分配独立 attempt timeout（单次尝试超时）并保留切换余量；单个最高优先账号超时不会直接把整个节点判红，只有本轮全部候选都失败才上报失败。OAuth 账号使用 ChatGPT Codex Responses（响应接口）的 `stream=true` SSE（服务端事件流），收到 `response.completed/response.done` 后立即结束；为兼容官方 Codex backend（后端），最小探针请求不发送其不接受的 `max_output_tokens` 字段。API Key（接口密钥）账号继续使用非流式 Chat Completions（聊天补全接口）。该请求不走用户 API Key、不扣用户套餐额度、不写用户请求日志；探测结果会覆盖本次健康状态，避免“网关进程正常但没有可用上游账号”时误显示 100%。状态含义为：`operational（正常）` = `2xx` 且回答正确，`degraded（缓慢）` = 回答正确但超过慢响应阈值，`failed（内容失败）` = `2xx` 但回答错误或空内容，`error（请求错误）` = 网络错误、超时、`429`、`5xx`、流错误或无法构造请求。
- real model probe（真实模型探测）会在 `metadata（元数据）` 中写入 `probe_source=gateway-lite-real-model-probe`、`probe_target（探测目标）`、`probe_model（探测模型）`、`probe_upstream_model（上游模型）`、`probe_account_id（探测账号编号）`、`probe_status（探测状态）`、`probe_status_code（探测状态码）`、`probe_ok（探测是否成功）`、`probe_error（探测错误）`、`probe_latency_ms（探测用时毫秒）`、`probe_cached（是否复用缓存结果）`、`probe_interval_seconds（探测间隔秒数）` 和 `probe_checked_at_millis（探测时间毫秒）`。流式账号的 `probe_latency_ms` 统计到 SSE 正常完成或失败，不把响应头/首包时间误当完整请求用时。`probe_error` 在上报前会按当前账号实际认证材料精确擦除，再执行常见密钥脱敏和长度限制，不会上报完整 API Key、Token、Agent Identity 私钥或签名；探针恢复成功时会显式上报空 `probe_error`，用于清除主站节点快照中的旧错误。
- runtime health monitor（运行时健康监控）也会在 `metadata（元数据）` 中上报轻量网关本地运行指标：`usage_record_*（用量记录工作池）`、`billing_cache_*（计费缓存写入队列）`、`redis_pool_*（Redis 连接池）`、`db_pool_*（PostgreSQL 数据库连接池）`、`upstream_*_5m（最近 5 分钟真实上游成功/失败率）`、`overload_*（过载保护）`、`account_slot_release_*（账号槽位批量释放）` 和 `user_api_key_slot_release_*（用户与接口密钥槽位批量释放）`，用于定位高并发下的队列堆积、丢弃、同步回退、连接瓶颈、真实上游故障和鉴权前排队。Redis 指标包含 `redis_pool_wait_count_total（累计等待连接次数）`、`redis_pool_wait_duration_ms_total（累计等待连接毫秒）`、`redis_pool_timeouts_total（累计连接等待超时）` 和当前总/空闲连接数。数据库池指标包含 `db_pool_max_open_connections（最大连接数）`、`db_pool_open_connections（当前打开）`、`db_pool_in_use（使用中）`、`db_pool_idle（空闲）`、`db_pool_wait_count_total（累计等待次数）` 和 `db_pool_wait_duration_ms_total（累计等待毫秒）`；这些值只读取 Go 进程内统计，不执行探测 SQL。上游指标每分钟最多聚合一次最近 5 分钟：`upstream_request_count_5m（样本数）`、`upstream_success_count_5m（成功数）`、`upstream_failure_count_5m（真实上游失败数）`和 `upstream_failure_rate_percent_5m（失败百分比）`；分子排除普通用户 4xx、内容拦截、业务限额、429/529 和 count_tokens（Token 预估），查询失败只上报 `upstream_metrics_error=query_failed`，不会泄露数据库错误原文。槽位释放指标包含当前长度/容量、入队量、批次数、完成量、批量失败、队列满与同步兜底；用户/API Key 队列另有 `user_api_key_slot_release_retry_total（瞬时失败重试次数）`。其中 `overload_waited_total（发生过等待的请求总数）`、`overload_wait_ms_total（累计等待毫秒）`、`overload_wait_ms_max（最大等待毫秒）` 与当前 inflight/waiting（处理中/等待中）数量会随健康上报发送；`usage_record_queue_capacity（用量记录队列容量）`、`usage_record_queue_utilization_percent（用量记录队列占用率）`、`usage_record_overflow_policy（队列满策略）`、`usage_record_auto_scale_*（自动扩缩容配置）` 会反映当前进程实际运行配置。
- `billing_cache_*（计费缓存写入指标）` 同时保留汇总字段，并细分 `billing_cache_critical_*（关键队列）`、`billing_cache_best_effort_*（非关键队列）` 和 `billing_cache_ordering_wait_total（同键保序等待次数）`。`billing_cache_preset` 与 `billing_cache_timeout_milliseconds` 表示当前真正生效的预设和写入超时；配置 worker 数与 active worker（当前实际工作协程）数分开上报。关键队列可观测队长、容量、队满次数、同步兜底及兜底失败；非关键队列还会上报 deferred（延后）长度/容量/累计量、同目标合并量、同步兜底与兜底失败。`billing_cache_reload_total` 表示工作池原子重载次数，`billing_cache_stopped` 表示已经停止接收任务。
- runtime health monitor（运行时健康监控）还会在 `metadata（元数据）` 中上报运行配置权威状态：`gateway_lite_runtime_authority=local（本地权威）`、`legacy_snapshot_scheduler_authority（是否启用旧快照调度权威）`、`snapshot_accounts_groups_compatibility_only（主站快照账号/分组是否仅兼容字段）` 和 `local_accounts_groups_scheduler_authoritative（本地账号/分组是否为调度权威）`。管理员可用这些字段确认回滚开关是否启用，避免误把主站旧 mirror（镜像数据）重新当成真实调度来源。
- runtime health monitor（运行时健康监控）和 config push（配置主动推送）优先使用当前 active/schedulable（有效/可调度）账号 `model_mapping（模型映射）` 汇总出的真实模型。只有账号池没有可用动态模型时，才回退到 `gateway_lite.available_models（轻量网关可用模型）` 或 `GATEWAY_LITE_AVAILABLE_MODELS（轻量网关可用模型环境变量）`；静态列表不会再与真实账号模型取并集，避免主站展示无法调度的模型。环境变量使用英文逗号分隔，例如 `gpt-4o,claude-sonnet-4`。
- `stream length（消息流长度）` 只作为观测指标上报，不直接触发告警；`pending count（待确认数量）` 超过阈值触发 `degraded（堆积/缓慢）`，`dead count（死信数量）` 超过阈值触发 `error（错误）`。

单条和批量上报中的 event（事件）字段一致，示例：

```json
{
  "request_id": "req_001",
  "user_id": 42,
  "key_id": "key123",
  "lease_id": "lease_local_1",
  "region": "jp",
  "gateway_id": "gateway-jp-1",
  "upstream_id": "18",
  "gateway_group_id": 3,
  "gateway_group_name": "高速",
  "protocol": "openai_compatible",
  "model": "gpt-5.6-terra",
  "requested_model": "gpt-5.6",
  "upstream_model": "gpt-5.6-terra",
  "model_mapping_chain": "gpt-5.6→gpt-5.6-terra",
  "method": "POST",
  "path": "/v1/responses",
  "status": 200,
  "tokens_in": 700,
  "tokens_out": 80,
  "cache_read_tokens": 200,
  "cache_write_tokens": 100,
  "service_tier": "priority",
  "sent_service_tier": "priority",
  "response_service_tier": "priority",
  "requested_service_tier": "priority",
  "priority_detection_source": "response",
  "priority_policy_action": "pass",
  "priority_policy_rule_id": "",
  "priority_policy_fallback": false,
  "long_context_billing_applied": true,
  "billing_mode": "token",
  "estimated_cents": 1,
  "actual_cents": 1,
  "rate_multiplier": 1.2,
  "account_rate_multiplier": 1,
  "latency_millis": 1800,
  "first_token_millis": 420,
  "auth_latency_millis": 4,
  "quota_latency_millis": 6,
  "routing_latency_millis": 8,
  "routing_breakdown_millis": {"version":1,"user_slot":1,"billing_recheck":0,"session_policy":0,"account_selection":6,"post_selection_account_slot":0,"other":1},
  "upstream_latency_millis": 1600,
  "response_latency_millis": 170,
  "report_latency_millis": 2,
  "started_at_millis": 1783692000000,
  "ended_at_millis": 1783692001800
}
```

金额规则：`actual_cents（实际费用分）` 是唯一扣费依据。gateway-lite（轻量网关）先把 USD（美元）费用精确到百万分之一美分，再按 `gateway + API Key + user（网关、接口密钥和用户）` 累计不足 1 美分的零头；累计满 1 美分才上报，避免每次向上取整多收或每次截断少收。
- 如果 control-plane（主站控制面）的 gateway node（网关节点）`metadata（元数据）` 里配置了 `health_probe_interval_seconds（健康探测间隔秒数）`，gateway-lite（轻量网关）会在 config snapshot（配置快照）同步后动态使用该间隔；低于 `15` 秒按 `15` 秒执行，高于 `3600` 秒按 `3600` 秒执行，否则真实模型探测使用 `300` 秒默认值。
- 如果 control-plane（主站控制面）的 gateway node（网关节点）`metadata（元数据）` 里配置了 `health_probe_model（健康探测模型）`，gateway-lite（轻量网关）会优先使用该模型做真实模型探测；否则使用本地 `GATEWAY_LITE_HEALTH_PROBE_MODEL（健康探测模型）` 或账号池中第一个真实模型。

配置变更主动同步规则：

- gateway-lite（轻量网关）启动后，以及本地后台修改 `accounts（账号池）`、`groups（分组）`、`model prices（模型价格）` 后，会 debounce（防抖）合并短时间内的多次变更，并优先调用 `POST /internal/gateway/config-push（网关配置主动推送）`。
- `config-push（配置主动推送）`与`config-changed（配置变更通知）`沿用同一节点身份规则：`gateway_code（节点代码）`和`region（区域）`分别取运行配置，不得把两者合并为同一个 scope（范围）值。
- 账号相关通知动作包括：`resource=accounts,action=create（创建账号）`、`resource=accounts,action=update（编辑账号/关闭账号）`、`resource=accounts,action=delete（删除账号）`、`resource=accounts,action=bulk_update（批量更新）`、`resource=accounts,action=schedulable（单独切换是否可调度）`。这些通知用于让 control-plane（主站控制面）及时回拉账号池和分组镜像。
- 主动推送体携带完整的非敏感账号摘要、全部 `local_only=false（允许发布）`分组配置和当前可用模型价格，并固定设置 `accounts_full=true`、`groups_full=true`、`model_prices_full=true`。`local_only=true` 分组及其最终策略不会进入该快照；账号摘要中的分组编号也只保留允许发布的分组。control-plane（主站控制面）会按每类完整列表删除已经不存在的镜像项；分组切换为仅本地使用时只移除对应分组、策略和账号分组关联，不删除节点级模型价格目录。
- 每个 `groups[]（分组）` 上报 `description（分组描述）`和 `sort_order（展示顺序）`，内容来自轻量网关本地分组配置；主站只做镜像，描述用于用户界面展示，排序用于 API 线路和 API Key（接口密钥）候选顺序，两者都不参与真实请求调度、能力或计费判断。
- 每个 `groups[]（分组）` 同时上报 `effective_models（有效模型集合）`、`effective_model_count（有效模型数量）`、`effective_models_reported（是否已明确上报）` 和 `effective_models_version（有效模型版本）`。集合只使用该分组内 `status=active（有效）` 且 `schedulable=true（参与调度）` 的账号，应用账号 `model_mapping（模型映射）`、分组手工模型白名单、平台、OAuth、隐私和媒体权限后生成。临时 429、短期过载和短期冷却不会让价格页模型反复消失。每个分组还按客户可见模型上报 `model_prices（匿名完整倍率前合同）`及固定为 `true` 的 `model_prices_reported（分组价格合同已明确上报）`；合同合并全局默认价格与分组渠道覆盖价，但不乘分组、图片或视频倍率。合同即使为空也表示经过路由与账号映射歧义检查后的权威结果，用于阻止主站回退节点目录并展示错误价格。
- `effective_models=[]` 且 `effective_models_reported=true` 表示新版网关明确确认该分组当前没有有效模型；字段缺失或 `effective_models_reported=false` 表示旧网关尚未提供这类事实，主站必须显示“数据待同步”，不能回退成节点全部模型。
- 模型价格主动推送会完整保留 `per_request（按次计费）`、视频各分辨率每秒价格，以及标准、Priority（优先处理）、超过 272K 和“超过 272K + Priority”的价格矩阵；主站定时回拉使用同一字段口径。
- 分组匿名合同只包含客户请求模型、公开目标平台、计费方式、价格、币种、价格来源类别和分档信息；不包含渠道名、渠道编号、账号、账号编号、映射后模型或实际上游模型。主站用户页面只能消费该匿名合同，不能从管理员成本规则或账号镜像拼装销售价格。
- 主动推送同时固定设置 `effective_policies_full=true` 并携带 `effective_policies[]（最终生效策略影子）`。每个分组策略由轻量网关在配置推送阶段根据本地分组、可调度账号、有效模型和模型价格生成，包含 `schema_version/revision/checksum/state/reason_code/source_versions/capabilities/models（契约版本、修订号、校验和、状态、原因码、来源版本、能力和模型档位）`。聚合分组逐模型额外上报 `target_platform/upstream_model/price_model/price_source/route_source/eligible_account_count（真实平台/上游模型/计价模型/价格来源/路由来源/该平台有效账号数）`，且 `source_versions.composite_routes（聚合路由版本）`参与 revision（修订号）与 checksum（校验和）；找不到真实平台价格时失败关闭。它不进入用户请求热路径，也不允许主站借此放宽本地请求规则。
- 策略中的图片、视频能力只在分组中确实存在可用的对应媒体模型时标记 `available（可用）`，不能仅因分组开关开启就把纯文本模型误报成媒体能力。OpenAI 分组存在有效账号、有效模型和完整标准价格时，`web_search（网页搜索）`能力也明确标记为 `available`；其它平台标记 `unsupported`，主站不再根据接口路径猜测。分组配置完整 `image_price_1k/2k/4k（图片三档单张价格）`时，策略模型的最终 `billing_mode（计费方式）`同步标记为 `image（按张）`，与真实分组结算合同一致。
- 每次完整快照会计算 `snapshot_checksum（完整配置校验和）`，覆盖账号、分组、模型价格、最终策略校验和和运行开关的逻辑内容，但排除周期生成时间、上报时间、策略修订号和分组模型临时版本。首次同步或逻辑内容变化时仍发送全部镜像；只有主站明确返回 `snapshot_checksum_accepted=true（校验和已确认）`后，轻量网关才记住该校验和。旧主站不返回该字段时继续使用完整快照，允许主站与网关分步升级。
- 即使本地配置长期没有变化，轻量网关也会默认每 5 分钟后台保活，但相同 `snapshot_checksum` 只发送 `refresh_only=true（仅刷新新鲜度）`和校验和，不再重复传输或重写账号、分组、价格及最终策略正文。主站返回 `full_snapshot_required=true（需要完整快照）`时，轻量网关会在同一次后台同步中立即补发完整快照；主站清库或丢失同步状态不会被保活掩盖。间隔可用 `GATEWAY_LITE_EFFECTIVE_POLICY_REFRESH_INTERVAL_SECONDS（最终策略刷新间隔秒数）`缩短，超过 5 分钟会按 5 分钟执行，避免主站 10 分钟过期门禁把健康但安静的节点隐藏。刷新复用现有防抖、超时、失败回退和指数重试，不进入用户请求热路径。
- `checksum（校验和）` 是排序规范化后的 SHA-256，排除传输时间、主站节点编号和修订号；相同逻辑输入在重试时得到相同校验和。`revision（修订号）` 单调前进，主站必须拒绝旧修订覆盖新策略。策略状态只使用 `available/disabled/unavailable/unsupported/unknown（可用/关闭/不可用/不适用/未知）`，详细解释通过稳定 `reason_code（原因码）`表达。
- 用量事件新增可选 `policy_revision（策略修订号）`、`policy_checksum（策略校验和）`和`price_version（价格版本）`。轻量网关只在主站明确确认配置推送成功后原子替换只读审计索引；推送失败时继续使用上一份已确认版本。请求结算只做一次无锁映射读取，不重新计算或序列化策略；旧节点或首次成功推送前允许三项为空，不改变既有金额结果。
- `runtime_settings（运行设置镜像）` 会随启动推送和设置变更推送一起发送，只包含 gateway-lite（轻量网关）本机当前有效、可公开给管理员排障的运行策略摘要。当前包含 `openai_fast_policy_settings（OpenAI Fast/Flex 策略摘要）`：`version（配置版本）`、`priority_detection_by_request_enabled（根据请求判断）`、`priority_detection_by_response_enabled（根据响应判断）`、`rule_count（规则数量）` 和 `reported_at_millis（上报时间毫秒）`。这个字段是只读镜像，control-plane（主站控制面）不会反向覆盖轻量网关本地设置。
- 账号 `credentials（凭证配置）` 会在发出 HTTP（网络请求）前删除 API Key（接口密钥）、OAuth Token（授权令牌）、Cookie（登录凭据）、私钥、密码和其它敏感子字段；只保留模型映射、Base URL（上游基础地址）等非敏感配置，并通过 `credentials_status（凭证存在状态）` 表示某类凭证是否已经配置。`extra/runtime（扩展与运行状态）` 同样递归脱敏。
- `event_id（事件幂等编号）` 由 `gateway_code + resource + action + resource_id + changed_at_millis` 稳定生成，同一个失败事件重试时保持不变，方便 control-plane（主站控制面）排障和后续去重。
- `config-push（配置推送）` 只允许推送 gateway-lite（轻量网关）运行态 mirror（镜像数据），禁止携带用户、套餐、订阅、支付、余额、额度租约、用量明细和账本流水。用户资格、套餐额度、支付账本仍由 control-plane（主站控制面）通过专用接口权威处理。
- 如果完整快照构建失败、主动推送网络失败或 control-plane（主站控制面）返回一般性 `ok=false`，gateway-lite（轻量网关）会立即调用 `POST /internal/gateway/config-changed（网关配置变更通知）`，让主站使用管理接口回拉；同时保留原事件并按指数退避重试主动推送。定时拉取、管理员手动同步和 `cache invalidate（缓存失效）` 短轮询继续作为最终兜底。
- 如果 `config-push（配置主动推送）`或其 `config-changed（配置变更通知）`兜底明确返回 `ok=false,error=gateway_node_inactive`，表示该节点已经被主站维护或归档。gateway-lite（轻量网关）把当前事件视为 terminal（终止）结果：不再调用另一条兜底链路、不进入指数重试，也不确认本次 `snapshot_checksum（快照校验和）`或发布新的 effective policy audit（最终策略审计版本）。这样拒绝的镜像不会伪装成已确认；节点以后重新启用时，新产生的配置事件仍可正常上报。
- 主动同步不进入用户请求热路径；配置变更只在后台任务中读取本地账号、分组和模型目录。
- runtime health report（运行时健康上报）会把最近配置变更通知状态写入 `metadata（元数据）`：`config_changed_pending_retry（是否有待重试通知）`、`config_changed_last_event_id（最近事件编号）`、`config_changed_last_success_at_millis（最近成功时间）`、`config_changed_last_failure_at_millis（最近失败时间）`、`config_changed_last_failure_error（最近失败错误）` 和 `config_changed_next_retry_delay_seconds（下一次重试延迟秒数）`，方便主站管理员排查镜像不同步。

相关环境变量：

- `GATEWAY_LITE_RUNTIME_HEALTH_INTERVAL_SECONDS`：runtime health report interval（运行时健康上报间隔），默认 `15` 秒；这是启动默认值，可被主站 gateway node metadata（网关节点元数据）里的 `health_probe_interval_seconds（健康探测间隔秒数）` 覆盖。
- `GATEWAY_LITE_EFFECTIVE_POLICY_REFRESH_INTERVAL_SECONDS`：effective policy freshness refresh（最终策略新鲜度刷新间隔），默认并最多 `300` 秒；配置未变化时只发校验和保活，变化或主站要求时才发完整快照，不改变请求或计费规则。
- `GATEWAY_LITE_MODEL_PROBE_INTERVAL_SECONDS`：real model probe interval（真实模型探测间隔），默认 `300` 秒；真实上游请求只按该间隔执行，普通运行时上报会复用最近一次探测结果。
- `GATEWAY_LITE_HEALTH_PROBE_MODEL`：real model probe model（真实模型探测模型），为空时自动从本地可调度账号池选择模型。
- `GATEWAY_LITE_RUNTIME_ACTIVE_WINDOW_SECONDS`：online users active window（在线用户活跃窗口），默认 `300` 秒。
- `GATEWAY_LITE_AVAILABLE_MODELS`：available models fallback（可用模型兜底列表），英文逗号分隔；只有当前有效且可调度账号没有动态模型映射时才用于上报。
- `GATEWAY_LITE_USAGE_QUEUE_PENDING_ALERT_THRESHOLD`：pending count alert threshold（待确认数量告警阈值），默认 `1000`。
- `GATEWAY_LITE_USAGE_QUEUE_DEAD_ALERT_THRESHOLD`：dead stream alert threshold（死信数量告警阈值），默认 `1`。

### 计费缓存写入双队列配置

`billing.cache_write（计费缓存写入）` 是启动级配置，修改 YAML（配置文件）或环境变量后需要重启轻量网关。余额扣减、订阅用量更新和 API Key（接口密钥）限额用量更新进入 critical（关键）队列；可从数据库重建的余额与订阅缓存刷新进入 best-effort（非关键）队列。两类任务使用独立的有界队列，缓存未命中洪峰不会挤占关键扣减容量。

- `preset`: `low / standard / high（低配/标准/高并发）` 三档，默认 `standard`；非法名称会让进程启动失败，不会静默回退。
- `low`: 关键 `2 worker / 512 buffer`，非关键 `1 worker / 128 buffer`，写入超时 `2000ms`。
- `standard`: 关键 `8 / 2048`，非关键 `2 / 512`，写入超时 `2000ms`。
- `high`: 关键 `16 / 8192`，非关键 `4 / 2048`，写入超时 `1500ms`。
- `critical_worker_count`: 可选单项覆盖，范围 `1-128`。
- `critical_buffer_size`: 可选单项覆盖，范围 `1-1048576`。
- `best_effort_worker_count`: 可选单项覆盖，范围 `1-128`。
- `best_effort_buffer_size`: 可选单项覆盖，范围 `1-1048576`。
- `timeout_milliseconds`: 可选单项覆盖，范围 `10-30000` 毫秒。

关键队列满时会在当前调用路径执行一次有超时上限的同步兜底，不能静默丢弃金额或限额更新。非关键队列满时先进入同样有界的 deferred latest-value map（按目标保留最新值的延后区）；相同目标只保留最新刷新并安全跳过被替换序号。队列与延后区都满时，同样执行有界同步兜底，因此不会丢失最终刷新状态，也不会让内存无限增长。同一 balance/subscription/API Key cache key（余额/订阅/密钥缓存键）跨两个队列共享严格执行序号，后到的增量扣减不会被较早但较慢的绝对值刷新覆盖。工作池内部重载会先发布新池、再排空旧代；`Stop()` 会拒绝新任务并完整排空已进入两个队列和延后区的任务。

同名环境变量依次为 `BILLING_CACHE_WRITE_PRESET`、`BILLING_CACHE_WRITE_CRITICAL_WORKER_COUNT`、`BILLING_CACHE_WRITE_CRITICAL_BUFFER_SIZE`、`BILLING_CACHE_WRITE_BEST_EFFORT_WORKER_COUNT`、`BILLING_CACHE_WRITE_BEST_EFFORT_BUFFER_SIZE` 和 `BILLING_CACHE_WRITE_TIMEOUT_MILLISECONDS`。显式数值始终覆盖预设；只想切换档位时不要同时保留旧数值覆盖。

### 本地用量记录队列配置

`gateway.usage_record（本地用量记录队列）` 控制 gateway-lite（轻量网关）请求完成后的本地统计/用量记录任务。它不替代 control-plane（主站控制面）的最终入账，只负责本地请求日志、统计和缓存类收尾任务的后台执行。

gateway-lite（轻量网关）默认配置：

- `worker_count`: `64`，初始 worker（工作协程）数量。
- `queue_size`: `16384`，等待队列容量。
- `task_timeout_seconds`: `5`，单个本地记录任务超时时间。
- `overflow_policy`: `sync`，队列满时同步兜底，关键计费任务不能抽样丢弃。
- `overflow_sample_percent`: `10`，只有管理员显式改成 `sample` 时才生效。
- `auto_scale_enabled`: `true`，是否按队列占用率自动扩缩容。
- `auto_scale_min_workers`: `64`，自动扩缩容最小 worker 数。
- `auto_scale_max_workers`: `128`，自动扩缩容最大 worker 数。
- `auto_scale_up_queue_percent`: `70`，队列占用率达到该值时扩容。
- `auto_scale_down_queue_percent`: `15`，队列占用率低于该值时缩容。
- `auto_scale_up_step`: `32`，单次扩容 worker 数。
- `auto_scale_down_step`: `16`，单次缩容 worker 数。
- `auto_scale_check_interval_seconds`: `3`，扩缩容检查间隔。
- `auto_scale_cooldown_seconds`: `10`，扩缩容冷却时间。

`overflow_policy（队列满策略）` 说明：

- `drop（丢弃）`：队列满时丢弃本地记录任务，最不影响请求尾部耗时，但本地统计可能缺失。
- `sample（抽样同步）`：队列满时按 `overflow_sample_percent` 比例同步执行，其他丢弃；不建议 gateway-lite 使用，因为任务中包含关键计费修正。
- `sync（同步回退）`：gateway-lite 默认策略，队列满时同步执行关键用量任务，避免少账。

远程主站 Key（接口密钥）完成请求后，真实 correction（计费修正）会先持久化进入 Redis Stream，再处理本地 usage log（用量镜像日志）。同一个远程用户/Key 到本地影子身份的 PostgreSQL UPSERT（插入或更新）使用 10 分钟进程内缓存和 singleflight（重复请求合并）；本地镜像日志使用容量 32768、批量 256、窗口 20ms 的非阻塞队列。该本地队列满时只允许丢弃节点镜像日志，已经持久化的主站计费事件不受影响，也不会同步回退拖慢用户响应。gateway-lite 的本地自有 Key 没有主站 correction，仍沿用等待落库结果的原路径。

standard（标准 sub2api）模式保持通用默认 `worker_count=128`、自动扩缩容 `128→512`，不受上述 gateway-lite 特化默认值影响。

### `POST /internal/control-plane/events`

用途：接收 control-plane（主站控制面）Outbox（事务消息）主动推送的配置、API Key（接口密钥）、lease（额度租约）失效事件和 effective policy（最终生效策略）重新上报请求。该接口不进入用户请求热路径。

路由保护：embed（嵌入前端）中间件会明确绕过 `/internal/*`。该接口无论鉴权成功或失败都返回 JSON，不会被 SPA（单页应用）回退错误替换成 `index.html`。

鉴权：必须携带 `Authorization: Bearer <GATEWAY_LITE_CONTROL_PLANE_TOKEN>`。接收端使用常量时间比较；未配置共享 Token（令牌）返回 `503`，Token 错误返回 `401`。

请求体示例：

```json
{
  "delivery_id": 27,
  "event": {
    "id": 105,
    "scope": "lease:snapshot",
    "reason": "lease_pool_rebalanced",
    "gateway_code": "sg-1",
    "region": "sg-1",
    "config_version": 8,
    "payload": {
      "user_id": 42,
      "lease_id": "lease_42_sg_1",
      "version": 8,
      "allocated_cents": 500
    }
  }
}
```

成功响应：

```json
{
  "ok": true,
  "acknowledged": true,
  "already_processed": false,
  "delivery_id": 27,
  "event_id": 105
}
```

处理规则：

- 推送和轮询共用同一个 `event.id（事件编号）`。处理成功后在 Redis（缓存服务）写入 30 天幂等标记；ACK（确认）丢失后重复推送不会产生重复副作用。
- 定向事件的 `gateway_code（网关固定编码）` 必须与本机当前编码一致，否则返回 `409 gateway_mismatch（网关不匹配）`。
- `config:snapshot（配置快照）` 和 `key:snapshot（密钥快照）` 只触发“拉取最新配置”或“删除本地缓存”，事件自身不携带可直接覆盖运行配置的数据，因此迟到事件不会把新配置回滚成旧配置。
- `lease:snapshot（额度租约快照）` 会校验 `user_id（用户编号）`、`lease_id（租约编号）` 和 `version（版本）`。只有租约编号相同且本地版本不高于事件版本时才删除本地租约和低水位补充锁；旧租约或迟到旧版本事件会被忽略。
- `effective-policy:snapshot（最终策略快照）`只触发本地 `ConfigChangeNotifier（配置变更通知器）`防抖重算，并强制上报完整权威快照；即使旧 checksum（校验值）已经确认，也不会降级成 `refresh_only（仅刷新）`空心跳。事件不携带策略正文，主站不能借此覆盖轻量网关账号、分组、价格、调度或真实计费规则。
- 主动推送处理失败返回 `503 event_apply_failed（事件应用失败）`，主站可以安全重试。

### `POST /internal/cache/invalidate`

`POST /internal/cache/invalidate（缓存失效事件）` 用于让 gateway-lite（轻量网关）短轮询主站配置变更事件。主动推送上线后，该 5 秒轮询仍永久保留为恢复兜底，不进入用户请求热路径。托管 `gb_`（节点运行凭据）调用按前文“主站绑定与安全改绑”的持久事件接收范围过滤读取和 `ack_ids`（确认事件编号）；不能凭自报编码读取另一节点事件或确认不存在/不属于自己的事件，Key 改绑后旧节点仍可收到其应接收的撤销通知。

请求体示例：

```json
{
  "gateway_code": "sg-1",
  "region": "sg-1",
  "since_id": 0,
  "limit": 50,
  "ack_ids": [1, 2]
}
```

响应体示例：

```json
{
  "ok": true,
  "events": [
    {
      "id": 1,
      "scope": "config:snapshot",
      "reason": "gateway_config_updated",
      "gateway_code": "sg-1",
      "region": "sg-1",
      "config_version": 12
    }
  ],
  "latest_id": 1
}
```

事件处理规则：

- `config:snapshot（配置快照）`：gateway-lite（轻量网关）强制调用 `POST /internal/config/snapshot（配置快照）` 拉取主站业务规则和诊断镜像；不会把其中的 `accounts/groups（账号/分组）` 写入 scheduler cache（调度缓存）。
- `key:snapshot（密钥快照）`：gateway-lite（轻量网关）清理本 gateway scope（网关作用域，当前等于 gateway_code）下的 Key cache（密钥缓存），下一次用户请求会重新解析 key（密钥）。
- `lease:snapshot（额度租约快照）`：按租约编号和版本有条件删除本地 Redis（缓存服务）租约，下一次请求同步向主站申请当前权威额度；不同租约编号或更高本地版本不会被迟到事件删除。
- 其他 scope（范围）暂时忽略，但会推进 cursor（游标），避免重复消费。
- 主动推送、5 秒轮询、定时同步和后台手工同步共用同一个 `ConfigSyncer（配置同步器）`串行门。完整的“拉取主站快照、写 Redis、应用本地运行设置”同一时刻最多执行一次；等待中的同步会响应 `context（上下文）`取消，旧请求不能在新请求之后完成并覆盖新快照。该门只在配置控制冷路径运行，不进入用户模型请求热路径。
- 轮询处理完事件后必须先把本页 `ack_ids（确认事件编号）`成功提交给主站，主站返回 `ok=true` 后才能推进 Redis cursor（轮询游标）。网络失败、空响应或 `ok=false` 都保留旧游标；下一轮会从旧位置重放，而 30 天 processed marker（已处理标记）会阻止重复清缓存、重复租约失效或重复完整同步。主站以单条批量 SQL（数据库语句）确认最多 200 个编号，并把缓存事件确认、目标节点 delivery（投递）确认和告警恢复放在同一个 PostgreSQL（数据库）事务中。

相关环境变量：

- `GATEWAY_LITE_GATEWAY_CODE`：gateway code（网关编号），默认使用 `GATEWAY_LITE_REGION（网关区域）`。
- `GATEWAY_LITE_CACHE_INVALIDATION_INTERVAL_SECONDS`：cache invalidation polling interval（缓存失效轮询间隔），默认 `5` 秒。
- `GATEWAY_LITE_CONFIG_SYNC_INTERVAL_SECONDS`：config snapshot sync interval（配置快照同步间隔），默认 `30` 秒。
- `GATEWAY_LITE_LEGACY_SNAPSHOT_SCHEDULER_AUTHORITY`：legacy rollback switch（旧快照调度权威回滚开关），默认 `false`。只有紧急回滚时才设为 `true`；开启后 gateway-lite（轻量网关）会恢复使用 control-plane（主站控制面）config snapshot（配置快照）里的 `accounts/groups（账号/分组）` 覆盖本地 scheduler cache（调度缓存）的旧行为。
- `GATEWAY_LITE_INVALID_AUTH_ABUSE_ENABLED`：invalid auth abuse limiter（非法 Key 防刷）总开关，默认 `true`。
- `GATEWAY_LITE_INVALID_AUTH_ABUSE_THRESHOLD/WINDOW_SECONDS/BLOCK_SECONDS`：默认在 60 秒内连续 120 次非法鉴权后拒绝 60 秒。来源地址和 Key 指纹分开计数。
- `GATEWAY_LITE_INVALID_AUTH_ABUSE_CAPACITY`：本地最多跟踪的不可逆指纹数，默认 `16384`。
- `GATEWAY_LITE_INVALID_AUTH_ABUSE_REDIS_TIMEOUT_MS`：Redis（缓存服务）多节点防刷操作超时，默认 `25ms`；超时或 Redis 故障时 fail-open（失败开放），继续走正常 Key 鉴权，不会把合法用户全部拒绝。

网关权限拦截：key snapshot（密钥快照）必须是 `gateway_strategy=fixed_group（固定网关分组）`，并同时包含有效 `key_default_gateway_node_id（Key 绑定网关节点编号）`、`group_id（分组编号）` 和 `group（分组快照）`；否则返回 `FIXED_GROUP_REQUIRED（必须绑定固定网关分组）`。随后 gateway-lite（轻量网关）会把当前 `GATEWAY_LITE_GATEWAY_CODE（网关编号）` 对应节点与 Key 绑定节点精确比较；即使当前节点也在用户套餐可用列表中，只要不是 Key 绑定节点，仍返回 `GATEWAY_NOT_ALLOWED（网关不可用或无权限）`，且不会预占额度。

非法 Key 防刷只在鉴权前识别“缺失/格式错误/不存在/已停用/secret 不匹配”。内存和 Redis 中只保存 SHA-256 截断指纹，不保存 Key 原文或完整来源地址。分组、等级、绑定、模型权限和余额错误不计为非法 Key。触发后返回 HTTP `429` + `Retry-After`；该 `401/429` 分支位于 lease（额度租约）预占、账本和用量上报之前，因此不会产生任何计费副作用。运行健康上报会增加 `invalid_auth_abuse_*` 聚合计数，不包含任何指纹值。

跨节点 Redis 检查不会因同一 NAT（共享公网地址）下一次输错 Key 就进入所有合法请求：本地失败达到封锁阈值的 `1/4` 后才视为“合理怀疑”，该门槛最少 `2`、最多 `16`；Redis 未发现封锁或短暂不可用时，负结果在当前进程缓存 `1` 秒。同一 Key 命中进程内 `500ms` 正缓存、且 secret 与 active snapshot（启用快照）匹配时，直接跳过 invalid-auth Redis 检查，再继续正式授权，避免合法热请求等待最多 `25ms`。真正已经在本地命中封锁的非法请求仍会快速拒绝。

密钥解析：gateway-lite（轻量网关）调用 control-plane（主站控制面）`POST /internal/key/resolve（内部密钥解析）` 时会传 `gateway_code（网关唯一编码）`，兼容字段 `region` 会填入同一个 gateway scope（网关作用域）。主站会记录该 Key 最近被哪些网关解析过，用于后续 `key:snapshot（密钥快照）` 定向缓存失效；全局缓存失效事件仍作为兜底。

缓存失效：gateway-lite（轻量网关）短轮询 `cache invalidation（缓存失效）` 事件时，`key:snapshot（密钥快照）` 事件如果 payload（负载）包含 `key_id`，会只删除当前 gateway scope（网关作用域）下该 Key 的 Redis（缓存服务）快照；如果没有 `key_id` 但包含 `user_id`，会删除当前 gateway scope 下该用户的全部 Key 快照；两者都没有时才回退为删除当前 gateway scope 的全部 Key 快照。每次 Key、用户、企业或整区失效都会先推进 Redis 共享的 `generation（失效代次）`；缓存未命中回源主站前会固定当前代次，回源后仅在代次未变化时用 Lua 原子写入快照与用户/企业索引。这样企业网关白名单、成员权限或 Key 在回源期间收紧时，在途旧响应可以结束当前请求，但不能重新缓存旧授权，下一次请求必须重新解析最新权限。

config snapshot（配置快照）中的 `plan_gateway_rules（套餐网关规则）` 现在可能包含 `gateway_group_id（网关分组编号）`。该字段由 control-plane（主站控制面）用于用户线路展示和 API Key（接口密钥）固定分组校验；gateway-lite（轻量网关）只接受 `gateway_strategy=fixed_group（固定网关分组）` 的 Key snapshot（密钥快照）。托管 `gb_` 的专用节点快照不返回这些平台跨节点规则，权限仍由实时密钥解析取得。

config snapshot（配置快照）中的 `accounts/groups（账号/分组快照）` 是 compatibility-only（兼容字段），只代表 control-plane（主站控制面）的最近镜像和诊断信息。gateway-lite（轻量网关）本地账号池、分组、倍率、模型路由、模型价格和调度桶以本地数据库为 source of truth（事实来源）。

### API Key 专属网关和分组调度

control-plane（主站控制面）创建 API Key（接口密钥）时必须把 Key 绑定到专属 `default_gateway_node_id（绑定网关节点编号）` 和 `group_id（网关分组编号）`。gateway-lite（轻量网关）不重新判断用户套餐等级，但会严格校验 `POST /internal/key/resolve（内部密钥解析）` 返回的 key snapshot（密钥快照）结构：

- 当前轻量网关必须既在 `available_gateways（可用网关）` 中，又与 `key_default_gateway_node_id（Key 绑定网关节点编号）` 完全一致，否则返回 `GATEWAY_NOT_ALLOWED（网关不可用或无权限）`。
- `group_id（网关分组编号）` 是 control-plane（主站控制面）的全局分组 ID；`group.local_group_id（本地分组编号）` 是当前 gateway-lite（轻量网关）本地真实分组 ID。热路径只使用该固定分组选择本地账号、判断模型权限和应用计费倍率；用量上报时携带本地分组 ID，control-plane（主站控制面）会按 `gateway_id（网关标识） + local_group_id（本地分组编号）` 还原为全局 `gateway_group_id（网关分组编号）` 入库，不存在全账号池兜底。
- `group（网关分组快照）` 来自主站 Key snapshot（密钥快照），用于证明 Key 的稳定绑定关系、套餐权限、模型白名单、用量归属和页面提示；它不是运行时分组配置的事实来源。认证中间件会把快照中的 `local_group_id（本地分组编号）` 交给本地 scheduler cache（调度缓存），使用当前本地分组覆盖快照里的名称、状态、Messages dispatch（Messages 调度）、生图/视频开关、Claude Code only（仅 Claude Code）、OAuth only（仅 OAuth 账号）、privacy account（隐私账号）、模型列表、倍率、模型价格、`max_reasoning_effort（最高思考档次）`和`reasoning_effort_mappings（思考档次精确映射）`。因此远程主站 Key 的最终上游思考档次也只采用轻量网关本地权威策略；调度缓存缺失时只回源一次本地数据库并重新写入缓存，不会让每个用户请求都查询数据库。
- 主站 Key snapshot（密钥快照）的可空 `user_rate_multiplier（用户专属倍率）` 是当前固定分组的主站权威 `user×group（用户×分组）`覆盖值；未配置时使用本地权威分组默认倍率。`user_group_rate_multipliers（用户分组倍率映射）` 只包含同节点允许关键词重路由目标的已配置覆盖值，并以本地分组编号为键。重路由后必须按目标分组精确读取，缺失时恢复目标分组默认倍率，绝不能沿用原固定分组覆盖值。远程主站 Key 仍不得查询 gateway-lite 本地影子用户表补造倍率。
- gateway-lite（轻量网关）创建、编辑或删除本地分组时，会在数据库写入成功后同步更新 scheduler cache（调度缓存），outbox（可靠事件队列）继续作为失败修复路径。因此后台修改分组状态、倍率或能力后，下一次新请求直接读取新配置，不需要等待主站镜像同步；本地分组已删除或停用时，即使主站旧 Key snapshot（密钥快照）仍保留该分组，请求也会返回 `503 GATEWAY_GROUP_UNAVAILABLE（网关分组不可用）`。
- Key snapshot（密钥快照）不再包含或读取 `priority_mode（旧 API Key 优先偏好）`。Priority/Flex 是否发送完全由客户请求和轻量网关本地 Fast/Flex Policy（规则）决定，避免同一请求同时受主站 Key 设置和网关规则重复改写。
- `/v1/models（模型列表）` 会固定使用当前 API Key（接口密钥）绑定的本地分组查询账号，不再因为 gateway-lite（轻量网关）运行模式而退回整个账号池。OpenAI 分组不能看到 Grok 分组独有模型，Grok 分组也不能看到 GPT、Codex 或 OpenAI 图片模型。
- `/v1/models（模型列表）` 会按分组模型列表和 `allow_image_generation（允许生图）` 过滤结果；分组未开启生图时，`gpt-image-*`、`dall-e*` 和常见 `*-image` 生图模型不会展示，真实生图请求也会被权限错误拦截。
- 如果固定分组在当前轻量网关没有 active/schedulable（有效/可调度）上游账号，请求会在账号选择阶段失败，不会自动 fallback（兜底）到其他分组。OpenAI-compatible（OpenAI 兼容）响应使用 HTTP `503`，`error.type=api_error`，`error.code=GATEWAY_GROUP_UNAVAILABLE`，`error.message=Gateway group is unavailable on this gateway`。同时 gateway-lite（轻量网关）会通过现有 `config-changed（配置变更通知）` 通道上报 `resource=groups`、`action=unavailable`、`resource_id=group_id`，提示 control-plane（主站控制面）刷新该网关的分组镜像。
- 如果主站修改用户套餐、网关规则或 API Key（接口密钥）绑定，会写入 `key:snapshot（密钥快照）` 或 `config:snapshot（配置快照）` 失效事件；gateway-lite（轻量网关）消费后清理主站规则/Key 缓存，下一次请求重新解析 Key 或拉取主站规则。轻量网关本地分组状态变更应由本地后台写入并主动上报主站镜像。

## 后台会话绑定、TOTP 与操作审计

这些能力只保护 gateway-lite（轻量网关）后台 Web session（浏览器会话）和 `/api/v1/admin/*` 管理接口，不进入 `/v1/*` 客户请求热路径，也不改变 API Key（接口密钥）、主站内部令牌或 gateway admin key（网关管理密钥）的协议。

### 会话绑定

相关配置：

- `server.trusted_proxies`：Gin（Web 框架）可信代理 CIDR/IP 列表；未配置时不信任转发链。
- `security.trust_forwarded_ip_for_api_key_acl` / 后台设置 `api_key_acl_trust_forwarded_ip`：安全客户端 IP 总开关。默认关闭；开启后仍只接受直连来源命中 `server.trusted_proxies` 的转发信息，同时影响 API Key ACL（接口密钥访问控制）、hybrid auth（混合鉴权）、后台会话绑定、操作审计和访问日志，已登录管理员需要重新登录。
- `security.forwarded_client_ip_headers` / 后台设置 `forwarded_client_ip_headers`：可选自定义来源 Header（请求头），也可通过环境变量 `SECURITY_FORWARDED_CLIENT_IP_HEADERS` 使用 JSON 数组或逗号分隔值配置。服务端规范化、大小写不敏感去重并限制最多 16 项；后台提供 Cloudflare、Nginx 和常用 CDN 快捷按钮。仅在上述总开关开启且直连来源可信时按管理员顺序读取，跳过非法 IP，优先选择公开地址，只有没有公开地址时才使用合法私网地址回退。配置热更新使用不可变原子快照，单个请求内 ACL、会话、审计和日志始终读取同一份结果。
- `security.session_binding_mode`：`off（关闭）`、`observe（观察）`、`enforce（强制）`，默认 `off`。

登录后服务端在 Redis（缓存服务）保存 `sid（会话编号）`、规范化客户端 IP、UA hash（浏览器标识哈希）、binding hash（绑定哈希）、创建/最后访问/最后 TOTP 验证时间和过期时间；不会保存完整 User-Agent（浏览器标识）长文本。Access token（访问令牌）携带 `sid` 与 `bnd`，refresh token（刷新令牌）保存相同绑定证据。

- `observe`：IP/UA 变化时放行请求并写成功的观察审计，原绑定不漂移。
- `enforce`：IP/UA 变化时撤销当前 refresh token family（刷新令牌家族）和服务端 Web session，返回 `401 SESSION_BINDING_MISMATCH`。
- 登录、刷新、退出、撤销全部会话、修改密码、启用/停用 TOTP 会同步维护或撤销服务端会话。

### TOTP 设置接口

#### `POST /api/v1/user/totp/setup`

管理员必须提交当前密码；普通用户继续按系统邮件验证配置使用密码或邮箱验证码。

请求示例：

```json
{
  "password": "current-password"
}
```

响应 `data` 增加：

```json
{
  "secret": "BASE32_SECRET",
  "qr_code_url": "otpauth://totp/...",
  "qr_code_data_url": "data:image/png;base64,...",
  "setup_token": "one-time-setup-token",
  "countdown": 300
}
```

`qr_code_data_url` 由网关进程本地生成，不把 TOTP secret（双因素秘密）发送给第三方二维码服务。

#### `POST /api/v1/user/totp/enable` / `POST /api/v1/user/totp/disable`

启用或停用成功后会撤销该用户全部旧登录会话，前端必须清理本地令牌并返回登录入口。

### `POST /api/v1/user/totp/step-up`

用途：为一个明确高风险操作签发一次性 step-up 2FA（现场二次验证）授权。仅接受 JWT Web session（网页会话）；管理员 API Key、gateway admin key 和其他 machine credential（机器凭据）不得获取或消费授权。

请求：

```json
{
  "code": "123456",
  "action_scope": "accounts.export"
}
```

成功响应 `data`：

```json
{
  "verified": true,
  "token": "su_once_...",
  "expires_in": 300
}
```

授权规则：

- 默认 5 分钟过期，由 `security.step_up_ttl_seconds` 配置。
- 绑定 user/session/IP/UA/action scope/role/token version（用户、会话、网络、浏览器、操作范围、角色和令牌版本）。
- Redis 只保存授权 token（令牌）的 SHA-256；消费时使用 Lua 原子读取并删除。
- 授权只能重放原操作一次；绑定不匹配、并发重放或错误 scope（范围）同样会消耗 token，刷新页面不会自动复用。
- 高风险接口先不带 Header 调用时返回 `403 STEP_UP_REQUIRED`；验证后仅在一次重放请求中携带 `X-Step-Up-Token: <token>`。
- 未启用 TOTP 返回 `403 STEP_UP_TOTP_NOT_ENABLED`；非浏览器会话返回 `STEP_UP_MACHINE_CREDENTIAL_FORBIDDEN` 或 `STEP_UP_SESSION_REQUIRED`。

当前 scope（操作范围）：

| Scope | 受保护操作 |
| --- | --- |
| `accounts.export` | 账号导出 |
| `proxies.export` | 代理导出 |
| `backups.create` / `backups.download` / `backups.restore` / `backups.delete` | 备份创建、下载、恢复和删除 |
| `backups.s3.read` / `backups.s3.update` | 备份 S3 配置读取、修改和测试 |
| `data_management.s3.read` / `data_management.s3.update` | data-management S3 配置、测试和备份任务 |
| `admin.role.promote` | 管理员角色提升 |
| `audit_logs.clear` | 清空操作审计日志 |

### 备份 S3 秘密配置接口

以下三个接口只接受当前 JWT Web session（网页会话），并在服务端强制消费与当前 session、IP/UA binding（网络与浏览器绑定）、用户角色和 action scope（操作范围）一致的一次性 step-up token。管理员 API Key、gateway admin key、旧 session、错误 scope、过期 token、并发双击和已消费 token 均不能绕过验证。

#### `GET /api/v1/admin/backups/s3-config`

要求 `X-Step-Up-Token` 的 scope 为 `backups.s3.read`。响应只返回非秘密配置和凭据存在状态，不返回可复用的 Access Key ID 或 Secret Access Key：

```json
{
  "endpoint": "https://storage.example.com",
  "region": "auto",
  "bucket": "gateway-backups",
  "prefix": "backups/",
  "force_path_style": false,
  "access_key_id_configured": true,
  "secret_access_key_configured": true
}
```

#### `PUT /api/v1/admin/backups/s3-config`

要求 scope 为 `backups.s3.update`。请求可以提交 `endpoint`、`region`、`bucket`、`prefix`、`force_path_style`、`access_key_id` 和 `secret_access_key`。凭据字段为空时保留已保存值，避免读取脱敏响应后更新普通字段时误清空密钥；新 secret 和保留的旧 secret 都必须加密后才能写入设置表。成功响应与 GET 使用同一脱敏结构。

#### `POST /api/v1/admin/backups/s3-config/test`

要求 scope 为 `backups.s3.update`，因此携带尚未保存的新凭据与正式保存使用同级服务端门控。失败响应固定为：

```json
{
  "ok": false,
  "code": "S3_CONNECTION_FAILED",
  "message": "connection failed; verify the S3 endpoint, bucket and credentials"
}
```

上游 SDK 错误原文不得进入响应、操作审计或普通日志，因为其中可能包含 endpoint、bucket、Access Key、credential scope 或签名 URL 查询参数。更新 step-up 绑定规则后，部署时无需迁移数据，但所有已打开后台页面必须重新登录并重新完成 TOTP 验证；回滚到不认识脱敏响应字段的旧节点前，应先停止 S3 配置修改流量，且不得把已经签发的一次性 token 视为可跨版本复用。

### 大文件备份分卷

`POST /api/v1/admin/backups` 创建 PostgreSQL（数据库）压缩备份。压缩文件不超过 `4 GiB` 时继续写入一个对象；超过阈值时按压缩后的字节顺序分卷。每卷记录连续 `index（序号）`、`s3_key（对象键）`、`size_bytes（大小）` 和 SHA-256（完整性指纹）。上传失败会用独立超时上下文清理整份分卷计划，防止客户端已超时但对象实际落盘后留下孤儿文件。该能力仅支持单次分卷上传、完整性校验和恢复重组；不保存跨进程断点，不提供断点续传或续传状态接口。

`POST /api/v1/admin/backups/:id/restore` 恢复分卷备份前会按序下载至临时文件，逐卷校验序号、大小和 SHA-256；缺卷、乱序、大小不符或内容损坏都会在调用数据库恢复前拒绝。旧的单对象备份记录保持原流式恢复行为。

`GET /api/v1/admin/backups/:id/download-url` 对旧单文件保持兼容响应：

```json
{"url":"https://storage.example.com/signed-backup-url"}
```

分卷备份返回按序排列的 `parts`，调用端需要按 `index` 顺序下载并直接拼接二进制内容：

```json
{"parts":[{"index":1,"size_bytes":4294967296,"url":"https://storage.example.com/signed-part-1"}]}
```

删除和定时过期清理会删除记录关联的全部分卷；任一对象删除失败时保留备份记录，便于管理员重试，不能丢失对象键证据。

### 操作审计接口

审计记录只保存操作者、稳定操作枚举、资源类型/编号、结果、请求编号、来源 IP、UA hash、HTTP 状态、耗时和字段级脱敏摘要。禁止保存 API Key、access/refresh token、cookie、TOTP secret、代理密码、完整请求体或对象存储密钥。`extra（额外字段）` 只接受服务端白名单中的布尔值、数值和不超过 128 字节的短字符串；任意 map（映射）、数组、长文本、请求体和外部响应原文会在写入前丢弃，历史记录读取时也会再次过滤。

字段摘要也使用固定安全 schema（结构）：只保留不超过 128 个字段名及 `changed/configured/cleared_or_omitted（修改/已配置/清空或省略）`状态。字段名最长 64 字节且只允许普通标识符字符；超长、控制字符、空格或疑似凭据形式的攻击者字段名会改为不可逆短 SHA-256 指纹。service（服务层）和 repository（数据库仓储层）都会再次过滤，handler（接口处理器）不能通过自定义 map 绕过脱敏。

普通后台变更异步批量写入；队列或数据库失败写 Ops（运维）错误日志。账号/代理导出、备份、S3 秘密配置、权限提升和审计清空等安全关键操作先同步写 `pending（待确认）` 意图，写入失败返回 `503 AUDIT_LOG_UNAVAILABLE` 并禁止执行原操作。

#### `GET /api/v1/admin/audit-logs`

查询参数：

- `before_id`：稳定 keyset cursor（键集游标），只读取更旧的记录。
- `limit`：`1-200`，默认由服务端限制。
- `actor_user_id`、`actor_email`、`action`、`resource_type`、`resource_id`、`result`。
- `start_time`、`end_time`：RFC3339 时间。

响应 `data`：

```json
{
  "items": [
    {
      "id": 120,
      "created_at": "2026-07-17T11:30:00Z",
      "actor_user_id": 1,
      "actor_email": "admin@example.com",
      "actor_role": "admin",
      "auth_method": "jwt",
      "action": "admin.accounts.export",
      "resource_type": "account",
      "result": "success",
      "method": "GET",
      "path": "/api/v1/admin/accounts/data",
      "request_id": "req_...",
      "client_ip": "203.0.113.10",
      "ua_hash": "truncated-sha256",
      "status_code": 200,
      "latency_ms": 12,
      "summary": {"fields": ["ids"], "states": {"ids": "changed"}}
    }
  ],
  "next_cursor": 116
}
```

#### `GET /api/v1/admin/audit-logs/:id`

返回单条完整脱敏记录；不存在时返回 `404 OPERATION_AUDIT_NOT_FOUND`。

#### `GET /api/v1/admin/audit-logs/clear/preview`

清空前只读预览，不消耗 step-up（敏感操作二次确认）token（令牌），返回将被删除的总行数、最早和最新记录时间；空表只返回 `rows=0`：

```json
{
  "rows": 1024,
  "oldest_at": "2026-01-01T00:00:00Z",
  "newest_at": "2026-08-01T00:00:00Z"
}
```

#### `POST /api/v1/admin/audit-logs/clear`

必须携带 `X-Step-Up-Token` 且 scope 为 `audit_logs.clear`。删除全部旧记录和插入新的 `admin.audit_logs.clear` 尾记录在同一个 PostgreSQL（数据库）事务中完成；任一步失败则整体回滚。

成功响应 `data`：

```json
{
  "deleted": 1024
}
```

保留期由 `security.audit_log_retention_days`、`audit_log_cleanup_batch_size` 和 `audit_log_cleanup_interval_minutes` 控制；默认保留 180 天并限批清理。

## Redis（缓存服务）用途

- Key cache（密钥缓存）：减少每次请求访问 control-plane（主站控制面）。
- quota lease（额度租约）：在当前 gateway scope（网关作用域，当前等于 gateway_code）下快速判断余额。
- usage queue（用量队列）：异步回传用量，避免影响用户请求。
- scheduler cache（调度缓存）：保存 gateway-lite（轻量网关）本地账号和分组快照，用于选择上游账号，并完整保留最高思考档次及思考档次精确映射；不再由 control-plane（主站控制面）的 config snapshot（配置快照）覆盖。
- upstream billing probe leader（上游倍率探测主节点锁）：`leader:lock:<gateway_lite.redis_prefix>:upstream_billing_probe:leader`，使用 compare-token 续租和释放；锁异常时自动任务停止，不影响客户请求。
- config snapshot（配置快照）：`gl:config:snapshot`。
- config version（配置版本）：`gl:config:version`。
- cache invalidation cursor（缓存失效游标）：`gl:config:invalidation:last_id`。
- processed control event（已处理主站事件）：`gl:config:event:processed:<event_id>`，默认保留 30 天。
- key snapshot（密钥快照）：`gl:key:<gateway_code>:<key_id>`。
- usage stream（用量消息流）：`gl:usage:stream`。
- usage dead stream（用量死信消息流）：`gl:usage:dead`。

## ChatGPT Live（实时会话，v0.1.165 条件接入）

Live 使用官方两段式协议，接受绑定到 OpenAI 分组，或绑定到 Composite（聚合）分组且 `session.model（会话模型）`最终路由到 OpenAI 的有效 API Key（接口密钥）。聚合分组按 Responses（响应接口）路由域解析 `session.model`，并在发往上游前改写为路由配置的 `upstream_model（上游模型）`；路由到其他平台时明确拒绝：

- `POST /v1/live`：JSON 或 `multipart/form-data` 请求体包含 `sdp` 与 JSON object（对象）格式的 `session`，成功返回 `application/sdp`，`Location` 指向 `/v1/live/:call_id`。
- `GET /v1/live/:call_id`：升级为 Sideband WebSocket（旁路长连接）。会话只能由创建时同一用户、Key 和分组接管。
- `POST /backend-api/codex/realtime/calls`：Codex 原生创建别名，输入输出语义与 `POST /v1/live` 相同。
- `GET /backend-api/codex/:call_id`：Codex 原生 Sideband WebSocket（旁路长连接）别名。

开放条件必须同时满足：`gateway_lite.allow_live=true（节点总开关）`、分组 `allow_live=true`、分组平台为 OpenAI、存在真实 ChatGPT OAuth 账号且官方 DeviceCheck attestation（设备证明）可用。API Key 账号、PAT（个人访问令牌）、Agent Identity（代理身份）、Linux 和 Windows 节点不能伪装为支持；条件不足时返回 `404/403/503`，具体取决于总开关、分组权限或设备证明状态。两个开关均默认关闭。

Live 使用 Redis 独立 60 秒 lease（长连接租约），但与普通 HTTP/SSE 请求共同计算账号和用户并发上限，并独立记录 API Key 活跃数。每 20 秒续租；账号、用户、Key 任一租约缺失时立即结束连接，不能重新创建丢失租约。正常结束、断线重连、硬超时和租约丢失都竞争同一个 Redis `closed` 原子标记，只有第一个终结器释放槽位并写一次 `request_type=live` usage（用量）事件。

当前官方协议没有返回可核验 Token 用量，因此四条 Live 路径不做普通一美分额度预占，最终事件固定 `estimated_cents=0`、`actual_cents=0`、`protocol=openai_live`，只保存会话持续时间、实际账号、Key/用户/分组和创建时的 effective policy（最终生效策略）版本证据。取得官方可核验用量前不得把 Live 宣传为收费能力。

管理员分组创建、编辑和查询对象新增 `allow_live（允许实时会话）`；设置接口 `GET/PUT /api/v1/admin/settings/gateway-lite` 新增 `allow_live（节点总开关）`。前端只提供点击开关，不要求手工输入。主动配置上报的 `groups[].allow_live` 和 effective policy 的 `capabilities[].id=live`供 control-plane（主站控制面）只读消费：总开关关闭时完全省略，分组关闭为 `disabled/live_disabled`，无 OAuth 账号或无设备证明时为 `unavailable`，只有全部条件满足才为 `available`。

## 管理员 API Key（接口密钥）按需重复查看

管理员 API Key 默认只以脱敏形式展示；已登录的管理员可以主动再次查看或复制完整值。完整值接口只接受 JWT Web session（网页会话），不接受管理员 API Key 自身或 gateway admin key（网关管理员同步密钥）。响应禁止缓存，操作进入安全审计但审计内容不包含密钥。

### `GET /api/v1/admin/settings/admin-api-key`

读取状态，不返回完整密钥：

```json
{
  "exists": true,
  "masked_key": "admin-0123...cdef"
}
```

### `POST /api/v1/admin/settings/admin-api-key/reveal`

按需读取现有完整密钥。要求管理员 JWT Web session；请求体为空。成功响应包含：

```json
{
  "key": "admin-..."
}
```

- 响应头包含 `Cache-Control: no-store`和`Pragma: no-cache`。
- 未配置密钥返回 `404`。
- 使用管理员 API Key 或其他 machine credential（机器凭据）调用返回 `403`。
- 完整值不得写入日志、URL、审计摘要或浏览器持久化存储。
# 响应声明模型与主站身份日志

- 分组创建、编辑接口的 models_list_config 新增 response_model_mode 与 response_model_custom。模式支持 request（跟随用户请求，默认）、upstream（上游原样）和 custom（自定义，最多 200 个 Unicode 字符）。
- 账号创建、编辑接口的 extra 支持同名字段；账号额外支持 inherit（继承分组，默认）。账号设置优先于分组设置。
- 该配置只改写客户端响应中的模型声明，不影响路由、模型映射、计费模型或轻量网关保存的上游原始模型审计。
- `request（跟随用户请求）` 使用分组/渠道映射前的客户原始模型；即使请求体已经改写为上游模型，客户端声明仍保持原始请求值。上游实际模型和原始响应声明继续独立记录。
- 下游客户端实际收到的 JSON/SSE/WebSocket 响应中的 `model`、`response.model`、`message.model`、`modelVersion` 等声明字段，也统一按该策略处理；因此客户请求 `gpt-5.6-sol` 映射到 `gpt-5.6-luna` 时，`request` 模式对外仍返回 `gpt-5.6-sol`。映射后的模型只保留在轻量网关内部路由、计费和管理员审计字段中。
- 失败切换、协议转换、Responses→Chat/Anthropic 回退以及请求体规范化不会覆盖已保存的客户原始模型；同一请求重复进入兼容层时继续复用首次客户声明。若账号单独设置 `upstream（上游原样）` 或 `custom（自定义）`，账号策略仍优先于分组策略。
- OpenAI Responses（响应接口）的 HTTP 流式、非流式及 WebSocket（网络长连接）首轮和省略模型的后续轮次均遵循同一规则。
- 管理员请求记录新增 main_site_username、main_site_email、main_site_user_id，可按主站用户名或真实邮箱筛选；内部影子邮箱不再返回给管理员列表。

## Codex remote compaction v2（远程上下文压缩第二版）兼容说明

- 裸 `POST /v1/responses`、`stream=true` 且 `input` 含 `type=compaction_trigger` 时按原生 v2 透传，不再改写为旧 `/v1/responses/compact`。
- 原生 v2 强制选择具备 Responses（响应接口）能力的上游账号，并确保 `x-codex-beta-features` 包含 `remote_compaction_v2`；旧 Compact 专属模型映射只作用于旧路径。
- 管理员的 Compact 测试使用流式 `/responses` 探测，只有响应真实出现 `compaction` 或 `compaction_summary` 输出项才标记支持，避免只看 HTTP 200 误判。
- 上游响应的 `x-codex-turn-state` 会回传客户端；同一 API Key（接口密钥）与客户端会话故障切换到其他账号时，会剥离由旧账号生成的状态，避免跨账号回显冲突。
- Codex OAuth（授权登录）账号的指纹收敛默认关闭；只有管理员显式选择 `device/session/full（设备/会话/完全）` 才会改写标识，且普通转发与 passthrough（透传）路径保持一致。

## Codex 292 路由 Cookie 与滚动续票

- 2026-09 起，292 `x-codex-turn-state`（回合状态票）默认按约 240 秒记账，并在到期前 60 秒刷新；`gateway.openai_codex_ticket.ttl_seconds` 与 `refresh_before_seconds` 仍可显式覆盖。
- `__cfblb` / `__oailb`（路由 Cookie）按账号共用一个池，不与单张票绑定。后台打票探针、HTTP（普通请求）、passthrough（透传）和 WebSocket（长连接）握手会注入池内未过期 Cookie；没有 Cookie 时 fail-open（不额外拦截），仍按原 `fail_closed`（无票是否拒绝）规则处理票。
- 上游响应 `Set-Cookie` 中的新路由 Cookie 会回写内存池并节流持久化到账号 `extra.codex_route_cookies`；该键是服务端托管字段，账号编辑会保留当前值，导出/备份会脱敏。
- HTTP、passthrough 与 WebSocket 握手响应里新铸出的合格 292 会滚动覆盖同账号同模型的旧票，按捕获时刻重新计算 240 秒有效期。
- `GET /api/v1/admin/accounts` 与账号详情在原 `codex_turn_tickets` 外返回 `codex_route_cookies`，只含 Cookie 名称、就绪状态、捕获/过期时间和剩余秒数，不返回 Cookie 值。

- Grok 图片、视频、音频、语音、转写和 Realtime（实时）媒体模型不会再误套文本 Token（令牌）兜底价；Grok 长上下文继续使用其官方价梯，不受 OpenAI 专属账号开关影响。

## 分组真实请求归属

管理员可以用以下接口区分“请求最初进入的分组”和“最终实际处理/结算的分组”：

- `GET /api/v1/admin/usage` 返回 `requested_group_id/requested_group_name`（请求分组）与 `effective_group_id/effective_group_name`（最终分组），以及 `group_rerouted`（是否发生请求级切组）。历史记录没有初始分组证据时，请求分组回退为最终分组。
- `GET /api/v1/admin/groups/usage-summary` 在原有费用字段外返回 `today_requests`、`total_requests`，均按 `usage_logs.group_id`（最终实际分组）统计；同时返回 `today_rerouted_requests`、`total_rerouted_requests` 便于核对切组流量。

这些字段只用于管理员观测，不改变账号调度、额度租约、计费或用户侧日志字段。`group_id` 仍是本次实际结算分组；`original_group_id` 只作为请求路由证据保存。

## Sub2API v0.1.183 选择性兼容说明

本轻量网关以官方 Sub2API `v0.1.183`（2026-08-25）为对照基线，只纳入与本地请求热路径和账号可靠性直接相关的修复；不会用官方源码整包覆盖 Tokenav 自有账务、租约、分组映射或调度策略。

### OpenAI OAuth `429` 分类与失败切换

- 账号收到 `429` 时，先解析 Codex 配额窗口。5 小时或 7 天使用率达到 `100%` 时，账号按对应窗口的重置时间暂停；有明确重置时间就使用该时间，没有时间则使用安全兜底冷却。
- 没有额度耗尽信号、但有普通 `Retry-After`/重置提示的瞬时限流，才允许本地失败切换预算规定的有限同账号重试；配额耗尽类 `429` 不重复撞同一账号，会继续选择同分组其他合格账号或按既有兜底策略处理。
- 流式 HTTP `200` 内的语义 `response.failed`/顶层 `error` 不能直接套用成功响应头判断额度；分类使用语义事件自身的状态和正文，响应头只继续用于向客户端传递重试信息。
- 该分类只隔离 OpenAI OAuth 账号及对应额度维度，不连坐 API Key、其他平台或账号的其他模型；恢复后由单探测请求重新确认可用。普通客户端 `4xx`、内容审计和参数错误不触发账号长期暂停。

### 其余 v0.1.183 修复对照

- Kimi 单账号并发上限的专用 `403` 保持可恢复：安装短期账号冷却并进入同组故障切换，不增加永久凭据失效计数；其他 `403` 仍按常规规则。
- Codex `session-id` 与容量溢出粘性规则已与本地会话策略合并；容量分流不迁移持久绑定，随机模式仍不恢复旧会话绑定。
- Responses custom tool/tool search 的恢复 ID、HTTP bridge、API Key 和 WebSocket 续链使用同一规范化规则，避免工具丢失或项目 ID 前缀错误。
- Antigravity 兼容转换层对 `max_tokens`/`max_output_tokens` 施加 `64000` 安全上限；显式较小值保留，超限值在发往上游前收敛。
- Composite 监控聚合按具体目标平台和账号归属修正 SQL 条件，避免同一组合分组的统计串平台。
- 邮箱换绑别名与并发守卫属于 control-plane（主站控制面）认证职责，不在轻量网关复制；官方独有 quota monitor（额度监控）或 Ollama Cloud 架构同样不纳入。

上述兼容修复不新增对外 API 路径；管理员仍通过现有账号、分组、运行状态和用量日志接口观察冷却、尝试链、实际上游模型及错误分类。新增或调整 API 时，须同步更新本文件。

### 临时请求/响应结构调试

管理员运维设置提供临时结构调试接口：

- `GET /api/v1/admin/ops/runtime/request-debug`：读取当前开关、过期时间和内存记录数量。
- `PUT /api/v1/admin/ops/runtime/request-debug`：启用并清空记录。请求体使用 `preset=5m|15m|30m`、可选 `max_entries`，以及采集范围 `scope`（`{"type":"all"}`、`{"type":"group","value":"<group_id>"}`、`{"type":"api_key","value":"<key_id>"}` 或 `{"type":"path","value":"/v1/responses"}`），最长 30 分钟。分组和 Key 只按数字 ID 匹配。
- `GET /api/v1/admin/ops/runtime/request-debug/records`：读取当前进程内的结构记录。
- `DELETE /api/v1/admin/ops/runtime/request-debug/records`：立即关闭并清空记录。
- `GET /api/v1/admin/ops/runtime/request-debug/full-config`：读取本地 API Key 专属完整请求/响应调试状态；仅返回 Key ID、TTL、记录和容量统计。
- `PUT /api/v1/admin/ops/runtime/request-debug/full-config`：为一个本地 `glk_` API Key 开启临时完整正文采集。请求体支持 `preset=5m|15m|30m`、`api_key_id`（必填）以及 `max_records`、`max_body_bytes`、`max_total_bytes`；数据仅保存在进程内内存，敏感请求头自动脱敏。
- `POST /api/v1/admin/ops/runtime/request-debug/full-config/disable`：立即关闭并清空完整正文记录。
- `GET /api/v1/admin/ops/runtime/request-debug/full-records`：读取完整请求/响应正文记录（正文以 UTF-8 或 Base64 标记返回）。
- `DELETE /api/v1/admin/ops/runtime/request-debug/full-records`：清空完整正文记录但保持调试开关。

该功能默认关闭，仅采集客户 `/v1/*` 的 JSON HTTP 请求：请求头和响应头只保留名称及出现次数，正文和响应只保留 Content-Type、字节数、截断标记以及 JSON 字段路径、类型和数组长度，绝不保存字段值、Prompt（提示词）、Cookie（登录凭据）、Token（令牌）、API Key（接口密钥）或媒体内容。请求和响应结构均只保存在进程内有界内存环形缓冲区，自动过期或关闭后清空，不写入 PostgreSQL（数据库）、Redis（缓存服务）或文件日志。每条记录附带 `api_key_id`、`api_key_ref`、`group_id`、`group_name` 供范围核对；这些是内部标识，不包含密钥秘密段。当前不采集 SSE（流式事件）和 WebSocket（长连接）帧，也不解析非 JSON 请求正文。

## 插件系统（官方 v0.2.7 对齐）

管理前缀：`/api/v1/admin/plugins`。协议与官方 `Wei-Shaw/sub2api` `v0.2.7` 一致：只接受签名 `.s2plugin`，运行时只接管 OpenAI OAuth 出站 HTTP。

只读接口不消耗 step-up（二次验证）票据：

- `GET /api/v1/admin/plugins`：已安装插件列表。
- `GET /api/v1/admin/plugins/:id`：单个插件。
- `GET /api/v1/admin/plugins/:id/config`：插件配置 JSON（裸响应，不是统一信封）。
- `GET /api/v1/admin/plugins/:id/status`：被动运行状态。插件未运行时返回 `healthy=false`、`message=插件未运行`，不会为了查询而启动进程。成功信封的 `data` 为：

```json
{
  "healthy": true,
  "message": "",
  "status_json": "{}"
}
```

`status_json` 是插件自定义的被动状态 JSON 字符串，只用于配置界面展示；读取它不得改配置、访问上游或产生副作用。
网关版本线已与官方对齐：`cmd/server/VERSION` 为 `0.2.7`，与官方 `v0.2.7` 插件协议同一版本线发布；插件清单 `requires.sub2api` 直接用该版本评估，范围表达式同时支持 `">= 0.2.5"`（空格分隔）与 `">=0.2.5"`（紧贴）两种官方写法。官方发行插件安装后即可落在 `compatible` 状态；清单未把 `0.2.7` 列入 `tested_sub2api_versions` 时仍是 `untested`，启用需要管理员 `accept_untested` 确认。官方插件内置信任根：插件 ID `local.sub2api.openai-transport`、密钥 ID `sub2api-openai-transport-v1`（公钥内置，不受部署配置覆盖）。

以下写操作在 `security.step_up_enabled=true` 时必须携带 `X-Step-Up-Token`，scope 与操作一一对应。开关关闭时中间件放行，升级后不会锁死后台：

| 方法与路径 | step-up scope |
|---|---|
| `POST /api/v1/admin/plugins/upload` | `plugin.upload` |
| `POST /api/v1/admin/plugins/:id/enable` | `plugin.enable` |
| `POST /api/v1/admin/plugins/:id/disable` | `plugin.disable` |
| `DELETE /api/v1/admin/plugins/:id` | `plugin.delete` |
| `PUT /api/v1/admin/plugins/:id/config` | `plugin.config` |
| `POST /api/v1/admin/plugins/:id/test` | `plugin.test` |

`POST /api/v1/admin/plugins/:id/ui-session` 仍只要求管理员登录，不消耗 step-up。配置 iframe 通过 UI Bridge 调用 `plugin.status`，宿主转成上面的只读状态接口。

## 官方 v0.2.13 对齐说明（2026-10-03）

本轮把网关版本线从 `0.2.7` 对齐全官最新 tag **`0.2.13`**，并对官方 `v0.2.8`～`v0.2.13` 的 79 条发布条目逐条过筛。完整结论与理由见 `docs/modules/upgrade-v0.2.13.md`，本节只记录**对外可观测**的行为变化。

### 1. 分组模型白名单支持通配符位置匹配（glob）

- `models` 白名单条目中的 `*` **不再限制在末尾**，可出现在开头、中间或结尾；匹配是锚定的（首尾对齐）且大小写不敏感。
  - 例：`gpt-*-codex-*`、`*sonnet*` 均可保存并生效；此前 `*` 只能位于末尾，其他位置会返回 HTTP `400`、`INVALID_MODEL_ALLOWLIST`。
- 准入（`Allows`）与列表输出（`FilterForListing`）保持一致：列表会把通配条目展开为来源集合中匹配的模型，并保持来源顺序、全局去重。
- 白名单匹配额外容忍客户端附加的**推理档位 / `codex` 中缀**：`gpt-5.5-codex-high` 可命中白名单中的 `gpt-5.5`。该容忍仅作用于白名单匹配；**计费归一化仍走严格路径**，不会因未知型号被误按旗舰价计费。
- `enabled=true` 且列表为空仍视为配置错误（HTTP `400`），语义不变。

### 2. OpenAI 透传账号的模型发现

- 启用「自动透传（仅替换认证）」的 OpenAI 账号（`IsOpenAIPassthroughEnabled`）现在与「未配置模型映射」的账号同样补齐平台默认模型清单，不再因为存在 `model_mapping` 而隐藏已映射模型。

### 3. 新增模型支持（模型目录与路由）

模型计费目录（`resources/model-pricing/model_prices_and_context_window.json`）新增 6 个条目，与官方目录键集合一致（209 条）：

| 模型 ID | 来源版本 | 说明 |
|---|---|---|
| `gpt-6-sol` | 0.2.8 | GPT-6 Sol |
| `gpt-6-luna` | 0.2.8 | GPT-6 Luna |
| `claude-opus-5-5` | 0.2.8 | Claude Opus 5.5 |
| `grok-4.7` | 0.2.8 | Grok 4.7 |
| `claude-sonnet-5-5` | 0.2.10 | Claude Sonnet 5.5；Bedrock 走 `global.anthropic.claude-sonnet-5-5` |
| `gpt-6.1-sol` | 0.2.11 | GPT-6.1 Sol；Codex 基线指令使用官方嵌入元数据 |

配套同步：

- Claude 模型常量新增 `claude-opus-5-5`、`claude-sonnet-5-5`；推理档位族支持两者，并新增 `IsOpus55` / `IsSonnet55` 判定。
- 归一化新增 Bedrock 区域/全局前缀剥离（`us.` / `eu.` / `apac.` / `jp.` / `au.` / `us-gov.` / `global.`）与 OpenRouter 点号小版本（`claude-opus-5.5` → `claude-opus-5-5`）。
- Codex 模型映射表新增 `gpt-6.1-sol` / `gpt-6-sol` / `gpt-6-luna` / `gpt-6-astra` 与其版本前缀。
- `gpt-6.1-sol` 不支持 `none` / `minimal` 推理档位：网关在兼容入口显式拒绝（不再静默抬高档位），并在 Anthropic 桥接路径把 `gpt-6.1-sol-<effort>` 归一为模型 + 档位。

### 4. 工具 Schema 清洗

- Responses 工具 Schema 中 `required: null` 会被删除（符合 JSON Schema 要求，避免 xAI / Moonshot 直接 `400`）；缺省与空数组语义等价，客户端意图不变。
- 清洗上下文扩展到 Anthropic Messages 的 `input_schema`（`/v1/messages` 工具同样进入清洗），并把 `contentSchema` 视为子模式容器。
- `prefixItems` / 元组数组的处理沿用既有实现。

### 5. OpenAI 桥接的 thinking 语义

- 当 Anthropic 桥接结果明确为 `none`（客户端禁用 thinking）时，网关不再用 `max` / `xhigh` 修正覆盖，保持客户端意图。

### 6. 明示不引入的官方能力（网关侧）

以下官方能力**不在轻量网关落地**：TypeSafe（Jev System One）平台、充值优惠阶梯、API Key 创建限制与余额在途预占、账号额度重置兑换、备份/联盟推广/日志保留策略、运营侧风控用户白名单、官方安装向导与官方 `frontend/`。理由（业务边界 / 技术边界 / 分批实施）见 `docs/modules/upgrade-v0.2.13.md` 第一节。
