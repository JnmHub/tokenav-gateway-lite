<script setup>
/* 6 指标卡：请求 / SLA / 请求错误率 / 耗时 / TTFT / 上游错误率
 * 契约来源：旧 OpsDashboardHeader.vue 右侧 6 卡（order 1~6）：
 * - 请求：request_count_total、token_consumed、qps.avg、tps.avg → 详情=请求明细(all)
 * - SLA：sla*100 保留 3 位，异常数=request_count_sla-success_count → 详情=请求明细(error)
 * - 请求错误率：error_rate*100，error_count_sla、business_limited_count → 详情=错误列表(request)
 * - 耗时/TTFT：P99 主值 + P95/P90/P50/Avg/Max → 详情=请求明细(duration_desc)
 * - 上游错误率：upstream_error_rate*100，excl_429_529、429/529 → 详情=错误列表(upstream)
 * 阈值着色取 GET /admin/ops/settings/metric-thresholds（缺省回退旧内置阈值） */
import { computed } from 'vue'

const props = defineProps({
  overview: { type: Object, default: null },
  thresholds: { type: Object, default: null },
})
const emit = defineEmits(['detail'])

const fmt = (n) => (n === null || n === undefined ? '-' : Number(n).toLocaleString('zh-CN'))
const pct = (v) => (typeof v === 'number' ? v * 100 : null)

/* 阈值分级：高于阈值 80% 即预警（与旧 getXXXThresholdLevel 一致） */
function level(v, max, lowerBetter = true) {
  if (v == null || max == null) return 'normal'
  if (lowerBetter) {
    if (v >= max) return 'critical'
    if (v >= max * 0.8) return 'warning'
  } else {
    if (v <= max) return 'critical'
    if (v <= max + 0.1) return 'warning'
  }
  return 'normal'
}
const LV_COLOR = { normal: 'var(--text1, #1c1c1c)', warning: '#d97706', critical: '#dc2626' }

const sla = computed(() => pct(props.overview?.sla))
const slaLv = computed(() => level(sla.value, props.thresholds?.sla_percent_min ?? 99.5, false))
const errRate = computed(() => pct(props.overview?.error_rate))
const errLv = computed(() => level(errRate.value, props.thresholds?.request_error_rate_percent_max ?? 5))
const upRate = computed(() => pct(props.overview?.upstream_error_rate))
const upLv = computed(() => level(upRate.value, props.thresholds?.upstream_error_rate_percent_max ?? 5))
const ttftP99 = computed(() => props.overview?.ttft?.p99_ms ?? null)
const ttftLv = computed(() => level(ttftP99.value, props.thresholds?.ttft_p99_ms_max ?? 500))

const dur = computed(() => props.overview?.duration || {})
const ttft = computed(() => props.overview?.ttft || {})
const PERCS = [['P95', 'p95_ms'], ['P90', 'p90_ms'], ['P50', 'p50_ms'], ['Avg', 'avg_ms'], ['Max', 'max_ms']]
</script>

