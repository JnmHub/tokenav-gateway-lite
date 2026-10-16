<script setup>
/* 账号统计定价规则编辑器（win12 版）
 * 契约来源：internal/handler/admin/channel_handler.go
 *   accountStatsPricingRuleRequest/Response + channelModelPricingRequest
 * 规则 = { name, group_ids[], account_ids[], pricing[] }；规则本身无平台字段，
 *   platform 在每个 pricing 条目上（isPlatformMatch：条目 platform 为空 = 不限平台）。
 * 匹配语义（internal/service/account_stats_pricing.go）：accountID ∈ account_ids
 *   或 groupID ∈ group_ids，规则数组先命中为准；规则始终优先于「定价计入账号统计」
 *   开关，不依赖开关开启，因此编辑器常驻展示。
 * 规则定价不入倍率（allowMultipliers=false）：PricingEntryCard 传 show-multipliers=false
 *   隐藏 Fast/Flex 与区间倍率，避免用户填了被后端静默丢弃。
 * rules 由父级传入（form.stats_rules），就地变更，与 PlatformSection 同语义；
 *   _uid 仅用于前端 key 与搜索状态，提交时由 statsRulesFormToAPI 剔除。 */
import { computed, reactive, watch } from 'vue'
import { request } from '../../api'
import { DeleteOutlined, SearchOutlined } from '@ant-design/icons-vue'
import PricingEntryCard from './PricingEntryCard.vue'
import { emptyStatsPricingEntry, emptyStatsRule } from './statsRules'
import { PLATFORM_META, platformLabel, platformTagStyle } from './pricing'

const props = defineProps({
  /* 规则数组（父级 form.stats_rules，就地变更） */
  rules: { type: Array, required: true },
  /* 全部分组（弹窗打开时已加载） */
  groups: { type: Array, default: () => [] },
  /* 渠道已绑定分组的并集（规则引用范围）；已存规则引用了已解绑分组时也回显 */
  channelGroupIds: { type: Array, default: () => [] },
  /* 渠道已启用平台（规则定价条目的平台候选，按 PLATFORM_ORDER 过滤） */
  platforms: { type: Array, default: () => [] },
})

/* ── 规则增删 ── */
function addRule() {
  props.rules.push(emptyStatsRule())
}
function removeRule(idx) {
  props.rules.splice(idx, 1)
}
function ruleSummary(rule) {
  return `${rule.group_ids.length} 分组 · ${rule.account_ids.length} 账号 · ${rule.pricing.length} 定价`
}

/* ── 匹配分组 chips ── */
const chipGroups = computed(() => {
  const ids = new Set(props.channelGroupIds || [])
  for (const r of props.rules) for (const gid of r.group_ids || []) ids.add(gid)
  return (props.groups || []).filter((g) => ids.has(g.id))
})
function toggleRuleGroup(rule, gid) {
  const ids = rule.group_ids
  const i = ids.indexOf(gid)
  if (i >= 0) ids.splice(i, 1)
  else ids.push(gid)
}
function groupName(g) {
  return g.name || `分组 ${g.id}`
}

/* ── 指定账号：chips + 防抖搜索（对照 GroupPricingFields 账号搜索） ── */
const queries = reactive({})
const results = reactive({})
const known = reactive({})
const timers = new Map()
function searchAccounts(uid) {
  const t = timers.get(uid)
  if (t) clearTimeout(t)
  timers.set(
    uid,
    setTimeout(async () => {
      try {
        const data = await request('/admin/accounts', {
          method: 'GET',
          query: { page: 1, page_size: 20, lite: 'true', search: queries[uid]?.trim() || undefined },
        })
        results[uid] = data.items || []
        for (const a of data.items || []) known[a.id] = a
      } catch {
        results[uid] = []
      }
    }, 300),
  )
}
function addRuleAccount(rule, account) {
  known[account.id] = account
  if (!rule.account_ids.includes(account.id)) rule.account_ids.push(account.id)
  queries[rule._uid] = ''
  results[rule._uid] = []
}
function removeRuleAccount(rule, id) {
  const i = rule.account_ids.indexOf(id)
  if (i >= 0) rule.account_ids.splice(i, 1)
}
const accountLabel = (id) => known[id]?.name || `#${id}`

