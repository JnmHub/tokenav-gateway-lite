<script setup>
/* ============================================================
 * OpenAI Fast / Flex 策略卡（对照旧 SettingsView.vue priority-policy-panel）
 * - 请求/响应双检测来源：至少启用一个（关闭最后一个时阻止并提示）
 * - 规则 CRUD + 上下移动（移动即改变真实匹配顺序）
 * - 指定用户搜索：GET /admin/usage/search-users?q=（300ms 防抖）
 * - 模型白名单逗号输入 → 去重去空
 * - 兜底动作：空串 = 默认透传（提交时归一为 undefined）
 * 保存由父组件执行：emit('save', 构建好的 openai_fast_policy_settings)
 * ============================================================ */
import { ref, computed } from 'vue'
import { request, isCancellation } from '../../api'
import { useRequestLifetime } from '../../composables/useRequestLifetime'
import { createLatestRequest } from '../../api/latestRequest'
import { message } from 'ant-design-vue'
import { parseWhitelist } from './fields'

const props = defineProps({
  model: { type: Object, required: true },   // openai_fast_policy_settings 响应式对象
  saving: { type: Boolean, default: false },
})
const emit = defineEmits(['save'])

const TIER_OPTIONS = [
  { value: 'all', label: '全部已识别层级' },
  { value: 'priority', label: 'Priority（优先处理）' },
  { value: 'flex', label: 'Flex' },
]
const ACTION_OPTIONS = [
  { value: 'pass', label: '透传' },
  { value: 'filter', label: '过滤字段' },
  { value: 'block', label: '阻断请求' },
  { value: 'force_priority', label: '改写已有层级为 Priority' },
]
const FALLBACK_OPTIONS = [{ value: '', label: '默认透传' }, ...ACTION_OPTIONS]
const SCOPE_OPTIONS = [
  { value: 'all', label: '全部账号' },
  { value: 'oauth', label: 'OAuth' },
  { value: 'apikey', label: 'API Key' },
  { value: 'bedrock', label: 'Bedrock' },
]

/* ── 检测来源：至少启用一个 ── */
const setDetection = (source, enabled) => {
  const other = source === 'request' ? props.model.priority_detection_by_response_enabled : props.model.priority_detection_by_request_enabled
  if (!enabled && !other) {
    message.error('根据请求和根据响应至少要开启一个')
    return
  }
  if (source === 'request') props.model.priority_detection_by_request_enabled = enabled
  else props.model.priority_detection_by_response_enabled = enabled
}

/* ── 规则 CRUD + 移动 ── */
const addRule = () => {
  props.model.rules.push({
    id: `rule_ui_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`,
    service_tier: 'priority',
    action: 'pass',
    scope: 'all',
    user_ids: [],
    model_whitelist: [],
    fallback_action: 'pass',
  })
}

const removeRule = (index) => props.model.rules.splice(index, 1)

const moveRule = (index, direction) => {
  const target = index + direction
  if (target < 0 || target >= props.model.rules.length) return
  const [rule] = props.model.rules.splice(index, 1)
  if (rule) props.model.rules.splice(target, 0, rule)
}

/* ── 指定用户搜索（防抖 300ms） ── */
const userOptions = ref([]) // 累积已见用户 { id, email, deleted }
const searching = ref(false)
let searchTimer = null
const lane = createLatestRequest()
const lifetime = useRequestLifetime(() => { clearTimeout(searchTimer); lane.cancel(); userOptions.value = []; searching.value = false })

const searchUsers = (kw) => {
  const keyword = String(kw || '').trim()
  clearTimeout(searchTimer)
  lane.cancel()
  if (!keyword || !lifetime.isAlive()) { searching.value = false; return }
  const turn = lane.begin()
  searchTimer = setTimeout(async () => {
    if (!turn.isCurrent() || !lifetime.isAlive()) return
    searching.value = true
    try {
      const list = await request('/admin/usage/search-users', { method: 'GET', query: { q: keyword }, ...lifetime, signal: AbortSignal.any([turn.signal, lifetime.signal]) })
      if (!turn.isCurrent() || !lifetime.isAlive()) return
      const known = new Set(userOptions.value.map((u) => u.id))
      for (const u of list || []) {
        if (!known.has(u.id)) userOptions.value.push({ id: u.id, email: u.email || `用户 #${u.id}`, deleted: Boolean(u.deleted) })
      }
    } catch (e) { if (turn.isCurrent() && !isCancellation(e)) message.warning('用户搜索失败：' + e.message) } finally {
      if (turn.isCurrent() && lifetime.isAlive()) searching.value = false
    }
  }, 300)
}

const userLabel = (option) => `${option.email}${option.deleted ? '（已删除）' : ''} #${option.id}`

/* ── 白名单输入（即时回写数组） ── */
const onWhitelistInput = (rule, e) => { rule.model_whitelist = parseWhitelist(e.target.value) }

/* ── 构建提交对象（对照旧 buildOpenAIFastPolicySettings） ── */
const build = () => ({
  version: 1,
  priority_detection_by_request_enabled: props.model.priority_detection_by_request_enabled,
  priority_detection_by_response_enabled: props.model.priority_detection_by_response_enabled,
  rules: props.model.rules.map((rule) => ({
    id: String(rule.id || '').trim() || undefined,
    service_tier: rule.service_tier,
    action: rule.action,
    scope: rule.scope,
    user_ids: Array.from(new Set((rule.user_ids || []).filter((id) => Number.isInteger(id) && id > 0))),
    error_message: String(rule.error_message || '').trim() || undefined,
    model_whitelist: Array.from(new Set((rule.model_whitelist || []).map((i) => String(i).trim()).filter(Boolean))),
    fallback_action: rule.fallback_action || undefined,
    fallback_error_message: String(rule.fallback_error_message || '').trim() || undefined,
  })),
})

