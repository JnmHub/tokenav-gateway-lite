<script setup>
import { SearchOutlined } from '@ant-design/icons-vue'
/* 分组专属定价编辑器（二期 A）
 * 契约来源：旧 components/admin/group/GroupPricingFields.vue + groupPricing.ts
 *  - 启用开关：关闭 = 继承全局价格；读取成功前禁存（旧 notReady 语义）
 *  - 多规则定价：Token / 按次 / 图片 / 视频 四种计费模式（model_pricing[]）
 *  - 高级：限制模型列表、web 搜索仿真（anthropic）、Bedrock CC 兼容（anthropic）、
 *    Codex 图片桥（openai）、账号成本统计规则（account_stats_pricing_rules）
 *  - shared=true 时提示「保存后自动拆分独立渠道」（后端负责拆分）
 *  - 主价格与账号成本共用 GroupPricingEntry，完整回填/编辑/提交阶梯价与档位倍率
 */
import { computed, reactive, ref, onBeforeUnmount, watch } from 'vue'
import { request } from '../../api/index.js'
import { createLatestRequest } from '../../api/latestRequest.js'
import GroupPricingEntry from './GroupPricingEntry.vue'
import { createPricingEntry } from './pricing.js'

const props = defineProps({
  formState: { type: Object, required: true },  // GroupPricingFormState（pricing.js）
  platform: { type: String, required: true },
  groupId: { type: Number, default: null },
  loading: { type: Boolean, default: false },
  shared: { type: Boolean, default: false },
  loadError: { type: Boolean, default: false },
})
const emit = defineEmits(['update:formState', 'retry'])

const advancedOpen = ref(false)
const composite = computed(() => props.platform === 'composite')

const patch = (values) => emit('update:formState', { ...props.formState, ...values })

const billingModes = [
  { value: 'token', label: 'Token' },
  { value: 'per_request', label: '按次' },
  { value: 'image', label: '图片' },
  { value: 'video', label: '视频' },
]

// —— 主价格规则列表 ——
const addPricing = (mode) => {
  const entryPlatform = composite.value ? undefined : props.platform
  patch({ model_pricing: [...props.formState.model_pricing, createPricingEntry(mode, entryPlatform)] })
}
const updatePricing = (index, entry) => {
  const entries = [...props.formState.model_pricing]
  entries[index] = entry
  patch({ model_pricing: entries })
}
const removePricing = (index) => {
  const entries = [...props.formState.model_pricing]
  entries.splice(index, 1)
  patch({ model_pricing: entries })
}

// —— 账号成本统计规则 ——
const addRule = () => patch({
  account_stats_pricing_rules: [...props.formState.account_stats_pricing_rules, { name: '', scope: 'whole_group', account_ids: [], pricing: [] }],
})
const patchRule = (index, values) => {
  const rules = props.formState.account_stats_pricing_rules.map((r, i) => (i === index ? { ...r, ...values } : r))
  patch({ account_stats_pricing_rules: rules })
}
const removeRule = (index) => {
  clearSearch()
  const rules = [...props.formState.account_stats_pricing_rules]
  rules.splice(index, 1)
  patch({ account_stats_pricing_rules: rules })
}
const setRuleScope = (index, scope) => {
  patchRule(index, { scope, account_ids: scope === 'whole_group' ? [] : props.formState.account_stats_pricing_rules[index].account_ids })
}
const addRulePricing = (ruleIndex, mode) => {
  const rule = props.formState.account_stats_pricing_rules[ruleIndex]
  const entryPlatform = composite.value ? undefined : props.platform
  patchRule(ruleIndex, { pricing: [...rule.pricing, createPricingEntry(mode, entryPlatform)] })
}
const updateRulePricing = (ruleIndex, pricingIndex, entry) => {
  const pricing = [...props.formState.account_stats_pricing_rules[ruleIndex].pricing]
  pricing[pricingIndex] = entry
  patchRule(ruleIndex, { pricing })
}
const removeRulePricing = (ruleIndex, pricingIndex) => {
  const pricing = [...props.formState.account_stats_pricing_rules[ruleIndex].pricing]
  pricing.splice(pricingIndex, 1)
  patchRule(ruleIndex, { pricing })
}

