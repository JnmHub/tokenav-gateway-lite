<script setup>
/* 健康分圆环 + 诊断 popover
 * 契约来源：旧 OpsDashboardHeader.vue healthScoreColor / diagnosisReport：
 * - 圆环色：≥90 绿 #10b981 / ≥60 黄 #f59e0b / 红 #ef4444；系统空闲（QPS=0 且错误率=0）灰
 * - 诊断 9 类规则（阈值全部来自旧代码）：
 *   1 DB 不可用 critical  2 Redis 不可用 warning
 *   3 CPU>90 critical / >80 warning  4 内存>90 critical / >85 warning
 *   5 TTFT P99>500ms warning  6 上游错误率>5% critical / >2% warning
 *   7 请求错误率>3% critical / >0.5% warning  8 SLA<90% critical / <98% warning
 *   9 健康分<60 critical / <90 warning；空载与全部正常给 info */
import { computed } from 'vue'
import { QuestionCircleOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  overview: { type: Object, default: null },
})

const score = computed(() => {
  const v = props.overview?.health_score
  return typeof v === 'number' && Number.isFinite(v) ? v : null
})
const isIdle = computed(() => {
  const ov = props.overview
  if (!ov) return true
  return (ov.qps?.current ?? 0) === 0 && (ov.error_rate ?? 0) === 0
})
const color = computed(() => {
  if (isIdle.value || score.value == null) return '#9ca3af'
  if (score.value >= 90) return '#10b981'
  if (score.value >= 60) return '#f59e0b'
  return '#ef4444'
})
const statusText = computed(() => {
  if (isIdle.value) return '系统空闲'
  if (score.value == null) return '未知'
  if (score.value >= 90) return '健康'
  if (score.value >= 60) return '有风险'
  return '异常'
})

/* 圆环几何：100px / 8px 描边，与旧 Header 一致 */
const RADIUS = 46
const CIRC = 2 * Math.PI * RADIUS
const dashOffset = computed(() => {
  if (score.value == null) return CIRC
  const s = Math.max(0, Math.min(100, score.value))
  return CIRC - (s / 100) * CIRC
})

const pct = (v) => ((v ?? 0) * 100)

const rules = computed(() => {
  const ov = props.overview
  if (!ov) return []
  if (isIdle.value) {
    return [{ type: 'info', text: '系统当前无请求且无错误，处于空闲状态，指标不反映负载表现。' }]
  }
  const list = []
  const sm = ov.system_metrics
  if (sm) {
    if (sm.db_ok === false) list.push({ type: 'critical', text: '数据库连接异常（db_ok=false）', why: '请求无法落库，鉴权与用量记录失败', act: '检查数据库连接与网络' })
    if (sm.redis_ok === false) list.push({ type: 'warning', text: 'Redis 连接异常（redis_ok=false）', why: '鉴权缓存退化，热路径延迟上升', act: '检查 Redis 服务与连接池' })
    const cpu = sm.cpu_usage_percent ?? 0
    if (cpu > 90) list.push({ type: 'critical', text: `CPU 使用率 ${cpu.toFixed(1)}%（>90%）`, why: '节点过载，可能丢弃请求', act: '扩容或排查热点' })
    else if (cpu > 80) list.push({ type: 'warning', text: `CPU 使用率偏高 ${cpu.toFixed(1)}%（>80%）`, why: '接近过载阈值', act: '关注趋势，必要时扩容' })
    const mem = sm.memory_usage_percent ?? 0
    if (mem > 90) list.push({ type: 'critical', text: `内存使用率 ${mem.toFixed(1)}%（>90%）`, why: '有 OOM 风险', act: '排查内存泄漏并扩容' })
    else if (mem > 85) list.push({ type: 'warning', text: `内存使用率偏高 ${mem.toFixed(1)}%（>85%）`, why: '接近 OOM 阈值', act: '观察增长趋势' })
  }
  const ttftP99 = ov.ttft?.p99_ms ?? 0
  if (ttftP99 > 500) list.push({ type: 'warning', text: `TTFT P99 达 ${ttftP99.toFixed(0)}ms（>500ms）`, why: '首字延迟影响流式体验', act: '检查上游账号与网络链路' })
  const upRate = pct(ov.upstream_error_rate)
  if (upRate > 5) list.push({ type: 'critical', text: `上游错误率 ${upRate.toFixed(2)}%（>5%）`, why: '上游账号大面积异常', act: '检查账号可用性与限流' })
  else if (upRate > 2) list.push({ type: 'warning', text: `上游错误率偏高 ${upRate.toFixed(2)}%（>2%）`, why: '部分上游请求失败', act: '查看上游错误明细' })
  const errRate = pct(ov.error_rate)
  if (errRate > 3) list.push({ type: 'critical', text: `请求错误率 ${errRate.toFixed(2)}%（>3%）`, why: '整体服务质量明显下降', act: '按阶段排查错误日志' })
  else if (errRate > 0.5) list.push({ type: 'warning', text: `请求错误率升高 ${errRate.toFixed(2)}%（>0.5%）`, why: '高于正常波动区间', act: '关注错误趋势' })
  const sla = pct(ov.sla)
  if (sla < 90) list.push({ type: 'critical', text: `SLA ${sla.toFixed(2)}%（<90%）`, why: '服务可用性不达标', act: '立即排查错误来源' })
  else if (sla < 98) list.push({ type: 'warning', text: `SLA 偏低 ${sla.toFixed(2)}%（<98%）`, why: '接近不达标线', act: '排查主要错误类型' })
  if (score.value != null) {
    if (score.value < 60) list.push({ type: 'critical', text: `健康分 ${score.value}（<60）`, why: '多项指标同时异常', act: '按诊断项逐项处理' })
    else if (score.value < 90) list.push({ type: 'warning', text: `健康分偏低 ${score.value}（<90）`, why: '存在单项指标异常', act: '参照上方诊断项处理' })
  }
  if (!list.length) list.push({ type: 'info', text: '各项指标均在正常范围内，系统运行健康。' })
  return list
})
const TYPE_COLOR = { critical: '#ef4444', warning: '#f59e0b', info: '#3b82f6' }
</script>

