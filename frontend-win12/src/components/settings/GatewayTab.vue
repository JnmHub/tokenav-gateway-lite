<script setup>
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
/* ============================================================
 * 网关 tab（对照旧 SettingsView.vue「网关」tab，全部字段来自 GET/PATCH /admin/settings）
 * 分区（每区独立保存，PATCH 带修订且只带本区字段）：
 *  1 客户端统一错误文案（500 字上限 + 字数统计）
 *  2 模型兜底：开关 + anthropic/openai/gemini/antigravity 四兜底模型
 *  3 身份补丁：开关 + 提示词
 *  4 版本与 UA：min/max Claude Code、Antigravity UA、Codex UA、
 *    固定 Codex 版本、自动同步开关、当前同步版本（只读）
 *  5 行为开关组：7 开关 + 客户端日期行指纹归一化
 *  6 Grok 组：默认文本模型 + 跨客户端映射 + 调度停调阈值（整数 1-100）
 *  7 Sub2API 采购倍率自动探测：开关 + 周期/单批/并发三组预设按钮
 *  8 OpenAI Fast/Flex 策略（FastPolicyCard，openai_fast_policy_settings）
 *  9 Claude OAuth 系统提示词注入：开关 + prompt + blocks JSON（空串显式清空）
 * 10 高级调度跳转链接（跳「运行摘要」应用）
 * 11 Grok 视频编辑/扩展热开关（独立接口 /admin/settings/grok-video-transforms，
 *    保存即生效、不重启进程；关闭时路由返回 404）
 * 12 Grok 运行时自检（GrokSanityCard，GET /admin/grok/runtime-sanity 只读校验明细）
 * ============================================================ */
import { ref, reactive, onMounted } from 'vue'
import { request as apiRequest, isCancellation } from '../../api'
import { settingsErrorMessage } from '../../api/settings'
import { useSettingsContract } from '../../composables/useSettingsContract'
import { createLatestRequest } from '../../api/latestRequest'
import { useDraftBaseline, cloneDraft, requireFields, sameDraft } from '../../composables/useDraftBaseline'
import { message } from 'ant-design-vue'
import { openApp } from '../../core/apps'
import { clampInt } from './fields'
import FastPolicyCard from './FastPolicyCard.vue'
import GrokSanityCard from './GrokSanityCard.vue'

const loading = ref(true)
const err = ref('')
const s = ref(null)          // /admin/settings 原始回显
const fast = reactive({ version: 1, priority_detection_by_request_enabled: true, priority_detection_by_response_enabled: true, rules: [] })
const savingKey = ref('')
const loadLane = createLatestRequest()
const contract = useSettingsContract(() => { loadLane.cancel(); draft.clear(); s.value = null; fast.rules = []; err.value = '' })
const draft = useDraftBaseline(() => s.value && ({ ...s.value, openai_fast_policy_settings: fast }), () => !!savingKey.value)
const { conflict } = contract
const request = (path, options) => apiRequest(path, { ...options, ...contract.lifetime, signal: options?.signal || contract.lifetime.signal })

const load = async () => {
  if (!!savingKey.value) return
  const turn = loadLane.begin()
  loading.value = true
  try {
    const [d, videoTransforms] = await Promise.all([
      contract.read({ signal: turn.signal }),
      request('/admin/settings/grok-video-transforms', { method: 'GET', signal: AbortSignal.any([turn.signal, contract.lifetime.signal]) }),
    ])
    if (!turn.isCurrent() || !contract.lifetime.isAlive()) return
    const edits = draft.edits()
    apply(d)
    draft.accept()
    for (const [key, value] of Object.entries(edits)) {
      if (key === 'openai_fast_policy_settings') Object.assign(fast, value)
      else s.value[key] = value
    }
    videoTransformsState.enabled = Boolean(videoTransforms?.enabled)
    videoTransformsState.loaded = true
    err.value = ''
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) err.value = e.message
  } finally {
    if (turn.isCurrent() && contract.lifetime.isAlive()) loading.value = false
  }
}