// 账号搜索（防抖 300ms；lite=true 减少返回体积；旧版走 accounts.list）
const queries = reactive({})
const results = reactive({})
const known = reactive({})
const timers = new Map(), searchLanes = new Map()
const clearSearch = () => {
  for (const timer of timers.values()) clearTimeout(timer)
  for (const lane of searchLanes.values()) lane.dispose()
  timers.clear(); searchLanes.clear()
  for (const state of [queries, results, known]) for (const key of Object.keys(state)) delete state[key]
}
onBeforeUnmount(clearSearch)
watch(() => [props.groupId, props.platform], clearSearch)
const searchAccounts = (key) => {
  clearTimeout(timers.get(key))
  if (!searchLanes.has(key)) searchLanes.set(key, createLatestRequest())
  const lane = searchLanes.get(key), turn = lane.begin()
  const rule = props.formState.account_stats_pricing_rules[key]
  timers.set(key, setTimeout(async () => {
    timers.delete(key)
    try {
      const data = await request('/admin/accounts', { method: 'GET', signal: turn.signal,
        query: { page: 1, page_size: 20, lite: 'true', platform: composite.value ? undefined : props.platform,
          group: props.groupId ? String(props.groupId) : undefined, search: queries[key]?.trim() || undefined } })
      if (!turn.isCurrent() || props.formState.account_stats_pricing_rules[key] !== rule) return
      results[key] = data.items || []
      for (const account of data.items || []) known[account.id] = account
    } catch { if (turn.isCurrent()) results[key] = [] }
  }, 300))
}
const addRuleAccount = (ruleIndex, account) => {
  known[account.id] = account
  const rule = props.formState.account_stats_pricing_rules[ruleIndex]
  if (!rule.account_ids.includes(account.id)) {
    patchRule(ruleIndex, { account_ids: [...rule.account_ids, account.id] })
  }
  clearTimeout(timers.get(ruleIndex)); searchLanes.get(ruleIndex)?.cancel()
  queries[ruleIndex] = ''
  results[ruleIndex] = []
}
const removeRuleAccount = (ruleIndex, accountId) => {
  const rule = props.formState.account_stats_pricing_rules[ruleIndex]
  patchRule(ruleIndex, { account_ids: rule.account_ids.filter((id) => id !== accountId) })
}
const accountLabel = (id) => known[id]?.name || `#${id}`

</script>