<template>
  <div class="hcard">
    <a-popover trigger="click" placement="right" overlay-class-name="ops-diag-pop">
      <template #content>
        <div class="diag">
          <div class="diag-title">健康诊断（{{ rules.length }} 项）</div>
          <div v-for="(r, i) in rules" :key="i" class="diag-item">
            <span class="dot" :style="{ background: TYPE_COLOR[r.type] }"></span>
            <div>
              <div class="diag-msg">{{ r.text }}</div>
              <div v-if="r.why" class="diag-sub">影响：{{ r.why }}</div>
              <div v-if="r.act" class="diag-sub">建议：{{ r.act }}</div>
            </div>
          </div>
        </div>
      </template>
      <div class="ring-wrap">
        <svg width="100" height="100" viewBox="0 0 100 100">
          <circle cx="50" cy="50" :r="RADIUS" fill="none" stroke="var(--hr)" stroke-width="8" />
          <circle
            cx="50" cy="50" :r="RADIUS" fill="none" :stroke="color" stroke-width="8"
            stroke-linecap="round" :stroke-dasharray="CIRC" :stroke-dashoffset="dashOffset"
            transform="rotate(-90 50 50)" style="transition: stroke-dashoffset .5s cubic-bezier(.4,0,.2,1)"
          />
        </svg>
        <div class="ring-center">
          <div class="score" :style="{ color }">{{ isIdle ? '空闲' : (score ?? '--') }}</div>
          <div class="score-label">健康分</div>
        </div>
      </div>
    </a-popover>
    <div class="hmeta">
      <div class="hstatus" :style="{ color }">
        {{ statusText }}
        <QuestionCircleOutlined class="help" />
      </div>
      <div class="hcond">≥90 健康 · 60-89 有风险 · &lt;60 异常，点击圆环查看诊断</div>
    </div>
  </div>
</template>

<style scoped>
.hcard { display: flex; align-items: center; gap: 16px; padding: 12px 18px 12px 12px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.ring-wrap { position: relative; width: 100px; height: 100px; cursor: pointer; flex-shrink: 0; }
.ring-center { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; pointer-events: none; }
.score { font-size: 26px; font-weight: 600; line-height: 1; }
.score-label { margin-top: 2px; font-size: 10px; color: var(--text2); }
.hmeta { min-width: 0; }
.hstatus { font-size: 14px; font-weight: 500; }
.help { margin-left: 4px; font-size: 12px; opacity: .6; }
.hcond { margin-top: 4px; font-size: 11px; color: var(--text2); line-height: 1.5; }
.diag { max-width: 320px; max-height: 380px; overflow: auto; }
.diag-title { font-size: 13px; font-weight: 500; margin-bottom: 8px; }
.diag-item { display: flex; gap: 8px; padding: 6px 0; border-top: 1px solid var(--hr); }
.diag-item:first-of-type { border-top: none; }
.dot { width: 8px; height: 8px; border-radius: 50%; margin-top: 5px; flex-shrink: 0; }
.diag-msg { font-size: 12px; }
.diag-sub { font-size: 11px; color: var(--text2); margin-top: 2px; }
</style>
