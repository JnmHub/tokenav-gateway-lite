<script setup>
/* ============================================================
 * V2 渠道监控矩阵视图（Win12 重构版）
 * 数据源（admin 路由，v1 模式下后端 403，由父组件控制显隐）：
 *   GET /admin/channel-monitor-v2/dimensions?range=
 *     → { platforms:[{value,label,platform?,request_count}],
 *         groups:[{id,name,platform?,request_count}],
 *         models:[{value,label,platform?,request_count}] }
 *   GET /admin/channel-monitor-v2/matrix?range=&group_by=&platform=&group_id=&model=
 *     → { group_by, coverage, items:[{ platform, group_id?, group_name?, model?,
 *         metrics, health, buckets:[{bucket_start, metrics, health}] }] }
 * group_by 枚举（service.ParseChannelMonitorV2GroupBy）：
 *   platform / platform_group（后端默认）/ platform_model / platform_group_model
 * 展示：筛选条（维度来自 dimensions）+ 矩阵表格（行内分桶热力条，
 *   颜色按每桶错误率取低饱和绿/黄/红，tooltip 展示每桶请求数与错误率）。
 * ============================================================ */
import { ref, computed, watch, onMounted } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { getV2Dimensions, getV2Matrix } from './api'
import { V2_RANGES, fmtNum, fmtPercent, fmtMs, fmtTime } from './constants'

/* ---------- 筛选状态 ---------- */
const range = ref('90m')
const groupBy = ref('platform_group')
const platformFilter = ref('') // '' = 全部
const groupFilter = ref('') // '' = 全部，值为字符串化 group id
const modelFilter = ref('') // '' = 全部

const GROUP_BY_OPTIONS = [
  { value: 'platform', label: '按平台' },
  { value: 'platform_group', label: '平台 + 分组' },
  { value: 'platform_model', label: '平台 + 模型' },
  { value: 'platform_group_model', label: '平台 + 分组 + 模型' },
]

/* ---------- 维度选项（dimensions 接口驱动） ---------- */
const dims = ref({ platforms: [], groups: [], models: [] })
const platformOptions = computed(() => [
  { value: '', label: '全部平台' },
  ...(dims.value.platforms || []).map((p) => ({ value: p.value, label: p.label || p.value })),
])
const groupOptions = computed(() => [
  { value: '', label: '全部分组' },
  ...(dims.value.groups || []).map((g) => ({ value: String(g.id), label: g.name })),
])
const modelOptions = computed(() => [
  { value: '', label: '全部模型' },
  ...(dims.value.models || []).map((m) => ({ value: m.value, label: m.label || m.value })),
])

/* ---------- 矩阵数据 ---------- */
const loading = ref(false)
const items = ref([])
const coverage = ref(null)

/* 加载序号守卫：快速切换筛选时丢弃过期响应 */
let loadSeq = 0

async function loadDimensions() {
  try {
    const d = await getV2Dimensions(range.value)
    dims.value = d || { platforms: [], groups: [], models: [] }
  } catch (e) {
    message.error(`监控维度读取失败：${e.message}`)
  }
}