const save = () => {
  if (!props.model.priority_detection_by_request_enabled && !props.model.priority_detection_by_response_enabled) {
    message.error('根据请求和根据响应至少要开启一个')
    return
  }
  emit('save', build())
}

// 已选用户的下拉回显：把已有 id 合成静态选项（无邮箱时显示 #id）
const selectedUserOptions = computed(() => {
  const ids = new Set(props.model.rules.flatMap((r) => r.user_ids || []))
  const known = new Map(userOptions.value.map((u) => [u.id, u]))
  return Array.from(ids).map((id) => known.get(id) || { id, email: `用户 #${id}`, deleted: false })
})
</script>

<template>
  <div class="card">
    <div class="card-title">OpenAI Fast / Flex 策略</div>
    <div class="card-sub">按用户、账号类型、模型和 service_tier 顺序匹配；用户专属规则优先，同层只执行第一条命中规则。</div>

    <!-- 检测来源 -->
    <div class="sw-grid" style="margin-bottom: 8px">
      <div class="subcard">
        <div style="display: flex; align-items: center; justify-content: space-between">
          <strong style="font-size: 13px">根据请求判断</strong>
          <a-switch :checked="model.priority_detection_by_request_enabled" @change="(v) => setDetection('request', v)" />
        </div>
        <div class="hint">使用规则处理后最终发送给上游的 service_tier 判断计费模式；响应缺失时按实际发送值作为证据。</div>
      </div>
      <div class="subcard">
        <div style="display: flex; align-items: center; justify-content: space-between">
          <strong style="font-size: 13px">根据响应判断</strong>
          <a-switch :checked="model.priority_detection_by_response_enabled" @change="(v) => setDetection('response', v)" />
        </div>
        <div class="hint">优先使用上游响应明确返回的 service_tier；双开时响应优先，非法响应记录异常后回退请求。</div>
      </div>
    </div>

    <!-- 规则列表 -->
    <div style="display: flex; align-items: center; justify-content: space-between; margin: 12px 0 8px">
      <div>
        <strong style="font-size: 13px">处理规则</strong>
        <div class="hint">上下移动会改变真实匹配顺序。无规则时只透传客户已提供的合法 service_tier。</div>
      </div>
      <a-button @click="addRule">添加规则</a-button>
    </div>

    <div v-if="model.rules.length === 0" class="hint" style="padding: 12px 8px">当前没有 Fast / Flex 规则。</div>

    <div v-for="(rule, index) in model.rules" :key="rule.id || index" class="subcard" style="margin-bottom: 8px">
      <div style="display: flex; align-items: center; justify-content: space-between; margin-bottom: 4px">
        <strong style="font-size: 13px">规则 {{ index + 1 }}</strong>
        <div style="display: flex; gap: 6px">
          <button type="button" class="xbtn" :disabled="index === 0" title="上移" @click="moveRule(index, -1)">↑</button>
          <button type="button" class="xbtn" :disabled="index === model.rules.length - 1" title="下移" @click="moveRule(index, 1)">↓</button>
          <button type="button" class="xbtn danger" title="删除规则" @click="removeRule(index)">删除</button>
        </div>
      </div>
      <div class="sw-grid">
        <div class="frow"><label>service_tier</label><a-select v-model:value="rule.service_tier" class="fld" :options="TIER_OPTIONS" /></div>
        <div class="frow"><label>动作</label><a-select v-model:value="rule.action" class="fld" :options="ACTION_OPTIONS" /></div>
        <div class="frow"><label>账号范围</label><a-select v-model:value="rule.scope" class="fld" :options="SCOPE_OPTIONS" /></div>
      </div>
      <div class="frow">
        <label>指定用户</label>
        <div class="fmain" style="flex-direction: column; align-items: stretch">
          <a-select
            :value="rule.user_ids || []"
            mode="multiple"
            show-search
            allow-clear
            placeholder="留空表示所有用户；指定用户规则优先于全局规则"
            :filter-option="false"
            :loading="searching"
            @search="searchUsers"
            @change="(ids) => (rule.user_ids = ids || [])"
          >
            <a-select-option v-for="u in selectedUserOptions" :key="`k${u.id}`" :value="u.id">{{ userLabel(u) }}</a-select-option>
            <a-select-option v-for="u in userOptions.filter((x) => !(rule.user_ids || []).includes(x.id))" :key="`o${u.id}`" :value="u.id">{{ userLabel(u) }}</a-select-option>
          </a-select>
        </div>
      </div>
      <div class="sw-grid">
        <div class="frow"><label>模型白名单（逗号分隔）</label><a-input :value="(rule.model_whitelist || []).join(', ')" class="fld-full" placeholder="gpt-5.5, gpt-5.6-*" @input="onWhitelistInput(rule, $event)" /></div>
        <div class="frow"><label>白名单未命中时</label><a-select v-model:value="rule.fallback_action" class="fld" :options="FALLBACK_OPTIONS" /></div>
      </div>
      <div v-if="rule.action === 'block' || rule.fallback_action === 'block'" class="sw-grid">
        <div v-if="rule.action === 'block'" class="frow"><label>阻断提示</label><a-input v-model:value="rule.error_message" class="fld-full" /></div>
        <div v-if="rule.fallback_action === 'block'" class="frow"><label>兜底阻断提示</label><a-input v-model:value="rule.fallback_error_message" class="fld-full" /></div>
      </div>
    </div>

    <div class="savebar"><a-button type="primary" class="save" :loading="saving" @click="save">保存 Fast / Flex 策略</a-button></div>
  </div>
</template>

<style scoped>
@import './settings.css';
</style>