/* 已选账号名称回填：未知 id 逐个拉详情，失败显示 #id（对照旧 populateRuleAccountNameCache） */
const fetched = new Set()
watch(
  () => props.rules.flatMap((r) => r.account_ids || []),
  (ids) => {
    for (const id of ids) {
      if (fetched.has(id) || known[id]) continue
      fetched.add(id)
      request(`/admin/accounts/${id}`, { method: 'GET' })
        .then((acc) => {
          if (acc?.name) known[id] = acc
        })
        .catch(() => {})
    }
  },
  { immediate: true },
)

/* ── 规则定价条目 ── */
const platformOptions = computed(() => {
  const keys = (props.platforms || []).length ? props.platforms : Object.keys(PLATFORM_META)
  return keys.map((p) => ({ value: p, label: platformLabel(p) }))
})
function addRulePricing(rule) {
  rule.pricing.push(emptyStatsPricingEntry(props.platforms?.[0]))
}
function updateRulePricing(rule, idx, updated) {
  rule.pricing.splice(idx, 1, updated)
}
function removeRulePricing(rule, idx) {
  rule.pricing.splice(idx, 1)
}
</script>

<template>
  <div class="sre">
    <div class="sre-head">
      <span class="sre-d">命中规则的账号用量按规则价格统计，始终优先于上方开关与模型目录价（多条规则先命中为准）。</span>
      <button type="button" class="mini-btn accent" @click="addRule">+ 添加规则</button>
    </div>

    <div v-if="rules.length === 0" class="empty-tip">
      暂无规则。不配置时按「定价计入账号统计」开关与模型目录价格统计。
    </div>

    <div v-for="(rule, ri) in rules" :key="rule._uid" class="rule">
      <div class="rule-head">
        <a-input v-model:value="rule.name" placeholder="规则名称（选填）" class="rule-name" />
        <span class="rule-meta">{{ ruleSummary(rule) }}</span>
        <a-tooltip title="删除规则">
          <a-button size="small" type="text" danger @click="removeRule(ri)"><DeleteOutlined /></a-button>
        </a-tooltip>
      </div>

      <div class="rule-body">
        <!-- 匹配分组：与指定账号为「或」关系，命中任一即进入下一条规则匹配前的定价查找 -->
        <div class="blk">
          <span class="blk-t">匹配分组</span>
          <div class="grp-box">
            <div v-if="chipGroups.length === 0" class="empty-tip">渠道未绑定分组，请先在平台配置中勾选分组</div>
            <label
              v-for="g in chipGroups" :key="g.id"
              class="grp-chip" :class="{ on: rule.group_ids.includes(g.id) }"
            >
              <input
                type="checkbox" :checked="rule.group_ids.includes(g.id)"
                @change="toggleRuleGroup(rule, g.id)"
              />
              <span class="grp-name">{{ groupName(g) }}</span>
              <span class="pmark" :style="platformTagStyle(g.platform)">{{ PLATFORM_META[g.platform]?.label || g.platform }}</span>
            </label>
          </div>
        </div>

        <!-- 指定账号：chips + 防抖搜索下拉 -->
        <div class="blk">
          <span class="blk-t">指定账号</span>
          <div v-if="rule.account_ids.length" class="chips">
            <span v-for="id in rule.account_ids" :key="id" class="chip">
              {{ accountLabel(id) }}
              <button type="button" class="chip-x" title="移除账号" @click="removeRuleAccount(rule, id)">×</button>
            </span>
          </div>
          <a-input
            v-model:value="queries[rule._uid]" placeholder="搜索账号名称后添加" allow-clear
            @input="searchAccounts(rule._uid)"
          >
            <template #suffix><SearchOutlined class="suf-ico" @click="searchAccounts(rule._uid)" /></template>
          </a-input>
          <div v-if="results[rule._uid]?.length" class="acct-drop">
            <button
              v-for="a in results[rule._uid]" :key="a.id" type="button" class="acct-item"
              :disabled="rule.account_ids.includes(a.id)" @click="addRuleAccount(rule, a)"
            >
              <span>{{ a.name }}</span><span class="acct-id">#{{ a.id }}</span>
            </button>
          </div>
          <span class="blk-sub">分组与账号二选一即可，都填则命中任一即生效。</span>
        </div>

        <!-- 规则定价：复用 PricingEntryCard，平台每条目自选、不展示倍率 -->
        <div class="blk">
          <div class="blk-head">
            <span class="blk-t">规则定价 <i class="req">*</i></span>
            <button type="button" class="mini-btn accent" @click="addRulePricing(rule)">+ 添加定价</button>
          </div>
          <div v-if="rule.pricing.length === 0" class="empty-tip">至少添加一个定价条目</div>
          <div v-else class="price-list">
            <PricingEntryCard
              v-for="(entry, pi) in rule.pricing" :key="pi"
              :entry="entry" :platform="entry.platform || props.platforms?.[0] || 'anthropic'"
              platform-editable :platform-options="platformOptions" :show-multipliers="false"
              @update="updateRulePricing(rule, pi, $event)" @remove="removeRulePricing(rule, pi)"
            />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sre { display: grid; gap: 8px; }
