import { buildBulkHeaderPatch } from './bulkHeaderPatch.js'

// 批量接口合并 credentials/extra，清除旧覆盖时必须显式写 false、0 或空对象。
export function buildBulkUpdates(enabled, values, { allOpenAIOAuthOnly = false, headerEligible = false } = {}) {
  const E = key => !!enabled[key]
  const v = { value: values }
  const updates = {}, credentials = {}, extra = {}
  const ensureExtra = () => extra
  if (E('proxy_id')) updates.proxy_id = v.value.proxy_id === null ? 0 : v.value.proxy_id
  if (E('concurrency')) updates.concurrency = v.value.concurrency
  if (E('load_factor')) {
    const lf = v.value.load_factor
    updates.load_factor = lf != null && !Number.isNaN(lf) && lf > 0 ? lf : 0
  }
  if (E('priority')) updates.priority = v.value.priority
  if (E('rate_multiplier')) updates.rate_multiplier = v.value.rate_multiplier
  if (E('status')) updates.status = v.value.status
  if (E('group_ids')) updates.group_ids = (v.value.group_ids || []).map(Number)
  if (E('keyword_policy_mode')) ensureExtra().keyword_policy_mode = v.value.keyword_policy_mode
  if (E('base_url')) {
    const s = v.value.base_url.trim()
    if (s) credentials.base_url = s
  }
  if (E('header_override')) {
    if (!headerEligible) throw new Error('所选账号不支持批量请求头覆写')
    Object.assign(credentials, buildBulkHeaderPatch(v.value.hdr_on, v.value.hdr_rows))
  }
  if (E('openai_passthrough')) {
    ensureExtra().openai_passthrough = v.value.openai_passthrough
    if (!v.value.openai_passthrough) ensureExtra().openai_oauth_passthrough = false
  }
  if (E('openai_flatten') && allOpenAIOAuthOnly) ensureExtra().openai_responses_flatten_namespaces = v.value.openai_flatten
  if (E('long_context_billing')) ensureExtra().openai_long_context_billing_enabled = v.value.long_context_billing
  if (E('model_restrict')) {
    const mapping = {}
    if (v.value.model_mode === 'whitelist') {
      for (const m of v.value.model_whitelist || []) {
        const t = String(m).trim()
        if (t && !t.includes('*')) mapping[t] = t
      }
    } else {
      for (const row of v.value.model_mappings || []) {
        if (row.from?.trim() && row.to?.trim()) mapping[row.from.trim()] = row.to.trim()
      }
    }
    credentials.model_mapping = mapping
  }
  if (E('error_codes')) {
    credentials.custom_error_codes_enabled = true
    credentials.custom_error_codes = [...(v.value.error_codes || [])]
  }
  if (E('intercept_warmup')) credentials.intercept_warmup_requests = v.value.intercept_warmup
  if (E('oauth_ws_mode')) {
    ensureExtra().openai_oauth_responses_websockets_v2_mode = v.value.oauth_ws_mode
    ensureExtra().openai_oauth_responses_websockets_v2_enabled = v.value.oauth_ws_mode !== 'off'
  }
  if (E('apikey_ws_mode')) {
    ensureExtra().openai_apikey_responses_websockets_v2_mode = v.value.apikey_ws_mode
    ensureExtra().openai_apikey_responses_websockets_v2_enabled = v.value.apikey_ws_mode !== 'off'
  }
  if (E('codex_cli_only')) ensureExtra().codex_cli_only = v.value.codex_cli_only
  if (E('codex_cli_claude_code')) ensureExtra().codex_cli_only_allowed_clients = v.value.codex_cli_claude_code ? ['claude_code'] : []
  if (E('codex_fingerprint') && v.value.codex_fingerprint !== 'off') ensureExtra().codex_fingerprint_mode = v.value.codex_fingerprint
  if (E('codex_image_tool')) {
    Object.assign(ensureExtra(), {
      codex_image_generation_bridge: null,
      codex_image_generation_bridge_enabled: null,
      codex_image_generation_explicit_tool_policy: null,
    })
    if (v.value.codex_image_tool === 'enabled' || v.value.codex_image_tool === 'disabled') extra.codex_image_generation_bridge = v.value.codex_image_tool === 'enabled'
    else if (v.value.codex_image_tool === 'block') extra.codex_image_generation_explicit_tool_policy = 'strip'
  }
  if (E('compact_mode')) ensureExtra().openai_compact_mode = v.value.compact_mode
  if (E('compact_mappings')) {
    const mapping = {}
    for (const row of v.value.compact_mappings || []) {
      if (row.from?.trim() && row.to?.trim()) mapping[row.from.trim()] = row.to.trim()
    }
    credentials.compact_model_mapping = mapping
  }
  if (E('rpm')) {
    if (v.value.rpm_enabled && v.value.base_rpm != null && v.value.base_rpm > 0) {
      ensureExtra().base_rpm = v.value.base_rpm
      ensureExtra().rpm_strategy = v.value.rpm_strategy
      if (v.value.rpm_sticky_buffer != null && v.value.rpm_sticky_buffer > 0) ensureExtra().rpm_sticky_buffer = v.value.rpm_sticky_buffer
    } else {
      ensureExtra().base_rpm = 0
      ensureExtra().rpm_strategy = ''
      ensureExtra().rpm_sticky_buffer = 0
    }
  }
  if (E('umq_mode')) {
    ensureExtra().user_msg_queue_mode = v.value.umq_mode
    ensureExtra().user_msg_queue_enabled = false
  }
  if (Object.keys(credentials).length) updates.credentials = credentials
  if (Object.keys(extra).length) updates.extra = extra
  return Object.keys(updates).length ? updates : null
}
