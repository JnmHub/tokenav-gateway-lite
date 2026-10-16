<script setup>
import { computed, ref, watch } from 'vue'
import { message, Modal } from 'ant-design-vue'
import dayjs from 'dayjs'
import { request } from '../../api'
import { PlusOutlined, DeleteOutlined } from '@ant-design/icons-vue'
import { PLATFORMS, platformLabel, hdrTemplateFor, isHdrEligible, GROK_BASE_PRESETS, CN_PLATFORMS, CN_BASE_PRESETS, TIMEZONES, BEDROCK_REGIONS,
  resolveOpenCodeMode, defaultOpenCodeRules, defaultOpenCodeBaseUrl, serializeOpenCodeRules, parseOpenCodeRules } from './platformPresets'
import ModelWhitelistPanel from './ModelWhitelistPanel.vue'
import AccountDisguisePanel from './AccountDisguisePanel.vue'
import AccountQuotaResetPanel from './AccountQuotaResetPanel.vue'
import OpenCodeGoProtocolRulesEditor from './OpenCodeGoProtocolRulesEditor.vue'

/* ============================================================
 * 账号创建/编辑统一编辑器（对照旧 CreateAccountModal/EditAccountModal）
 * - 类别分段（创建）：API Key / Bedrock(anthropic) / Vertex 服务账号(gemini|anthropic)
 *   bedrock credentials: {auth_mode, aws_region, aws_access_key_id, aws_secret_access_key,
 *   aws_session_token?, api_key?, aws_force_global?}（结构逐字核实自旧版 5444-5462 行）
 *   service_account credentials: {service_account_json, project_id, client_email, location,
 *   tier_id:'vertex'}（旧版 5541-5549 行）
 * - 分区导航：基础 / 连接 / 调度与容量 / 分组与代理 / 模型与高级（旧 accountEditorSections 等价）
 * - 补齐：分组模型映射（POST/PUT 顶层 group_model_mappings）、过期自动暂停
 *   auto_pause_on_expired、混合渠道风险确认 confirm_mixed_channel_risk
 * - 补齐：伪装与中继（TLS 伪装/会话 ID 伪装/缓存 TTL 覆写/自定义中继，anthropic
 *   oauth/setup-token 专属，AccountDisguisePanel）+ 配额固定重置（apikey/bedrock，
 *   AccountQuotaResetPanel）——extra 键写入语义见各子面板头注释
 * ============================================================ */