.sre-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.sre-d { font-size: 12px; color: var(--text2); line-height: 1.6; }
.rule { border: 1px solid var(--hr); border-radius: 8px; background: var(--card); overflow: hidden; }
.rule-head { display: flex; align-items: center; gap: 8px; padding: 6px 10px; border-bottom: 1px solid var(--hr); }
.rule-name { flex: 1; min-width: 0; }
.rule-meta { font-size: 11.5px; color: var(--text2); flex: none; }
.rule-body { padding: 10px; display: grid; gap: 12px; }
.blk { display: grid; gap: 6px; }
.blk-head { display: flex; align-items: center; gap: 8px; }
.blk-t { font-size: 12.5px; font-weight: 500; }
.blk-sub { font-size: 11.5px; color: var(--text2); }
.req { color: #d13438; font-style: normal; }
.mini-btn {
  height: 26px; padding: 0 10px; border: 1px solid var(--hr); border-radius: 6px;
  background: transparent; color: var(--text2); font-size: 11.5px; cursor: pointer; font-family: inherit;
  flex: none;
}
.mini-btn:hover { border-color: var(--theme-2); color: var(--theme-2); }
.mini-btn.accent { border-color: var(--theme-2); color: var(--theme-2); }

/* 分组 chips（与 PlatformSection 的 grp-chip 同形态） */
.grp-box {
  display: flex; flex-wrap: wrap; gap: 6px; max-height: 150px; overflow: auto;
  padding: 8px; border: 1px solid var(--hr); border-radius: 8px;
}
.grp-chip {
  display: inline-flex; align-items: center; gap: 5px; padding: 3px 8px;
  border: 1px solid var(--hr); border-radius: 6px; font-size: 12px; cursor: pointer;
  transition: background 100ms;
}
.grp-chip:hover { background: var(--hover-b); }
.grp-chip.on { border-color: var(--theme-2); background: color-mix(in srgb, var(--theme-2) 12%, transparent); }
.grp-chip input { margin: 0; }
.grp-name { font-weight: 500; }
.pmark {
  display: inline-flex; align-items: center; height: 16px; padding: 0 5px;
  border-radius: 4px; font-size: 10.5px;
}

/* 账号 chips + 搜索下拉 */
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip {
  display: inline-flex; align-items: center; gap: 4px; font-size: 11.5px;
  border: 1px solid var(--hr); border-radius: 6px; padding: 2px 8px;
}
.chip-x { border: 0; background: none; cursor: pointer; color: var(--text2); padding: 0; font-size: 12px; }
.chip-x:hover { color: #d13438; }
.suf-ico { color: var(--text2); cursor: pointer; font-size: 12px; }
.acct-drop {
  border: 1px solid var(--hr); border-radius: 8px; background: var(--card);
  max-height: 176px; overflow-y: auto;
}
.acct-item {
  width: 100%; display: flex; justify-content: space-between; gap: 8px; padding: 6px 10px;
  border: none; background: none; cursor: pointer; font-size: 12.5px; text-align: left; font-family: inherit;
}
.acct-item:hover:not(:disabled) { background: var(--hover-b); }
.acct-item:disabled { opacity: 0.45; cursor: default; }
.acct-id { color: var(--text2); }

.price-list { display: grid; gap: 8px; }
.empty-tip {
  padding: 12px; text-align: center; font-size: 11.5px; color: var(--text2);
  border: 1px dashed var(--hr); border-radius: 8px;
}
</style>