<template>
  <div class="mgrid">
    <!-- 请求 -->
    <div class="mcard">
      <div class="mh"><span class="mk">请求</span><a class="md" @click="emit('detail', { kind: 'all', sort: 'created_at_desc' })">详情</a></div>
      <div class="mv">{{ fmt(overview?.request_count_total) }}</div>
      <div class="mrow"><span>Token</span><b>{{ fmt(overview?.token_consumed) }}</b></div>
      <div class="mrow"><span>平均 QPS</span><b>{{ overview?.qps?.avg?.toFixed(1) ?? '-' }}</b></div>
      <div class="mrow"><span>平均 TPS</span><b>{{ overview?.tps?.avg?.toFixed(1) ?? '-' }}</b></div>
    </div>
    <!-- SLA -->
    <div class="mcard">
      <div class="mh"><span class="mk">SLA</span><a class="md" @click="emit('detail', { kind: 'error', sort: 'created_at_desc' })">详情</a></div>
      <div class="mv" :style="{ color: LV_COLOR[slaLv] }">{{ sla == null ? '-' : sla.toFixed(3) }}%</div>
      <div class="mrow"><span>成功</span><b>{{ fmt(overview?.success_count) }}</b></div>
      <div class="mrow"><span>异常</span><b class="bad">{{ fmt((overview?.request_count_sla ?? 0) - (overview?.success_count ?? 0)) }}</b></div>
    </div>
    <!-- 请求错误率 -->
    <div class="mcard">
      <div class="mh"><span class="mk">请求错误率</span><a class="md" @click="emit('detail', 'request')">详情</a></div>
      <div class="mv" :style="{ color: LV_COLOR[errLv] }">{{ errRate == null ? '-' : errRate.toFixed(2) }}%</div>
      <div class="mrow"><span>错误数</span><b>{{ fmt(overview?.error_count_sla) }}</b></div>
      <div class="mrow"><span>业务限流</span><b>{{ fmt(overview?.business_limited_count) }}</b></div>
    </div>
    <!-- 上游错误率 -->
    <div class="mcard">
      <div class="mh"><span class="mk">上游错误率</span><a class="md" @click="emit('detail', 'upstream')">详情</a></div>
      <div class="mv" :style="{ color: LV_COLOR[upLv] }">{{ upRate == null ? '-' : upRate.toFixed(2) }}%</div>
      <div class="mrow"><span>非 429/529</span><b>{{ fmt(overview?.upstream_error_count_excl_429_529) }}</b></div>
      <div class="mrow"><span>429 / 529</span><b>{{ fmt(overview?.upstream_429_count) }} / {{ fmt(overview?.upstream_529_count) }}</b></div>
    </div>
    <!-- 耗时 -->
    <div class="mcard">
      <div class="mh"><span class="mk">耗时 P99</span><a class="md" @click="emit('detail', { kind: 'all', sort: 'duration_desc', title: '请求明细 · 按耗时' })">详情</a></div>
      <div class="mv">{{ dur.p99_ms ?? '-' }}<span class="unit">ms</span></div>
      <div class="pgrid">
        <span v-for="[lab, key] in PERCS" :key="key">{{ lab }} <b>{{ dur[key] ?? '-' }}</b></span>
      </div>
    </div>
    <!-- TTFT -->
    <div class="mcard">
      <div class="mh"><span class="mk">TTFT P99</span><a class="md" @click="emit('detail', { kind: 'all', sort: 'duration_desc', title: '请求明细 · 首字延迟' })">详情</a></div>
      <div class="mv" :style="{ color: LV_COLOR[ttftLv] }">{{ ttftP99 ?? '-' }}<span class="unit">ms</span></div>
      <div class="pgrid">
        <span v-for="[lab, key] in PERCS" :key="key">{{ lab }} <b>{{ ttft[key] ?? '-' }}</b></span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mgrid { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 12px; }
.mcard { padding: 12px 14px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); display: flex; flex-direction: column; }
.mh { display: flex; align-items: center; justify-content: space-between; }
.mk { font-size: 12px; color: var(--text2); }
.md { font-size: 11px; color: #3b82f6; cursor: pointer; }
.md:hover { text-decoration: underline; }
.mv { margin-top: 4px; font-size: 22px; font-weight: 600; line-height: 1.2; }
.unit { margin-left: 4px; font-size: 11px; font-weight: 400; color: var(--text2); }
.mrow { display: flex; justify-content: space-between; margin-top: 5px; font-size: 12px; color: var(--text2); }
.mrow b { font-weight: 500; color: var(--text1, #1c1c1c); }
.mrow b.bad { color: #dc2626; }
.pgrid { margin-top: 8px; display: grid; grid-template-columns: repeat(auto-fit, minmax(64px, 1fr)); gap: 3px 8px; font-size: 11px; color: var(--text2); }
.pgrid b { font-weight: 500; color: var(--text1, #1c1c1c); }
</style>