/* 11 Grok 视频编辑/扩展热开关：独立接口读写，保存后无需重启进程 */
const videoTransformsState = reactive({ enabled: false, loaded: false })
const saveVideoTransforms = async (enabled) => {
  if (savingKey.value || !videoTransformsState.loaded) return
  savingKey.value = 'videoTransforms'
  loadLane.cancel(); loading.value = false
  try {
    const updated = await request('/admin/settings/grok-video-transforms', { method: 'PUT', body: { enabled } })
    videoTransformsState.enabled = Boolean(updated?.enabled)
    videoTransformsState.loaded = true
    message.success(enabled ? '已开启，视频编辑/扩展立即生效' : '已关闭，视频编辑/扩展立即返回 404')
  } catch (e) {
    if (isCancellation(e) || !contract.lifetime.isAlive()) return
    // 失败时回读真实状态，避免开关视觉状态与后台实际值不一致
    try {
      const current = await request('/admin/settings/grok-video-transforms', { method: 'GET' })
      videoTransformsState.enabled = Boolean(current?.enabled)
    } catch { /* 回读失败保持原值，由用户重试 */ }
    if (contract.lifetime.isAlive()) message.error(`保存失败：${e.message}`)
  } finally {
    savingKey.value = ''
  }
}

/* 回显（对照旧 applySettings：布尔默认、数值 clamp） */
const apply = (d) => {
  requireFields(d, [
    "gateway_custom_error_message",
    "enable_model_fallback",
    "fallback_model_anthropic",
    "fallback_model_openai",
    "fallback_model_gemini",
    "fallback_model_antigravity",
    "enable_identity_patch",
    "identity_patch_prompt",
    "min_claude_code_version",
    "max_claude_code_version",
    "antigravity_user_agent_version",
    "openai_codex_user_agent",
    "openai_codex_client_version",
    "openai_codex_version_auto_sync_enabled",
    "allow_ungrouped_key_scheduling",
    "enable_fingerprint_unification",
    "enable_metadata_passthrough",
    "enable_cch_signing",
    "enable_anthropic_cache_ttl_1h_injection",
    "rewrite_message_cache_control",
    "openai_allow_claude_code_codex_plugin",
    "enable_client_dateline_normalization",
    "grok_default_text_model",
    "grok_cross_client_model_map_enabled",
    "grok_scheduling_threshold_percent",
    "upstream_billing_auto_probe_enabled",
    "upstream_billing_probe_interval_minutes",
    "upstream_billing_probe_batch_size",
    "upstream_billing_probe_concurrency",
    "enable_claude_oauth_system_prompt_injection",
    "claude_oauth_system_prompt",
    "claude_oauth_system_prompt_blocks",
  ])
  s.value = {
    gateway_custom_error_message: d.gateway_custom_error_message || '',
    enable_model_fallback: Boolean(d.enable_model_fallback),
    fallback_model_anthropic: d.fallback_model_anthropic || '',
    fallback_model_openai: d.fallback_model_openai || '',
    fallback_model_gemini: d.fallback_model_gemini || '',
    fallback_model_antigravity: d.fallback_model_antigravity || '',
    enable_identity_patch: Boolean(d.enable_identity_patch),
    identity_patch_prompt: d.identity_patch_prompt || '',
    min_claude_code_version: d.min_claude_code_version || '',
    max_claude_code_version: d.max_claude_code_version || '',
    antigravity_user_agent_version: d.antigravity_user_agent_version || '',
    openai_codex_user_agent: d.openai_codex_user_agent || '',
    openai_codex_client_version: d.openai_codex_client_version || '',
    openai_codex_client_version_synced: d.openai_codex_client_version_synced || '',
    openai_codex_version_auto_sync_enabled: d.openai_codex_version_auto_sync_enabled !== false,
    allow_ungrouped_key_scheduling: Boolean(d.allow_ungrouped_key_scheduling),
    enable_fingerprint_unification: Boolean(d.enable_fingerprint_unification),
    enable_metadata_passthrough: Boolean(d.enable_metadata_passthrough),
    enable_cch_signing: Boolean(d.enable_cch_signing),
    enable_anthropic_cache_ttl_1h_injection: Boolean(d.enable_anthropic_cache_ttl_1h_injection),
    rewrite_message_cache_control: Boolean(d.rewrite_message_cache_control),
    openai_allow_claude_code_codex_plugin: Boolean(d.openai_allow_claude_code_codex_plugin),
    enable_client_dateline_normalization: d.enable_client_dateline_normalization !== false,
    grok_default_text_model: d.grok_default_text_model || 'grok-4.5',
    grok_cross_client_model_map_enabled: Boolean(d.grok_cross_client_model_map_enabled),
    grok_scheduling_threshold_percent: clampInt(d.grok_scheduling_threshold_percent, 100, 1, 100),
    upstream_billing_auto_probe_enabled: Boolean(d.upstream_billing_auto_probe_enabled),
    upstream_billing_probe_interval_minutes: clampInt(d.upstream_billing_probe_interval_minutes, 30, 5, 1440),
    upstream_billing_probe_batch_size: clampInt(d.upstream_billing_probe_batch_size, 20, 1, 20),
    upstream_billing_probe_concurrency: clampInt(d.upstream_billing_probe_concurrency, 4, 1, 4),
    enable_claude_oauth_system_prompt_injection: Boolean(d.enable_claude_oauth_system_prompt_injection),
    claude_oauth_system_prompt: d.claude_oauth_system_prompt || '',
    claude_oauth_system_prompt_blocks: d.claude_oauth_system_prompt_blocks || '',
  }
  // Fast/Flex：双检测全关时按旧逻辑回正为双开
  const fp = d.openai_fast_policy_settings
  fast.version = fp?.version || 1
  fast.priority_detection_by_request_enabled = fp?.priority_detection_by_request_enabled !== false
  fast.priority_detection_by_response_enabled = fp?.priority_detection_by_response_enabled !== false
  if (!fast.priority_detection_by_request_enabled && !fast.priority_detection_by_response_enabled) {
    fast.priority_detection_by_request_enabled = true
    fast.priority_detection_by_response_enabled = true
  }
  fast.rules = (fp?.rules || []).map((rule) => ({ ...rule, user_ids: [...(rule.user_ids || [])], model_whitelist: [...(rule.model_whitelist || [])] }))
}