const props = defineProps({
  open: { type: Boolean, default: false },
  account: { type: Object, default: null }, // null = 创建
  groups: { type: Array, default: () => [] },
  proxies: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:open', 'saved'])

const isEdit = computed(() => !!props.account?.id)
const openRef = computed({
  get: () => props.open,
  set: (v) => emit('update:open', v),
})

const SECTION_BASE = 564
const SECTIONS = [
  { key: 'base', label: '基础信息' },
  { key: 'conn', label: '连接凭据' },
  { key: 'sched', label: '调度与容量' },
  { key: 'grp', label: '分组与代理' },
  { key: 'adv', label: '模型与高级' },
]
const activeSection = ref('base')
const jump = (k) => {
  activeSection.value = k
  const el = document.getElementById(`acct-sec-${k}`)
  el?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

// ---------- 表单状态 ----------
const category = ref('apikey') // apikey | bedrock | service_account
const f = ref({})
const saving = ref(false)
// 分组模型映射行编辑器：[{gid, src, dst}]；gmmDirty 才在保存时携带
const gmmRows = ref([])
const gmmDirty = ref(false)
const addGmmRow = () => { gmmRows.value.push({ gid: props.groups[0]?.id ?? null, src: '', dst: '' }); gmmDirty.value = true }
const delGmmRow = (i) => { gmmRows.value.splice(i, 1); gmmDirty.value = true }

// SA JSON 粘贴 → 自动抽取 project_id / client_email（旧 applyVertexServiceAccountJson 等价）
const saHint = ref('')
const onSaJson = (raw) => {
  saHint.value = ''
  if (!raw?.trim()) return
  try {
    const parsed = JSON.parse(raw)
    const pid = typeof parsed.project_id === 'string' ? parsed.project_id.trim() : ''
    const email = typeof parsed.client_email === 'string' ? parsed.client_email.trim() : ''
    if (!pid || !email || typeof parsed.private_key !== 'string') { saHint.value = 'JSON 缺少 project_id / client_email / private_key'; return }
    f.value.sa_project_id = pid
    f.value.sa_client_email = email
    saHint.value = `已识别：${pid} · ${email}`
  } catch { saHint.value = 'JSON 格式无效' }
}

// 混合渠道：所选分组与账号平台不一致时要求确认（后端 ConfirmMixedChannelRisk）
const mixedRisk = computed(() => {
  if (!f.value.platform) return false
  return (f.value.group_ids || []).some((gid) => {
    const g = props.groups.find((x) => x.id === gid)
    if (!g) return false
    return g.platform !== f.value.platform && !(f.value.platform === 'antigravity' && ['anthropic', 'gemini'].includes(g.platform))
  })
})

// 伪装与中继：仅 anthropic oauth/setup-token（后端 dto/mappers.go 回读范围，与旧前端一致）
const isAnthropicOAuth = computed(() => f.value.platform === 'anthropic' && ['oauth', 'setup-token'].includes(f.value.type || ''))
// 配额固定重置：apikey/bedrock（创建态用类别，编辑态用类型；创建可配，POST body.extra 同样支持）
const quotaResetType = computed(() => ['apikey', 'bedrock'].includes(f.value.type || category.value))

const reset = (acc) => {
  activeSection.value = 'base'
  saHint.value = ''
  gmmDirty.value = false
  if (!acc) {
    category.value = 'apikey'
    f.value = {
      name: '', platform: 'openai', status: 'active', notes: '',
      base_url: '', api_key: '',
      br_auth_mode: 'sigv4', br_region: 'us-east-1', br_key_id: '', br_secret: '', br_token: '', br_api_key: '', br_global: false,
      sa_json: '', sa_project_id: '', sa_client_email: '', sa_location: 'us-central1',
      priority: 0, schedulable: true, concurrency: 1, load_factor: null, rate_multiplier: 1,
      expires_at: null, auto_pause_on_expired: true,
      group_ids: [], proxy_id: 0, confirm_mixed_risk: false,
  __pool: [], __poolConc: {}, __mainConc: null,
      model_whitelist: [], upstream_timezone: '',
      hdr_on: false, hdr_rows: [],
      cn_mode: 'payg', cn_protocol: 'chat_completions',
      // OpenCode（opencode_go）：接入模式（zen 按量 / go 订阅）+ API 协议（adaptive 默认）+ 协议规则行
      oc_mode: 'zen', oc_protocol: 'adaptive', oc_rules: defaultOpenCodeRules('zen'),
      ant_mixed: false, ant_overages: false,
      // 伪装与中继（anthropic oauth/setup-token）/ 配额固定重置（apikey/bedrock）默认值
      tls_on: false, tls_profile_id: null, masking_on: false,
      ttl_on: false, ttl_target: '5m', relay_on: false, relay_url: '',
      qr_daily_mode: '', qr_daily_hour: 0, qr_weekly_mode: '', qr_weekly_day: 1, qr_weekly_hour: 0, qr_tz: '',
      // 通用策略默认均为「继承」，新增账号不应意外覆盖全局策略。
      keyword_policy_mode: 'inherit', response_model_mode: 'inherit', response_model_custom: '',
      temp_unsched_on: false, temp_unsched_rules: [],
      pool_mode: false, pool_retry_count: 1, pool_retry_codes: '', custom_error_on: false, custom_error_codes: '',
      upstream_provider: 'standard', upstream_billing_auto_probe: false,
      // OpenAI 专项：只在适用的平台/类型显示，写入时不会影响非 OpenAI 账号。
      oai_passthrough: false, oai_flatten_ns: false, oai_ws_mode: 'off', oai_long_context: false,
      oai_codex_cli_only: false, oai_allow_claude_code: false, oai_fingerprint: 'off',
      oai_compact_mode: 'auto', oai_compact_mappings: [], oai_responses_mode: 'auto', oai_capabilities: ['chat_completions', 'embeddings'],
      oai_pause5_on: false, oai_pause5_threshold: 0, oai_pause7_on: false, oai_pause7_threshold: 0,
      anth_passthrough: false, anth_auth_scheme: 'x_api_key', anth_web_search: 'default',
    }
    gmmRows.value = []
    return
  }
  const r = acc
  category.value = ['bedrock', 'service_account'].includes(r.type) ? r.type : 'apikey'
  const creds = r.credentials || {}
  f.value = {
    name: r.name || '', platform: r.platform, type: r.type, status: ['active', 'inactive', 'error'].includes(r.status) ? r.status : 'active',
    notes: r.notes || '',
    base_url: creds.base_url || '', api_key: '',
    br_auth_mode: creds.auth_mode === 'apikey' ? 'apikey' : 'sigv4', br_region: creds.aws_region || 'us-east-1',
    br_key_id: '', br_secret: '', br_token: '', br_api_key: '', br_global: creds.aws_force_global === 'true',
    sa_json: '', sa_project_id: creds.project_id || '', sa_client_email: creds.client_email || '', sa_location: creds.location || 'us-central1',
    priority: r.priority ?? 0, schedulable: r.schedulable !== false, concurrency: r.concurrency ?? 1,
    load_factor: r.load_factor ?? null, rate_multiplier: r.rate_multiplier ?? 1,
    expires_at: r.expires_at ? dayjs(r.expires_at * 1000) : null,
    auto_pause_on_expired: r.auto_pause_on_expired !== false,
    group_ids: Array.isArray(r.group_ids) ? [...r.group_ids] : [],
    proxy_id: r.proxy_id ?? 0, confirm_mixed_risk: false,
    model_whitelist: [], upstream_timezone: creds.upstream_timezone || '',
    __mapping: creds.model_mapping || {},
    hdr_on: !!creds.header_override_enabled,
    hdr_rows: Object.entries(creds.header_overrides || {}).map(([name, value]) => ({ name, value: String(value ?? '') })),
    cn_mode: creds.account_mode === 'coding' ? 'coding' : 'payg',
    cn_protocol: ['chat_completions', 'anthropic', 'responses'].includes(creds.api_protocol) ? creds.api_protocol : 'chat_completions',
    // OpenCode 回填：account_mode 仅 'zen' 视为 Zen（未设置按 go 兼容，官方 resolveOpenCodeAccountMode 语义）；
    // 协议非法/缺失回落 adaptive；协议规则缺失时按模式重置为内置默认表
    oc_mode: resolveOpenCodeMode(creds.account_mode),
    oc_protocol: ['adaptive', 'chat_completions', 'anthropic', 'responses'].includes(creds.api_protocol) ? creds.api_protocol : 'adaptive',
    oc_rules: parseOpenCodeRules(creds.protocol_rules) ?? defaultOpenCodeRules(resolveOpenCodeMode(creds.account_mode)),
    ant_mixed: r.extra?.mixed_scheduling === true,
    ant_overages: r.extra?.allow_overages === true,
    // 伪装与中继回填（dto 顶层字段，仅 anthropic oauth/setup-token 回读；
    // tls_fingerprint_profile_id=-1（随机）dto 不回传，从 extra 兜底读取）
    tls_on: r.enable_tls_fingerprint === true,
    tls_profile_id: r.tls_fingerprint_profile_id ?? (Number(r.extra?.tls_fingerprint_profile_id) || null),
    masking_on: r.session_id_masking_enabled === true,
    ttl_on: r.cache_ttl_override_enabled === true,
    ttl_target: r.cache_ttl_override_target || '5m',
    relay_on: r.custom_base_url_enabled === true,
    relay_url: r.custom_base_url || '',
    // 配额固定重置回填（extra 键；后端仅在 fixed 时回读 mode/hour，rolling 账号无键）
    qr_daily_mode: r.extra?.quota_daily_reset_mode === 'fixed' ? 'fixed' : '',
    qr_daily_hour: Number(r.extra?.quota_daily_reset_hour ?? 0),
    qr_weekly_mode: r.extra?.quota_weekly_reset_mode === 'fixed' ? 'fixed' : '',
    qr_weekly_day: Number(r.extra?.quota_weekly_reset_day ?? 1),
    qr_weekly_hour: Number(r.extra?.quota_weekly_reset_hour ?? 0),
    qr_tz: r.extra?.quota_reset_timezone || '',
    // 代理池（多出口）：extra.proxy_pool 快照恢复选中集合与每代理独立并发
    __pool: Array.isArray(r.extra?.proxy_pool) ? r.extra.proxy_pool.map((e) => e.id).filter((id) => id !== r.proxy_id) : [],
    __poolConc: Object.fromEntries((r.extra?.proxy_pool || []).map((e) => [e.id, Number(e.concurrency) > 0 ? Number(e.concurrency) : null])),
    __mainConc: Number(r.extra?.proxy_main_concurrency) > 0 ? Number(r.extra.proxy_main_concurrency) : null,
    // 高级通用 / 平台策略：读取已有值，未展示的平台保存时不会触碰这些 extra。
    keyword_policy_mode: r.extra?.keyword_policy_mode || 'inherit',
    response_model_mode: r.extra?.response_model_mode || 'inherit',
    response_model_custom: r.extra?.response_model_custom || '',
    temp_unsched_on: creds.temp_unschedulable_enabled === true,
    temp_unsched_rules: Array.isArray(creds.temp_unschedulable_rules) ? creds.temp_unschedulable_rules.map((x) => ({ error_code: x.error_code || '', duration_minutes: Number(x.duration_minutes) || 30, keywords: Array.isArray(x.keywords) ? x.keywords.join(', ') : String(x.keywords || ''), description: x.description || '' })) : [],
    pool_mode: creds.pool_mode === true, pool_retry_count: Number(creds.pool_mode_retry_count) || 1,
    pool_retry_codes: Array.isArray(creds.pool_mode_retry_status_codes) ? creds.pool_mode_retry_status_codes.join(', ') : '',
    custom_error_on: creds.custom_error_codes_enabled === true, custom_error_codes: Array.isArray(creds.custom_error_codes) ? creds.custom_error_codes.join(', ') : '',
    upstream_provider: r.extra?.upstream_provider === 'sub2api' ? 'sub2api' : 'standard', upstream_billing_auto_probe: r.extra?.upstream_billing_auto_probe_enabled === true,
    oai_passthrough: r.extra?.openai_passthrough === true || r.extra?.openai_oauth_passthrough === true,
    oai_flatten_ns: r.extra?.openai_responses_flatten_namespaces === true,
    oai_ws_mode: r.extra?.openai_oauth_responses_websockets_v2_mode || r.extra?.openai_apikey_responses_websockets_v2_mode || 'off',
    oai_long_context: r.extra?.openai_long_context_billing_enabled === true,
    oai_codex_cli_only: r.extra?.codex_cli_only === true, oai_allow_claude_code: Array.isArray(r.extra?.codex_cli_only_allowed_clients) && r.extra.codex_cli_only_allowed_clients.includes('claude_code'),
    oai_fingerprint: r.extra?.codex_fingerprint_mode || 'off', oai_compact_mode: r.extra?.openai_compact_mode || 'auto',
    oai_compact_mappings: Object.entries(creds.compact_model_mapping || {}).map(([from, to]) => ({ from, to })), oai_responses_mode: r.extra?.openai_responses_mode || 'auto',
    oai_capabilities: Array.isArray(creds.openai_capabilities) ? creds.openai_capabilities : ['chat_completions', 'embeddings'],
    oai_pause5_on: r.extra?.auto_pause_5h_disabled !== true && Number(r.extra?.auto_pause_5h_threshold) > 0, oai_pause5_threshold: Number(r.extra?.auto_pause_5h_threshold) || 0,
    oai_pause7_on: r.extra?.auto_pause_7d_disabled !== true && Number(r.extra?.auto_pause_7d_threshold) > 0, oai_pause7_threshold: Number(r.extra?.auto_pause_7d_threshold) || 0,
    anth_passthrough: r.extra?.anthropic_passthrough === true, anth_auth_scheme: r.extra?.anthropic_apikey_auth_scheme || 'x_api_key', anth_web_search: r.extra?.web_search_emulation || 'default',
  }
  // 白名单 = model_mapping 身份映射；非身份映射保留回传（Panel 用 {from,to} 对象行编辑）
  const wl = []
  const keep = []
  for (const [from, to] of Object.entries(f.value.__mapping || {})) {
    if (from === to) wl.push(from)
    else keep.push({ from, to })
  }
  f.value.model_whitelist = wl
  f.value.__keep_mappings = keep
  // 分组模型映射回填（顶层字段）
  // 回填兼容：顶层 group_model_mappings 或 account_groups[].model_mapping（本网关实际存储形态）
  const gmm = r.group_model_mappings || Object.fromEntries(
    (r.account_groups || [])
      .filter((b) => b.model_mapping && Object.keys(b.model_mapping).length > 0)
      .map((b) => [String(b.group_id), b.model_mapping])
  )
  gmmRows.value = Object.entries(gmm).flatMap(([gid, m]) => Object.entries(m || {}).map(([src, dst]) => ({ gid: Number(gid), src, dst })))
}
watch(() => [props.open, props.account], ([o]) => { if (o) reset(props.account) })

const platformOptions = PLATFORMS.map((v) => ({ value: v, label: platformLabel(v) }))
// ===== OpenCode（opencode_go）联动 =====
const isOpenCode = computed(() => f.value.platform === 'opencode_go')
// 协议选项：adaptive 默认（按模型分流），三原生协议可 pinned 固定
const ocProtocolOptions = [
  { value: 'adaptive', label: 'adaptive（自适应分流）' },
  { value: 'chat_completions', label: 'Chat Completions' },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'responses', label: 'Responses' },
]
// 选定平台为 opencode_go 时初始化默认端点（官方 selectOpenCodeGoPlatform 语义）
watch(() => f.value.platform, (p) => {
  if (p !== 'opencode_go' || isEdit.value) return
  if (!f.value.base_url) f.value.base_url = defaultOpenCodeBaseUrl(f.value.oc_mode, f.value.oc_protocol)
})
// 接入模式切换：base_url 若仍等于旧组合默认值（或为空）则跟随新默认；
// 协议规则若未被自定义（仍等于旧模式默认表）则同步刷新为新模式默认表（官方 watch(openCodeAccountMode) 语义）
watch(() => f.value.oc_mode, (mode, prev) => {
  if (!isOpenCode.value) return
  const prevBase = defaultOpenCodeBaseUrl(prev, f.value.oc_protocol)
  if (!f.value.base_url || f.value.base_url === prevBase) f.value.base_url = defaultOpenCodeBaseUrl(mode, f.value.oc_protocol)
  if (JSON.stringify(f.value.oc_rules) === JSON.stringify(defaultOpenCodeRules(prev))) f.value.oc_rules = defaultOpenCodeRules(mode)
})
// pinned 协议切换：非 adaptive 时 base_url 若仍等于旧默认则跟随（adaptive→anthropic 等组合变化）
watch(() => f.value.oc_protocol, (proto, prev) => {
  if (!isOpenCode.value) return
  const prevBase = defaultOpenCodeBaseUrl(f.value.oc_mode, prev)
  if (!f.value.base_url || f.value.base_url === prevBase) f.value.base_url = defaultOpenCodeBaseUrl(f.value.oc_mode, proto)
})
// 保存前写入 OpenCode 凭据键（官方 applyOpenCodeGoProtocolRules 语义：create 非空才写 rules，edit 始终写）
const applyOpenCodeCreds = (creds, mode) => {
  creds.account_mode = f.value.oc_mode
  creds.api_protocol = f.value.oc_protocol
  const rules = serializeOpenCodeRules(f.value.oc_rules)
  if (rules.length > 0 || mode === 'edit') creds.protocol_rules = rules
  else delete creds.protocol_rules
}
const statusOptions = computed(() => {
  const opts = [{ value: 'active', label: 'active（启用）' }, { value: 'inactive', label: 'inactive（停用）' }]
  if (f.value.status === 'error') opts.push({ value: 'error', label: 'error（错误）' })
  return opts
})
const groupOptions = computed(() => props.groups.map((g) => ({ value: g.id, label: `${g.name} (#${g.id})` })))
const proxyOptions = computed(() => [
  { value: 0, label: '直连（不使用代理）' },
  ...props.proxies.map((p) => ({ value: p.id, label: `${p.name} (${p.protocol}://${p.host}:${p.port})` })),
])
// ===== 代理池（多出口）=====
// 池候选：全部代理（主代理标注「主」且不可再勾选）
const poolCandidates = computed(() => props.proxies)
const poolName = (id) => props.proxies.find((p) => p.id === id)?.name || `#${id}`
const togglePool = (id) => {
  if (f.value.proxy_id === id) return // 主代理不进池
  const list = f.value.__pool.includes(id)
    ? f.value.__pool.filter((x) => x !== id)
    : [...f.value.__pool, id]
  f.value.__pool = list
}
// 池成员实时观测（账号详情接口的 proxy_stats）：按 host:port 与 proxy_key 匹配，
// 展示在途/上限 + 最近 5 次请求成败点（旧 poolProxyStat 语义）
const poolStat = (id) => {
  const p = props.proxies.find((x) => x.id === id)
  if (!p) return null
  const key = `${p.host}:${p.port}`
  return (props.account?.proxy_stats || []).find((s) => s.proxy_key === key) || null
}
const categoryOptions = computed(() => {
  // antigravity 的「上游（Upstream）」= apikey 类别（base_url + api_key），仅换显示文案
  const apiKeyLabel = f.value.platform === 'antigravity' ? '上游（Upstream）' : 'API Key'
  const list = [{ value: 'apikey', label: apiKeyLabel }]
  if (['anthropic'].includes(f.value.platform)) list.push({ value: 'bedrock', label: 'AWS Bedrock' })
  if (['gemini', 'anthropic'].includes(f.value.platform)) list.push({ value: 'service_account', label: 'Vertex 服务账号' })
  if (!list.some((o) => o.value === category.value)) category.value = 'apikey'
  return list
})

// ---------- 保存 ----------
const buildCommonBody = () => ({
  name: f.value.name.trim(),
  notes: f.value.notes,
  status: f.value.status,
  priority: Number(f.value.priority) || 0,
  group_ids: (f.value.group_ids || []).map(Number),
  // 未选代理时必须省略字段（旧前端传 null）：后端把 0 当真实外键解析会 500
  proxy_id: Number(f.value.proxy_id) > 0 ? Number(f.value.proxy_id) : undefined,
  concurrency: Number(f.value.concurrency) || 1,
  load_factor: f.value.load_factor == null || Number(f.value.load_factor) <= 0 ? 0 : Number(f.value.load_factor),
  rate_multiplier: f.value.rate_multiplier == null ? 1 : Number(f.value.rate_multiplier),
  expires_at: (() => { const d = f.value.expires_at ? dayjs(f.value.expires_at) : null; return d && d.isValid() ? d.unix() : 0 })(),
})
const buildGmm = () => {
  const map = {}
  for (const r of gmmRows.value) {
    const src = String(r.src || '').trim(); const dst = String(r.dst || '').trim()
    if (!src || !dst || r.gid == null) continue
    map[r.gid] = map[r.gid] || {}; map[r.gid][src] = dst
  }
  return map
}
const buildMappingCreds = (creds) => {
  // 白名单 → model_mapping 身份映射；非身份映射原样保留；* 条目跳过（旧规则）
  // __keep_mappings 支持 [from,to] 数组（reset 回填）与 {from,to} 对象（Panel 编辑）两种形态
  const mapping = {}
  for (const item of f.value.model_whitelist || []) {
    const t = String(item).trim()
    if (t && !t.includes('*')) mapping[t] = t
  }
  for (const m of f.value.__keep_mappings || []) {
    const from = String(Array.isArray(m) ? m[0] : m.from || '').trim()
    const to = String(Array.isArray(m) ? m[1] : m.to || '').trim()
    if (from && to) mapping[from] = to
  }
  if (Object.keys(mapping).length) creds.model_mapping = mapping
  else delete creds.model_mapping
  return creds
}
// Header 覆写写入凭据（旧 applyHeaderOverride 语义：开=写两键；关且编辑=删两键）
const applyHdr = (creds, mode) => {
  if (!isHdrEligible(f.value.platform, f.value.type || category.value)) return
  if (f.value.hdr_on) {
    const obj = {}
    for (const r of f.value.hdr_rows) {
      const name = r.name.trim()
      if (name) obj[name] = r.value.trim()
    }
    creds.header_override_enabled = true
    creds.header_overrides = obj
  } else if (mode === 'edit') {
    delete creds.header_override_enabled
    delete creds.header_overrides
  }
}
const addHdrRow = () => f.value.hdr_rows.push({ name: '', value: '' })
const rmHdrRow = (i) => f.value.hdr_rows.splice(i, 1)
// 国产平台预设一键应用：端点+账号模式+协议三者配套写入
const pickCnPreset = (p) => {
  f.value.base_url = p.url
  f.value.cn_mode = p.mode
  f.value.cn_protocol = p.protocol
}
// 模板填充：按平台补齐缺失的官方客户端请求头（值留空由管理员填写）
const fillHdrTemplate = () => {
  const exist = new Set(f.value.hdr_rows.map((r) => r.name.trim().toLowerCase()).filter(Boolean))
  for (const name of hdrTemplateFor(f.value.platform)) {
    if (!exist.has(name)) f.value.hdr_rows.push({ name, value: '' })
  }
}
// ===== 账号级分组映射：保存前单点风险校验（旧 ensureGroupModelMappingRiskConfirmed 等价）=====
// POST /admin/accounts/validate-group-model-mappings：咨询性检查——若映射后某分组
// 只剩本账号可服务某模型（single_point_risk），弹明细确认；同一映射内容不重复弹。
let gmmRiskConfirmedKey = ''
const buildGmmKey = (mappings) => JSON.stringify({
  platform: f.value.platform,
  account_id: props.account?.id ?? null,
  group_ids: [...(f.value.group_ids || [])].sort((a, b) => a - b),
  group_model_mappings: mappings,
})
const ensureGmmRiskConfirmed = async () => {
  if (!gmmDirty.value) return true
  const mappings = buildGmm()
  if (!Object.keys(mappings).length) return true
  const key = buildGmmKey(mappings)
  if (key === gmmRiskConfirmedKey) return true
  const result = await request('/admin/accounts/validate-group-model-mappings', {
    method: 'POST',
    body: {
      platform: f.value.platform,
      account_id: props.account?.id,
      group_ids: f.value.group_ids || [],
      group_model_mappings: mappings,
    },
  })
  const risky = (result?.items || []).filter((it) => it.single_point_risk)
  if (!risky.length) return true
  const details = risky.map((it) => {
    const g = props.groups.find((c) => c.id === it.group_id)
    return `${g ? `${g.name} (#${g.id})` : `#${it.group_id}`}：${it.requested_model} → ${it.mapped_model}（有效账号 ${it.effective_accounts} 个）`
  }).join('；')
  return new Promise((resolve) => {
    Modal.confirm({
      title: '分组映射单点风险确认',
      content: `保存后以下映射将使分组仅依赖此账号提供对应模型：${details}。仍要保存吗？`,
      okText: '仍要保存',
      cancelText: '返回修改',
      onOk: () => { gmmRiskConfirmedKey = key; resolve(true) },
      onCancel: () => resolve(false),
    })
  })
}
// ===== 伪装与中继 / 配额固定重置：extra 键写入（旧 EditAccountModal 5141-5175、5374-5394 语义）=====
// 开=写键，关=删键；两组各自只在对应账号类型上生效——
// 面板不展示的类型不触碰 extra（避免把表单无法回显的既有状态误清除）。
const applyConfigExtraPatches = (extra) => {
  if (isAnthropicOAuth.value) {
    // TLS 伪装：profileId null=删键（平台内置默认），-1=随机，>0=绑定指定配置
    if (f.value.tls_on) {
      extra.enable_tls_fingerprint = true
      if (f.value.tls_profile_id) extra.tls_fingerprint_profile_id = f.value.tls_profile_id
      else delete extra.tls_fingerprint_profile_id
    } else {
      delete extra.enable_tls_fingerprint
      delete extra.tls_fingerprint_profile_id
    }
    // 会话 ID 伪装
    if (f.value.masking_on) extra.session_id_masking_enabled = true
    else delete extra.session_id_masking_enabled
    // 缓存 TTL 覆写（target 枚举 5m/1h，对应后端写回逻辑）
    if (f.value.ttl_on) {
      extra.cache_ttl_override_enabled = true
      extra.cache_ttl_override_target = f.value.ttl_target
    } else {
      delete extra.cache_ttl_override_enabled
      delete extra.cache_ttl_override_target
    }
    // 自定义中继
    if (f.value.relay_on && f.value.relay_url.trim()) {
      extra.custom_base_url_enabled = true
      extra.custom_base_url = f.value.relay_url.trim()
    } else {
      delete extra.custom_base_url_enabled
      delete extra.custom_base_url
    }
  }
  if (quotaResetType.value) {
    // 日配额固定重置
    if (f.value.qr_daily_mode === 'fixed') {
      extra.quota_daily_reset_mode = 'fixed'
      extra.quota_daily_reset_hour = f.value.qr_daily_hour ?? 0
    } else {
      delete extra.quota_daily_reset_mode
      delete extra.quota_daily_reset_hour
    }
    // 周配额固定重置
    if (f.value.qr_weekly_mode === 'fixed') {
      extra.quota_weekly_reset_mode = 'fixed'
      extra.quota_weekly_reset_day = f.value.qr_weekly_day ?? 1
      extra.quota_weekly_reset_hour = f.value.qr_weekly_hour ?? 0
    } else {
      delete extra.quota_weekly_reset_mode
      delete extra.quota_weekly_reset_day
      delete extra.quota_weekly_reset_hour
    }
    // 时区仅在任一固定重置启用时写入（旧版同款条件，缺省按 UTC）
    if (f.value.qr_daily_mode === 'fixed' || f.value.qr_weekly_mode === 'fixed') {
      extra.quota_reset_timezone = f.value.qr_tz || 'UTC'
    } else {
      delete extra.quota_reset_timezone
    }
  }
}
// 高级策略序列化：保留字段由各平台/类型条件限制，避免编辑非适用账号时误清历史配置。
const csvNumbers = (value) => String(value || '').split(',').map((x) => Number(x.trim())).filter((n) => Number.isInteger(n) && n >= 100 && n <= 599)
const normalizeTempRules = () => (f.value.temp_unsched_rules || []).map((r) => ({
  error_code: String(r.error_code || '').trim(), duration_minutes: Math.max(1, Number(r.duration_minutes) || 30),
  keywords: String(r.keywords || '').split(',').map((x) => x.trim()).filter(Boolean), description: String(r.description || '').trim(),
})).filter((r) => r.error_code || r.keywords.length || r.description)
const applyAdvancedCredentials = (creds) => {
  const poolEligible = ['apikey', 'bedrock'].includes(f.value.type || category.value)
  if (poolEligible) {
    if (f.value.pool_mode) {
      creds.pool_mode = true
      creds.pool_mode_retry_count = Math.max(0, Number(f.value.pool_retry_count) || 0)
      const retry = csvNumbers(f.value.pool_retry_codes)
      if (retry.length) creds.pool_mode_retry_status_codes = retry; else delete creds.pool_mode_retry_status_codes
    } else { delete creds.pool_mode; delete creds.pool_mode_retry_count; delete creds.pool_mode_retry_status_codes }
    if (f.value.custom_error_on) { creds.custom_error_codes_enabled = true; creds.custom_error_codes = csvNumbers(f.value.custom_error_codes) }
    else { delete creds.custom_error_codes_enabled; delete creds.custom_error_codes }
  }
  if (f.value.temp_unsched_on) { creds.temp_unschedulable_enabled = true; creds.temp_unschedulable_rules = normalizeTempRules() }
  else { delete creds.temp_unschedulable_enabled; delete creds.temp_unschedulable_rules }
  if (f.value.platform === 'openai') {
    const compact = Object.fromEntries((f.value.oai_compact_mappings || []).map((r) => [String(r.from || '').trim(), String(r.to || '').trim()]).filter(([from, to]) => from && to))
    if (Object.keys(compact).length) creds.compact_model_mapping = compact; else delete creds.compact_model_mapping
    creds.openai_capabilities = Array.isArray(f.value.oai_capabilities) && f.value.oai_capabilities.length ? f.value.oai_capabilities : ['chat_completions']
  }
}
const applyAdvancedExtra = (extra) => {
  extra.keyword_policy_mode = f.value.keyword_policy_mode || 'inherit'
  extra.response_model_mode = f.value.response_model_mode || 'inherit'
  if (f.value.response_model_mode === 'custom' && f.value.response_model_custom.trim()) extra.response_model_custom = f.value.response_model_custom.trim()
  else delete extra.response_model_custom
  if (['openai', 'kimi', 'zhipu', 'deepseek'].includes(f.value.platform) && (f.value.type || category.value) === 'apikey') {
    if (f.value.upstream_provider === 'sub2api') { extra.upstream_provider = 'sub2api'; extra.upstream_billing_auto_probe_enabled = !!f.value.upstream_billing_auto_probe }
    else { delete extra.upstream_provider; delete extra.upstream_billing_auto_probe_enabled }
  }
  if (f.value.platform === 'openai') {
    const oauth = (f.value.type || category.value) === 'oauth'
    if (f.value.oai_passthrough) extra.openai_passthrough = true; else { delete extra.openai_passthrough; delete extra.openai_oauth_passthrough }
    if (oauth && f.value.oai_flatten_ns) extra.openai_responses_flatten_namespaces = true; else delete extra.openai_responses_flatten_namespaces
    const wsKey = oauth ? 'openai_oauth_responses_websockets_v2_mode' : 'openai_apikey_responses_websockets_v2_mode'
    const wsEnabled = oauth ? 'openai_oauth_responses_websockets_v2_enabled' : 'openai_apikey_responses_websockets_v2_enabled'
    extra[wsKey] = f.value.oai_ws_mode || 'off'; extra[wsEnabled] = (f.value.oai_ws_mode || 'off') !== 'off'
    extra.openai_long_context_billing_enabled = !!f.value.oai_long_context
    if (oauth && f.value.oai_codex_cli_only) { extra.codex_cli_only = true; extra.codex_cli_only_allowed_clients = f.value.oai_allow_claude_code ? ['claude_code'] : [] } else { delete extra.codex_cli_only; delete extra.codex_cli_only_allowed_clients }
    if (oauth && f.value.oai_fingerprint !== 'off') extra.codex_fingerprint_mode = f.value.oai_fingerprint; else delete extra.codex_fingerprint_mode
    if (f.value.oai_compact_mode !== 'auto') extra.openai_compact_mode = f.value.oai_compact_mode; else delete extra.openai_compact_mode
    if ((f.value.type || category.value) === 'apikey' && f.value.oai_responses_mode !== 'auto') extra.openai_responses_mode = f.value.oai_responses_mode; else delete extra.openai_responses_mode
    if (Number(f.value.oai_pause5_threshold) > 0) { extra.auto_pause_5h_disabled = !f.value.oai_pause5_on; extra.auto_pause_5h_threshold = Number(f.value.oai_pause5_threshold) } else { delete extra.auto_pause_5h_disabled; delete extra.auto_pause_5h_threshold }
    if (Number(f.value.oai_pause7_threshold) > 0) { extra.auto_pause_7d_disabled = !f.value.oai_pause7_on; extra.auto_pause_7d_threshold = Number(f.value.oai_pause7_threshold) } else { delete extra.auto_pause_7d_disabled; delete extra.auto_pause_7d_threshold }
  }
  if (f.value.platform === 'anthropic' && (f.value.type || category.value) === 'apikey') {
    if (f.value.anth_passthrough) extra.anthropic_passthrough = true; else delete extra.anthropic_passthrough
    if (f.value.anth_auth_scheme === 'authorization_bearer') extra.anthropic_apikey_auth_scheme = 'authorization_bearer'; else delete extra.anthropic_apikey_auth_scheme
    if (f.value.anth_web_search !== 'default') extra.web_search_emulation = f.value.anth_web_search; else delete extra.web_search_emulation
  }
}
const save = async () => {
  if (!f.value.name.trim()) { message.error('名称必填'); jump('base'); return }
  // 启用自定义中继必须有地址（空地址的中继无意义，显式拦截优于静默丢弃）
  if (f.value.relay_on && !f.value.relay_url.trim()) { message.error('启用自定义中继时，中继地址必填'); jump('sched'); return }
  saving.value = true
  try {
    // 编辑模式：分组映射有改动时先过单点风险校验（用户取消则中止保存）
    if (isEdit.value && !(await ensureGmmRiskConfirmed())) return
    if (isEdit.value) {
      const body = buildCommonBody()
      const creds = buildMappingCreds({ ...(props.account?.credentials || {}) })
      if (f.value.type === 'apikey') {
        if (f.value.base_url != null && f.value.base_url !== '') creds.base_url = f.value.base_url
        if (f.value.api_key?.trim()) creds.api_key = f.value.api_key.trim()
      } else if (f.value.type === 'bedrock') {
        creds.auth_mode = f.value.br_auth_mode
        creds.aws_region = f.value.br_region.trim() || 'us-east-1'
        if (f.value.br_auth_mode === 'sigv4') {
          if (f.value.br_key_id.trim()) creds.aws_access_key_id = f.value.br_key_id.trim() // 留空=保留
          if (f.value.br_secret.trim()) creds.aws_secret_access_key = f.value.br_secret.trim()
          if (f.value.br_token.trim()) creds.aws_session_token = f.value.br_token.trim()
        } else if (f.value.br_api_key.trim()) creds.api_key = f.value.br_api_key.trim()
        if (f.value.br_global) creds.aws_force_global = 'true'; else delete creds.aws_force_global
      } else if (f.value.type === 'service_account') {
        if (f.value.sa_json.trim()) creds.service_account_json = f.value.sa_json.trim() // 留空=保留
        creds.project_id = f.value.sa_project_id
        creds.client_email = f.value.sa_client_email
        creds.location = f.value.sa_location.trim()
      }
      if (f.value.platform === 'openai') {
        const tz = (f.value.upstream_timezone || '').trim()
        if (tz) creds.upstream_timezone = tz; else delete creds.upstream_timezone
      }
      if (CN_PLATFORMS.includes(f.value.platform)) {
        // 国产平台：模式/协议始终随表单写入（调度与转发格式依赖，缺省会退 chat_completions/payg）
        creds.account_mode = f.value.cn_mode
        creds.api_protocol = f.value.cn_protocol
      }
      if (f.value.platform === 'opencode_go') applyOpenCodeCreds(creds, 'edit')
      applyHdr(creds, 'edit')
      applyAdvancedCredentials(creds)
      body.credentials = creds
      body.auto_pause_on_expired = !!f.value.auto_pause_on_expired
      // antigravity 专属 extra 开关（未勾选时清除对应键）
      const extra = { ...(props.account?.extra || {}) }
      if (f.value.platform === 'antigravity') {
        if (f.value.ant_mixed) extra.mixed_scheduling = true; else delete extra.mixed_scheduling
        if (f.value.ant_overages) extra.allow_overages = true; else delete extra.allow_overages
      }
      // 代理池（多出口）：只提交 {id,name,concurrency}（凭据不下发），
      // 后端按 id 对账补全 URL 快照并剔除已删除代理；主代理不重复计入。
      const pool = f.value.__pool
        .filter((id) => id !== f.value.proxy_id)
        .map((id) => {
          const p = props.proxies.find((x) => x.id === id)
          if (!p) return null
          const entry = { id: p.id, name: p.name }
          const conc = Number(f.value.__poolConc?.[id])
          if (Number.isFinite(conc) && conc > 0) entry.concurrency = Math.floor(conc)
          return entry
        })
        .filter(Boolean)
      if (pool.length) extra.proxy_pool = pool; else delete extra.proxy_pool
      const mainConc = Number(f.value.__mainConc)
      if (Number.isFinite(mainConc) && mainConc > 0) extra.proxy_main_concurrency = Math.floor(mainConc); else delete extra.proxy_main_concurrency
      // 伪装与中继 / 配额固定重置 extra 键写入（各键语义见 applyConfigExtraPatches）
      applyConfigExtraPatches(extra)
      applyAdvancedExtra(extra)
      if (Object.keys(extra).length) body.extra = extra; else body.extra = {}
      if (gmmDirty.value) {
        // 空映射不携带：防止「加行未填就保存」误传空对象清空已有映射
        const g = buildGmm()
        if (Object.keys(g).length) body.group_model_mappings = g
      }
      await request(`/admin/accounts/${props.account.id}`, { method: 'PUT', body })
      await request(`/admin/accounts/${props.account.id}/schedulable`, { method: 'POST', body: { schedulable: !!f.value.schedulable } })
      message.success('账号已保存')
    } else {
      // 创建：POST /admin/accounts（credentials 必填 map；类型区分 apikey/bedrock/service_account）
      let creds = {}
      let type = category.value
      if (category.value === 'apikey') {
        // antigravity 上游端点必填（旧 upstream 表单 required）
        if (f.value.platform === 'antigravity' && !f.value.base_url?.trim()) { message.error('上游 Base URL 必填'); jump('conn'); return }
        if (f.value.base_url?.trim()) creds.base_url = f.value.base_url.trim()
        if (!f.value.api_key?.trim()) { message.error('API Key 必填'); jump('conn'); return }
        creds.api_key = f.value.api_key.trim()
        if (CN_PLATFORMS.includes(f.value.platform)) {
          creds.account_mode = f.value.cn_mode
          creds.api_protocol = f.value.cn_protocol
        }
        if (f.value.platform === 'opencode_go') {
          // OpenCode：base_url 留空时按模式+协议补默认端点（后端转发依赖该基址路由）
          creds.base_url = f.value.base_url?.trim() || defaultOpenCodeBaseUrl(f.value.oc_mode, f.value.oc_protocol)
          applyOpenCodeCreds(creds, 'create')
        }
        applyHdr(creds, 'create')
      } else if (category.value === 'bedrock') {
        creds = { auth_mode: f.value.br_auth_mode, aws_region: f.value.br_region.trim() || 'us-east-1' }
        if (f.value.br_auth_mode === 'sigv4') {
          if (!f.value.br_key_id.trim()) { message.error('Access Key ID 必填'); jump('conn'); return }
          if (!f.value.br_secret.trim()) { message.error('Secret Access Key 必填'); jump('conn'); return }
          creds.aws_access_key_id = f.value.br_key_id.trim()
          creds.aws_secret_access_key = f.value.br_secret.trim()
          if (f.value.br_token.trim()) creds.aws_session_token = f.value.br_token.trim()
        } else {
          if (!f.value.br_api_key.trim()) { message.error('Bedrock API Key 必填'); jump('conn'); return }
          creds.api_key = f.value.br_api_key.trim()
        }
        if (f.value.br_global) creds.aws_force_global = 'true'
      } else {
        if (!f.value.sa_json.trim()) { message.error('请粘贴服务账号 JSON'); jump('conn'); return }
        onSaJson(f.value.sa_json)
        if (!f.value.sa_project_id) { message.error('服务账号 JSON 无效'); jump('conn'); return }
        if (!f.value.sa_location.trim()) { message.error('Location 必填'); jump('conn'); return }
        creds = {
          service_account_json: f.value.sa_json.trim(), project_id: f.value.sa_project_id,
          client_email: f.value.sa_client_email, location: f.value.sa_location.trim(), tier_id: 'vertex',
        }
      }
      creds = buildMappingCreds(creds)
      applyAdvancedCredentials(creds)
      if (f.value.platform === 'openai' && f.value.upstream_timezone?.trim()) creds.upstream_timezone = f.value.upstream_timezone.trim()
      const body = { ...buildCommonBody(), platform: f.value.platform, type, credentials: creds }
      body.auto_pause_on_expired = !!f.value.auto_pause_on_expired
      // antigravity 专属 extra 开关 + 配额固定重置（创建态可直接配置，POST body.extra 支持）
      const extra = {}
      if (f.value.platform === 'antigravity') {
        if (f.value.ant_mixed) extra.mixed_scheduling = true
        if (f.value.ant_overages) extra.allow_overages = true
      }
      applyConfigExtraPatches(extra)
      applyAdvancedExtra(extra)
      if (Object.keys(extra).length) body.extra = extra
      const gmm = buildGmm()
      if (Object.keys(gmm).length) body.group_model_mappings = gmm
      if (mixedRisk.value && f.value.confirm_mixed_risk) body.confirm_mixed_channel_risk = true
      await request('/admin/accounts', { method: 'POST', body })
      message.success('账号已创建')
    }
    openRef.value = false
    emit('saved')
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-modal v-model:open="openRef" :title="isEdit ? `编辑账号 · ${account?.name || ''}` : '新增账号'" width="780px"
    :confirm-loading="saving" ok-text="保存" cancel-text="取消" @ok="save">
    <div class="aed">
      <!-- 类别分段（仅创建；编辑时类型固定只读展示） -->
      <div v-if="!isEdit" class="aed-cat">
        <span class="aed-cat-label">账号类别</span>
        <a-segmented v-model:value="category" :options="categoryOptions" />
      </div>
      <div v-else class="aed-cat">
        <span class="aed-cat-label">类型</span>
        <a-tag>{{ f.type }}</a-tag>
        <a-tag>{{ f.platform }}</a-tag>
      </div>

      <!-- 分区导航 -->
      <div class="aed-nav">
        <button v-for="s in SECTIONS" :key="s.key" type="button" class="aed-nav-btn" :class="{ on: activeSection === s.key }"
          @click="jump(s.key)">{{ s.label }}</button>
      </div>

      <div class="aed-body" :style="{ height: SECTION_BASE + 'px' }">
        <!-- 基础信息 -->
        <section id="acct-sec-base" class="wp-form">
          <div class="sec-title">基础信息</div>
          <div class="frow"><span class="flabel req">名称</span>
            <div class="fctl"><a-input v-model:value="f.name" placeholder="如：主力 Claude 官转" /></div>
          </div>
          <div class="frow"><span class="flabel">平台</span>
            <div class="fctl"><a-select v-model:value="f.platform" :options="platformOptions" :disabled="isEdit" style="width: 200px" />
              <span v-if="isEdit" class="hint">创建后不可更改</span></div>
          </div>
          <div class="frow"><span class="flabel">状态</span>
            <div class="fctl"><a-select v-model:value="f.status" :options="statusOptions" style="width: 200px" /></div>
          </div>
          <div class="frow"><span class="flabel">备注</span>
            <div class="fctl"><a-textarea v-model:value="f.notes" :rows="2" placeholder="选填" /></div>
          </div>
        </section>

        <!-- 连接凭据 -->
        <section id="acct-sec-conn" class="wp-form">
          <div class="sec-title">连接凭据</div>
          <template v-if="!isEdit">
            <div v-if="category === 'apikey'" class="frow"><span class="flabel req">API Key</span>
              <div class="fctl"><a-input-password v-model:value="f.api_key" placeholder="sk-…" /></div>
            </div>
            <div v-if="category === 'apikey'" class="frow"><span class="flabel">Base URL</span>
              <div class="fctl"><a-input v-model:value="f.base_url" :placeholder="f.platform === 'antigravity' ? 'https://cloudcode-pa.googleapis.com' : 'https://api.openai.com（官方可留空）'" /></div>
            </div>
            <div v-if="category === 'apikey' && f.platform === 'grok'" class="frow"><span class="flabel"></span>
              <div class="fctl" style="flex-wrap: wrap; gap: 6px">
                <a-button v-for="p in GROK_BASE_PRESETS" :key="p.url" size="small" @click="f.base_url = p.url">{{ p.label }}（{{ p.url.replace('https://', '') }}）</a-button>
              </div>
            </div>
            <div v-if="category === 'apikey' && CN_PLATFORMS.includes(f.platform)" class="frow"><span class="flabel"></span>
              <div class="fctl" style="flex-direction: column; align-items: stretch; gap: 6px">
                <a-button v-for="p in CN_BASE_PRESETS[f.platform]" :key="p.label" size="small" style="justify-content: flex-start"
                  @click="pickCnPreset(p)">{{ p.label }}<span class="hint" style="margin-left: 8px">{{ p.tag }}</span></a-button>
              </div>
            </div>
            <!-- OpenCode（opencode_go）：接入模式 + API 协议 + 协议规则（照官方 opencode 表单语义） -->
            <template v-if="category === 'apikey' && f.platform === 'opencode_go'">
              <div class="frow"><span class="flabel">接入模式</span>
                <div class="fctl">
                  <a-segmented v-model:value="f.oc_mode"
                    :options="[{ value: 'zen', label: 'Zen 按量付费' }, { value: 'go', label: 'Coding Plan（GO）' }]" />
                  <span class="hint">决定额度监控方式与默认端点</span>
                </div>
              </div>
              <div class="frow"><span class="flabel">API 协议</span>
                <div class="fctl"><a-select v-model:value="f.oc_protocol" :options="ocProtocolOptions" style="width: 240px" /></div>
              </div>
              <div v-if="f.oc_protocol === 'adaptive'" class="frow" style="align-items: flex-start">
                <span class="flabel" style="padding-top: 8px">协议规则</span>
                <div class="fctl" style="flex: 1"><OpenCodeGoProtocolRulesEditor v-model:rows="f.oc_rules" :plan="f.oc_mode" /></div>
              </div>
            </template>
            <!-- Header 覆写（grok/anthropic/openai/kimi/zhipu/deepseek × apikey）：旧版便利功能恢复 -->
            <template v-if="category === 'apikey' && isHdrEligible(f.platform, 'apikey')">
              <div class="frow"><span class="flabel">Header 覆写</span>
                <div class="fctl"><a-switch v-model:checked="f.hdr_on" /><span class="hint">改写转发请求头（伪装官方 CLI 客户端特征）</span></div>
              </div>
              <template v-if="f.hdr_on">
                <div v-for="(row, i) in f.hdr_rows" :key="`hdr-${i}`" class="frow"><span class="flabel"></span>
                  <div class="fctl" style="gap: 8px">
                    <a-input v-model:value="row.name" placeholder="header-name" style="width: 240px; font-family: Consolas, monospace" />
                    <a-input v-model:value="row.value" placeholder="value（可留空）" style="flex: 1; font-family: Consolas, monospace" />
                    <a-button size="small" type="text" danger @click="rmHdrRow(i)"><DeleteOutlined /></a-button>
                  </div>
                </div>
                <div class="frow"><span class="flabel"></span>
                  <div class="fctl" style="gap: 8px">
                    <a-button size="small" @click="addHdrRow"><PlusOutlined /> 添加请求头</a-button>
                    <a-button size="small" @click="fillHdrTemplate"><PlusOutlined /> 填充 {{ f.platform }} 官方客户端模板</a-button>
                  </div>
                </div>
              </template>
            </template>
            <template v-if="category === 'bedrock'">
              <div class="frow"><span class="flabel">认证方式</span>
                <div class="fctl">
                  <a-segmented v-model:value="f.br_auth_mode" :options="[{ value: 'sigv4', label: 'SigV4 密钥' }, { value: 'apikey', label: 'Bedrock API Key' }]" />
                </div>
              </div>
              <div class="frow"><span class="flabel">Region</span>
                <div class="fctl"><a-select v-model:value="f.br_region" :options="BEDROCK_REGIONS.map((r) => ({ value: r, label: r }))" style="width: 200px" /></div>
              </div>
              <template v-if="f.br_auth_mode === 'sigv4'">
                <div class="frow"><span class="flabel req">Access Key ID</span>
                  <div class="fctl"><a-input v-model:value="f.br_key_id" placeholder="AKIA…" /></div>
                </div>
                <div class="frow"><span class="flabel req">Secret Access Key</span>
                  <div class="fctl"><a-input-password v-model:value="f.br_secret" /></div>
                </div>
                <div class="frow"><span class="flabel">Session Token</span>
                  <div class="fctl"><a-input-password v-model:value="f.br_token" placeholder="临时凭据时填，选填" /></div>
                </div>
              </template>
              <div v-else class="frow"><span class="flabel req">Bedrock API Key</span>
                <div class="fctl"><a-input-password v-model:value="f.br_api_key" placeholder="Bedrock API Key（密钥认证）" /></div>
              </div>
              <div class="frow"><span class="flabel">全局端点</span>
                <div class="fctl"><a-switch v-model:checked="f.br_global" /><span class="hint">跨境推理时开启（aws_force_global）</span></div>
              </div>
            </template>
            <template v-if="category === 'service_account'">
              <div class="frow"><span class="flabel req">服务账号 JSON</span>
                <div class="fctl" style="flex-direction: column; align-items: stretch">
                  <a-textarea v-model:value="f.sa_json" :rows="5" placeholder='粘贴 GCP 服务账号 JSON（含 project_id / client_email / private_key）' @change="onSaJson(f.sa_json)" />
                  <span class="hint">{{ saHint || '粘贴后自动识别项目与邮箱' }}</span>
                </div>
              </div>
              <div class="frow"><span class="flabel">Location</span>
                <div class="fctl"><a-input v-model:value="f.sa_location" placeholder="us-central1" style="width: 200px" /></div>
              </div>
            </template>
          </template>
          <template v-else>
            <div v-if="f.type === 'apikey'" class="frow"><span class="flabel">Base URL</span>
              <div class="fctl"><a-input v-model:value="f.base_url" placeholder="https://…" /></div>
            </div>
            <div v-if="f.type === 'apikey' && f.platform === 'grok'" class="frow"><span class="flabel"></span>
              <div class="fctl" style="flex-wrap: wrap; gap: 6px">
                <a-button v-for="p in GROK_BASE_PRESETS" :key="p.url" size="small" @click="f.base_url = p.url">{{ p.label }}（{{ p.url.replace('https://', '') }}）</a-button>
              </div>
            </div>
            <div v-if="f.type === 'apikey' && CN_PLATFORMS.includes(f.platform)" class="frow"><span class="flabel"></span>
              <div class="fctl" style="flex-direction: column; align-items: stretch; gap: 6px">
                <a-button v-for="p in CN_BASE_PRESETS[f.platform]" :key="p.label" size="small" style="justify-content: flex-start"
                  @click="pickCnPreset(p)">{{ p.label }}<span class="hint" style="margin-left: 8px">{{ p.tag }}</span></a-button>
              </div>
            </div>
            <!-- OpenCode（opencode_go）编辑态：接入模式 / API 协议 / 协议规则回显 -->
            <template v-if="f.type === 'apikey' && f.platform === 'opencode_go'">
              <div class="frow"><span class="flabel">接入模式</span>
                <div class="fctl">
                  <a-segmented v-model:value="f.oc_mode"
                    :options="[{ value: 'zen', label: 'Zen 按量付费' }, { value: 'go', label: 'Coding Plan（GO）' }]" />
                  <span class="hint">决定额度监控方式与默认端点</span>
                </div>
              </div>
              <div class="frow"><span class="flabel">API 协议</span>
                <div class="fctl"><a-select v-model:value="f.oc_protocol" :options="ocProtocolOptions" style="width: 240px" /></div>
              </div>
              <div v-if="f.oc_protocol === 'adaptive'" class="frow" style="align-items: flex-start">
                <span class="flabel" style="padding-top: 8px">协议规则</span>
                <div class="fctl" style="flex: 1"><OpenCodeGoProtocolRulesEditor v-model:rows="f.oc_rules" :plan="f.oc_mode" /></div>
              </div>
            </template>
            <div v-if="f.type === 'apikey'" class="frow"><span class="flabel">API Key</span>
              <div class="fctl"><a-input-password v-model:value="f.api_key" placeholder="留空保留原 Key" /></div>
            </div>
            <!-- Header 覆写（编辑态）：回填自凭据，关闭保存=删除两键 -->
            <template v-if="isHdrEligible(f.platform, f.type)">
              <div class="frow"><span class="flabel">Header 覆写</span>
                <div class="fctl"><a-switch v-model:checked="f.hdr_on" /><span class="hint">改写转发请求头（伪装官方 CLI 客户端特征）</span></div>
              </div>
              <template v-if="f.hdr_on">
                <div v-for="(row, i) in f.hdr_rows" :key="`hdr-${i}`" class="frow"><span class="flabel"></span>
                  <div class="fctl" style="gap: 8px">
                    <a-input v-model:value="row.name" placeholder="header-name" style="width: 240px; font-family: Consolas, monospace" />
                    <a-input v-model:value="row.value" placeholder="value（可留空）" style="flex: 1; font-family: Consolas, monospace" />
                    <a-button size="small" type="text" danger @click="rmHdrRow(i)"><DeleteOutlined /></a-button>
                  </div>
                </div>
                <div class="frow"><span class="flabel"></span>
                  <div class="fctl" style="gap: 8px">
                    <a-button size="small" @click="addHdrRow"><PlusOutlined /> 添加请求头</a-button>
                    <a-button size="small" @click="fillHdrTemplate"><PlusOutlined /> 填充 {{ f.platform }} 官方客户端模板</a-button>
                  </div>
                </div>
              </template>
            </template>
            <template v-if="f.type === 'bedrock'">
              <div class="frow"><span class="flabel">认证方式</span>
                <div class="fctl"><a-segmented v-model:value="f.br_auth_mode" :options="[{ value: 'sigv4', label: 'SigV4 密钥' }, { value: 'apikey', label: 'Bedrock API Key' }]" /></div>
              </div>
              <div class="frow"><span class="flabel">Region</span>
                <div class="fctl"><a-select v-model:value="f.br_region" :options="BEDROCK_REGIONS.map((r) => ({ value: r, label: r }))" style="width: 200px" /></div>
              </div>
              <template v-if="f.br_auth_mode === 'sigv4'">
                <div class="frow"><span class="flabel">Access Key ID</span>
                  <div class="fctl"><a-input v-model:value="f.br_key_id" placeholder="留空保留原值" /></div>
                </div>
                <div class="frow"><span class="flabel">Secret Access Key</span>
                  <div class="fctl"><a-input-password v-model:value="f.br_secret" placeholder="留空保留原值" /></div>
                </div>
                <div class="frow"><span class="flabel">Session Token</span>
                  <div class="fctl"><a-input-password v-model:value="f.br_token" placeholder="留空保留原值" /></div>
                </div>
              </template>
              <div v-else class="frow"><span class="flabel">Bedrock API Key</span>
                <div class="fctl"><a-input-password v-model:value="f.br_api_key" placeholder="留空保留原值" /></div>
              </div>
              <div class="frow"><span class="flabel">全局端点</span>
                <div class="fctl"><a-switch v-model:checked="f.br_global" /></div>
              </div>
            </template>
            <template v-if="f.type === 'service_account'">
              <div class="frow"><span class="flabel">服务账号 JSON</span>
                <div class="fctl" style="flex-direction: column; align-items: stretch">
                  <a-textarea v-model:value="f.sa_json" :rows="5" placeholder="留空保留原 JSON" />
                </div>
              </div>
              <div class="frow"><span class="flabel">Project / Email</span>
                <div class="fctl"><span class="hint">{{ f.sa_project_id }} · {{ f.sa_client_email }}</span></div>
              </div>
              <div class="frow"><span class="flabel">Location</span>
                <div class="fctl"><a-input v-model:value="f.sa_location" style="width: 200px" /></div>
              </div>
            </template>
            <div v-if="!['apikey', 'bedrock', 'service_account'].includes(f.type)" class="frow"><span class="flabel">凭据</span>
              <div class="fctl"><span class="hint">{{ f.type }} 类型凭据由此处的凭据合并逻辑保护，敏感键留空即保留；OAuth 类请使用「OAuth 接入」重新授权</span></div>
            </div>
            <div v-if="f.platform === 'openai'" class="frow"><span class="flabel">上游时区</span>
              <div class="fctl"><a-select v-model:value="f.upstream_timezone" :options="TIMEZONES.map((t) => ({ value: t, label: t || '自动探测（推荐）' }))" style="width: 260px" /></div>
            </div>
          </template>
        </section>

        <!-- 调度与容量 -->
        <section id="acct-sec-sched" class="wp-form">
          <div class="sec-title">调度与容量</div>
          <div class="frow"><span class="flabel">优先级</span>
            <div class="fctl"><a-input-number v-model:value="f.priority" :min="0" style="width: 140px" /><span class="hint">大者优先调度</span></div>
          </div>
          <div class="frow"><span class="flabel">可调度</span>
            <div class="fctl"><a-switch v-model:checked="f.schedulable" /><span class="hint">关闭后此账号不参与请求分配</span></div>
          </div>
          <div class="frow"><span class="flabel">并发上限</span>
            <div class="fctl"><a-input-number v-model:value="f.concurrency" :min="1" style="width: 140px" /></div>
          </div>
          <div class="frow"><span class="flabel">负载因子</span>
            <div class="fctl"><a-input-number v-model:value="f.load_factor" :min="1" :max="10000" placeholder="自动" style="width: 140px" /></div>
          </div>
          <div class="frow"><span class="flabel">计费倍率</span>
            <div class="fctl"><a-input-number v-model:value="f.rate_multiplier" :min="0" :step="0.1" style="width: 140px" /></div>
          </div>
          <div class="frow"><span class="flabel">过期时间</span>
            <div class="fctl"><a-date-picker v-model:value="f.expires_at" show-time placeholder="永不过期" style="width: 220px" />
              <a-switch v-model:checked="f.auto_pause_on_expired" size="small" />
              <span class="hint">过期后自动暂停调度</span></div>
          </div>
          <template v-if="f.platform === 'antigravity'">
            <div class="frow"><span class="flabel">混合调度</span>
              <div class="fctl"><a-switch v-model:checked="f.ant_mixed" /><span class="hint">与其它平台账号混合参与分配（extra.mixed_scheduling）</span></div>
            </div>
            <div class="frow"><span class="flabel">允许超额</span>
              <div class="fctl"><a-switch v-model:checked="f.ant_overages" /><span class="hint">配额不足时允许超额请求（extra.allow_overages）</span></div>
            </div>
          </template>
          <!-- 伪装与中继：anthropic oauth/setup-token 专属（extra 键写入，AccountDisguisePanel） -->
          <AccountDisguisePanel v-if="isAnthropicOAuth"
            v-model:tls-on="f.tls_on" v-model:tls-profile-id="f.tls_profile_id"
            v-model:masking-on="f.masking_on"
            v-model:ttl-on="f.ttl_on" v-model:ttl-target="f.ttl_target"
            v-model:relay-on="f.relay_on" v-model:relay-url="f.relay_url" />
          <!-- 配额固定重置：apikey/bedrock（创建/编辑均可配，AccountQuotaResetPanel） -->
          <AccountQuotaResetPanel v-if="quotaResetType"
            v-model:daily-mode="f.qr_daily_mode" v-model:daily-hour="f.qr_daily_hour"
            v-model:weekly-mode="f.qr_weekly_mode" v-model:weekly-day="f.qr_weekly_day"
            v-model:weekly-hour="f.qr_weekly_hour" v-model:timezone="f.qr_tz" />
        </section>

        <!-- 分组与代理 -->
        <section id="acct-sec-grp" class="wp-form">
          <div class="sec-title">分组与代理</div>
          <div class="frow"><span class="flabel">分组</span>
            <div class="fctl"><a-select v-model:value="f.group_ids" mode="multiple" :options="groupOptions" :max-tag-count="3"
              placeholder="未分组" show-search option-filter-prop="label" style="min-width: 280px" /></div>
          </div>
          <div class="frow"><span class="flabel">代理</span>
            <div class="fctl"><a-select v-model:value="f.proxy_id" :options="proxyOptions" show-search option-filter-prop="label" style="min-width: 280px" /></div>
          </div>
          <!-- 代理池（多出口）：extra.proxy_pool；调度层在主代理与池之间避开满载、按最小在途选出口 -->
          <div class="frow" style="align-items: flex-start"><span class="flabel" style="padding-top: 8px">代理池</span>
            <div class="fctl" style="flex-direction: column; align-items: stretch">
              <div class="pool-chips">
                <span v-for="p in poolCandidates" :key="p.id" class="pool-chip"
                  :class="{ on: f.__pool.includes(p.id), main: f.proxy_id === p.id }"
                  :title="f.proxy_id === p.id ? '该代理已是主代理' : `${p.protocol}://${p.host}:${p.port}`"
                  @click="togglePool(p.id)">
                  {{ p.name }}<span v-if="f.proxy_id === p.id" class="main-tag">（主）</span>
                </span>
                <span v-if="!poolCandidates.length" class="hint">暂无可用代理（先在代理管理新增，或开启 IPv6 出口自动生成）</span>
              </div>
              <div v-if="f.__pool.length" class="pool-rows">
                <div v-for="pid in f.__pool" :key="pid" class="pool-row">
                  <span class="pool-name" :title="poolName(pid)">{{ poolName(pid) }}</span>
                  <!-- 实时观测：在途/上限 + 最近 5 次成败点（账号详情 proxy_stats） -->
                  <span v-if="poolStat(pid)" class="pool-stat"
                    :title="`在途 ${poolStat(pid).in_flight}/${poolStat(pid).limit}`">
                    <i v-for="(ok, i) in (poolStat(pid).recent || []).slice(-5)" :key="i"
                      class="pdot" :class="ok ? 'ok' : 'bad'"></i>
                    <span class="pnum">{{ poolStat(pid).in_flight }}/{{ poolStat(pid).limit }}</span>
                  </span>
                  <a-input-number v-model:value="f.__poolConc[pid]" :min="0" size="small" style="width: 130px"
                    :placeholder="`跟随账号并发（${f.concurrency || 1}）`" />
                </div>
              </div>
              <div class="frow" style="margin: 0"><span class="hint" style="flex: none">主代理独立并发</span>
                <span v-if="poolStat(f.proxy_id)" class="pool-stat"
                  :title="`在途 ${poolStat(f.proxy_id).in_flight}/${poolStat(f.proxy_id).limit}`">
                  <i v-for="(ok, i) in (poolStat(f.proxy_id).recent || []).slice(-5)" :key="i"
                    class="pdot" :class="ok ? 'ok' : 'bad'"></i>
                  <span class="pnum">{{ poolStat(f.proxy_id).in_flight }}/{{ poolStat(f.proxy_id).limit }}</span>
                </span>
                <a-input-number v-model:value="f.__mainConc" :min="0" size="small" style="width: 130px"
                  :placeholder="`跟随账号并发（${f.concurrency || 1}）`" />
              </div>
              <span class="hint">勾选后保存即生效：满载代理自动避开，其余按最小并发分流；每个代理可设独立并发上限（留空/0 = 跟随账号并发）。</span>
            </div>
          </div>
          <div v-if="mixedRisk" class="frow"><span class="flabel">混合渠道</span>
            <div class="fctl">
              <a-checkbox v-model:checked="f.confirm_mixed_risk"><span class="hint">我已了解：所选分组与账号平台不一致，可能产生跨渠道调度风险</span></a-checkbox>
            </div>
          </div>
        </section>

        <!-- 模型与高级 -->
        <section id="acct-sec-adv" class="wp-form">
          <div class="sec-title">模型与高级</div>
          <div class="frow" style="align-items: flex-start"><span class="flabel" style="padding-top: 8px">模型白名单</span>
            <div class="fctl" style="flex-direction: column; align-items: stretch">
              <ModelWhitelistPanel
                v-model:model-whitelist="f.model_whitelist"
                v-model:mappings="f.__keep_mappings"
                :platform="f.platform"
                :account-id="isEdit && account?.id ? account.id : null"
                :credentials="{ type: f.type || category, base_url: f.base_url, api_key: f.api_key }" />
              <span class="hint">白名单写入 credentials.model_mapping（身份映射）；映射行写入非身份映射</span>
            </div>
          </div>
          <div class="frow" style="align-items: flex-start"><span class="flabel" style="padding-top: 8px">分组模型映射</span>
            <div class="fctl" style="flex-direction: column; align-items: stretch">
              <div v-for="(row, i) in gmmRows" :key="i" class="gmm-row">
                <a-select v-model:value="row.gid" :options="groupOptions" size="small" style="width: 150px" show-search option-filter-prop="label" />
                <a-input v-model:value="row.src" size="small" placeholder="请求模型（源）" />
                <span class="gmm-arrow">→</span>
                <a-input v-model:value="row.dst" size="small" placeholder="上游模型（目标）" />
                <a-button size="small" type="text" danger @click="delGmmRow(i)"><DeleteOutlined /></a-button>
              </div>
              <div><a-button size="small" @click="addGmmRow"><PlusOutlined /> 添加映射规则</a-button>
                <span class="hint" style="margin-left: 8px">账号级映射：仅此账号在所选分组内把请求模型改写为上游模型（区别于分组编辑里的分组级映射）</span>
                <div v-if="isEdit && gmmRows.length" class="hint" style="color: #c42b1c; margin-top: 2px">
                  注意：接口不返回已有映射，保存将覆盖该账号全部分组映射；不添加规则则不修改
                </div></div>
            </div>
          </div>

          <a-collapse class="adv-collapse" :bordered="false">
            <a-collapse-panel key="common" header="通用高级策略">
              <div class="frow"><span class="flabel">关键词策略</span><div class="fctl"><a-select v-model:value="f.keyword_policy_mode" style="width: 210px" :options="[{value:'inherit',label:'继承全局（推荐）'},{value:'strict',label:'严格关键词策略'},{value:'off',label:'关闭关键词策略'}]" /></div></div>
              <div class="frow"><span class="flabel">响应模型</span><div class="fctl"><a-select v-model:value="f.response_model_mode" style="width: 210px" :options="[{value:'inherit',label:'继承请求模型'},{value:'request',label:'返回请求模型'},{value:'upstream',label:'返回上游模型'},{value:'custom',label:'自定义返回模型'}]" /><a-input v-if="f.response_model_mode === 'custom'" v-model:value="f.response_model_custom" placeholder="自定义模型名" style="width: 220px" /></div></div>
              <div class="frow"><span class="flabel">临时不可调度</span><div class="fctl"><a-switch v-model:checked="f.temp_unsched_on" /><span class="hint">命中错误规则后临时摘出调度池</span></div></div>
              <template v-if="f.temp_unsched_on"><div v-for="(rule,i) in f.temp_unsched_rules" :key="i" class="policy-row"><a-input-number v-model:value="rule.error_code" :min="100" :max="599" placeholder="状态码" style="width:100px" /><a-input-number v-model:value="rule.duration_minutes" :min="1" placeholder="分钟" style="width:100px" /><a-input v-model:value="rule.keywords" placeholder="关键词，逗号分隔" /><a-input v-model:value="rule.description" placeholder="说明（选填）" /><a-button size="small" type="text" danger @click="f.temp_unsched_rules.splice(i,1)"><DeleteOutlined /></a-button></div><div class="frow"><span class="flabel"></span><div class="fctl"><a-button size="small" @click="f.temp_unsched_rules.push({error_code:'',duration_minutes:30,keywords:'',description:''})"><PlusOutlined /> 添加规则</a-button></div></div></template>
              <template v-if="['apikey','bedrock'].includes(f.type || category)"><div class="frow"><span class="flabel">上游池重试</span><div class="fctl"><a-switch v-model:checked="f.pool_mode" /><a-input-number v-if="f.pool_mode" v-model:value="f.pool_retry_count" :min="0" style="width:100px" /><a-input v-if="f.pool_mode" v-model:value="f.pool_retry_codes" placeholder="重试状态码，如 429,500" style="width:210px" /></div></div><div class="frow"><span class="flabel">自定义错误码</span><div class="fctl"><a-switch v-model:checked="f.custom_error_on" /><a-input v-if="f.custom_error_on" v-model:value="f.custom_error_codes" placeholder="如 400,404,422" style="width:210px" /></div></div></template>
              <template v-if="['openai','kimi','zhipu','deepseek'].includes(f.platform) && (f.type || category) === 'apikey'"><div class="frow"><span class="flabel">上游计费</span><div class="fctl"><a-segmented v-model:value="f.upstream_provider" :options="[{value:'standard',label:'标准'},{value:'sub2api',label:'Sub2API'}]" /><a-switch v-if="f.upstream_provider === 'sub2api'" v-model:checked="f.upstream_billing_auto_probe" /><span v-if="f.upstream_provider === 'sub2api'" class="hint">自动探测</span></div></div></template>
            </a-collapse-panel>

            <a-collapse-panel v-if="f.platform === 'openai'" key="openai" header="OpenAI / Codex 高级配置">
              <div class="frow"><span class="flabel">透传模式</span><div class="fctl"><a-switch v-model:checked="f.oai_passthrough" /><span class="hint">直接透传上游请求</span></div></div>
              <div class="frow"><span class="flabel">WebSocket 模式</span><div class="fctl"><a-select v-model:value="f.oai_ws_mode" style="width:210px" :options="[{value:'off',label:'关闭'},{value:'ctx_pool',label:'上下文池'},{value:'passthrough',label:'直接透传'}]" /></div></div>
              <div class="frow"><span class="flabel">长上下文计费</span><div class="fctl"><a-switch v-model:checked="f.oai_long_context" /></div></div>
              <template v-if="(f.type || category) === 'oauth'"><div class="frow"><span class="flabel">命名空间摊平</span><div class="fctl"><a-switch v-model:checked="f.oai_flatten_ns" /></div></div><div class="frow"><span class="flabel">Codex 限制</span><div class="fctl"><a-switch v-model:checked="f.oai_codex_cli_only" /><a-checkbox v-if="f.oai_codex_cli_only" v-model:checked="f.oai_allow_claude_code">允许 Claude Code</a-checkbox></div></div><div class="frow"><span class="flabel">指纹模式</span><div class="fctl"><a-select v-model:value="f.oai_fingerprint" style="width:210px" :options="[{value:'off',label:'关闭'},{value:'device',label:'设备'},{value:'session',label:'会话'},{value:'full',label:'完整'}]" /></div></div></template>
              <div class="frow"><span class="flabel">Compact 模式</span><div class="fctl"><a-select v-model:value="f.oai_compact_mode" style="width:210px" :options="[{value:'auto',label:'自动'},{value:'on',label:'开启'},{value:'off',label:'关闭'}]" /></div></div>
              <div v-for="(map,i) in f.oai_compact_mappings" :key="`compact-${i}`" class="policy-row"><a-input v-model:value="map.from" placeholder="请求模型" /><span>→</span><a-input v-model:value="map.to" placeholder="Compact 模型" /><a-button size="small" type="text" danger @click="f.oai_compact_mappings.splice(i,1)"><DeleteOutlined /></a-button></div><div class="frow"><span class="flabel"></span><div class="fctl"><a-button size="small" @click="f.oai_compact_mappings.push({from:'',to:''})"><PlusOutlined /> 添加 Compact 映射</a-button></div></div>
              <template v-if="(f.type || category) === 'apikey'"><div class="frow"><span class="flabel">Responses API</span><div class="fctl"><a-select v-model:value="f.oai_responses_mode" style="width:210px" :options="[{value:'auto',label:'自动'},{value:'force_responses',label:'强制 Responses'},{value:'force_chat_completions',label:'强制 Chat'}]" /></div></div><div class="frow"><span class="flabel">端点能力</span><div class="fctl"><a-checkbox-group v-model:value="f.oai_capabilities" :options="[{value:'chat_completions',label:'Chat'},{value:'responses',label:'Responses'},{value:'embeddings',label:'Embeddings'},{value:'images',label:'Images'}]" /></div></div></template>
              <div class="frow"><span class="flabel">5h 自动暂停</span><div class="fctl"><a-switch v-model:checked="f.oai_pause5_on" /><a-input-number v-model:value="f.oai_pause5_threshold" :min="0" :max="100" addon-after="%" style="width:130px" /></div></div><div class="frow"><span class="flabel">7d 自动暂停</span><div class="fctl"><a-switch v-model:checked="f.oai_pause7_on" /><a-input-number v-model:value="f.oai_pause7_threshold" :min="0" :max="100" addon-after="%" style="width:130px" /></div></div>
            </a-collapse-panel>

            <a-collapse-panel v-if="f.platform === 'anthropic' && (f.type || category) === 'apikey'" key="anthropic" header="Anthropic API Key 高级配置">
              <div class="frow"><span class="flabel">透传模式</span><div class="fctl"><a-switch v-model:checked="f.anth_passthrough" /></div></div><div class="frow"><span class="flabel">认证方案</span><div class="fctl"><a-select v-model:value="f.anth_auth_scheme" style="width:210px" :options="[{value:'x_api_key',label:'x-api-key（默认）'},{value:'authorization_bearer',label:'Authorization Bearer'}]" /></div></div><div class="frow"><span class="flabel">Web Search 模拟</span><div class="fctl"><a-select v-model:value="f.anth_web_search" style="width:210px" :options="[{value:'default',label:'跟随全局'},{value:'enabled',label:'开启'},{value:'disabled',label:'关闭'}]" /></div></div>
            </a-collapse-panel>
          </a-collapse>
        </section>
      </div>
    </div>
  </a-modal>
</template>

<style scoped>
.aed-cat { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.aed-cat-label { font-size: 12px; color: var(--txt2); flex: none; }
/* 分区导航：Win11 胶囊导航 */
.aed-nav { display: flex; gap: 4px; padding-bottom: 10px; margin-bottom: 4px; border-bottom: 1px solid var(--stroke-card); flex-wrap: wrap; }
.aed-nav-btn { border: 1px solid transparent; background: transparent; border-radius: 4px; padding: 4px 10px; font-size: 12px; color: var(--txt2); cursor: pointer; transition: background 0.12s, color 0.12s; }
.aed-nav-btn:hover { background: var(--fill-subtle); }
.aed-nav-btn.on { background: var(--fill-press); color: var(--txt1); font-weight: 600; }
.aed-body { overflow-y: auto; padding-right: 4px; }
.aed-body section { scroll-margin-top: 8px; }
.aed-body section + section { margin-top: 14px; }
.sec-title { font-size: 12px; font-weight: 600; color: var(--txt2); padding: 6px 8px 4px; border-bottom: 1px solid var(--stroke-card); margin-bottom: 4px; }
.req::after { content: ' *'; color: #c42b1c; }
.gmm-row { display: flex; align-items: center; gap: 6px; width: 100%; }
.gmm-row .ant-input { flex: 1; min-width: 0; }
.gmm-arrow { color: var(--txt3); flex: none; }
.adv-collapse { margin-top: 12px; width: 100%; background: color-mix(in srgb, var(--fill-subtle) 55%, transparent); border-radius: 8px; }
.adv-collapse :deep(.ant-collapse-item) { border-bottom-color: var(--stroke-card); }
.adv-collapse :deep(.ant-collapse-header) { font-size: 12px; font-weight: 500; color: var(--txt1); }
.adv-collapse :deep(.ant-collapse-content) { background: transparent; }
.adv-collapse :deep(.ant-collapse-content-box) { padding: 4px 8px 10px; }
.policy-row { display: flex; align-items: center; gap: 6px; margin: 5px 0; width: 100%; }
.policy-row .ant-input { flex: 1; min-width: 0; }
.pool-chips { display: flex; flex-wrap: wrap; gap: 6px; }
.pool-chip { font-size: 12px; line-height: 24px; padding: 0 10px; border-radius: 6px; border: 1px solid var(--stroke-card); background: var(--card); color: var(--txt2); cursor: pointer; transition: border-color 0.12s, background 0.12s; }
.pool-chip:hover { background: var(--fill-subtle); }
.pool-chip.on { border-color: var(--accent, #3b91d8); background: var(--accent-soft, #3b91d81a); color: var(--txt1); }
.pool-chip.main { opacity: 0.75; cursor: not-allowed; }
.main-tag { color: var(--txt3); font-size: 11px; }
.pool-rows { display: flex; flex-direction: column; gap: 6px; }
.pool-row { display: flex; align-items: center; gap: 10px; }
.pool-name { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--txt2); }
.pool-stat { flex: none; display: inline-flex; align-items: center; gap: 3px; padding: 1px 6px; border-radius: 4px; background: var(--card); }
.pdot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; }
.pdot.ok { background: #2e7d32; }
.pdot.bad { background: #c42b1c; }
.pnum { font-family: Consolas, monospace; font-size: 10px; color: var(--txt2); }
</style>




