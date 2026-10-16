<script setup>
/* 运维快照 V2 卡（一期补充）：单接口聚合核心观测数据
 * 契约来源：internal/handler/admin/ops_snapshot_v2_handler.go
 * - GET /admin/ops/dashboard/snapshot-v2
 *   参数：time_range（默认 1h；custom 时传 start_time/end_time）、platform、group_id、mode=auto
 *   响应：{ generated_at, overview, throughput_trend, error_trend }（服务端 30s 缓存 + ETag）
 * 与一期 4 个分散接口同源，这里用一次请求核对并展示核心指标 + 吞吐量/错误 2 张趋势简图 */
import { ref, computed, onMounted } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const props = defineProps({
  timeRange: { type: String, default: '1h' },
  customStart: { type: String, default: '' },
  customEnd: { type: String, default: '' },
  platform: { type: String, default: '' },
  groupId: { type: Number, default: null },
})

const loading = ref(false)
const snap = ref(null)

const buildQuery = () => {
  const query = { mode: 'auto' }
  if (props.platform) query.platform = props.platform
  if (props.groupId) query.group_id = props.groupId
  if (props.timeRange === 'custom') {
    if (props.customStart && props.customEnd) {
      query.start_time = props.customStart; query.end_time = props.customEnd
    } else {
      query.time_range = '1h' // 自定义未就绪时回退，避免后端 400（与一期同策略）
    }
  } else {
    query.time_range = props.timeRange
  }
  return query
}

const load = async () => {
  loading.value = true
  try {
    snap.value = await request('/admin/ops/dashboard/snapshot-v2', { method: 'GET', query: buildQuery() })
  } catch (e) {
    message.error(`快照 V2 加载失败：${e.message}`)
  } finally { loading.value = false }
}
onMounted(load)
defineExpose({ refresh: load })

const ov = computed(() => snap.value?.overview || {})
const fmtNum = (v) => (typeof v === 'number' && Number.isFinite(v) ? v.toLocaleString('zh-CN') : '-')
const f1 = (v) => (typeof v === 'number' && Number.isFinite(v) ? v.toFixed(1) : '-')
const pct = (v) => (typeof v === 'number' && Number.isFinite(v) ? `${(v * 100).toFixed(2)}%` : '-')
const genAt = computed(() => (snap.value?.generated_at ? String(snap.value.generated_at).replace('T', ' ').slice(11, 19) : '--:--:--'))

/* ---------- 趋势简图（柱形，复用一期视觉语言） ---------- */
const thrPoints = computed(() => (snap.value?.throughput_trend?.points ?? []).slice(-40))
const thrMax = computed(() => Math.max(1, ...thrPoints.value.map((p) => p.request_count || 0)))
const thrH = (v) => (v <= 0 ? '4%' : `${Math.max(10, Math.round((v / thrMax.value) * 100))}%`)

const errPoints = computed(() => (snap.value?.error_trend?.points ?? []).slice(-40))
const errMax = computed(() => Math.max(1, ...errPoints.value.map((p) => p.error_count_total || 0)))
const errH = (v) => (v <= 0 ? '4%' : `${Math.max(10, Math.round((v / errMax.value) * 100))}%`)
const bucketTime = (v) => { try { return String(v).slice(11, 16) } catch { return '--:--' } }
</script>

