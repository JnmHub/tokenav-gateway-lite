<script setup>
/* ============================================================
 * 策略 tab（对照旧 SettingsView.vue「策略」tab）
 * 六个分区各自独立 GET/PUT（保存前数值 clamp，对照旧代码区间）：
 *  - 529 过载冷却      /admin/settings/overload-cooldown        cooldown_minutes 1-120
 *  - 429 限流冷却      /admin/settings/rate-limit-429-cooldown  cooldown_seconds 1-3600
 *  - 账号兜底排队      /admin/settings/fallback-queue           max_waiting 1-100000 / wait 1-3600
 *  - 流超时处理        /admin/settings/stream-timeout           action 三选项 / 分钟 1-1440 / 次数 1-100 / 窗口 1-1440
 *  - 修正器            /admin/settings/rectifier                含 apikey_signature_patterns 多行文本
 *  - Beta 策略         /admin/settings/beta-policy              规则 CRUD + 空规则清洗
 * ============================================================ */
import { ref, reactive, onMounted } from 'vue'
import { request as apiRequest, isCancellation } from '../../api'
import { useRequestLifetime } from '../../composables/useRequestLifetime'
import { useDraftBaseline, cloneDraft, requireFields, retainEdits } from '../../composables/useDraftBaseline'
import { createLatestRequest } from '../../api/latestRequest'
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
import { message } from 'ant-design-vue'
import { clampInt } from './fields'

const loading = ref(true)
const err = ref('')

/* 每分区独立 loading/saving */
const busy = reactive({})
const setBusy = (k, on) => { busy[k] = on }

const STREAM_ACTIONS = [
  { value: 'temp_unsched', label: '临时下线' },
  { value: 'error', label: '直接报错' },
  { value: 'none', label: '仅记录' },
]
const BETA_ACTIONS = [
  { value: 'pass', label: '透传' },
  { value: 'filter', label: '过滤' },
  { value: 'block', label: '阻断' },
]
const BETA_SCOPES = [
  { value: 'all', label: '全部' },
  { value: 'oauth', label: 'OAuth' },
  { value: 'apikey', label: 'API Key' },
  { value: 'bedrock', label: 'Bedrock' },
]

const oc = ref(null)  // 529
const rl = ref(null)  // 429
const fq = ref(null)  // fallback queue
const st = ref(null)  // stream timeout
const rf = ref(null)  // rectifier
const rfPatterns = ref('') // 多行文本 ↔ 数组
const bp = ref(null)  // beta policy rules

