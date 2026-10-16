<script setup>
/* anthropic 模型路由规则编辑器（二期 A，model_routing）
 * 契约来源：旧 GroupsView.vue 1901-2093（UI）、4753-5026（逻辑）
 *  - API 结构：{ "模型模式": [账号ID...] }，pattern 支持通配符（claude-opus-*）
 *  - UI 结构：规则数组 [{ pattern, accounts: [{id, name}] }]
 *  - 回填：旧版逐个 GET /admin/accounts/:id 换取名称，失败显示 #id
 *  - 搜索：GET /admin/accounts?page=1&page_size=20&platform=anthropic&search=xxx（防抖 300ms）
 *  - 仅 anthropic 平台显示；model_routing_enabled=false 时规则不生效
 */
import { reactive, watch, onBeforeUnmount } from 'vue'
import { request } from '../../api/index.js'
import { createLatestRequest } from '../../api/latestRequest.js'

const props = defineProps({
  enabled: { type: Boolean, default: false },
  groupId: { type: Number, default: null },   // model_routing_enabled
  rules: { type: Array, default: () => [] },    // ModelRoutingRule[]
})
const emit = defineEmits(['update:enabled', 'update:rules'])

const writeRules = (rules) => emit('update:rules', rules)
const addRule = () => writeRules([...props.rules, { pattern: '', accounts: [] }])
const removeRule = index => { clearSearch(); writeRules(props.rules.filter((_, row) => row !== index)) }
const setPattern = (index, pattern) => writeRules(props.rules.map((r, i) => (i === index ? { ...r, pattern } : r)))
const removeAccount = (ruleIndex, accountId) => {
  writeRules(props.rules.map((r, i) => (i === ruleIndex ? { ...r, accounts: r.accounts.filter((a) => a.id !== accountId) } : r)))
}
const selectAccount = (ruleIndex, account) => {
  writeRules(props.rules.map((r, i) => {
    if (i !== ruleIndex || r.accounts.some((a) => a.id === account.id)) return r
    return { ...r, accounts: [...r.accounts, account] }
  }))
  clearTimeout(searchTimer); searchLane.cancel()
  queries[ruleIndex] = ''
  results[ruleIndex] = []
  shows[ruleIndex] = false
}

// 账号搜索状态：按规则下标隔离；focus 且无结果时先搜一次（对照旧 onAccountSearchFocus）
const queries = reactive({})
const results = reactive({})
const shows = reactive({})
const searchLane = createLatestRequest(), blurTimers = new Map()
let searchTimer = null
const clearSearch = () => {
  clearTimeout(searchTimer); searchLane.cancel()
  for (const timer of blurTimers.values()) clearTimeout(timer)
  blurTimers.clear()
  for (const state of [queries, results, shows]) for (const key of Object.keys(state)) delete state[key]
}
const doSearch = async (key, turn = searchLane.begin()) => {
  if (!turn.isCurrent() || !props.enabled) return
  const pattern = props.rules[key]?.pattern
  try {
    const data = await request('/admin/accounts', { method: 'GET', signal: turn.signal,
      query: { page: 1, page_size: 20, platform: 'anthropic', search: queries[key]?.trim() || undefined } })
    if (turn.isCurrent() && props.rules[key]?.pattern === pattern) results[key] = (data.items || []).map(account => ({ id: account.id, name: account.name }))
  } catch { if (turn.isCurrent()) results[key] = [] }
}
const onInput = key => {
  clearTimeout(searchTimer)
  const turn = searchLane.begin()
  searchTimer = setTimeout(() => doSearch(key, turn), 300)
}
const onFocus = key => {
  clearTimeout(blurTimers.get(key)); clearTimeout(searchTimer)
  shows[key] = true
  if (!results[key]?.length) doSearch(key)
}
const onBlur = key => {
  clearTimeout(blurTimers.get(key))
  blurTimers.set(key, setTimeout(() => { shows[key] = false; blurTimers.delete(key) }, 180))
}
watch(() => props.groupId, clearSearch)
watch(() => props.enabled, on => { if (!on) clearSearch() })
onBeforeUnmount(() => { clearSearch(); searchLane.dispose() })
</script>

