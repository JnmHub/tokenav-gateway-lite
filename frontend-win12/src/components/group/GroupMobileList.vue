<script setup>
import W12Status from '../design/W12Status.vue'
import GroupRowActions from './GroupRowActions.vue'
const props = defineProps({
  rows: { type: Array, default: () => [] },
  loading: Boolean,
  usageMap: { type: Map, required: true },
  capacityMap: { type: Map, required: true },
  duplicatingIds: { type: Set, required: true },
})
const emit = defineEmits(['edit', 'more'])
const platforms = { anthropic: 'Anthropic', openai: 'OpenAI', gemini: 'Gemini', antigravity: 'Antigravity', grok: 'Grok', kimi: 'Kimi', zhipu: '智谱', deepseek: 'DeepSeek', composite: '聚合模型' }
const capacityText = (group, kind) => {
  const capacity = props.capacityMap.get(group.id)
  if (!capacity) return '—'
  const used = capacity[`${kind}_used`] ?? 0
  const max = capacity[`${kind}_max`] ?? 0
  return `${used} / ${max > 0 ? max : '不限'}`
}
</script>

<template>
  <div class="group-mobile-list" :aria-busy="loading">
    <article v-for="group in rows" :key="group.id" class="group-card">
      <header>
        <div class="group-title"><span>{{ group.name }}</span><small>#{{ group.id }} · {{ platforms[group.platform] || group.platform }}</small></div>
        <W12Status :tone="group.status === 'active' ? 'success' : 'error'">{{ group.status === 'active' ? '启用' : '停用' }}</W12Status>
      </header>
      <div class="group-tags">
        <W12Status>{{ group.subscription_type === 'subscription' ? '订阅' : '余额' }} · {{ group.is_exclusive ? '独占' : '公开' }}</W12Status>
        <W12Status v-if="group.local_only" tone="info">本地</W12Status>
        <W12Status v-if="group.minimum_tier != null">T{{ group.minimum_tier }}</W12Status>
        <W12Status v-if="group.allow_blocked_keyword_requests">关键词放行</W12Status>
      </div>
      <dl>
        <div><dt>计费倍率</dt><dd>×{{ group.rate_multiplier }}</dd></div>
        <div><dt>账号</dt><dd>{{ group.active_account_count || 0 }} 正常 / {{ group.rate_limited_account_count || 0 }} 限流 / {{ group.account_count || 0 }} 总数</dd></div>
        <div><dt>今日 / 累计费用</dt><dd v-if="usageMap.get(group.id)">${{ Number(usageMap.get(group.id).today_cost || 0).toFixed(2) }} / ${{ Number(usageMap.get(group.id).total_cost || 0).toFixed(2) }}</dd><dd v-else>—</dd></div>
        <div><dt>今日 / 累计请求</dt><dd v-if="usageMap.get(group.id)">{{ (usageMap.get(group.id).today_requests || 0).toLocaleString() }} / {{ (usageMap.get(group.id).total_requests || 0).toLocaleString() }}</dd><dd v-else>—</dd></div>
        <div><dt>实时入口请求</dt><dd>{{ capacityMap.get(group.id)?.ingress_requests_used ?? '—' }}</dd></div>
      </dl>
      <details class="group-capacity">
        <summary>账号容量详情</summary>
        <!-- 手机保留桌面的三种容量；读取失败显示缺失，不能把未知值当成零。 -->
        <dl>
          <div><dt>账号并发</dt><dd>{{ capacityText(group, 'concurrency') }}</dd></div>
          <div><dt>会话</dt><dd>{{ capacityText(group, 'sessions') }}</dd></div>
          <div><dt>每分钟请求</dt><dd>{{ capacityText(group, 'rpm') }}</dd></div>
        </dl>
      </details>
      <GroupRowActions :group="group" :duplicating="duplicatingIds.has(group.id)" @edit="emit('edit', $event)" @more="(action, row) => emit('more', action, row)" />
    </article>
  </div>
</template>

<style scoped>
.group-mobile-list { display: grid; gap: 8px; }
.group-card { background: var(--w12-card); padding: 12px; border-radius: 8px; }
header { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; }
.group-title { min-width: 0; display: grid; gap: 4px; font-size: 14px; }
.group-title > span { overflow-wrap: anywhere; }
.group-title small { color: var(--w12-muted); font-size: 12px; }
.group-tags { display: flex; flex-wrap: wrap; gap: 4px; margin: 12px 0; }
dl { margin: 0 0 8px; font-size: 12px; }
dl > div { display: flex; justify-content: space-between; gap: 8px; padding: 4px 0; }
dt { flex: none; color: var(--w12-muted); }
dd { margin: 0; text-align: right; overflow-wrap: anywhere; }
.group-capacity { font-size: 12px; margin-bottom: 8px; }
.group-capacity summary { min-height: 32px; line-height: 32px; cursor: pointer; color: var(--w12-muted); border-radius: 8px; }
.group-capacity summary:hover { background: var(--w12-hover); }
.group-capacity summary:focus-visible { outline: 1px solid var(--accent); outline-offset: 2px; }
</style>
