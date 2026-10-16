<script setup>
/* OpenAI Token 请求统计卡（二期）：时间范围 30m~30d + TopN/分页双视图 + 7 列模型表
 * 契约来源：旧 OpsOpenAITokenStatsCard.vue + frontend/src/api/admin/ops.ts
 * - GET /admin/ops/dashboard/openai-token-stats
 *   ?time_range=30m|1h|1d|15d|30d&platform&group_id&top_n | page&page_size
 * - 响应：{ time_range, start_time, end_time, items[], total, page?, page_size?, top_n? }
 * - 7 列：model / request_count / avg_tokens_per_sec / avg_first_token_ms /
 *   total_output_tokens / avg_duration_ms / requests_with_first_token */
import { ref, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'

const props = defineProps({
  platform: { type: String, default: '' },
  groupId: { type: [Number, null], default: null },
  refreshToken: { type: Number, default: 0 },
})

const loading = ref(false)
const response = ref(null)

const timeRange = ref('30d')
const viewMode = ref('topn') // 'topn' | 'pagination'
const topN = ref(20)
const page = ref(1)
const pageSize = ref(20)

const TR_OPTS = [
  { value: '30m', label: '近30分钟' }, { value: '1h', label: '近1小时' },
  { value: '1d', label: '近1天' }, { value: '15d', label: '近15天' }, { value: '30d', label: '近30天' },
]
const MODE_OPTS = [{ value: 'topn', label: 'TopN' }, { value: 'pagination', label: '分页' }]
const TOPN_OPTS = [10, 20, 50, 100].map((v) => ({ value: v, label: `Top ${v}` }))
const SIZE_OPTS = [10, 20, 50, 100].map((v) => ({ value: v, label: String(v) }))

const items = computed(() => response.value?.items ?? [])
const total = computed(() => response.value?.total ?? 0)
const totalPages = computed(() => {
  if (viewMode.value !== 'pagination') return 1
  const size = pageSize.value > 0 ? pageSize.value : 20
  return Math.max(1, Math.ceil(total.value / size))
})

const buildParams = () => {
  const params = { time_range: timeRange.value }
  if (props.platform) params.platform = props.platform
  if (typeof props.groupId === 'number' && props.groupId > 0) params.group_id = props.groupId
  if (viewMode.value === 'topn') params.top_n = topN.value
  else { params.page = page.value; params.page_size = pageSize.value }
  return params
}

const loadData = async () => {
  loading.value = true
  try {
    response.value = await request('/admin/ops/dashboard/openai-token-stats', { method: 'GET', query: buildParams() })
    // 防御：total 变化导致当前页越界时回退末页再拉一次（旧实现同策略）
    if (viewMode.value === 'pagination' && page.value > totalPages.value) {
      page.value = totalPages.value
      response.value = await request('/admin/ops/dashboard/openai-token-stats', { method: 'GET', query: buildParams() })
    }
  } catch (e) { response.value = null; message.error(`加载 OpenAI Token 统计失败：${e.message}`) }
  finally { loading.value = false }
}

watch(
  () => ({ tr: timeRange.value, mode: viewMode.value, topN: topN.value, page: page.value, size: pageSize.value, plat: props.platform, gid: props.groupId, tk: props.refreshToken }),
  (next, prev) => {
    // 筛选变化时只重置页码，等待下一次 watch（仅 page 变化）再请求，避免双请求
    const filtersChanged = !prev || next.tr !== prev.tr || next.mode !== prev.mode || next.size !== prev.size || next.plat !== prev.plat || next.gid !== prev.gid
    if (next.mode === 'pagination' && filtersChanged && next.page !== 1) { page.value = 1; return }
    loadData()
  },
  { immediate: true },
)

const fmtInt = (v) => (typeof v === 'number' && Number.isFinite(v) ? Math.round(v).toLocaleString('zh-CN') : '-')
const fmtRate = (v) => (typeof v === 'number' && Number.isFinite(v) ? v.toFixed(2) : '-')

const columns = [
  { title: '模型', dataIndex: 'model', key: 'model', width: 220 },
  { title: '请求数', key: 'req', width: 90 },
  { title: '平均 Tokens/秒', key: 'tps', width: 120 },
  { title: '平均首 Token 延迟(ms)', key: 'ttft', width: 160 },
  { title: '输出 Token 总数', key: 'tok', width: 130 },
  { title: '平均时长(ms)', key: 'dur', width: 120 },
  { title: '首 Token 样本数', key: 'samp', width: 130 },
]
</script>

<template>
  <div class="card">
    <div class="card-h">
      <div>
        <div class="sec-t">OpenAI Token 请求统计</div>
        <div class="sub-t">按模型聚合的请求量、输出 Token 与首 Token 延迟</div>
      </div>
      <div class="h-btns">
        <a-select v-model:value="timeRange" :options="TR_OPTS" size="small" style="width: 104px" />
        <a-select v-model:value="viewMode" :options="MODE_OPTS" size="small" style="width: 84px" />
        <a-select v-if="viewMode === 'topn'" v-model:value="topN" :options="TOPN_OPTS" size="small" style="width: 96px" />
        <template v-else>
          <a-select v-model:value="pageSize" :options="SIZE_OPTS" size="small" style="width: 70px" />
          <a-button size="small" :disabled="loading || page <= 1" @click="page -= 1">上一页</a-button>
          <a-button size="small" :disabled="loading || page >= totalPages" @click="page += 1">下一页</a-button>
          <span class="dim">第 {{ page }}/{{ totalPages }} 页</span>
        </template>
      </div>
    </div>

    <a-spin :spinning="loading">
      <div v-if="!loading && items.length === 0" class="empty">当前筛选条件下暂无 OpenAI Token 请求统计数据</div>
      <template v-else>
        <a-table
          :columns="columns" :data-source="items" row-key="model" size="small"
          :scroll="{ x: 'max-content' }" :pagination="false"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'req'">{{ fmtInt(record.request_count) }}</template>
            <template v-else-if="column.key === 'tps'">{{ fmtRate(record.avg_tokens_per_sec) }}</template>
            <template v-else-if="column.key === 'ttft'">{{ fmtRate(record.avg_first_token_ms) }}</template>
            <template v-else-if="column.key === 'tok'">{{ fmtInt(record.total_output_tokens) }}</template>
            <template v-else-if="column.key === 'dur'">{{ fmtInt(record.avg_duration_ms) }}</template>
            <template v-else-if="column.key === 'samp'">{{ fmtInt(record.requests_with_first_token) }}</template>
          </template>
        </a-table>
        <div v-if="viewMode === 'topn'" class="dim" style="margin-top: 8px">模型总数：{{ total.toLocaleString('zh-CN') }}</div>
      </template>
    </a-spin>
  </div>
</template>

<style scoped>
.card { background: var(--card); border: 1px solid var(--hr); border-radius: 10px; padding: 12px 14px; min-width: 0; }
.card-h { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.sec-t { font-size: 13px; font-weight: 500; }
.sub-t { font-size: 11px; color: var(--text2); margin-top: 2px; }
.h-btns { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
.dim { font-size: 11px; color: var(--text2); }
.empty { padding: 24px 0; text-align: center; font-size: 12px; color: var(--text2); }
</style>
