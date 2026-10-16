/* ============================================================
 * 用量明细行「展开详情」数据构建（Win12 重构版）
 * 字段契约（全部经后端代码核实，禁止臆造）：
 *  - internal/handler/dto/types.go         AdminUsageLog / UsageLog
 *  - internal/gatewaylite/rate_policy.go   RatePolicyAudit 费率证据对象
 * 展示文案对照旧 frontend/src/components/admin/usage/UsageTable.vue：
 *  映射链 a→b→c、优先级模式/动作/判定来源、切组链 requested→effective、
 *  缓存写 5m/1h 拆分、账号倍率与账号成本口径。
 * 输出统一为「分组 → 标签+值行」，由 UsageDetailPanel.vue 渲染小网格。
 * ============================================================ */
import dayjs from 'dayjs'

/* ---------- 优先级策略文案（与后端枚举取值一一对应） ---------- */
// priority_policy_mode：follow_request / key_prefer_priority / force_priority / force_standard
export const PRIORITY_MODE_LABELS = {
  follow_request: '跟随请求',
  key_prefer_priority: 'Key 偏好优先',
  force_priority: '强制优先',
  force_standard: '强制标准',
}
// priority_policy_action（对照旧 getPriorityPolicyActionLabel）
export const PRIORITY_ACTION_LABELS = {
  pass: '放行',
  filter: '过滤降级',
  block: '拦截',
  force_priority: '强制优先',
  filter_invalid: '过滤（响应无效）',
}
// priority_detection_source（对照旧 getPriorityDetectionSourceLabel）
export const PRIORITY_SOURCE_LABELS = {
  request: '请求声明',
  response: '上游响应',
  request_response_invalid: '请求声明（响应无效后回退）',
  standard_default: '默认标准档',
  standard_response_invalid: '标准档（响应无效后确认）',
  historical: '历史请求（无判定证据）',
}

/* ---------- 详情网格口径的数值格式化（金额 6 位 / 倍率 4 位，对齐旧 tooltip 精度） ---------- */
const fmtMoney6 = (v) => (v === null || v === undefined || Number.isNaN(Number(v)) ? '-' : `$${Number(v).toFixed(6)}`)
const fmtMul = (v) => (v === null || v === undefined || Number.isNaN(Number(v)) ? '-' : `${Math.round(Number(v) * 10000) / 10000}x`)
const fmtInt = (n) => Number(n || 0).toLocaleString('zh-CN')

/* RatePolicyAudit → 标签+值行。
 * 这是请求开始时冻结的销售倍率证据（仅管理员可见）：账号采购成本倍率不在此结构内，
 * 不允许把采购成本与客户扣费混算，故这里只展示销售侧口径。 */
const buildRateAuditRows = (audit) => {
  if (!audit || typeof audit !== 'object') return []
  const rows = [
    { k: '计费口径', v: audit.billing_scope || '-' },
    { k: '分组倍率', v: fmtMul(audit.group_rate_multiplier) },
  ]
  // 用户专属倍率是可选覆盖（omitempty），仅在配置过时展示
  if (audit.user_rate_multiplier != null) rows.push({ k: '用户专属倍率', v: fmtMul(audit.user_rate_multiplier) })
  rows.push(
    { k: '解析后基础倍率', v: fmtMul(audit.resolved_rate_multiplier) },
    {
      k: '峰时计费',
      v: audit.peak_rate_enabled
        ? `启用（${audit.peak_start || '-'} ~ ${audit.peak_end || '-'} · ${fmtMul(audit.peak_rate_multiplier)}）`
        : '未启用',
    },
    { k: '峰时实乘倍率', v: fmtMul(audit.applied_peak_multiplier) },
    { k: '最终生效倍率', v: fmtMul(audit.effective_rate_multiplier), cls: 'hl' },
    { k: '解析时区', v: audit.timezone || '-' },
  )
  if (audit.resolved_at_millis > 0) rows.push({ k: '证据冻结时间', v: dayjs(audit.resolved_at_millis).format('YYYY-MM-DD HH:mm:ss') })
  if (audit.version) rows.push({ k: '策略版本', v: String(audit.version), mono: true })
  return rows
}

/* AdminUsageLog → 分组详情数据。
 * 值为 null 的占位行被剔除、全空的组不产出；'-' 是「有字段但为空」的显式展示。 */
