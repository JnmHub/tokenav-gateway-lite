<script setup>
/* 聚合回填（补入口）
 * 接口：POST /admin/dashboard/aggregation/backfill
 *   实测请求体：{ start: <RFC3339>, end: <RFC3339> }（必填，二者都按 RFC3339 解析，否则 400 Invalid start/end time）
 *   实测响应：成功 { status: "accepted" }；服务端未开启时 403「Backfill is disabled」；
 *             范围过大 400「Backfill range too large」
 *   注意：本机 lite 实例实测返回 403 Backfill is disabled（配置未开启），故面板如实回显错误，
 *         不做任何“成功”伪造；成功/失败均走 message 提示 + 面板内结果行留存。
 * 二次确认使用统一 confirmAction（危红主按钮），不使用 window.confirm。 */
import { ref } from 'vue'
import { ThunderboltOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import dayjs from 'dayjs'
import { request } from '../../api'
import { confirmAction } from '../../utils/confirmAction'

const props = defineProps({
  startDate: { type: String, default: '' },
  endDate: { type: String, default: '' },
})

const ymd = (d) => d.format('YYYY-MM-DD')
const range = ref([
  props.startDate || ymd(dayjs().subtract(6, 'day')),
  props.endDate || ymd(dayjs()),
])
const loading = ref(false)
const result = ref(null) // { ok, text, at }

const useDashboardRange = () => {
  if (props.startDate && props.endDate) {
    range.value = [props.startDate, props.endDate]
    result.value = null
  } else {
    message.warning('仪表盘当前未选定日期范围')
  }
}

// dayjs 的 Z 标记输出本地偏移（如 +08:00），后端 time.Parse(time.RFC3339) 可直接解析
const toRFC3339 = (date, plusOneDay = false) => {
  const t = plusOneDay ? dayjs(date).add(1, 'day').startOf('day') : dayjs(date).startOf('day')
  return t.format('YYYY-MM-DDTHH:mm:ssZ')
}

const run = async () => {
  if (!range.value || range.value.length !== 2) { message.warning('请先选择回填时间范围'); return }
  const [s, e] = range.value
  const ok = await confirmAction({
    title: '确认回填聚合数据？',
    content: `将对 ${s} 00:00:00 ~ ${e} 23:59:59（含当日）触发预聚合回填任务，可能占用服务端资源。是否继续？`,
    okText: '开始回填',
    danger: true,
  })
  if (!ok) return

  loading.value = true
  result.value = null
  try {
    const res = await request('/admin/dashboard/aggregation/backfill', {
      body: { start: toRFC3339(s), end: toRFC3339(e, true) },
    })
    const status = res?.status || 'accepted'
    result.value = { ok: true, text: `任务已受理（status=${status}）`, at: new Date().toTimeString().slice(0, 8) }
    message.success('回填任务已提交')
  } catch (err) {
    const raw = err.message || '请求失败'
    // request() 对非 401 的非 2xx 只抛出「请求失败（HTTP nnn）」，拿不到网关 message，
    // 这里按已实测的两种拒绝语义补一句说明，便于定位原因（原文仍完整保留）。
    const hint = /HTTP 403/.test(raw)
      ? ' —— 服务端拒绝：该实例未开启聚合回填（网关返回 Backfill is disabled）'
      : /HTTP 400/.test(raw)
        ? ' —— 服务端拒绝：回填范围过大（Backfill range too large）或时间格式非法'
        : ''
    result.value = { ok: false, text: raw + hint, at: new Date().toTimeString().slice(0, 8) }
    message.error(`回填失败：${raw}`)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="panel">
    <div class="phead">
      <h3>聚合回填</h3>
      <span class="hint">POST /admin/dashboard/aggregation/backfill · 请求体 { start, end }（RFC3339）</span>
    </div>

    <div class="bar">
      <a-range-picker
        v-model:value="range"
        value-format="YYYY-MM-DD"
        :allow-clear="false"
        style="width: 250px"
      />
      <a-button size="small" @click="useDashboardRange">用仪表盘当前范围</a-button>
      <a-button size="small" type="primary" :loading="loading" @click="run">
        <template #icon><ThunderboltOutlined /></template>
        回填聚合
      </a-button>
    </div>

    <div v-if="result" class="result" :class="{ bad: !result.ok }">
      {{ result.at }} · {{ result.ok ? '成功' : '失败' }}：{{ result.text }}
    </div>

    <div class="note">
      说明：回填按时间范围触发服务端预聚合重算，单次范围上限由服务端配置决定；
      若返回「Backfill is disabled」表示该实例未开启回填（本机 lite 即此状态，接口连通但被服务端拒绝）。
    </div>
  </div>
</template>

<style scoped>
.panel { padding: 14px 16px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.phead { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.phead h3 { margin: 0; font-size: 13px; font-weight: 600; color: var(--text); }
.hint { font-size: 12px; color: var(--text2); }
.bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.result { margin-top: 10px; font-size: 12px; color: #2f9e63; }
.result.bad { color: #e05252; }
.note { margin-top: 8px; font-size: 12px; color: var(--text2); line-height: 1.6; }
/* 移动端：日期选择器占满一行 */
@media (max-width: 768px) {
  .bar :deep(.ant-picker) { width: 100% !important; }
}
</style>