const sections = { oc, rl, fq, st, rf, bp }
const fields = {
  oc: ['enabled', 'cooldown_minutes'], rl: ['enabled', 'cooldown_seconds'],
  fq: ['enabled', 'max_waiting', 'wait_timeout_seconds'],
  st: ['enabled', 'action', 'temp_unsched_minutes', 'threshold_count', 'threshold_window_minutes'],
  rf: ['enabled', 'thinking_signature_enabled', 'thinking_budget_enabled', 'apikey_signature_enabled', 'apikey_signature_patterns'],
  bp: ['rules'],
}
const lane = createLatestRequest()
const lifetime = useRequestLifetime(() => {
  lane.cancel(); for (const section of Object.values(sections)) section.value = null
  rfPatterns.value = ''; err.value = ''; draft.clear()
})
const getDraft = () => Object.fromEntries(Object.entries(sections).map(([key, value]) => [key, key === 'rf' && value.value ? { ...value.value, patternsInput: rfPatterns.value } : value.value]))
const draft = useDraftBaseline(getDraft, () => Object.values(busy).some(Boolean))
const request = (path, options) => apiRequest(path, { ...options, ...lifetime, signal: options?.signal || lifetime.signal })
const normalize = (key, data) => {
  requireFields(data, fields[key])
  if (key === 'bp' && !Array.isArray(data.rules)) throw new Error('后台返回的策略规则无效')
  if (key === 'rf' && !Array.isArray(data.apikey_signature_patterns)) throw new Error('后台返回的签名模式无效')
  return cloneDraft(data)
}
const setSection = (key, data) => {
  sections[key].value = data
  if (key === 'rf') rfPatterns.value = data.apikey_signature_patterns.join(String.fromCharCode(10))
}
const load = async () => {
  if (Object.values(busy).some(Boolean)) return
  const turn = lane.begin(); loading.value = true
  try {
    const paths = ['overload-cooldown', 'rate-limit-429-cooldown', 'fallback-queue', 'stream-timeout', 'rectifier', 'beta-policy']
    const keys = Object.keys(sections)
    const data = await Promise.all(paths.map(path => request('/admin/settings/' + path, { method: 'GET', signal: AbortSignal.any([turn.signal, lifetime.signal]) })))
    const values = data.map((value, index) => normalize(keys[index], value))
    if (!turn.isCurrent() || !lifetime.isAlive()) return
    const edits = draft.edits()
    keys.forEach((key, index) => setSection(key, values[index]))
    draft.accept()
    for (const [key, value] of Object.entries(edits)) {
      sections[key].value = value
      if (key === 'rf') rfPatterns.value = value.patternsInput
    }
    err.value = ''
  } catch (e) { if (turn.isCurrent() && !isCancellation(e)) err.value = e.message }
  finally { if (turn.isCurrent() && lifetime.isAlive()) loading.value = false }
}
const save = async (key, path, payload) => {
  if (busy[key] || !sections[key].value || !lifetime.isAlive()) return
  lane.cancel(); loading.value = false
  const submitted = cloneDraft(getDraft()[key])
  setBusy(key, true)
  try {
    const updated = normalize(key, await request(path, { method: 'PUT', body: payload }))
    const edits = retainEdits(getDraft()[key], submitted)
    setSection(key, updated)
    draft.acceptPart({ [key]: getDraft()[key] })
    Object.assign(sections[key].value, edits)
    if (key === 'rf' && Object.hasOwn(edits, 'patternsInput')) rfPatterns.value = edits.patternsInput
    message.success('已保存')
  } catch (e) { if (!isCancellation(e) && lifetime.isAlive()) message.error('保存失败：' + e.message) }
  finally { setBusy(key, false) }
}

/* ── 各分区保存（payload 只带本分区字段，数值 clamp 对照旧代码） ── */
const saveOc = () => save('oc', '/admin/settings/overload-cooldown', {
  enabled: oc.value.enabled,
  cooldown_minutes: clampInt(oc.value.cooldown_minutes, 10, 1, 120),
})
const saveRl = () => save('rl', '/admin/settings/rate-limit-429-cooldown', {
  enabled: rl.value.enabled,
  cooldown_seconds: clampInt(rl.value.cooldown_seconds, 5, 1, 3600),
})
const saveFq = () => save('fq', '/admin/settings/fallback-queue', {
  enabled: fq.value.enabled,
  max_waiting: clampInt(fq.value.max_waiting, 100, 1, 100000),
  wait_timeout_seconds: clampInt(fq.value.wait_timeout_seconds, 120, 1, 3600),
})
const saveSt = () => save('st', '/admin/settings/stream-timeout', {
  enabled: st.value.enabled,
  action: st.value.action,
  temp_unsched_minutes: clampInt(st.value.temp_unsched_minutes, 5, 1, 1440),
  threshold_count: clampInt(st.value.threshold_count, 3, 1, 100),
  threshold_window_minutes: clampInt(st.value.threshold_window_minutes, 10, 1, 1440),
})
const saveRf = () => {
  // 多行文本 → 数组：逐行 trim、去空（对照旧 saveRectifierSettings）
  const patterns = rfPatterns.value.split(/\r?\n/).map((i) => i.trim()).filter(Boolean)
  return save('rf', '/admin/settings/rectifier', {
    enabled: rf.value.enabled,
    thinking_signature_enabled: rf.value.thinking_signature_enabled,
    thinking_budget_enabled: rf.value.thinking_budget_enabled,
    apikey_signature_enabled: rf.value.apikey_signature_enabled,
    apikey_signature_patterns: patterns,
  })
}