export const buildDetailGroups = (r) => {
  if (!r) return []
  const groups = []
  const push = (title, rows) => {
    const list = rows.filter((x) => x && x.v !== undefined && x.v !== null && x.v !== '')
    if (list.length) groups.push({ title, rows: list })
  }

  /* 1. 请求标识：request_id + 入站/上游端点（等宽字体，便于与日志对拍） */
  push('请求标识', [
    { k: 'request_id', v: r.request_id || '-', mono: true },
    { k: '入站端点', v: r.inbound_endpoint?.trim() || '-', mono: true },
    { k: '上游端点', v: r.upstream_endpoint?.trim() || '-', mono: true },
  ])

  /* 2. 模型映射链 a→b→c（后端以 → 连接）；补上游响应模型与一致性结论，
   *    映射失效/渠道换模的审计线索（主列已有不一致警示，这里看全链） */
  const chain = String(r.model_mapping_chain || '').trim()
  push('模型映射', [
    chain ? { k: '映射链', v: chain, mono: true } : null,
    r.upstream_model ? { k: '上游模型', v: r.upstream_model, mono: true } : null,
    r.upstream_response_model ? { k: '上游响应模型', v: r.upstream_response_model, mono: true } : null,
    r.upstream_model_mismatch === true
      ? { k: '一致性', v: '不一致（上游实际模型与映射预期不符）', cls: 'bad' }
      : null,
  ])

  /* 3. 优先级策略：mode/action/rule_id/fallback/detection_source。
   *    四个判定字段全空视为该请求未走优先级策略，整组隐藏
   *    （fallback 是非指针 bool 恒返回 false，不能单独作为展示依据）。 */
  if (r.priority_policy_mode || r.priority_policy_action || r.priority_policy_rule_id || r.priority_detection_source) {
    push('优先级策略', [
      { k: '策略模式', v: PRIORITY_MODE_LABELS[r.priority_policy_mode] || r.priority_policy_mode || '-' },
      { k: '策略动作', v: PRIORITY_ACTION_LABELS[r.priority_policy_action] || r.priority_policy_action || '-' },
      { k: '命中规则', v: r.priority_policy_rule_id || '-', mono: true },
      { k: '降级回退', v: r.priority_policy_fallback ? '是（证据不足按标准档回退）' : '否' },
      { k: '判定来源', v: PRIORITY_SOURCE_LABELS[r.priority_detection_source] || r.priority_detection_source || '-' },
    ])
  }

  /* 4. 费率证据：请求开始时冻结的销售倍率快照（key-value 网格） */
  const auditRows = buildRateAuditRows(r.rate_policy_audit)
  if (auditRows.length) groups.push({ title: '费率证据（请求时冻结）', rows: auditRows })

  /* 5. 倍率与成本三口径：实际扣费（用户钱包）/ 标准成本 / 账号采购成本，彼此不可混算 */
  push('倍率与成本', [
    { k: '实际扣费（用户）', v: fmtMoney6(r.actual_cost), cls: 'hl' },
    { k: '标准成本', v: fmtMoney6(r.total_cost) },
    { k: '销售倍率', v: fmtMul(r.rate_multiplier || 1) },
    r.account_rate_multiplier != null ? { k: '账号倍率', v: fmtMul(r.account_rate_multiplier) } : null,
    r.account_stats_cost != null ? { k: '账号成本（采购口径）', v: fmtMoney6(r.account_stats_cost) } : null,
  ])

  /* 6. 缓存写入：总量 + 5m/1h TTL 拆分（TTL 差价证据；全部为 0 的行不展示） */
  if (Number(r.cache_creation_tokens) > 0 || Number(r.cache_creation_5m_tokens) > 0 || Number(r.cache_creation_1h_tokens) > 0) {
    push('缓存写入', [
      { k: '写总量', v: fmtInt(r.cache_creation_tokens) },
      { k: '其中 5m TTL', v: fmtInt(r.cache_creation_5m_tokens) },
      { k: '其中 1h TTL', v: fmtInt(r.cache_creation_1h_tokens) },
    ])
  }

  /* 7. 切组链：内容策略临时切组时 requested ≠ effective，主列只显示生效分组，
   *    这里看完整链。判定与旧 hasGroupReroute 一致：group_rerouted 标记优先，
   *    其次按 requested/effective 分组 ID 对比兜底。 */
  const rerouted =
    r.group_rerouted === true ||
    (r.requested_group_id != null && r.effective_group_id != null && r.requested_group_id !== r.effective_group_id)
  if (rerouted) {
    push('分组路由（切组链）', [
      { k: '请求分组', v: String(r.requested_group_name || '').trim() || '-' },
      { k: '生效分组', v: String(r.effective_group_name || '').trim() || '-', cls: 'warn', badge: '已切组' },
    ])
  }

  /* 8. 身份归属：对象字段展开（user/api_key/account）+ 主站身份（远程 Key 快照） */
  push('身份归属', [
    r.user?.email ? { k: '用户邮箱', v: r.user.email } : null,
    r.user?.username ? { k: '用户名', v: r.user.username } : null,
    r.api_key?.name ? { k: 'API Key', v: r.api_key.name } : null,
    r.account?.name ? { k: '账号', v: r.account.platform ? `${r.account.name}（${r.account.platform}）` : r.account.name } : null,
    r.main_site_username ? { k: '主站用户名', v: r.main_site_username } : null,
    r.main_site_email ? { k: '主站邮箱', v: r.main_site_email } : null,
  ])

  return groups
}
