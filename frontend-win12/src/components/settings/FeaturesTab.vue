<script setup>
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
/* ============================================================
 * 功能 tab：网关功能开关（对照旧 SettingsView.vue「功能」tab）
 * 即时开关（乐观更新，失败回滚）：
 *  - 聚合分组  GET/PUT /admin/settings/composite-groups
 *  - 防重复结算 GET/PUT /admin/settings/billing-settlement
 *  - Codex Tail-Burst GET/PUT /admin/ops/codex-tail-burst（旧网关可能省略响应字段，按请求值兜底）
 *  - ChatGPT Live 总开关 PUT /admin/gateway-lite/config（全量 payload）
 * 表单分区（独立保存，PATCH /admin/settings 带当前修订局部更新）：
 *  - 渠道监控组 enabled/mode(v1|v2)/间隔/隐藏吞吐
 *  - 风控中心 + 会话阻断 + 阻断时长（60-604800 秒）
 * ============================================================ */
import { ref, reactive, onMounted } from 'vue'
import { request as apiRequest, isCancellation } from '../../api'
import { settingsErrorMessage } from '../../api/settings'
import { assertGatewayConfig, saveGatewayConfig } from '../../api/gatewayConfig'
import { useSettingsContract } from '../../composables/useSettingsContract'
import { createLatestRequest } from '../../api/latestRequest'
import { useDraftBaseline, requireFields } from '../../composables/useDraftBaseline'
import { message } from 'ant-design-vue'
import { clampInt } from './fields'

const loading = ref(true)
const err = ref('')

// ── 三个即时开关的独立状态 ──
const cg = reactive({ loaded: false, saving: false, enabled: false })
const bs = reactive({ loaded: false, saving: false, idempotent: false, mode: 'legacy', legacyWarning: false })
const tb = reactive({ loaded: false, saving: false, enabled: false, cached: null })

// ── /admin/settings 表单分区（渠道监控 + 风控/会话阻断）──
const fs = ref(null)
const savingFs = ref(false)

// ── ChatGPT Live（gateway-lite config）──
const glc = ref(null)
const savingLive = ref(false)
const loadLane = createLatestRequest()
const contract = useSettingsContract(() => { loadLane.cancel(); draft.clear();
  fs.value = null; glc.value = null; err.value = ''
  cg.loaded = bs.loaded = tb.loaded = false; tb.cached = null
})
const draft = useDraftBaseline(() => fs.value, () => savingFs.value || savingLive.value || cg.saving || bs.saving || tb.saving)
const { conflict } = contract
const request = (path, options) => apiRequest(path, { ...options, ...contract.lifetime, signal: options?.signal || contract.lifetime.signal })
const applyFs = (settings) => {
  requireFields(settings, ["channel_monitor_enabled","channel_monitor_mode","channel_monitor_default_interval_seconds","channel_monitor_hide_throughput","risk_control_enabled","cyber_session_block_enabled","cyber_session_block_ttl_seconds","cyber_policy_user_allowlist"])
  fs.value = {
    channel_monitor_enabled: settings.channel_monitor_enabled !== false,
    channel_monitor_mode: settings.channel_monitor_mode === 'v2' ? 'v2' : 'v1',
    channel_monitor_default_interval_seconds: clampInt(settings.channel_monitor_default_interval_seconds, 60, 10, 86400),
    channel_monitor_hide_throughput: settings.channel_monitor_hide_throughput !== false,
    risk_control_enabled: settings.risk_control_enabled !== false,
    cyber_session_block_enabled: Boolean(settings.cyber_session_block_enabled),
    cyber_session_block_ttl_seconds: clampInt(settings.cyber_session_block_ttl_seconds, 3600, 60, 604800),
    cyber_policy_user_allowlist: String(settings.cyber_policy_user_allowlist || ''),
  }
}