async function loadMatrix() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const query = { range: range.value, group_by: groupBy.value }
    if (platformFilter.value) query.platform = platformFilter.value
    if (groupFilter.value) query.group_id = groupFilter.value
    if (modelFilter.value) query.model = modelFilter.value
    const data = await getV2Matrix(query)
    if (seq !== loadSeq) return
    items.value = data?.items || []
    coverage.value = data?.coverage || null
  } catch (e) {
    if (seq !== loadSeq) return
    message.error(`矩阵数据读取失败：${e.message}`)
    items.value = []
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function reload() {
  loadDimensions()
  loadMatrix()
}
watch([range, groupBy, platformFilter, groupFilter, modelFilter], reload)
onMounted(reload)

/* ---------- 矩阵列：随 group_by 动态拼接分组/模型列 ---------- */
const showGroupCol = computed(() => groupBy.value === 'platform_group' || groupBy.value === 'platform_group_model')
const showModelCol = computed(() => groupBy.value === 'platform_model' || groupBy.value === 'platform_group_model')
const columns = computed(() => {
  const cols = [{ title: '平台', dataIndex: 'platform', key: 'platform', width: 110 }]
  if (showGroupCol.value) cols.push({ title: '分组', key: 'group', width: 130, ellipsis: true })
  if (showModelCol.value) cols.push({ title: '模型', dataIndex: 'model', key: 'model', width: 170, ellipsis: true })
  cols.push(
    { title: '请求数', key: 'requests', width: 90, align: 'right' },
    { title: '成功率', key: 'success', width: 80, align: 'right' },
    { title: 'TTFT P50', key: 'ttft', width: 90, align: 'right' },
    { title: '健康分', key: 'score', width: 76, align: 'right' },
    { title: '分桶热力（时间顺序）', key: 'heat', ellipsis: true },
  )
  return cols
})

/* 健康分：样本不足时后端省略 score，显示 — */
const scoreText = (row) => (row?.health?.score != null ? Math.round(row.health.score) : '—')
const scoreColor = (row) => {
  const s = row?.health?.score
  if (s == null) return 'default'
  return s >= 80 ? 'green' : s >= 60 ? 'orange' : 'red'
}

/* 分桶热力：低饱和色块，错误率越高越红；无数据桶为浅灰 */
const bucketColor = (b) => {
  const m = b?.metrics
  if (!m || !m.request_count) return 'rgba(120,120,120,.16)'
  const er = m.error_rate || 0
  if (er >= 0.2) return 'rgba(209,52,56,.65)'
  if (er >= 0.05) return 'rgba(255,180,0,.55)'
  return 'rgba(16,185,129,.5)'
}
const bucketTitle = (b) => {
  const m = b?.metrics || {}
  return `${fmtTime(b.bucket_start)} · 请求 ${fmtNum(m.request_count)} · 错误率 ${fmtPercent(m.error_rate)}`
}

const coverageText = computed(() => {
  const c = coverage.value
  if (!c?.data_through) return ''
  return `数据更新至 ${fmtTime(c.data_through)}${c.coverage_complete ? '' : '（历史回填中，覆盖不完整）'}`
})
</script>

<template>
  <div class="mtx">
    <!-- 筛选条：维度选项全部来自 /dimensions，分组间 16px -->
    <div class="mtx-bar">
      <a-select v-model:value="range" :options="V2_RANGES" style="width: 110px" size="small" />
      <a-select v-model:value="groupBy" :options="GROUP_BY_OPTIONS" style="width: 170px" size="small" />
      <span class="mtx-div" />
      <a-select
        v-model:value="platformFilter" :options="platformOptions" style="width: 130px" size="small"
        show-search option-filter-prop="label"
      />
      <a-select
        v-model:value="groupFilter" :options="groupOptions" style="width: 140px" size="small"
        show-search option-filter-prop="label"
      />
      <a-select
        v-model:value="modelFilter" :options="modelOptions" style="width: 180px" size="small"
        show-search option-filter-prop="label"
      />
      <span style="flex: 1"></span>
      <span v-if="coverageText" class="mtx-cov">{{ coverageText }}</span>
      <a-button size="small" :loading="loading" @click="reload">
        <template #icon><ReloadOutlined /></template>刷新
      </a-button>
    </div>

    <a-table
      :columns="columns" :data-source="items" :loading="loading" size="small"
      :row-key="(r) => `${r.platform}|${r.group_id ?? ''}|${r.model ?? ''}`"
      :scroll="{ x: 'max-content' }" :pagination="false"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'group'">
          {{ record.group_name || '（未分组）' }}
        </template>
        <template v-else-if="column.key === 'requests'">{{ fmtNum(record.metrics?.request_count) }}</template>
        <template v-else-if="column.key === 'success'">{{ fmtPercent(record.metrics?.success_rate) }}</template>
        <template v-else-if="column.key === 'ttft'">{{ fmtMs(record.metrics?.ttft?.p50_ms) }}</template>
        <template v-else-if="column.key === 'score'">
          <a-tag :color="scoreColor(record)" class="mtx-tag">{{ scoreText(record) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'heat'">
          <div class="mtx-heat">
            <a-tooltip v-for="(b, i) in record.buckets || []" :key="i" :title="bucketTitle(b)">
              <span class="mtx-cell" :style="{ background: bucketColor(b) }" />
            </a-tooltip>
            <span v-if="!(record.buckets || []).length" class="mtx-nodata">无分桶数据</span>
          </div>
        </template>
      </template>
      <template #emptyText>
        <div class="mtx-empty">
          <p class="mtx-empty-t">该范围内暂无矩阵数据</p>
          <p class="mtx-empty-d">V2 被动统计基于真实网关请求聚合，切换时间范围或调整筛选后重试。</p>
        </div>
      </template>
    </a-table>
  </div>
</template>

<style scoped>
.mtx { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
.mtx-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.mtx-div { width: 1px; height: 18px; background: var(--hr); margin: 0 8px; }
.mtx-cov { font-size: 11.5px; color: var(--text2); }
.mtx-tag { margin-inline-end: 0; border-radius: 4px; }

/* 分桶热力条：8px 宽圆角色块，tooltip 给明细 */
.mtx-heat { display: inline-flex; align-items: center; gap: 2px; flex-wrap: nowrap; }
.mtx-cell { width: 8px; height: 16px; border-radius: 2px; display: inline-block; }
.mtx-nodata { font-size: 12px; color: var(--text2); }

.mtx-empty { padding: 18px 0; }
.mtx-empty-t { font-weight: 500; margin-bottom: 4px; }
.mtx-empty-d { font-size: 12px; color: var(--text2); margin: 0 auto; max-width: 420px; line-height: 1.7; }

@media (max-width: 640px) {
  .mtx-bar :deep(.ant-select) { flex: 1 1 45%; }
}
</style>