/* ── Beta 策略规则 CRUD ── */
const addBetaRule = () => {
  bp.value.rules.push({
    beta_token: '',
    action: 'pass',
    scope: 'all',
    error_message: '',
    model_whitelist: [],
    fallback_action: 'pass',
    fallback_error_message: '',
  })
}
const removeBetaRule = (index) => bp.value.rules.splice(index, 1)
const onBetaWhitelistInput = (rule, e) => {
  rule.model_whitelist = String(e.target.value || '').split(',').map((i) => i.trim()).filter(Boolean)
}
const saveBp = () => {
  // 空规则清洗（对照旧 saveBetaPolicySettings）：无 token 的规则丢弃；
  // 白名单为空时不下发兜底动作
  const cleaned = bp.value.rules
    .filter((rule) => String(rule.beta_token || '').trim())
    .map((rule) => {
      const whitelist = (rule.model_whitelist || []).filter((i) => String(i).trim())
      return {
        beta_token: String(rule.beta_token).trim(),
        action: rule.action,
        scope: rule.scope,
        error_message: String(rule.error_message || '').trim() || undefined,
        model_whitelist: whitelist.length > 0 ? whitelist : undefined,
        fallback_action: whitelist.length > 0 ? (rule.fallback_action || 'pass') : undefined,
        fallback_error_message: String(rule.fallback_error_message || '').trim() || undefined,
      }
    })
  return save('bp', '/admin/settings/beta-policy', { rules: cleaned })
}

onMounted(load)
</script>