const load = async () => {
  if (savingFs.value || savingLive.value || cg.saving || bs.saving || tb.saving) return
  const turn = loadLane.begin()
  loading.value = true
  try {
    const [cgRes, bsRes, tbRes, settings, config] = await Promise.all([
      request('/admin/settings/composite-groups', { method: 'GET', signal: AbortSignal.any([turn.signal, contract.lifetime.signal]) }).catch(() => null),
      request('/admin/settings/billing-settlement', { method: 'GET', signal: AbortSignal.any([turn.signal, contract.lifetime.signal]) }).catch(() => null),
      request('/admin/ops/codex-tail-burst', { method: 'GET', signal: AbortSignal.any([turn.signal, contract.lifetime.signal]) }).catch(() => null),
      contract.read({ signal: turn.signal }),
      request('/admin/gateway-lite/config', { method: 'GET', signal: AbortSignal.any([turn.signal, contract.lifetime.signal]) }),
    ])
    if (!turn.isCurrent() || !contract.lifetime.isAlive()) return
    assertGatewayConfig(config)
    if (cgRes) { cg.enabled = Boolean(cgRes.enabled); cg.loaded = true }
    if (bsRes) { bs.idempotent = Boolean(bsRes.idempotent_enabled); bs.mode = bsRes.mode || 'legacy'; bs.legacyWarning = Boolean(bsRes.legacy_warning); bs.loaded = true }
    if (tbRes) { tb.cached = tbRes; tb.enabled = tbRes.enabled ?? false; tb.loaded = true }
    // 渠道监控/风控默认值对照旧 applySettings：undefined 视为开启
    const edits = draft.edits()
    applyFs(settings)
    draft.accept()
    Object.assign(fs.value, edits)
    glc.value = assertGatewayConfig(config)
    err.value = ''
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) err.value = e.message
  } finally {
    if (turn.isCurrent() && contract.lifetime.isAlive()) loading.value = false
  }
}

/* ── 聚合分组：乐观切换，失败回滚 ── */
const toggleComposite = async (enabled) => {
  if (!cg.loaded || cg.saving || enabled === cg.enabled) return
  const prev = cg.enabled
  cg.enabled = enabled
  cg.saving = true
  loadLane.cancel(); loading.value = false
  try {
    const updated = await request('/admin/settings/composite-groups', { method: 'PUT', body: { enabled } })
    requireFields(updated, ['enabled'])
    cg.enabled = Boolean(updated.enabled)
    message.success(updated.enabled ? '聚合分组已开启并即时生效' : '聚合分组已关闭，新请求将被拒绝')
  } catch (e) {
    if (isCancellation(e) || !contract.lifetime.isAlive()) return
    cg.enabled = prev
    message.error(`切换聚合分组失败，已恢复原状态：${e.message}`)
  } finally {
    cg.saving = false
  }
}

/* ── 防重复结算：乐观切换；保存失败必须恢复完整服务端状态 ── */
const toggleBilling = async (enabled) => {
  if (!bs.loaded || bs.saving || enabled === bs.idempotent) return
  const prev = { idempotent: bs.idempotent, mode: bs.mode }
  bs.idempotent = enabled
  bs.mode = enabled ? 'idempotent_v2' : 'legacy'
  bs.saving = true
  loadLane.cancel(); loading.value = false
  try {
    const updated = await request('/admin/settings/billing-settlement', { method: 'PUT', body: { idempotent_enabled: enabled } })
    requireFields(updated, ['idempotent_enabled'])
    bs.idempotent = Boolean(updated.idempotent_enabled)
    bs.mode = updated.mode || bs.mode
    message.success(updated.idempotent_enabled ? '防重复结算已开启' : '已切换到旧版结算')
  } catch (e) {
    if (isCancellation(e) || !contract.lifetime.isAlive()) return
    bs.idempotent = prev.idempotent
    bs.mode = prev.mode
    message.error(`切换结算模式失败，已恢复原状态：${e.message}`)
  } finally {
    bs.saving = false
  }
}

/* ── Codex Tail-Burst：乐观切换；兼容旧网关省略响应字段 ── */
const toggleTailBurst = async (enabled) => {
  if (!tb.loaded || tb.saving || !tb.cached || enabled === tb.enabled) return
  const prev = tb.enabled
  tb.enabled = enabled
  tb.saving = true
  loadLane.cancel(); loading.value = false
  try {
    const updated = await request('/admin/ops/codex-tail-burst', { method: 'PUT', body: { enabled } })
    tb.cached = { ...updated, enabled: updated?.enabled ?? enabled }
    tb.enabled = tb.cached.enabled ?? enabled
    message.success(enabled ? '超额已开启：贴近窗口上限或即将重置时放行，直到上游权威拒绝' : '超额已关闭：达到配额阈值即停调')
  } catch (e) {
    if (isCancellation(e) || !contract.lifetime.isAlive()) return
    tb.enabled = prev
    message.error(`切换超额开关失败，已恢复原状态：${e.message}`)
  } finally {
    tb.saving = false
  }
}

