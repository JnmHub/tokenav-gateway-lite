<script setup>
/* 实时 QPS/TPS 面板
 * 契约来源：旧 OpsDashboardHeader.vue realtime 面板 + opsAPI.getRealtimeTrafficSummary：
 * GET /admin/ops/realtime-traffic?window=1min|5min|30min|1h[&platform][&group_id]
 * 响应 { enabled, summary: { window, qps: {current,peak,avg}, tps: {current,peak,avg} } }
 * 窗口档位受主顶栏时间范围约束（1min/5min/30min/1h ≤ 所选范围），切换范围后回 1min */
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { request } from '../../api'

const props = defineProps({
  platform: { type: String, default: '' },
  groupId: { type: Number, default: null },
  timeRangeMinutes: { type: Number, default: 60 },
  refreshToken: { type: Number, default: 0 },
})

const WINDOW_MIN = { '1min': 1, '5min': 5, '30min': 30, '1h': 60 }
const win = ref('1min')
const loading = ref(false)
const summary = ref(null)

const winOpts = computed(() => {
  const ok = Object.keys(WINDOW_MIN).filter((w) => WINDOW_MIN[w] <= (props.timeRangeMinutes || 60))
  return ok.map((w) => ({ value: w, label: w }))
})
watch(() => props.timeRangeMinutes, () => { win.value = '1min' })

const load = async () => {
  loading.value = true
  try {
    const query = { window: win.value }
    if (props.platform) query.platform = props.platform
    if (props.groupId) query.group_id = props.groupId
    const res = await request('/admin/ops/realtime-traffic', { method: 'GET', query })
    summary.value = res?.summary || null
  } catch { summary.value = null } finally { loading.value = false }
}
watch([win, () => props.platform, () => props.groupId], load, { immediate: false })
watch(() => props.refreshToken, load)
onMounted(load)

const f1 = (v) => (typeof v === 'number' && Number.isFinite(v) ? v.toFixed(1) : '-')
const qps = computed(() => summary.value?.qps || {})
const tps = computed(() => summary.value?.tps || {})

/* 面板内 10s 轻量轮询，让“实时”数字真正动起来（与主刷新节奏解耦） */
let timer = null
onMounted(() => { timer = setInterval(load, 10000) })
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="rcard">
    <div class="rhead">
      <div class="rtitle">
        <span class="pulse"></span>实时流量
      </div>
      <a-segmented v-model:value="win" :options="winOpts" size="small" />
    </div>
    <div class="rbody">
      <div class="rcur">
        <div class="rk">当前</div>
        <div class="rnums">
          <span class="rn">{{ f1(qps.current) }}<i>QPS</i></span>
          <span class="rn">{{ f1(tps.current) }}<i>TPS</i></span>
        </div>
      </div>
      <div class="rsplit"></div>
      <div class="rpeak-avg">
        <div class="rblock">
          <div class="rk">峰值</div>
          <div class="rline">{{ f1(qps.peak) }} <i>QPS</i></div>
          <div class="rline">{{ f1(tps.peak) }} <i>TPS</i></div>
        </div>
        <div class="rblock">
          <div class="rk">均值</div>
          <div class="rline">{{ f1(qps.avg) }} <i>QPS</i></div>
          <div class="rline">{{ f1(tps.avg) }} <i>TPS</i></div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.rcard { padding: 12px 16px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); height: 100%; display: flex; flex-direction: column; }
.rhead { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.rtitle { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text2); font-weight: 500; }
.pulse { width: 8px; height: 8px; border-radius: 50%; background: #3b82f6; animation: pul 2s ease-in-out infinite; }
@keyframes pul { 0%,100% { opacity: 1 } 50% { opacity: .35 } }
.rbody { margin-top: 10px; display: flex; gap: 16px; flex: 1; align-items: center; flex-wrap: wrap; }
.rk { font-size: 11px; color: var(--text2); }
.rnums { display: flex; gap: 18px; margin-top: 2px; }
.rn { font-size: 26px; font-weight: 600; line-height: 1.1; }
.rn i, .rline i { margin-left: 4px; font-size: 11px; font-style: normal; color: var(--text2); }
.rsplit { width: 1px; align-self: stretch; background: var(--hr); }
.rpeak-avg { display: flex; gap: 24px; }
.rline { font-size: 15px; font-weight: 600; margin-top: 4px; }
</style>