<template>
  <div class="card">
    <div class="card-h">
      <div>
        <div class="sec-t">运维快照 V2</div>
        <div class="sub-t">单接口聚合返回核心概览 + 吞吐量/错误趋势（服务端 30s 缓存，生成于 {{ genAt }}）</div>
      </div>
      <a-button size="small" :loading="loading" @click="load">
        <template #icon><ReloadOutlined /></template>刷新
      </a-button>
    </div>

    <a-spin :spinning="loading">
      <div class="ov-grid">
        <div class="ov-cell"><div class="dt-k">总请求</div><div class="ov-v">{{ fmtNum(ov.request_count_total) }}</div></div>
        <div class="ov-cell ok"><div class="dt-k">成功率（SLA）</div><div class="ov-v">{{ pct(ov.sla) }}</div></div>
        <div class="ov-cell bad"><div class="dt-k">SLA 错误</div><div class="ov-v">{{ fmtNum(ov.error_count_sla) }}</div></div>
        <div class="ov-cell"><div class="dt-k">Token 消耗</div><div class="ov-v">{{ fmtNum(ov.token_consumed) }}</div></div>
        <div class="ov-cell"><div class="dt-k">平均 QPS / TPS</div><div class="ov-v sm">{{ f1(ov.qps?.avg) }} / {{ f1(ov.tps?.avg) }}</div></div>
        <div class="ov-cell"><div class="dt-k">健康分</div><div class="ov-v">{{ ov.health_score ?? '-' }}</div></div>
      </div>

      <div class="tr-grid">
        <div class="tr-pane">
          <div class="sub-h">吞吐量趋势 <span class="dim">{{ snap?.throughput_trend?.bucket || '' }} 粒度</span></div>
          <div v-if="thrPoints.length" class="trend">
            <div v-for="p in thrPoints" :key="p.bucket_start" class="tbar"
              :title="`${bucketTime(p.bucket_start)} · ${p.request_count} 请求 · ${f1(p.qps)} QPS`">
              <span class="thr" :style="{ height: thrH(p.request_count || 0) }" />
            </div>
          </div>
          <div v-else class="empty">当前范围暂无吞吐量数据</div>
        </div>
        <div class="tr-pane">
          <div class="sub-h">错误趋势 <span class="dim">{{ snap?.error_trend?.bucket || '' }} 粒度</span></div>
          <div v-if="errPoints.length" class="trend">
            <div v-for="p in errPoints" :key="p.bucket_start" class="tbar"
              :title="`${bucketTime(p.bucket_start)} · ${p.error_count_total} 错误 · SLA ${p.error_count_sla}`">
              <span class="err" :style="{ height: errH(p.error_count_total || 0) }" />
            </div>
          </div>
          <div v-else class="empty">当前范围暂无错误数据</div>
        </div>
      </div>
    </a-spin>
  </div>
</template>

<style scoped>
.card { background: var(--card); border: 1px solid var(--hr); border-radius: 10px; padding: 12px 14px; min-width: 0; }
.card-h { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.sec-t { font-size: 13px; font-weight: 500; }
.sub-t { font-size: 11px; color: var(--text2); margin-top: 2px; }
.dt-k { font-size: 10px; color: var(--text2); }
.dim { font-size: 11px; color: var(--text2); }
.ov-grid { display: grid; grid-template-columns: repeat(6, 1fr); gap: 8px; }
.ov-cell { background: rgba(127, 127, 127, 0.06); border-radius: 8px; padding: 8px 12px; min-width: 0; }
.ov-cell.ok { background: rgba(22, 163, 74, 0.08); }
.ov-cell.bad { background: rgba(225, 29, 72, 0.08); }
.ov-v { font-size: 16px; font-weight: 600; margin-top: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ov-v.sm { font-size: 14px; }
.tr-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 4px; }
.tr-pane { min-width: 0; }
.sub-h { font-size: 12px; font-weight: 500; margin: 12px 0 6px; }
.trend { display: flex; align-items: flex-end; gap: 2px; height: 72px; background: rgba(127, 127, 127, 0.05); border-radius: 8px; padding: 8px 10px; }
.tbar { flex: 1; min-width: 0; height: 100%; display: flex; align-items: flex-end; cursor: default; }
.tbar span { display: block; width: 100%; border-radius: 2px 2px 0 0; min-height: 3px; transition: height 0.2s ease; }
.tbar span.thr { background: #3b82f6; }
.tbar span.err { background: #e11d48; }
.empty { padding: 16px 0; text-align: center; font-size: 12px; color: var(--text2); }
@media (max-width: 1100px) {
  .ov-grid { grid-template-columns: repeat(3, 1fr); }
  .tr-grid { grid-template-columns: 1fr; }
}
</style>
