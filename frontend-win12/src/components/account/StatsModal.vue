<script setup>
/* 账号统计弹窗——旧实现契约：frontend/src/components/account/AccountStatsModal.vue
 * 接口：GET /admin/accounts/:id/stats?days=30
 * 响应 AccountUsageStatsResponse（internal/pkg/usagestats/usage_log_types.go）：
 *   summary{days,actual_days_used,total_cost,total_user_cost,total_standard_cost,
 *           total_requests,total_tokens,avg_daily_*,avg_duration_ms,today,
 *           highest_cost_day,highest_request_day}
 *   history[{label,requests,tokens,actual_cost,user_cost}] / models[{model,...}] */
import { ref, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'

const open = defineModel('open', { type: Boolean, default: false })
const props = defineProps({
  account: { type: Object, default: null },
})

const loading = ref(false)
const stats = ref(null)

const fmtCost = (n) => `$${(Number(n) || 0).toFixed(4).replace(/\.?0+$/, '')}`
const fmtNum = (n) => new Intl.NumberFormat('zh-CN').format(Math.round(Number(n) || 0))
const fmtTokens = (n) => {
  const v = Number(n) || 0
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)}B`
  if (v >= 1e6) return `${(v / 1e6).toFixed(2)}M`
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}K`
  return String(Math.round(v))
}
const fmtMs = (n) => {
  const v = Number(n) || 0
  return v >= 1000 ? `${(v / 1000).toFixed(2)} s` : `${Math.round(v)} ms`
}

const load = async () => {
  if (!props.account?.id) return
  loading.value = true
  stats.value = null
  try {
    stats.value = await request(`/admin/accounts/${props.account.id}/stats`, { method: 'GET', query: { days: 30 } })
  } catch (e) {
    message.error(`读取统计失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

watch(open, (o) => { if (o) load() })

const modelColumns = [
  { title: '模型', dataIndex: 'model', key: 'model', ellipsis: true },
  { title: '请求', dataIndex: 'requests', key: 'requests', width: 90 },
  { title: 'Token', dataIndex: 'tokens', key: 'tokens', width: 110 },
  { title: '费用', dataIndex: 'actual_cost', key: 'actual_cost', width: 110 },
]
</script>

<template>
  <a-modal v-model:open="open" :title="`用量统计 · ${account?.name || ''}`" width="640px" :footer="null">
    <a-spin :spinning="loading">
      <template v-if="stats">
        <div class="skv">
          <div class="sitem"><span>统计周期</span><b>{{ stats.summary?.actual_days_used ?? 0 }} / {{ stats.summary?.days ?? 30 }} 天</b></div>
          <div class="sitem"><span>账号成本</span><b>{{ fmtCost(stats.summary?.total_cost) }}</b></div>
          <div class="sitem"><span>用户成本</span><b>{{ fmtCost(stats.summary?.total_user_cost) }}</b></div>
          <div class="sitem"><span>总请求</span><b>{{ fmtNum(stats.summary?.total_requests) }}</b></div>
          <div class="sitem"><span>总 Token</span><b>{{ fmtTokens(stats.summary?.total_tokens) }}</b></div>
          <div class="sitem"><span>平均耗时</span><b>{{ fmtMs(stats.summary?.avg_duration_ms) }}</b></div>
          <div class="sitem"><span>日均成本</span><b>{{ fmtCost(stats.summary?.avg_daily_cost) }}</b></div>
          <div class="sitem"><span>日均请求</span><b>{{ fmtNum(stats.summary?.avg_daily_requests) }}</b></div>
        </div>
        <div class="sub">今日</div>
        <div class="skv">
          <div class="sitem"><span>请求</span><b>{{ fmtNum(stats.summary?.today?.requests) }}</b></div>
          <div class="sitem"><span>Token</span><b>{{ fmtTokens(stats.summary?.today?.tokens) }}</b></div>
          <div class="sitem"><span>成本</span><b>{{ fmtCost(stats.summary?.today?.cost) }}</b></div>
          <div class="sitem"><span>用户成本</span><b>{{ fmtCost(stats.summary?.today?.user_cost) }}</b></div>
        </div>
        <div class="sub">峰值</div>
        <div class="skv">
          <div class="sitem">
            <span>最高成本日</span>
            <b>{{ stats.summary?.highest_cost_day?.label || '-' }}（{{ fmtCost(stats.summary?.highest_cost_day?.cost) }}）</b>
          </div>
          <div class="sitem">
            <span>最高请求日</span>
            <b>{{ stats.summary?.highest_request_day?.label || '-' }}（{{ fmtNum(stats.summary?.highest_request_day?.requests) }} 次）</b>
          </div>
        </div>
        <template v-if="(stats.models || []).length">
          <div class="sub">模型分布</div>
          <a-table :columns="modelColumns" :data-source="stats.models" row-key="model" size="small" :pagination="false" :scroll="{ y: 240 }" />
        </template>
        <div v-else class="empty">该账号近 30 天暂无用量记录</div>
      </template>
      <div v-else-if="!loading" class="empty">暂无数据</div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.skv { display: grid; grid-template-columns: 1fr 1fr; gap: 8px 16px; }
.sitem { display: flex; justify-content: space-between; align-items: center; gap: 8px; padding: 7px 10px; border-radius: 8px; background: var(--card); font-size: 13px; }
.sitem span { color: var(--text2); flex: none; }
.sitem b { font-weight: 500; word-break: break-all; text-align: right; }
.sub { font-weight: 500; font-size: 13px; margin: 14px 0 8px; padding-top: 10px; border-top: 1px solid var(--hr); }
.empty { color: var(--text2); font-size: 13px; padding: 24px 0; text-align: center; }
</style>