<template>
  <div class="gp">
    <!-- 头部：启用开关 + 徽标 -->
    <div class="gp-head">
      <div class="gp-head-main">
        <div class="gp-t">
          分组模型定价
          <span class="badge" :class="formState.enabled ? 'on' : ''">{{ formState.enabled ? '自定义' : '继承全局' }}</span>
          <span v-if="shared" class="badge shared">共享渠道</span>
        </div>
        <div class="gp-d">{{ formState.enabled ? '只为当前分组设置价格；Token、按次、图片和视频计费都在这里完成。' : '默认使用全局模型价格，无需重复填写。' }}</div>
      </div>
      <a-switch :checked="formState.enabled" size="small" @change="(v) => patch({ enabled: v })" />
    </div>

    <div v-if="loading" class="gp-note">正在读取分组价格...</div>
    <div v-else-if="loadError" class="gp-note err">
      读取分组价格失败。为防止误关闭已有定价，读取成功前不能保存。
      <a-button size="small" type="link" @click="emit('retry')">重试</a-button>
    </div>

    <template v-else-if="formState.enabled">
      <div v-if="shared" class="gp-note shared-note">当前价格与其他分组共用。保存修改后，系统会自动为本分组拆分独立配置。</div>

      <!-- 规则列表 -->
      <div class="gp-rules-head">
        <div>
          <div class="gp-st">模型价格</div>
          <div class="gp-d">点击计费方式后，只填写模型和必要价格；少输入也能完成。</div>
        </div>
        <div class="gp-modes">
          <a-button v-for="m in billingModes" :key="m.value" size="small" @click="addPricing(m.value)">+ {{ m.label }}</a-button>
        </div>
      </div>

      <div v-if="formState.model_pricing.length" class="gp-entries">
        <GroupPricingEntry v-for="(entry, index) in formState.model_pricing" :key="index" :entry="entry" :platform="platform" @update="value => updatePricing(index, value)" @remove="removePricing(index)" />
      </div>
      <div v-else class="gp-empty">
        <div class="gp-st">还没有自定义模型价格</div>
        <div class="gp-d">点击上方 Token、按次、图片或视频即可添加。</div>
      </div>

      <!-- 高级设置 -->
      <button type="button" class="gp-adv-toggle" :aria-expanded="advancedOpen" @click="advancedOpen = !advancedOpen">
        <span>
          <span class="gp-st">高级设置</span>
          <span class="gp-d" style="display: block">模型限制、协议能力与账号成本统计</span>
        </span>
        <span class="chev">{{ advancedOpen ? '收起' : '展开' }}</span>
      </button>

      <div v-if="advancedOpen" class="gp-adv">
        <div class="frow">
          <div class="frow-main">
            <div class="frow-t">只允许价格表中的模型</div>
            <div class="frow-d">开启后，未配置价格的模型会被拒绝。</div>
          </div>
          <a-switch :checked="formState.restrict_models" size="small" @change="(v) => patch({ restrict_models: v })" />
        </div>
        <div v-if="platform === 'anthropic'" class="frow">
          <div class="frow-main">
            <div class="frow-t">Web 搜索仿真定价</div>
            <div class="frow-d">对 anthropic 平台的 web_search 工具调用按搜索价格单独计费。</div>
          </div>
          <a-switch :checked="formState.web_search_emulation" size="small" @change="(v) => patch({ web_search_emulation: v })" />
        </div>
        <div v-if="platform === 'anthropic'" class="frow">
          <div class="frow-main">
            <div class="frow-t">Bedrock CC 兼容</div>
            <div class="frow-d">兼容 AWS Bedrock 的 Claude Code 调用形态。</div>
          </div>
          <a-switch :checked="formState.bedrock_cc_compat" size="small" @change="(v) => patch({ bedrock_cc_compat: v })" />
        </div>
        <div v-if="platform === 'openai'" class="frow">
          <div class="frow-main">
            <div class="frow-t">Codex 图片生成桥</div>
            <div class="frow-d">允许 Codex 请求桥接到图片生成模型。</div>
          </div>
          <a-switch :checked="formState.codex_image_generation_bridge" size="small" @change="(v) => patch({ codex_image_generation_bridge: v })" />
        </div>
        <div class="frow">
          <div class="frow-main">
            <div class="frow-t">按此价格统计账号成本</div>
            <div class="frow-d">仅影响上游账号成本统计，不改变客户扣费。</div>
          </div>
          <a-switch :checked="formState.apply_pricing_to_account_stats" size="small" @change="(v) => patch({ apply_pricing_to_account_stats: v })" />
        </div>

        <div v-if="formState.apply_pricing_to_account_stats" class="acct-rules">
          <div class="gp-rules-head">
            <div>
              <div class="gp-st">账号成本统计规则</div>
              <div class="gp-d">仅命中匹配账号的用量按规则价格统计成本。</div>
            </div>
            <a-button size="small" @click="addRule">+ 添加规则</a-button>
          </div>
          <div v-if="formState.account_stats_pricing_rules.length" class="gp-entries">
            <div v-for="(rule, ri) in formState.account_stats_pricing_rules" :key="ri" class="entry">
              <div class="entry-head">
                <a-input :value="rule.name" style="flex: 1" placeholder="例如：高成本账号" @change="(e) => patchRule(ri, { name: e.target.value })" />
                <a-button size="small" type="text" danger @click="removeRule(ri)">删除</a-button>
              </div>
              <div class="entry-body">
                <div class="entry-row">
                  <div>
                    <label>适用范围</label>
                    <div class="seg">
                      <button type="button" :class="{ on: rule.scope === 'whole_group' }" @click="setRuleScope(ri, 'whole_group')">整个分组</button>
                      <button type="button" :class="{ on: rule.scope === 'selected_accounts' }" @click="setRuleScope(ri, 'selected_accounts')">指定账号</button>
                    </div>
                  </div>
                </div>
                <div v-if="rule.scope === 'selected_accounts'" class="acct-search">
                  <div v-if="rule.account_ids.length" class="chips">
                    <span v-for="id in rule.account_ids" :key="id" class="chip">{{ accountLabel(id) }} <button type="button" @click="removeRuleAccount(ri, id)">×</button></span>
                  </div>
                  <a-input v-model:value="queries[ri]" placeholder="搜索账号名称" style="max-width: 320px"
                    @focus="searchAccounts(ri)" @input="searchAccounts(ri)" @press-enter="searchAccounts(ri)">
                    <template #suffix><SearchOutlined class="suf-ico" @click="searchAccounts(ri)" /></template>
                  </a-input>
                  <div v-if="results[ri]?.length" class="acct-drop">
                    <button
                      v-for="a in results[ri]" :key="a.id" type="button" class="acct-item"
                      :disabled="rule.account_ids.includes(a.id)" @click="addRuleAccount(ri, a)"
                    >
                      <span>{{ a.name }}</span><span class="acct-id">#{{ a.id }}</span>
                    </button>
                  </div>
                  <div class="gp-d">仅命中已选账号；至少选择一个。</div>
                </div>
                <p v-else class="gp-d" style="margin: 0">匹配当前分组的全部账号。</p>

                <div class="gp-modes" style="margin-top: 8px">
                  <a-button v-for="m in billingModes" :key="m.value" size="small" @click="addRulePricing(ri, m.value)">+ {{ m.label }}</a-button>
                </div>
                <GroupPricingEntry v-for="(entry, pi) in rule.pricing" :key="pi" :entry="entry" :platform="platform" @update="value => updateRulePricing(ri, pi, value)" @remove="removeRulePricing(ri, pi)" />
              </div>
            </div>
          </div>
          <p v-else class="gp-d">暂无账号成本规则。</p>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.gp { display: grid; gap: 10px; }