/* 分区保存仅回填本区规范化值；其他卡片的未保存草稿必须保留。 */
const savePart = async (key, fields, extra) => {
  if (savingKey.value || !s.value) return
  savingKey.value = key
  loadLane.cancel(); loading.value = false
  try {
    const body = JSON.parse(JSON.stringify({ ...fields, ...extra }))
    const submitted = cloneDraft({ ...s.value, openai_fast_policy_settings: fast })
    const updated = await contract.save(body)
    requireFields(updated, Object.keys(body))
    draft.acceptPart(Object.fromEntries(Object.keys(body).map(field => [field, updated[field]])))
    for (const field of Object.keys(body)) {
      if (field === 'openai_fast_policy_settings') {
        if (sameDraft(fast, submitted[field])) Object.assign(fast, structuredClone(updated[field]))
      } else {
        if (sameDraft(s.value[field], submitted[field])) s.value[field] = updated[field]
      }
    }
    message.success('已保存')
  } catch (e) {
    if (!isCancellation(e)) message.error(`保存失败：${settingsErrorMessage(e)}`)
  } finally {
    savingKey.value = ''
  }
}

const v = () => s.value
const str = (k) => String(v()[k] || '').trim()

/* 1 错误文案 */
const saveErrMsg = () => savePart('errmsg', { gateway_custom_error_message: str('gateway_custom_error_message') })