/* ── 渠道监控 + 风控/会话阻断：PATCH /admin/settings 带当前修订局部更新 ── */
const saveFs = async () => {
  if (savingFs.value || !fs.value) return
  savingFs.value = true
  loadLane.cancel(); loading.value = false
  try {
    const f = fs.value
    const body = {
      channel_monitor_enabled: f.channel_monitor_enabled,
      channel_monitor_mode: f.channel_monitor_mode,
      channel_monitor_default_interval_seconds: clampInt(f.channel_monitor_default_interval_seconds, 60, 10, 86400),
      channel_monitor_hide_throughput: f.channel_monitor_hide_throughput,
      risk_control_enabled: f.risk_control_enabled,
      cyber_session_block_enabled: f.cyber_session_block_enabled,
      cyber_session_block_ttl_seconds: clampInt(f.cyber_session_block_ttl_seconds, 3600, 60, 604800),
      cyber_policy_user_allowlist: String(f.cyber_policy_user_allowlist || '').trim(),
    }
    const submitted = JSON.parse(JSON.stringify(fs.value))
    const updated = await contract.save(body)
    const current = fs.value
    applyFs(updated)
    draft.accept()
    for (const [key, value] of Object.entries(current)) if (JSON.stringify(value) !== JSON.stringify(submitted[key])) fs.value[key] = value
    message.success('功能开关已保存')
  } catch (e) {
    if (!isCancellation(e)) message.error(`保存失败：${settingsErrorMessage(e)}`)
  } finally {
    savingFs.value = false
  }
}

/* ── ChatGPT Live：走 gateway-lite config 全量 payload（对照旧 persistGatewayLiteConfig）── */

const toggleLive = async (enabled) => {
  if (!glc.value || savingLive.value) return
  const prev = glc.value.allow_live
  glc.value.allow_live = enabled
  savingLive.value = true
  loadLane.cancel(); loading.value = false
  try {
    const updated = await saveGatewayConfig({ allow_live: enabled }, contract.lifetime)
    Object.assign(glc.value, updated, { control_plane_token: '' })
    message.success(updated.allow_live ? 'ChatGPT Live 已开启' : 'ChatGPT Live 已关闭')
  } catch (e) {
    if (isCancellation(e) || !contract.lifetime.isAlive() || !glc.value) return
    glc.value.allow_live = prev
    message.error(`切换 ChatGPT Live 失败，已恢复原状态：${e.message}`)
  } finally {
    savingLive.value = false
  }
}

onMounted(load)
</script>