<template>
  <div class="mr">
    <div class="mr-head">
      <div>
        <div class="mr-t">模型路由配置</div>
        <div class="mr-d">配置特定模型请求优先路由到指定账号。支持通配符匹配，如 claude-opus-* 匹配所有 opus 模型。</div>
      </div>
      <a-switch :checked="enabled" size="small" @change="(v) => emit('update:enabled', v)" />
    </div>
    <div class="mr-d">{{ enabled ? '添加路由规则以将特定模型请求优先路由到指定账号。' : '启用后，配置的路由规则才会生效。' }}</div>

    <template v-if="enabled">
      <div class="mr-rules">
        <div v-for="(rule, index) in rules" :key="index" class="mr-rule">
          <div class="mr-rule-row">
            <div>
              <label>模型模式</label>
              <a-input :value="rule.pattern" placeholder="claude-opus-*" @change="(e) => setPattern(index, e.target.value)" />
            </div>
            <a-button size="small" type="text" danger @click="removeRule(index)">删除规则</a-button>
          </div>
          <div class="mr-accts">
            <label>优先账号</label>
            <div v-if="rule.accounts.length" class="chips">
              <span v-for="a in rule.accounts" :key="a.id" class="chip">
                {{ a.name }} <button type="button" title="移除账号" @click="removeAccount(index, a.id)">×</button>
              </span>
            </div>
            <div class="mr-search">
              <input
                v-model="queries[index]" type="text" placeholder="搜索账号..."
                @input="onInput(index)" @focus="onFocus(index)" @blur="onBlur(index)"
              >
              <div v-if="shows[index] && results[index]?.length" class="mr-drop">
                <button
                  v-for="a in results[index]" :key="a.id" type="button" class="mr-item"
                  :disabled="rule.accounts.some((x) => x.id === a.id)" @click="selectAccount(index, a)"
                >
                  <span>{{ a.name }}</span><span class="mr-id">#{{ a.id }}</span>
                </button>
              </div>
            </div>
            <div class="mr-d">选择此模型模式优先使用的账号。</div>
          </div>
        </div>
      </div>
      <a-button size="small" type="dashed" block @click="addRule">+ 添加路由规则</a-button>
    </template>
  </div>
</template>

<style scoped>
.mr { display: grid; gap: 8px; }
.mr-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.mr-t { font-size: 13px; }
.mr-d { font-size: 12px; color: var(--text2); line-height: 1.6; }
.mr-rules { display: grid; gap: 8px; }
.mr-rule { border: 1px solid var(--hr); border-radius: 8px; background: var(--card); padding: 10px; display: grid; gap: 8px; }
.mr-rule-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; align-items: end; }
label { display: block; font-size: 11.5px; color: var(--text2); margin-bottom: 3px; }
.mr-accts { display: grid; gap: 6px; justify-items: start; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { font-size: 11.5px; border: 1px solid color-mix(in srgb, var(--accent) 30%, transparent); background: color-mix(in srgb, var(--accent) 10%, transparent); color: var(--accent); border-radius: 999px; padding: 2px 10px; display: inline-flex; gap: 4px; align-items: center; }
.chip button { border: none; background: none; cursor: pointer; color: inherit; padding: 0; font-size: 13px; }
.mr-search { position: relative; width: 100%; max-width: 320px; }
.mr-search input { height: 32px; width: 100%; padding: 0 12px; border-radius: var(--r-ctl); border: 1px solid var(--stroke-ctl); background: var(--surface-card); color: inherit; font-size: 13px; }
.mr-search input:hover { border-color: var(--accent); }
.mr-search input:focus { outline: none; border-color: var(--accent); }
.mr-drop { position: absolute; z-index: 30; top: 36px; left: 0; right: 0; max-height: 192px; overflow-y: auto; border: 1px solid var(--hr); border-radius: 8px; background: var(--card); box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12); }
.mr-item { width: 100%; display: flex; justify-content: space-between; gap: 8px; padding: 7px 12px; border: none; background: none; cursor: pointer; font-size: 12.5px; text-align: left; }
.mr-item:hover:not(:disabled) { background: var(--bg70); }
.mr-item:disabled { opacity: 0.45; cursor: default; }
.mr-id { color: var(--text2); }
</style>