/* 2 模型兜底 / 3 身份补丁 / 4 版本 UA / 5 行为开关 / 6 Grok / 7 探测 */
const saveFallback = () => savePart('fallback', {
  enable_model_fallback: v().enable_model_fallback,
  fallback_model_anthropic: str('fallback_model_anthropic'),
  fallback_model_openai: str('fallback_model_openai'),
  fallback_model_gemini: str('fallback_model_gemini'),
  fallback_model_antigravity: str('fallback_model_antigravity'),
})
const saveIdentity = () => savePart('identity', { enable_identity_patch: v().enable_identity_patch, identity_patch_prompt: v().identity_patch_prompt })
const saveVersions = () => savePart('versions', {
  min_claude_code_version: str('min_claude_code_version'),
  max_claude_code_version: str('max_claude_code_version'),
  antigravity_user_agent_version: str('antigravity_user_agent_version'),
  openai_codex_user_agent: str('openai_codex_user_agent'),
  openai_codex_client_version: str('openai_codex_client_version'),
  openai_codex_version_auto_sync_enabled: v().openai_codex_version_auto_sync_enabled,
})
const SWITCH_FIELDS = [
  'allow_ungrouped_key_scheduling', 'enable_fingerprint_unification', 'enable_metadata_passthrough',
  'enable_cch_signing', 'enable_anthropic_cache_ttl_1h_injection', 'rewrite_message_cache_control',
  'openai_allow_claude_code_codex_plugin', 'enable_client_dateline_normalization',
]
const saveSwitches = () => {
  const fields = {}
  for (const f of SWITCH_FIELDS) fields[f] = Boolean(v()[f])
  return savePart('switches', fields)
}
const saveGrok = () => {
  // 旧契约：阈值必须是 1-100 的整数（100 = 关闭停调）
  const t = Number(v().grok_scheduling_threshold_percent)
  if (!Number.isInteger(t) || t < 1 || t > 100) { message.error('Grok 调度阈值必须是 1 到 100 的整数'); return }
  return savePart('grok', {
    grok_default_text_model: str('grok_default_text_model') || 'grok-4.5',
    grok_cross_client_model_map_enabled: v().grok_cross_client_model_map_enabled,
    grok_scheduling_threshold_percent: clampInt(t, 100, 1, 100),
  })
}
const PROBE_PRESETS = {
  interval: [15, 30, 60, 180],
  batch: [5, 10, 20],
  concurrency: [1, 2, 3, 4],
}
const fmtMinutes = (m) => (m < 60 ? `${m} 分钟` : `${m / 60} 小时`)
const saveProbe = () => savePart('probe', {
  upstream_billing_auto_probe_enabled: v().upstream_billing_auto_probe_enabled,
  upstream_billing_probe_interval_minutes: clampInt(v().upstream_billing_probe_interval_minutes, 30, 5, 1440),
  upstream_billing_probe_batch_size: clampInt(v().upstream_billing_probe_batch_size, 20, 1, 20),
  upstream_billing_probe_concurrency: clampInt(v().upstream_billing_probe_concurrency, 4, 1, 4),
})

/* 8 Fast/Flex：由子组件构建，这里负责 PUT */
const saveFast = (payload) => savePart('fast', {}, { openai_fast_policy_settings: payload })

/* 非秘密提示词空字符串是显式清空，不得当作遗漏。 */
const saveClaudePrompt = () => savePart('claudefmt', {
  enable_claude_oauth_system_prompt_injection: v().enable_claude_oauth_system_prompt_injection,
  claude_oauth_system_prompt: v().claude_oauth_system_prompt,
  claude_oauth_system_prompt_blocks: v().claude_oauth_system_prompt_blocks,
})

onMounted(load)
</script>

