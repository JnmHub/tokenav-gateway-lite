<script setup>
/* 并发面板（二期）：平台/分组/账号/用户 4 视图切换 + 汇总卡 + 进度条 + 账号状态
 * 契约来源：旧 OpsConcurrencyCard.vue + frontend/src/api/admin/ops.ts
 * - GET /admin/ops/concurrency?platform&group_id → { enabled, platform{}, group{}, account{} }
 * - GET /admin/ops/user-concurrency → { enabled, user{} }
 * - GET /admin/ops/account-availability?platform&group_id → { enabled, platform{}, group{}, account{} }
 * 汇总行 = 并发(current_in_use/max_capacity/waiting_in_queue) + 可用性(total/available/rate_limit/error)
 * 账号状态字段：is_available/is_rate_limited(+rate_limit_remaining_sec)/is_overloaded(+overload_remaining_sec)/has_error(+error_message) */
import { ref, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined, UserOutlined, CloudOutlined, TeamOutlined, DatabaseOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const props = defineProps({
  platform: { type: String, default: '' },
  groupId: { type: [Number, null], default: null },
  refreshToken: { type: Number, default: 0 },
})

const loading = ref(false)
const concurrency = ref(null)
const availability = ref(null)
const userConcurrency = ref(null)
const dimension = ref('platform') // platform | group | account | user

const realtimeEnabled = computed(() => (concurrency.value?.enabled ?? true) && (availability.value?.enabled ?? true))
const safeNum = (n) => (typeof n === 'number' && Number.isFinite(n) ? n : 0)

const loadData = async () => {
  loading.value = true
  try {
    if (dimension.value === 'user') {
      userConcurrency.value = await request('/admin/ops/user-concurrency', { method: 'GET' })
    } else {
      const query = {}
      if (props.platform) query.platform = props.platform
      if (typeof props.groupId === 'number' && props.groupId > 0) query.group_id = props.groupId
      const [conc, avail] = await Promise.all([
        request('/admin/ops/concurrency', { method: 'GET', query }),
        request('/admin/ops/account-availability', { method: 'GET', query }),
      ])
      concurrency.value = conc; availability.value = avail
    }
  } catch (e) { message.error(`加载并发数据失败：${e.message}`) }
  finally { loading.value = false }
}

watch(() => props.refreshToken, () => { if (realtimeEnabled.value) loadData() })
watch(dimension, loadData)
watch(realtimeEnabled, (v) => { if (v) loadData() }, { immediate: true })

/* ---------- 汇总行（平台/分组）：并发表 + 可用性表按键合并 ---------- */
const summaryRows = computed(() => {
  if (dimension.value === 'user') return []
  const concMap = concurrency.value?.[dimension.value] || {}
  const availMap = availability.value?.[dimension.value] || {}
  const keys = new Set([...Object.keys(concMap), ...Object.keys(availMap)])
  const rows = []
  for (const k of keys) {
    const conc = concMap[k] || {}
    const avail = availMap[k] || {}
    // 分组视图按顶栏平台过滤；账号视图按顶栏分组过滤（旧实现同策略）
    if (dimension.value === 'group' && props.platform) {
      if (conc.platform !== props.platform && avail.platform !== props.platform) continue
    }
    if (dimension.value === 'account' && typeof props.groupId === 'number' && props.groupId > 0) {
      if (conc.group_id !== props.groupId && avail.group_id !== props.groupId) continue
    }
    const total = safeNum(avail.total_accounts), available = safeNum(avail.available_count)
    const cap = safeNum(conc.max_capacity), used = safeNum(conc.current_in_use)
    rows.push({
      key: k,
      name: dimension.value === 'platform' ? String(k).toUpperCase() : String(conc.group_name || avail.group_name || `Group ${k}`),
      platform: String(conc.platform || avail.platform || ''),
      total, available,
      rateLimited: safeNum(avail.rate_limit_count),
      errors: safeNum(avail.error_count),
      used, cap,
      waiting: safeNum(conc.waiting_in_queue),
      availPct: total > 0 ? Math.round((available / total) * 100) : 0,
      concPct: cap > 0 ? Math.round((used / cap) * 100) : 0,
    })
  }
  return rows.sort((a, b) => b.concPct - a.concPct)
})

/* ---------- 账号行（并发 + 状态合并，异常优先） ---------- */
const accountRows = computed(() => {
  if (dimension.value !== 'account') return []
  const concMap = concurrency.value?.account || {}
  const availMap = availability.value?.account || {}
  const keys = new Set([...Object.keys(concMap), ...Object.keys(availMap)])
  const rows = []
  for (const k of keys) {
    const conc = concMap[k] || {}
    const avail = availMap[k] || {}
    if (typeof props.groupId === 'number' && props.groupId > 0) {
      if (conc.group_id !== props.groupId && avail.group_id !== props.groupId) continue
    }
    rows.push({
      key: k,
      name: String(conc.account_name || avail.account_name || `Account ${k}`),
      platform: String(conc.platform || avail.platform || ''),
      groupName: String(conc.group_name || avail.group_name || ''),
      status: String(avail.status || ''),
      used: safeNum(conc.current_in_use), cap: safeNum(conc.max_capacity),
      waiting: safeNum(conc.waiting_in_queue),
      loadPct: safeNum(conc.load_percentage),
      isAvailable: !!avail.is_available,
      isRateLimited: !!avail.is_rate_limited,
      rlRemain: avail.rate_limit_remaining_sec,
      isOverloaded: !!avail.is_overloaded,
      olRemain: avail.overload_remaining_sec,
      hasError: !!avail.has_error,
      errMsg: avail.error_message || '',
    })
  }
  // 优先显示异常账号 → 限流 → 过载 → 按负载降序（旧实现同策略）
  return rows.sort((a, b) => {
    if (a.hasError !== b.hasError) return a.hasError ? -1 : 1
    if (a.isRateLimited !== b.isRateLimited) return a.isRateLimited ? -1 : 1
    return b.loadPct - a.loadPct
  })
})

/* ---------- 用户行 ---------- */
const userRows = computed(() => {
  if (dimension.value !== 'user') return []
  const userMap = userConcurrency.value?.user || {}
  return Object.keys(userMap).map((k) => {
    const u = userMap[k] || {}
    return {
      key: k,
      email: u.user_email || `User ${k}`,
      username: u.username || '',
      used: safeNum(u.current_in_use), cap: safeNum(u.max_capacity),
      waiting: safeNum(u.waiting_in_queue),
      loadPct: safeNum(u.load_percentage),
    }
  }).sort((a, b) => b.used - a.used || b.loadPct - a.loadPct)
})

const DIM_TABS = [
  { value: 'platform', label: '按平台', icon: CloudOutlined },
  { value: 'group', label: '按分组', icon: TeamOutlined },
  { value: 'account', label: '按账号', icon: DatabaseOutlined },
  { value: 'user', label: '按用户', icon: UserOutlined },
]
const DIM_TITLE = { platform: '按平台', group: '按分组', account: '按账号', user: '按用户' }

const loadColor = (p) => (p >= 90 ? '#dc2626' : p >= 70 ? '#ea580c' : p >= 50 ? '#ca8a04' : '#16a34a')
const fmtSec = (s) => {
  if (!(s > 0)) return '0s'
  if (s < 60) return `${Math.round(s)}s`
  const m = Math.floor(s / 60); if (m < 60) return `${m}m`
  return `${Math.floor(m / 60)}h`
}
const accStatus = (r) => {
  if (r.hasError) return { text: '异常', color: 'red' }
  if (r.isRateLimited) return { text: `限流 ${fmtSec(r.rlRemain)}`, color: 'orange' }
  if (r.isOverloaded) return { text: `过载 ${fmtSec(r.olRemain)}`, color: 'orange' }
  if (r.isAvailable) return { text: '可用', color: 'green' }
  return { text: '不可用', color: 'default' }
}
</script>

<template>
  <div class="card">
    <div class="card-h">
      <div>
        <div class="sec-t">并发 / 排队</div>
        <div class="sub-t">实时并发占用、排队深度与账号可用性</div>
      </div>
      <div class="h-btns">
        <a-radio-group v-model:value="dimension" size="small" button-style="solid">
          <a-radio-button v-for="t in DIM_TABS" :key="t.value" :value="t.value"><component :is="t.icon" />{{ t.label }}</a-radio-button>
        </a-radio-group>
        <a-button size="small" :loading="loading" @click="loadData"><template #icon><ReloadOutlined /></template></a-button>
      </div>
    </div>

    <div v-if="!realtimeEnabled" class="empty">已在设置中关闭实时监控。</div>

    <template v-else>
      <div class="dim-title">{{ DIM_TITLE[dimension] }} <span class="dim">共 {{ summaryRows.length || accountRows.length || userRows.length }} 项</span></div>

      <!-- 平台/分组：汇总卡 + 双进度条 -->
      <div v-if="['platform', 'group'].includes(dimension)">
        <div v-if="!summaryRows.length" class="empty">暂无数据</div>
        <div v-else class="sum-grid">
          <div v-for="r in summaryRows" :key="r.key" class="sum-card">
            <div class="sum-name">{{ r.name }} <a-tag v-if="r.platform" style="margin: 0" color="blue">{{ r.platform }}</a-tag></div>
            <div class="sum-nums">
              <span>并发 <b :style="{ color: loadColor(r.concPct) }">{{ r.used }}</b>/{{ r.cap }}</span>
              <span>排队 <b>{{ r.waiting }}</b></span>
            </div>
            <div class="pbar"><div class="pbar-in" :style="{ width: Math.min(100, Math.max(0, r.concPct)) + '%', background: loadColor(r.concPct) }" /></div>
            <div class="sum-nums">
              <span>账号 <b :style="{ color: loadColor(r.availPct) }">{{ r.available }}</b>/{{ r.total }} 可用</span>
              <span class="dim">限流 {{ r.rateLimited }} · 异常 {{ r.errors }}</span>
            </div>
            <div class="pbar"><div class="pbar-in" :style="{ width: Math.min(100, Math.max(0, r.availPct)) + '%', background: loadColor(r.availPct) }" /></div>
          </div>
        </div>
      </div>

      <!-- 账号：状态卡列表 -->
      <div v-else-if="dimension === 'account'">
        <div v-if="!accountRows.length" class="empty">暂无数据</div>
        <div v-else class="acc-grid">
          <div v-for="r in accountRows" :key="r.key" class="acc-card">
            <div class="acc-top">
              <span class="acc-name" :title="r.name">{{ r.name }}</span>
              <a-tag :color="accStatus(r).color" style="margin: 0">{{ accStatus(r).text }}</a-tag>
            </div>
            <div class="acc-meta">
              <a-tag style="margin: 0" color="blue">{{ r.platform || '-' }}</a-tag>
              <span class="dim">{{ r.groupName || '-' }}</span>
              <span class="dim">{{ r.status || '' }}</span>
            </div>
            <div class="sum-nums">
              <span>并发 <b :style="{ color: loadColor(r.loadPct) }">{{ r.used }}</b>/{{ r.cap }}</span>
              <span class="dim">队列 {{ r.waiting }}</span>
            </div>
            <div class="pbar"><div class="pbar-in" :style="{ width: Math.min(100, Math.max(0, r.loadPct)) + '%', background: loadColor(r.loadPct) }" /></div>
            <div v-if="r.errMsg" class="acc-err" :title="r.errMsg">{{ r.errMsg }}</div>
          </div>
        </div>
      </div>

      <!-- 用户：简表 -->
      <template v-else>
        <div v-if="!userRows.length" class="empty">暂无数据</div>
        <a-table v-else :columns="[
            { title: '用户', key: 'u' },
            { title: '并发', key: 'c', width: 110 },
            { title: '负载', key: 'l', width: 180 },
            { title: '队列', key: 'w', width: 70 },
          ]" :data-source="userRows" row-key="key" size="small" :pagination="false" :scroll="{ y: 300 }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'u'">
              <div class="r-name">{{ record.email }}</div>
              <div v-if="record.username" class="dim">{{ record.username }}</div>
            </template>
            <template v-else-if="column.key === 'c'">
              <b :style="{ color: loadColor(record.loadPct) }">{{ record.used }}</b> / {{ record.cap }}
            </template>
            <template v-else-if="column.key === 'l'">
              <div class="pbar"><div class="pbar-in" :style="{ width: Math.min(100, Math.max(0, record.loadPct)) + '%', background: loadColor(record.loadPct) }" /></div>
            </template>
            <template v-else-if="column.key === 'w'"><span class="dim">{{ record.waiting }}</span></template>
          </template>
        </a-table>
      </template>
    </template>
  </div>