<template>
  <a-spin :spinning="loading">
    <div class="sw w12-design">
      <div v-if="err" class="card"><W12Button variant="danger" @click="load">加载失败：{{ err }}（点击重试）</W12Button></div>
      <template v-if="oc && rl && fq && st && rf && bp">
        <!-- 529 过载冷却 -->
        <div class="card">
          <div class="card-title">529 过载冷却</div>
          <div class="card-sub">上游过载后临时冷却，避免持续打满故障渠道。</div>
          <div class="form">
            <div class="frow"><label>启用</label><div class="fmain"><a-switch v-model:checked="oc.enabled" /></div></div>
            <div class="frow"><label>冷却分钟（1-120）</label><a-input-number v-model:value="oc.cooldown_minutes" :min="1" :max="120" class="fld-n" /></div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="busy.oc" @click="saveOc">保存</W12Button></div>
        </div>

        <!-- 429 限流冷却 -->
        <div class="card">
          <div class="card-title">429 限流冷却</div>
          <div class="card-sub">请求被限流后短暂冷却，减少无效重试。</div>
          <div class="form">
            <div class="frow"><label>启用</label><div class="fmain"><a-switch v-model:checked="rl.enabled" /></div></div>
            <div class="frow"><label>冷却秒数（1-3600）</label><a-input-number v-model:value="rl.cooldown_seconds" :min="1" :max="3600" class="fld-n" /></div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="busy.rl" @click="saveRl">保存</W12Button></div>
        </div>

        <!-- 账号兜底排队 -->
        <div class="card">
          <div class="card-title">账号兜底排队</div>
          <div class="card-sub">所有可用账号并发满时，允许请求短暂排队等待账号槽位；关闭后满并发会立即返回并发限制。仅影响账号级兜底排队。</div>
          <div class="form">
            <div class="frow"><label>启用</label><div class="fmain"><a-switch v-model:checked="fq.enabled" /></div></div>
            <div class="frow"><label>单账号最大排队数</label><a-input-number v-model:value="fq.max_waiting" :min="1" :max="100000" class="fld-n" /></div>
            <div class="frow"><label>最长等待（秒）</label><a-input-number v-model:value="fq.wait_timeout_seconds" :min="1" :max="3600" class="fld-n" /></div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="busy.fq" @click="saveFq">保存</W12Button></div>
        </div>

        <!-- 流超时处理 -->
        <div class="card">
          <div class="card-title">流超时处理</div>
          <div class="card-sub">连续流式超时后执行临时下线、报错或忽略。</div>
          <div class="form">
            <div class="frow"><label>启用</label><div class="fmain"><a-switch v-model:checked="st.enabled" /></div></div>
            <div class="frow"><label>处置动作</label><a-select v-model:value="st.action" class="fld" :options="STREAM_ACTIONS" /></div>
            <div class="frow"><label>临时下线分钟（1-1440）</label><a-input-number v-model:value="st.temp_unsched_minutes" :min="1" :max="1440" class="fld-n" /></div>
            <div class="frow"><label>阈值次数（1-100）</label><a-input-number v-model:value="st.threshold_count" :min="1" :max="100" class="fld-n" /></div>
            <div class="frow"><label>统计窗口分钟（1-1440）</label><a-input-number v-model:value="st.threshold_window_minutes" :min="1" :max="1440" class="fld-n" /></div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="busy.st" @click="saveSt">保存</W12Button></div>
        </div>

        <!-- 修正器 -->
        <div class="card">
          <div class="card-title">修正器</div>
          <div class="card-sub">修正上游响应中的 thinking、budget 和 API Key 签名问题。</div>
          <div class="form">
            <div class="frow"><label>启用修正器</label><div class="fmain"><a-switch v-model:checked="rf.enabled" /></div></div>
            <div class="frow"><label>Thinking 签名修正</label><div class="fmain"><a-switch v-model:checked="rf.thinking_signature_enabled" /></div></div>
            <div class="frow"><label>Thinking budget 修正</label><div class="fmain"><a-switch v-model:checked="rf.thinking_budget_enabled" /></div></div>
            <div class="frow"><label>API Key 签名修正</label><div class="fmain"><a-switch v-model:checked="rf.apikey_signature_enabled" /></div></div>
            <div v-if="rf.apikey_signature_enabled" class="frow" style="align-items: flex-start">
              <label>签名模式（每行一个）</label>
              <div class="fmain" style="flex-direction: column; align-items: stretch">
                <a-textarea v-model:value="rfPatterns" class="mono" :rows="5" placeholder="每行一个 API Key 签名模式" />
              </div>
            </div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="busy.rf" @click="saveRf">保存</W12Button></div>
        </div>

        <!-- Beta 策略 -->
        <div class="card">
          <div class="card-title">Beta 策略</div>
          <div class="card-sub">控制 Anthropic beta token 的透传、过滤或阻断。</div>
          <div style="display: flex; justify-content: flex-end; margin-bottom: 8px"><W12Button @click="addBetaRule">添加规则</W12Button></div>
          <div v-if="bp.rules.length === 0" class="hint" style="padding: 8px">当前没有 beta 策略规则。</div>
          <div v-for="(rule, index) in bp.rules" :key="index" class="subcard" style="margin-bottom: 8px">
            <div class="sw-grid">
              <div class="frow"><label>Beta token</label><W12Input v-model:value="rule.beta_token" class="fld mono" placeholder="context-1m-2025-08-07" /></div>
              <div class="frow"><label>动作</label><a-select v-model:value="rule.action" class="fld" :options="BETA_ACTIONS" /></div>
              <div class="frow"><label>范围</label><a-select v-model:value="rule.scope" class="fld" :options="BETA_SCOPES" /></div>
              <div class="frow"><label>模型白名单（逗号分隔）</label><W12Input :value="(rule.model_whitelist || []).join(', ')" class="fld-full" @input="onBetaWhitelistInput(rule, $event)" /></div>
              <div class="frow"><label>兜底动作</label><a-select v-model:value="rule.fallback_action" class="fld" :options="BETA_ACTIONS" /></div>
              <div class="frow"><label>错误提示</label><W12Input v-model:value="rule.error_message" class="fld-full" /></div>
              <div v-if="rule.fallback_action === 'block'" class="frow"><label>兜底阻断提示</label><W12Input v-model:value="rule.fallback_error_message" class="fld-full" placeholder="模型未命中白名单时返回的提示；留空使用默认提示" /></div>
            </div>
            <div style="display: flex; justify-content: flex-end; margin-top: 8px">
              <button type="button" class="xbtn danger" @click="removeBetaRule(index)">删除规则</button>
            </div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="busy.bp" @click="saveBp">保存 Beta 策略</W12Button></div>
        </div>
      </template>
    </div>
  </a-spin>
</template>

<style scoped>
@import './settings.css';
</style>

