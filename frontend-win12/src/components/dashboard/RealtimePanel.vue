<script setup>
/* 实时指标卡（补入口）
 * 接口：GET /admin/dashboard/realtime
 * 实测响应（curl 确认，仅有这 4 个字段）：
 *   active_requests / requests_per_minute / error_rate / average_response_time
 * 轮询：15s 自动刷新（1s 倒计时驱动，写法与 OpsCenter.vue armTimer / OpsRealtimePanel.vue 一致）；
 *   命中任一条件自动跳过本轮（面板上会显示暂停原因）：
 *     1) 页面切到后台（document.hidden）
 *     2) 有弹窗/抽屉打开（Modal.confirm、抽屉、图片预览等 overlay）
 *     3) 父级 paused 或面板内手动暂停
 * 说明：后端该接口是采样指标，未接入采样时恒为 0（本机 lite 实测 4 项均为 0），如实展示不伪造。 */
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { ReloadOutlined, PauseOutlined, CaretRightOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'

const props = defineProps({
  paused: { type: Boolean, default: false }, // 供上层（弹窗等）暂停
  intervalSec: { type: Number, default: 15 },
})

const loading = ref(false)
const err = ref('')
const d = ref(null)
const manualPause = ref(false) // 面板内手动暂停
const autoReason = ref('')     // 自动暂停原因（空 = 正常轮询）
const left = ref(props.intervalSec)
const updatedAt = ref('')

const frozen = computed(() => props.paused || manualPause.value || !!autoReason.value)

const load = async (silent = false) => {
  if (!silent) loading.value = true
  try {
    d.value = (await request('/admin/dashboard/realtime', { method: 'GET' })) || null
    err.value = ''
    updatedAt.value = new Date().toTimeString().slice(0, 8)
  } catch (e) {
    err.value = e.message
    if (!silent) message.error(`实时指标加载失败：${e.message}`)
  } finally {
    if (!silent) loading.value = false
  }
}

// overlay 可见性判断：antd 的 wrap/mask 是 position:fixed，offsetParent 恒为 null，故用计算样式判断
const isVisible = (el) => {
  const st = getComputedStyle(el)
  return st.display !== 'none' && st.visibility !== 'hidden' && st.opacity !== '0'
}
const overlayOpen = () => {
  const nodes = document.querySelectorAll('.ant-modal-wrap, .ant-modal-mask, .ant-drawer-open, .ant-image-preview-wrap')
  for (const el of nodes) if (isVisible(el)) return true
  return false
}

let timer = null
const tick = () => {
  if (props.paused) { autoReason.value = '上层已暂停刷新'; return }
  if (manualPause.value) { autoReason.value = '手动暂停中'; return }
  if (document.hidden) { autoReason.value = '页面在后台'; return }
  if (overlayOpen()) { autoReason.value = '弹窗/抽屉已打开'; return }
  autoReason.value = ''
  left.value -= 1
  if (left.value <= 0) {
    left.value = props.intervalSec
    load(true)
  }
}
onMounted(() => {
  load()
  left.value = props.intervalSec
  timer = setInterval(tick, 1000)
})
onUnmounted(() => { if (timer) { clearInterval(timer); timer = null } })

const onManualRefresh = () => { left.value = props.intervalSec; load() }

const num = (v) => (v === null || v === undefined || !Number.isFinite(Number(v)) ? '-' : Number(v).toLocaleString('zh-CN'))
// error_rate 单位后端未标注（当前恒 0）：≤1 视为比例，>1 视为已是百分数
const errRateText = computed(() => {
  const v = Number(d.value?.error_rate)
  if (!Number.isFinite(v)) return '-'
  return (v <= 1 ? v * 100 : v).toFixed(2) + '%'
})
const respText = computed(() => {
  const v = Number(d.value?.average_response_time)
  if (!Number.isFinite(v)) return '-'
  return v >= 1000 ? (v / 1000).toFixed(2) + 's' : Math.round(v) + 'ms'
})
</script>

<template>
  <div class="rcard">
    <div class="rhead">
      <span class="pulse" :class="{ off: frozen }"></span>
      <h3>实时指标</h3>
      <span class="hint">GET /admin/dashboard/realtime · {{ intervalSec }}s 自动刷新</span>
      <span v-if="frozen" class="tag">已暂停：{{ autoReason || '手动暂停' }}</span>
      <span v-else class="tag">{{ left }}s 后刷新</span>
      <div class="actions">
        <span v-if="updatedAt" class="hint">更新于 {{ updatedAt }}</span>
        <a-button size="small" :disabled="manualPause" @click="manualPause = !manualPause, autoReason = ''">
          <template #icon>
            <CaretRightOutlined v-if="manualPause" />
            <PauseOutlined v-else />
          </template>
          {{ manualPause ? '继续' : '暂停' }}
        </a-button>
        <a-button size="small" :loading="loading" @click="onManualRefresh">
          <template #icon><ReloadOutlined /></template>
          刷新
        </a-button>
      </div>
    </div>

    <div v-if="err && !d" class="empty">{{ err }}</div>
    <div v-else class="rgrid">
      <div class="mcard">
        <div class="mk">活跃请求</div>
        <div class="mv">{{ num(d?.active_requests) }}</div>
        <div class="mrow"><span>面板暂停时数值不再跳动</span></div>
      </div>
      <div class="mcard">
        <div class="mk">每分钟请求</div>
        <div class="mv">{{ num(d?.requests_per_minute) }}<i>RPM</i></div>
        <div class="mrow"><span>由后端采样窗口统计</span></div>
      </div>
      <div class="mcard">
        <div class="mk">错误率</div>
        <div class="mv">{{ errRateText }}</div>
        <div class="mrow"><span>接口原值 {{ d?.error_rate ?? '-' }}</span></div>
      </div>
      <div class="mcard">
        <div class="mk">平均响应时间</div>
        <div class="mv">{{ respText }}</div>
        <div class="mrow"><span>接口原值 {{ d?.average_response_time ?? '-' }}</span></div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.rcard { padding: 14px 16px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.rhead { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.rhead h3 { margin: 0; font-size: 13px; font-weight: 600; color: var(--text); }
.hint { font-size: 12px; color: var(--text2); }
.actions { margin-left: auto; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.tag { font-size: 11px; color: var(--text2); padding: 1px 6px; border-radius: 4px; border: 1px solid var(--hr); }
.pulse { width: 8px; height: 8px; border-radius: 50%; background: #3b82f6; animation: pul 2s ease-in-out infinite; flex: none; }
.pulse.off { background: #9ca3af; animation: none; }
@keyframes pul { 0%, 100% { opacity: 1 } 50% { opacity: 0.35 } }
.rgrid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; }
.mcard { padding: 12px 14px; border-radius: 10px; background: color-mix(in srgb, var(--theme-2) 6%, transparent); border: 1px solid var(--hr); }
.mk { font-size: 12px; color: var(--text2); }
.mv { margin-top: 4px; font-size: 24px; font-weight: 600; line-height: 1.2; color: var(--text); }
.mv i { margin-left: 4px; font-size: 11px; font-style: normal; font-weight: 400; color: var(--text2); }
.mrow { margin-top: 5px; font-size: 11px; color: var(--text2); }
.empty { font-size: 12px; color: var(--text2); }
/* 移动端：卡片单列 */
@media (max-width: 768px) {
  .rgrid { grid-template-columns: 1fr; }
}
</style>