</template>

<style scoped>
.card { background: var(--card); border: 1px solid var(--hr); border-radius: 10px; padding: 12px 14px; min-width: 0; }
.card-h { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.h-btns { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
.dim-title { font-size: 12px; color: var(--text2); margin-bottom: 8px; }
.dim { font-size: 11px; color: var(--text2); }
.r-name { font-size: 12px; font-weight: 500; word-break: break-all; }
.empty { padding: 24px 0; text-align: center; font-size: 12px; color: var(--text2); }
.sum-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); gap: 8px; }
.sum-card { border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px; }
.sum-name { font-size: 12px; font-weight: 500; display: flex; align-items: center; gap: 6px; margin-bottom: 6px; word-break: break-all; }
.sum-nums { display: flex; justify-content: space-between; gap: 8px; font-size: 11px; color: var(--text2); margin: 4px 0; }
.pbar { height: 4px; border-radius: 2px; background: rgba(127, 127, 127, 0.15); overflow: hidden; }
.pbar-in { height: 100%; border-radius: 2px; transition: width 0.3s ease; }
.acc-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 8px; }
.acc-card { border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px; }
.acc-top { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.acc-name { font-size: 12px; font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.acc-meta { display: flex; align-items: center; gap: 6px; margin: 4px 0 2px; flex-wrap: wrap; }
.acc-err { font-size: 11px; color: #dc2626; margin-top: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