<template>
  <a-spin :spinning="loading">
    <div class="sw w12-design">
      <div v-if="err" class="card"><W12Button @click="load" variant="danger">加载失败：{{ err }}（点击重试）</W12Button></div>
      <a-alert v-if="conflict" :message="conflict" type="warning" show-icon>
        <template #action><W12Button :disabled="savingFs" @click="load">重新读取</W12Button></template>
      </a-alert>
      <template v-if="fs">
        <!-- 聚合分组（即时生效） -->
        <div class="card">
          <div class="frow"><label>聚合分组（多平台）</label><div class="fmain"><a-switch :checked="cg.enabled" :loading="cg.saving" :disabled="!cg.loaded" @change="toggleComposite" /></div></div>
          <div class="hint" style="padding-left: 8px">
            {{ cg.enabled ? '已允许创建聚合分组，并可按模型路由到不同平台账号。' : '关闭时保留已有配置，但已有聚合 Key 的新请求会返回 503。' }}
          </div>
        </div>
        <!-- Codex Tail-Burst（即时生效） -->
        <div class="card">
          <div class="frow"><label>Codex 超额（Tail-Burst）</label><div class="fmain"><a-switch :checked="tb.enabled" :loading="tb.saving" :disabled="!tb.loaded" @change="toggleTailBurst" /></div></div>
          <div class="hint" style="padding-left: 8px">
            {{ tb.enabled ? '账号贴近窗口上限或窗口/订阅即将重置时，跳过本地预防性停调，继续承接请求直到上游返回权威配额拒绝。' : '关闭时行为与普通配额阈值一致：达到阈值即停调，不做尾部放行。' }}
          </div>
        </div>
        <!-- 防重复结算（即时生效） -->
        <div class="card">
          <div class="frow"><label>防重复结算（推荐）</label><div class="fmain"><a-switch :checked="bs.idempotent" :loading="bs.saving" :disabled="!bs.loaded" @change="toggleBilling" /></div></div>
          <div class="hint" style="padding-left: 8px">
            {{ bs.idempotent ? '同一请求即使重复回调，也只推进一次不足一美分累计。' : '使用旧版累计逻辑，仅建议用于临时回退。' }}
          </div>
          <a-alert v-if="bs.loaded && !bs.idempotent && bs.legacyWarning" style="margin-top: 8px" message="当前为旧版结算：同一请求重复进入结算时，可能再次累计不足一美分余额并造成多扣。" type="warning" show-icon />
        </div>
        <!-- 渠道监控组 -->
        <div class="card">
          <div class="card-title">渠道监控</div>
          <div class="form">
            <div class="frow"><label>启用</label><div class="fmain"><a-switch v-model:checked="fs.channel_monitor_enabled" /><span class="static">可选择主动探测，或根据真实网关流量被动统计</span></div></div>
            <template v-if="fs.channel_monitor_enabled">
              <div class="frow">
                <label>监控模式</label>
                <div class="fmain" style="gap: 8px">
                  <button type="button" class="preset-btn" :class="{ on: fs.channel_monitor_mode === 'v2' }" style="height: 32px" @click="fs.channel_monitor_mode = 'v2'">V2 被动统计（推荐）</button>
                  <button type="button" class="preset-btn" :class="{ on: fs.channel_monitor_mode === 'v1' }" style="height: 32px" @click="fs.channel_monitor_mode = 'v1'">V1 主动探测</button>
                </div>
              </div>
              <div v-if="fs.channel_monitor_mode === 'v1'" class="frow"><label>默认监控间隔（秒）</label><a-input-number v-model:value="fs.channel_monitor_default_interval_seconds" :min="10" :max="86400" class="fld-n" /></div>
              <div v-if="fs.channel_monitor_mode === 'v2'" class="frow"><label>向普通用户隐藏 RPM/TPM</label><div class="fmain"><a-switch v-model:checked="fs.channel_monitor_hide_throughput" /><span class="static">管理员始终可以查看吞吐数据</span></div></div>
            </template>
          </div>
        </div>
        <!-- 风控中心 + 会话阻断 -->
        <div class="card">
          <div class="card-title">风控与会话阻断</div>
          <div class="form">
            <div class="frow"><label>风控中心</label><div class="fmain"><a-switch v-model:checked="fs.risk_control_enabled" /><span class="static">启用请求拦截、限流和安全策略配置</span></div></div>
            <div class="frow"><label>会话阻断</label><div class="fmain"><a-switch v-model:checked="fs.cyber_session_block_enabled" /><span class="static">对命中风控的会话设置临时阻断时间</span></div></div>
            <div v-if="fs.cyber_session_block_enabled" class="frow"><label>阻断时长（秒）</label><a-input-number v-model:value="fs.cyber_session_block_ttl_seconds" :min="60" :max="604800" class="fld-n" /></div>
            <div v-if="fs.cyber_session_block_enabled" class="frow"><label>只记录不拦截（用户 ID）</label><div class="fmain"><W12Input v-model:value="fs.cyber_policy_user_allowlist" class="fld" placeholder="逗号或空格分隔，如 12,34" /><span class="static">列出的用户命中风控时不阻断、不封号，仅留证</span></div></div>
          </div>
        </div>
        <!-- ChatGPT Live（即时生效） -->
        <div class="card">
          <div class="frow"><label>ChatGPT Live（实时会话）</label><div class="fmain"><a-switch :checked="glc?.allow_live" :loading="savingLive" :disabled="!glc" @change="toggleLive" /></div></div>
          <div class="hint" style="padding-left: 8px">节点总开关；OpenAI 分组还需单独允许。Linux 无设备证明时会安全拒绝。</div>
        </div>
        <div class="savebar"><W12Button variant="primary" class="save" :loading="savingFs" @click="saveFs">保存功能开关</W12Button></div>
      </template>
    </div>
  </a-spin>
</template>

<style scoped>
@import './settings.css';
</style>

