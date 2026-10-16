<script setup>
/* WebSocket 实时 QPS/TPS 流面板
 * 契约来源：internal/handler/admin/ops_ws_handler.go
 * - 连接 ws(s)://<host>/api/v1/admin/ops/ws/qps
 * - 鉴权：浏览器 WS 无法自定义 Authorization 头，走子协议握手：
 *   new WebSocket(url, ['sub2api-admin', `jwt.${token}`])（服务端只回显 sub2api-admin）
 * - 推送帧：{ type: 'qps_update', timestamp, data: { qps, tps, request_count } }，每 2s 一帧
 * - 服务端在「实时监控」关闭时会先完成 upgrade、再以关闭码 4001（realtime_disabled）关闭：
 *   识别为「实时监控已关闭」并停止重连；其余断开按指数退避（1s 起、上限 30s、带抖动）重连
 * - 组件卸载 / 窗口关闭时必须 cleanUp（clearTimeout + ws.close），防止后台连接泄漏 */
import { ref, onMounted, onUnmounted } from 'vue'
import { DisconnectOutlined, ThunderboltOutlined } from '@ant-design/icons-vue'
import { getToken } from '../../api'

const status = ref('idle') // idle | connecting | live | disabled | retrying
const disabled = ref(false) // 4001 realtime_disabled：提示并永久停止重连
const payload = ref(null) // 最近一帧 { qps, tps, request_count }
const lastAt = ref('')

let ws = null
let retryTimer = null
let attempts = 0
let alive = false // 组件存活标记：卸载后拒绝一切重连/回调

/* 指数退避：1s、2s、4s…上限 30s，加 0~30% 抖动避免多个窗口同时撞服务端 */
const backoffDelay = () => {
  const base = Math.min(30000, 1000 * 2 ** Math.min(attempts, 5))
  return base * (1 + Math.random() * 0.3)
}

const cleanUp = () => {
  if (retryTimer) { clearTimeout(retryTimer); retryTimer = null }
  if (ws) {
    try { ws.close() } catch { /* 已关闭 */ }
    ws = null
  }
}

const scheduleRetry = () => {
  if (!alive || disabled.value) return
  status.value = 'retrying'
  retryTimer = setTimeout(() => { retryTimer = null; connect() }, backoffDelay())
}

const connect = () => {
  if (!alive || disabled.value || ws) return
  const token = getToken()
  if (!token) { status.value = 'idle'; return }
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${proto}//${location.host}/api/v1/admin/ops/ws/qps`
  try {
    ws = new WebSocket(url, ['sub2api-admin', `jwt.${token}`])
  } catch {
    ws = null
    scheduleRetry()
    return
  }
  status.value = 'connecting'

  ws.onopen = () => {
    attempts = 0 // 连上即重置退避计数
    if (alive && !disabled.value) status.value = 'live'
  }
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      if (msg && msg.type === 'qps_update' && msg.data) {
        payload.value = msg.data
        lastAt.value = String(msg.timestamp || '').slice(11, 19)
      }
    } catch { /* 非 JSON 帧直接忽略 */ }
  }
  ws.onclose = (ev) => {
    ws = null
    if (ev.code === 4001) {
      // realtime_disabled：服务端主动告知实时监控已关闭，显示提示并停止重连
      disabled.value = true; status.value = 'disabled'
      cleanUp()
      return
    }
    if (!alive) return
    attempts += 1
    scheduleRetry()
  }
  ws.onerror = () => { /* 错误后必走 onclose，重连逻辑统一在 onclose 处理 */ }
}

const f1 = (v) => (typeof v === 'number' && Number.isFinite(v) ? v.toFixed(1) : '-')
const fmt = (v) => (typeof v === 'number' && Number.isFinite(v) ? v.toLocaleString('zh-CN') : '-')

onMounted(() => { alive = true; connect() })
onUnmounted(() => { alive = false; cleanUp() })
</script>

<template>
  <div class="rcard">
    <div class="rhead">
      <div class="rtitle"><ThunderboltOutlined />实时推送 · WebSocket</div>
      <span class="stag" :class="status">
        {{ { idle: '未连接', connecting: '连接中', live: '推送中', retrying: '重连中', disabled: '已关闭' }[status] }}
      </span>
    </div>

    <template v-if="disabled">
      <div class="off">
        <DisconnectOutlined class="off-ic" />
        <div class="off-t">实时监控已关闭</div>
        <div class="off-s">服务端已停止推送（realtime_disabled），请在运维设置中开启实时监控</div>
      </div>
    </template>

    <template v-else>
      <div class="rbody">
        <div class="rcur">
          <div class="rk">当前（近 1 分钟）</div>
          <div class="rnums">
            <span class="rn">{{ f1(payload?.qps) }}<i>QPS</i></span>
            <span class="rn">{{ f1(payload?.tps) }}<i>TPS</i></span>
          </div>
        </div>
        <div class="rsplit"></div>
        <div class="rblock">
          <div class="rk">窗口请求数</div>
          <div class="rline">{{ fmt(payload?.request_count) }}</div>
          <div class="rk last">最近推送 {{ lastAt || '--:--:--' }}</div>
        </div>
      </div>
      <div v-if="!payload" class="wait">{{ status === 'live' ? '等待第一帧推送…' : '正在建立 WebSocket 连接…' }}</div>
    </template>
  </div>
</template>

<style scoped>
.rcard { padding: 12px 16px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); height: 100%; display: flex; flex-direction: column; min-width: 0; }
.rhead { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.rtitle { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text2); font-weight: 500; }
.stag { font-size: 11px; border-radius: 4px; padding: 1px 8px; background: rgba(127, 127, 127, 0.1); color: var(--text2); }
.stag.live { background: rgba(22, 163, 74, 0.12); color: #15803d; }
.stag.retrying, .stag.connecting { background: rgba(234, 179, 8, 0.12); color: #b45309; }
.stag.disabled { background: rgba(225, 29, 72, 0.1); color: #be123c; }
.rbody { margin-top: 10px; display: flex; gap: 16px; flex: 1; align-items: center; flex-wrap: wrap; }
.rk { font-size: 11px; color: var(--text2); }
.rk.last { margin-top: 8px; }
.rnums { display: flex; gap: 18px; margin-top: 2px; }
.rn { font-size: 26px; font-weight: 600; line-height: 1.1; }
.rn i { margin-left: 4px; font-size: 11px; font-style: normal; color: var(--text2); }
.rsplit { width: 1px; align-self: stretch; background: var(--hr); }
.rline { font-size: 15px; font-weight: 600; margin-top: 4px; }
.wait { margin-top: 8px; font-size: 11px; color: var(--text2); }
.off { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 4px; padding: 18px 0; }
.off-ic { font-size: 22px; color: var(--text2); }
.off-t { font-size: 13px; font-weight: 500; }
.off-s { font-size: 11px; color: var(--text2); text-align: center; }
</style>