.gp-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; padding: 10px; border: 1px solid var(--hr); border-radius: 8px; background: var(--card); }
.gp-t { font-size: 13px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.badge { font-size: 11px; padding: 1px 8px; border-radius: 4px; background: var(--bg70); color: var(--text2); }
.badge.on { background: color-mix(in srgb, var(--accent) 14%, transparent); color: var(--accent); }
.badge.shared { background: color-mix(in srgb, #e8a33d 18%, transparent); color: #b7791f; }
.gp-d { font-size: 12px; color: var(--text2); margin-top: 2px; line-height: 1.6; }
.gp-note { font-size: 12px; color: var(--text2); border-radius: 8px; background: var(--card); padding: 10px 12px; }
.gp-note.err { color: #d13438; }
.shared-note { color: #b7791f; background: color-mix(in srgb, #e8a33d 12%, transparent); }
.gp-st { font-size: 12.5px; font-weight: 500; }
.gp-rules-head { display: flex; flex-wrap: wrap; align-items: flex-start; justify-content: space-between; gap: 12px; margin-top: 2px; }
.gp-modes { display: flex; gap: 6px; flex-wrap: wrap; }
.gp-entries { display: grid; gap: 8px; }
.gp-empty { border: 1px dashed var(--hr); border-radius: 8px; padding: 14px; text-align: center; }
.entry-body label i { color: #d13438; font-style: normal; }
.gp-adv-toggle { width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 12px; text-align: left; border: none; background: transparent; padding: 8px 2px; cursor: pointer; border-top: 1px solid var(--hr); }
.chev { font-size: 12px; color: var(--text2); }
.gp-adv { display: grid; gap: 8px; }
/* 开关行（与 Groups.vue 一致风格） */
.frow { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 8px 10px; border-radius: 8px; background: var(--card); min-height: 40px; }
.frow-main { min-width: 0; }
.frow-t { font-size: 13px; }
.frow-d { font-size: 12px; color: var(--text2); margin-top: 1px; }
.seg { display: inline-grid; grid-auto-flow: column; gap: 4px; padding: 3px; background: var(--bg70); border-radius: 8px; }
.seg button { border: none; background: transparent; border-radius: 6px; padding: 4px 12px; font-size: 12px; cursor: pointer; color: var(--text2); }
.seg button.on { background: var(--card); color: var(--text); font-weight: 500; box-shadow: 0 0 0 1px var(--hr); }
.acct-rules { border: 1px solid var(--hr); border-radius: 8px; padding: 10px; display: grid; gap: 8px; background: var(--bg70); }
.acct-search { display: grid; gap: 6px; justify-items: start; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { font-size: 11.5px; border: 1px solid var(--hr); border-radius: 6px; padding: 2px 8px; background: var(--card); display: inline-flex; gap: 4px; align-items: center; }
.chip button { border: none; background: none; cursor: pointer; color: var(--text2); padding: 0; }
.acct-drop { border: 1px solid var(--hr); border-radius: 8px; background: var(--card); max-height: 176px; overflow-y: auto; width: 100%; max-width: 320px; }
.acct-item { width: 100%; display: flex; justify-content: space-between; gap: 8px; padding: 6px 10px; border: none; background: none; cursor: pointer; font-size: 12.5px; text-align: left; }
.acct-item:hover:not(:disabled) { background: var(--bg70); }
.acct-item:disabled { opacity: 0.45; cursor: default; }
.acct-id { color: var(--text2); }
</style>