<template>
  <a-spin :spinning="loading">
    <div class="sw w12-design">
      <div v-if="err" class="card"><W12Button @click="load" variant="danger">加载失败：{{ err }}（点击重试）</W12Button></div>
      <a-alert v-if="conflict" :message="conflict" type="warning" show-icon>
        <template #action><W12Button :disabled="!!savingKey" @click="load">重新读取</W12Button></template>
      </a-alert>
      <template v-if="s">
        <!-- 1 客户端统一错误文案 -->
        <div class="card">
          <div class="card-title">客户端统一错误文案</div>
          <div class="frow frow-col">
            <label>错误文案</label>
            <div class="fmain fm-col">
              <a-textarea v-model:value="s.gateway_custom_error_message" :maxlength="500" :rows="4" placeholder="留空时返回真实错误；填写后，客户看到的错误说明统一替换为此文案。" />
              <div class="hint">只替换说明文字，HTTP 状态码和错误代码保持真实，内部日志仍记录原始原因（{{ (s.gateway_custom_error_message || '').length }}/500）。</div>
            </div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="savingKey === 'errmsg'" @click="saveErrMsg">保存</W12Button></div>
        </div>

        <!-- 2 模型兜底 -->
        <div class="card">
          <div class="card-title">模型兜底</div>
          <div class="form">
            <div class="frow"><label>启用模型兜底</label><div class="fmain"><a-switch v-model:checked="s.enable_model_fallback" /><span class="static">当请求模型需要重写时使用对应平台兜底模型</span></div></div>
            <template v-if="s.enable_model_fallback">
              <div class="frow"><label>Anthropic 兜底模型</label><W12Input v-model:value="s.fallback_model_anthropic" class="fld mono" /></div>
              <div class="frow"><label>OpenAI 兜底模型</label><W12Input v-model:value="s.fallback_model_openai" class="fld mono" /></div>
              <div class="frow"><label>Gemini 兜底模型</label><W12Input v-model:value="s.fallback_model_gemini" class="fld mono" /></div>
              <div class="frow"><label>Antigravity 兜底模型</label><W12Input v-model:value="s.fallback_model_antigravity" class="fld mono" /></div>
            </template>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="savingKey === 'fallback'" @click="saveFallback">保存</W12Button></div>
        </div>

        <!-- 3 身份补丁 -->
        <div class="card">
          <div class="card-title">身份补丁</div>
          <div class="form">
            <div class="frow"><label>启用身份补丁</label><div class="fmain"><a-switch v-model:checked="s.enable_identity_patch" /><span class="static">用于 Claude 到 Gemini 等场景的系统提示补丁</span></div></div>
            <div v-if="s.enable_identity_patch" class="frow" style="align-items: flex-start">
              <label>补丁提示词</label>
              <div class="fmain" style="flex-direction: column; align-items: stretch"><a-textarea v-model:value="s.identity_patch_prompt" class="mono" :rows="5" auto-size /></div>
            </div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="savingKey === 'identity'" @click="saveIdentity">保存</W12Button></div>
        </div>

        <!-- 4 版本与 UA -->
        <div class="card">
          <div class="card-title">客户端版本与 User-Agent</div>
          <div class="form">
            <div class="frow"><label>最低 Claude Code 版本</label><W12Input v-model:value="s.min_claude_code_version" class="fld mono" placeholder="1.0.0" /></div>
            <div class="frow"><label>最高 Claude Code 版本</label><W12Input v-model:value="s.max_claude_code_version" class="fld mono" placeholder="2.0.0" /></div>
            <div class="frow"><label>Antigravity UA 版本</label><W12Input v-model:value="s.antigravity_user_agent_version" class="fld mono" /></div>
            <div class="frow"><label>OpenAI Codex UA</label><W12Input v-model:value="s.openai_codex_user_agent" class="fld mono" /></div>
            <div class="frow"><label>固定 Codex 版本</label><div class="fmain"><W12Input v-model:value="s.openai_codex_client_version" class="fld mono" placeholder="0.146.0" /><span class="static">留空时使用自动同步版本</span></div></div>
            <div class="frow"><label>当前自动同步版本</label><span class="static mono">{{ s.openai_codex_client_version_synced || '-' }}</span></div>
            <div class="frow"><label>自动同步 Codex 稳定版本</label><div class="fmain"><a-switch v-model:checked="s.openai_codex_version_auto_sync_enabled" /><span class="static">默认每 6 小时检查一次；失败保留旧值，只向前升级</span></div></div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="savingKey === 'versions'" @click="saveVersions">保存</W12Button></div>
        </div>

        <!-- 5 行为开关组（7 开关 + 日期行归一化） -->
        <div class="card">
          <div class="card-title">转发行为开关</div>
          <div class="sw-grid">
            <div class="frow"><label>允许未分组 Key 调度</label><div class="fmain"><a-switch v-model:checked="s.allow_ungrouped_key_scheduling" /></div></div>
            <div class="frow"><label>统一指纹</label><div class="fmain"><a-switch v-model:checked="s.enable_fingerprint_unification" /></div></div>
            <div class="frow"><label>透传 metadata</label><div class="fmain"><a-switch v-model:checked="s.enable_metadata_passthrough" /></div></div>
            <div class="frow"><label>CCH 签名（已废弃）</label><div class="fmain"><a-switch v-model:checked="s.enable_cch_signing" /></div></div>
            <div class="frow"><label>注入 1h 缓存 TTL</label><div class="fmain"><a-switch v-model:checked="s.enable_anthropic_cache_ttl_1h_injection" /></div></div>
            <div class="frow"><label>重写消息缓存控制</label><div class="fmain"><a-switch v-model:checked="s.rewrite_message_cache_control" /></div></div>
            <div class="frow"><label>允许 Claude Code Codex 插件</label><div class="fmain"><a-switch v-model:checked="s.openai_allow_claude_code_codex_plugin" /></div></div>
            <div class="frow"><label>客户端日期行指纹归一化</label><div class="fmain"><a-switch v-model:checked="s.enable_client_dateline_normalization" /></div></div>
          </div>
          <div class="hint" style="padding-left: 8px">日期行归一化默认开启：只规范 Anthropic OAuth / Setup Token 请求中系统提示的日期行，清除非官方地址触发的隐藏指纹；API Key 账号不受影响。</div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="savingKey === 'switches'" @click="saveSwitches">保存</W12Button></div>
        </div>

        <!-- 6 Grok 组 -->
        <div class="card">
          <div class="card-title">Grok 调度</div>
          <div class="form">
            <div class="frow"><label>Grok 默认文本模型</label><div class="fmain"><W12Input v-model:value="s.grok_default_text_model" class="fld mono" placeholder="grok-4.5" /><span class="static">仅用于没有账号级映射的 Grok 账号</span></div></div>
            <div class="frow"><label>跨客户端模型映射</label><div class="fmain"><a-switch v-model:checked="s.grok_cross_client_model_map_enabled" /><span class="static">把 GPT、Codex、o 系列和 Claude 模型映射到上面的 Grok 模型</span></div></div>
            <div class="frow"><label>上游配额停调阈值（%）</label><div class="fmain"><a-input-number v-model:value="s.grok_scheduling_threshold_percent" :min="1" :max="100" :precision="0" class="fld-n" /><span class="static">1–99 达到阈值后暂停账号；100 表示关闭</span></div></div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="savingKey === 'grok'" @click="saveGrok">保存</W12Button></div>
        </div>

        <!-- 11 Grok 视频编辑/扩展热开关（保存即生效，不重启进程） -->
        <div class="card">
          <div class="card-title">Grok 视频编辑 / 扩展</div>
          <div class="card-sub">控制 <code>POST /v1/videos/edits</code>、<code>POST /v1/videos/extensions</code>。开关存数据库并立刻发布到进程内快照，保存后下一个请求即按新值判定，不需要重启网关。</div>
          <div class="form">
            <div class="frow">
              <label>允许视频编辑与扩展</label>
              <div class="fmain">
                <a-switch
                  :checked="videoTransformsState.enabled"
                  :loading="savingKey === 'videoTransforms'"
                  @change="(v) => saveVideoTransforms(Boolean(v))"
                />
                <span class="static">{{ videoTransformsState.enabled ? '已开启：请求会转发到 Grok 视频变换上游' : '已关闭：这两个接口返回 404（与未提供该能力一致）' }}</span>
              </div>
            </div>
            <div class="frow">
              <label>当前生效值</label>
              <div class="fmain">
                <span class="static mono">{{ videoTransformsState.loaded ? (videoTransformsState.enabled ? 'enabled' : 'disabled') : '读取中…' }}</span>
                <span class="static">首次保存前，默认值来自节点启动配置 gateway.grok_video_transforms_enabled</span>
              </div>
            </div>
          </div>
          <div class="hint" style="padding-left: 8px">视频生成（<code>/videos/generations</code>）与状态/内容查询不受此开关影响，始终可用。编辑与扩展请求必须携带 <code>Idempotency-Key</code>，并且只有 Grok 分组可以调用。</div>
        </div>

        <!-- 12 Grok 运行时自检（GET /admin/grok/runtime-sanity，只读校验明细） -->
        <GrokSanityCard />

        <!-- 7 Sub2API 采购倍率自动探测 -->
        <div class="card">
          <div class="card-title">Sub2API 采购倍率自动探测</div>
          <div class="card-sub">默认关闭。只探测账号实际采购倍率，不改变用户价格；多节点只由一个节点执行。</div>
          <div class="form">
            <div class="frow"><label>启用自动探测</label><div class="fmain"><a-switch v-model:checked="s.upstream_billing_auto_probe_enabled" /></div></div>
            <template v-if="s.upstream_billing_auto_probe_enabled">
              <div class="preset-group">
                <span>探测周期（分钟）</span>
                <a-input-number v-model:value="s.upstream_billing_probe_interval_minutes" :min="5" :max="1440" :precision="0" class="fld-n" />
                <button v-for="m in PROBE_PRESETS.interval" :key="`i${m}`" type="button" class="preset-btn" :class="{ on: s.upstream_billing_probe_interval_minutes === m }" @click="s.upstream_billing_probe_interval_minutes = m">{{ fmtMinutes(m) }}</button>
              </div>
              <div class="preset-group">
                <span>单批账号</span>
                <a-input-number v-model:value="s.upstream_billing_probe_batch_size" :min="1" :max="20" :precision="0" class="fld-n" />
                <button v-for="n in PROBE_PRESETS.batch" :key="`b${n}`" type="button" class="preset-btn" :class="{ on: s.upstream_billing_probe_batch_size === n }" @click="s.upstream_billing_probe_batch_size = n">{{ n }}</button>
              </div>
              <div class="preset-group">
                <span>并发探测</span>
                <a-input-number v-model:value="s.upstream_billing_probe_concurrency" :min="1" :max="4" :precision="0" class="fld-n" />
                <button v-for="c in PROBE_PRESETS.concurrency" :key="`c${c}`" type="button" class="preset-btn" :class="{ on: s.upstream_billing_probe_concurrency === c }" @click="s.upstream_billing_probe_concurrency = c">{{ c }}</button>
              </div>
            </template>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="savingKey === 'probe'" @click="saveProbe">保存</W12Button></div>
        </div>

        <!-- 8 OpenAI Fast/Flex 策略 -->
        <FastPolicyCard :model="fast" :saving="savingKey === 'fast'" @save="saveFast" />

        <!-- 9 Claude OAuth 系统提示词注入 -->
        <div class="card">
          <div class="card-title">Claude OAuth 系统提示词注入</div>
          <div class="form">
            <div class="frow"><label>启用注入</label><div class="fmain"><a-switch v-model:checked="s.enable_claude_oauth_system_prompt_injection" /><span class="static">保留网关转发层的系统提示词注入能力</span></div></div>
            <template v-if="s.enable_claude_oauth_system_prompt_injection">
              <div class="frow" style="align-items: flex-start"><label>系统提示词</label><div class="fmain" style="flex-direction: column; align-items: stretch"><a-textarea v-model:value="s.claude_oauth_system_prompt" class="mono" :rows="5" auto-size /></div></div>
              <div class="frow" style="align-items: flex-start"><label>提示词 blocks JSON</label><div class="fmain" style="flex-direction: column; align-items: stretch"><a-textarea v-model:value="s.claude_oauth_system_prompt_blocks" class="mono" :rows="6" auto-size placeholder="[ { &quot;type&quot;: &quot;text&quot;, &quot;text&quot;: &quot;...&quot; } ]" /></div></div>
            </template>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="savingKey === 'claudefmt'" @click="saveClaudePrompt">保存</W12Button></div>
        </div>

        <!-- 10 高级调度跳转 -->
        <div class="card">
          <div class="frow">
            <label>高级调度与采购成本排序</label>
            <div class="fmain" style="justify-content: space-between">
              <span class="static">调度开关、Top-K、订阅池偏好和全部权重集中在「运行摘要」应用，使用修订号原子保存。</span>
              <W12Button @click="openApp('runtimeSummary')">打开运行策略</W12Button>
            </div>
          </div>
        </div>
      </template>
    </div>
  </a-spin>
</template>

<style scoped>
@import './settings.css';
</style>

