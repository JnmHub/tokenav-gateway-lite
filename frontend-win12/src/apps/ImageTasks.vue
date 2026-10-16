<script setup>
/* ============================================================
 * 镜像任务（异步图像生成任务）完整版
 * 旧实现契约：frontend/src/views/admin/ImageTasksView.vue +
 *   image-tasks/ImageTaskConfigDialog.vue + api/admin/imageTasks.ts
 * 接口（全部已对照后端 handler 与真实响应核实）：
 *   GET    /admin/image-tasks?state&limit&cursor → {items, next_cursor}
 *   GET    /admin/image-tasks/:id                → {task}
 *   POST   /admin/image-tasks/:id/replay         body {confirm:'REPLAY_IMAGE_TASK'} → {task}
 *   POST   /admin/image-tasks/cleanup?limit      body {confirm:'CLEANUP_IMAGE_TASKS'} → {removed}
 *   GET    /admin/image-tasks/metrics            → {metrics, config(时间字段为纳秒)}
 *   GET/PUT /admin/image-tasks/config            → {revision, desired, effective_enabled, ...}
 *   POST   /admin/system/restart                 保存后重启网关生效
 * 重启流程：保存成功且 restart_required 时自动重启；恢复探测直接轮询
 *   /admin/image-tasks/config（走 /api 代理，dev 与 embed 都通；/health 在
 *   dev 代理下不可达），必须确认新进程已加载目标配置才算恢复。
 * ============================================================ */
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { request } from '../api'
import { message, Modal } from 'ant-design-vue'
import ImageTaskConfigModal from '../components/imagetask/ConfigModal.vue'
import ImageTaskArtifactPreview from '../components/imagetask/ArtifactPreview.vue'
import { confirmAction } from '../utils/confirmAction'

/* ---------- 列表状态 ---------- */
const loading = ref(false)
const loadingMore = ref(false)
const actionLoading = ref(false)
const err = ref('')
const okMsg = ref('')
const rows = ref([])
const stateFilter = ref('')
const nextCursor = ref('')
const autoRefresh = ref(true)
const runtime = ref(null)          // /metrics 响应（队列未运行时为 null）
const configState = ref(null)      // /config 响应（desired + effective）
const capabilities = ref(null)   // /capabilities 响应 {schema_version, enabled}：判断运行时的权威来源
let refreshTimer = undefined

/* ---------- 详情 drawer ---------- */
const drawerOpen = ref(false)
const selected = ref(null)

/* ---------- 配置弹窗（逻辑在 components/imagetask/ConfigModal.vue） ---------- */
const cfgOpen = ref(false)
const cfgRef = ref(null)
/* 横幅状态由子组件 emit 同步：保存/重启流程的提示与重启中标志 */
const serviceRestarting = ref(false)
const restartErrMsg = ref('')
const restartDegraded = ref(false)
const onCfgState = (s) => { if (s) configState.value = s }
const onCfgMessage = (m) => { okMsg.value = m.ok; restartErrMsg.value = m.err; restartDegraded.value = m.degraded }

/* 窄屏适配：详情 drawer 宽度 */
const narrow = ref(typeof window !== 'undefined' && window.innerWidth < 640)
const onResize = () => { narrow.value = window.innerWidth < 640 }

/* 状态枚举（后端 ImageTaskState：queued/running/succeeded/failed/cancelled/expired） */
const STATE_OPTIONS = [
  { value: '', label: '全部' },
  { value: 'queued', label: '排队' },
  { value: 'running', label: '执行中' },
  { value: 'succeeded', label: '成功' },
  { value: 'failed', label: '失败' },
  { value: 'cancelled', label: '已取消' },
  { value: 'expired', label: '已过期' },
]
const stateLabel = (s) => STATE_OPTIONS.find((i) => i.value === s)?.label || s
const stateTone = (s) => ({ queued: 'default', running: 'processing', succeeded: 'success', failed: 'error', cancelled: 'warning', expired: 'default' }[s] || 'default')
const settleLabel = (s) => (s === 'settled' ? '已结算' : s === 'released' ? '已释放' : '待结算')
const canReplay = (t) => t?.state === 'failed' || t?.state === 'expired'
/* 仅 queued/running 可取消（后端 Cancel 对其它终态报错）；cancel_requested 为已发出取消 */
const canCancel = (t) => (t?.state === 'queued' || t?.state === 'running') && !t?.cancel_requested

const columns = [
  { title: '任务 / 模型', key: 'task', width: 260 },
  { title: '状态', key: 'state', width: 88 },
  { title: '进度', key: 'progress', width: 70 },
  { title: '金额', key: 'amount', width: 100 },
  { title: '用户', key: 'user', width: 70 },
  { title: '更新时间', key: 'updated', width: 160 },
]

/* ---------- 展示格式化（照旧实现） ---------- */
const fmt = (n) => (n === null || n === undefined ? '-' : Number(n).toLocaleString('zh-CN'))
const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '—')
const shortID = (id) => (id && id.length > 18 ? `${id.slice(0, 8)}…${id.slice(-6)}` : id || '-')
/* 金额 toFixed(4) 去尾零；released 的实扣可以为 0，不能用 truthy 回退误显示预留额 */
const money = (v) => Number(v || 0).toFixed(4).replace(/0+$/, '').replace(/\.$/, '') || '0'
const displayAmount = (t) => (t.settlement_state === 'pending' ? t.reserved_amount : t.actual_amount)
const bytes = (v) => { const s = Number(v || 0); if (s < 1024) return `${s} B`; if (s < 1048576) return `${(s / 1024).toFixed(1)} KiB`; return `${(s / 1048576).toFixed(1)} MiB` }
/* metrics.config 的时间字段为纳秒 */
const duration = (ns) => { const h = Number(ns || 0) / 3.6e12; if (h >= 24) return `${Math.round(h / 24)} 天`; if (h >= 1) return `${Math.round(h)} 小时`; return `${Math.max(1, Math.round(h * 60))} 分钟` }

const queueRunning = computed(() => Boolean(configState.value?.effective_enabled))
const queueStatus = computed(() => {
  if (serviceRestarting.value) return { label: '正在重启', color: 'warning' }
  const c = configState.value
  if (!c) return { label: '状态未知', color: 'default' }
  if (c.restart_required && c.desired.enabled !== c.effective_enabled) {
    return c.desired.enabled ? { label: '待重启开启', color: 'warning' } : { label: '待重启关闭', color: 'warning' }
  }
  return c.effective_enabled ? { label: '队列运行中', color: 'success' } : { label: '队列已关闭', color: 'default' }
})
/* 运行时权威徽标：capabilities.enabled === false 明确显示未启用；接口未返回前不显示 */
const runtimeTag = computed(() => {
  if (!capabilities.value) return null
  return capabilities.value.enabled
    ? { label: '图像任务运行时：已启用', color: 'success' }
    : { label: '图像任务运行时：未启用', color: 'default' }
})

const metricCards = computed(() => runtime.value ? [
  { label: '排队任务', value: fmt(runtime.value.metrics.queue_depth), hint: `容量 ${fmt(runtime.value.metrics.queue_capacity)}` },
  { label: '正在执行', value: fmt(runtime.value.metrics.running_workers), hint: `工作线程 ${fmt(runtime.value.metrics.workers)}` },
  { label: '单用户并发', value: fmt(runtime.value.config.per_user_concurrency), hint: `单任务最多 ${fmt(runtime.value.config.max_images_per_task)} 张` },
  { label: '结果保留', value: duration(runtime.value.config.result_retention), hint: `单图上限 ${bytes(runtime.value.config.max_artifact_bytes)}` },
] : [])

/* ============================================================
 * 数据加载：队列关闭时不查询任务接口（后端会返回 503，属正常关闭
 * 而非故障，照旧实现直接清空本地数据避免误报）
 * ============================================================ */
async function refresh(silent = false) {
  if (!silent) loading.value = true
  err.value = ''
  try {
    const cfg = await request('/admin/image-tasks/config', { method: 'GET' })
    configState.value = cfg
    // 运行时能力（GET /admin/image-tasks/capabilities）与队列开关相互独立：
    // 队列关闭时也要取一次，顶部「运行时徽标」与取消按钮的禁用判定才有权威依据。
    // 失败不阻塞页面（能力查询是增强信息，不是任务列表的依赖）。
    try { capabilities.value = await request('/admin/image-tasks/capabilities', { method: 'GET' }) }
    catch { /* 忽略：徽标退化为不显示 */ }
    if (!cfg.effective_enabled) {
      runtime.value = null; rows.value = []; nextCursor.value = ''
      if (selected.value) { selected.value = null; drawerOpen.value = false }
      return
    }
    const [mtr, list] = await Promise.all([
      request('/admin/image-tasks/metrics', { method: 'GET' }),
      request('/admin/image-tasks', { method: 'GET', query: { state: stateFilter.value || undefined, limit: 50 } }),
    ])
    runtime.value = mtr
    rows.value = list.items || []
    nextCursor.value = list.next_cursor || ''
    // 保持详情与列表同步（任务被重放/结算后状态会变）
    if (selected.value) selected.value = rows.value.find((i) => i.id === selected.value?.id) || selected.value
  } catch (e) {
    err.value = e.message
    if (!silent) rows.value = []
  } finally {
    loading.value = false
  }
}

/* 游标分页：next_cursor 翻页，按 id 去重防止轮询间隙重复追加 */
async function loadMore() {
  if (!nextCursor.value || loadingMore.value) return
  loadingMore.value = true
  try {
    const r = await request('/admin/image-tasks', { method: 'GET', query: { state: stateFilter.value || undefined, limit: 50, cursor: nextCursor.value } })
    const known = new Set(rows.value.map((i) => i.id))
    rows.value.push(...(r.items || []).filter((i) => !known.has(i.id)))
    nextCursor.value = r.next_cursor || ''
  } catch (e) { message.error(`加载更多失败：${e.message}`) } finally { loadingMore.value = false }
}

function selectState(v) { if (stateFilter.value === v) return; stateFilter.value = v; selected.value = null; drawerOpen.value = false; nextCursor.value = ''; refresh() }

/* ---------- 详情：先展示列表行，再拉全量（artifacts 等完整字段） ---------- */
async function openDetail(task) {
  selected.value = task
  drawerOpen.value = true
  try { selected.value = (await request(`/admin/image-tasks/${encodeURIComponent(task.id)}`, { method: 'GET' })).task } catch (e) { message.error(`任务详情读取失败：${e.message}`) }
}

/* ---------- 回放：仅 failed/expired；已成功图片只重试结算不重新生成 ---------- */
function replay(task) {
  Modal.confirm({
    title: '重放失败任务',
    content: '确认重放这个失败任务？已成功的图片只会重试结算，不会再次生成。',
    okText: '确认重放', cancelText: '取消',
    onOk: async () => {
      actionLoading.value = true
      try {
        selected.value = (await request(`/admin/image-tasks/${encodeURIComponent(task.id)}/replay`, { method: 'POST', body: { confirm: 'REPLAY_IMAGE_TASK' } })).task
        message.success('任务已重新进入队列')
        await refresh(true)
      } catch (e) { message.error(`任务重放失败：${e.message}`) } finally { actionLoading.value = false }
    },
  })
}

/* ---------- 取消任务：破坏性操作，confirmAction 二次确认后 POST :id/cancel ---------- */
async function cancelTask(task) {
  const yes = await confirmAction({
    title: '取消图像任务',
    content: `确认取消任务 ${task.id}？未产出的图片将不会继续生成，该操作不可恢复。`,
    okText: '确认取消', cancelText: '再想想', danger: true,
  })
  if (!yes) return
  actionLoading.value = true
  try {
    const r = await request(`/admin/image-tasks/${encodeURIComponent(task.id)}/cancel`, { method: 'POST' })
    message.success('任务已取消')
    if (selected.value?.id === task.id && r?.task) selected.value = r.task
    await refresh(true)
  } catch (e) {
    message.error(`任务取消失败：${e.message}`)
  } finally {
    actionLoading.value = false
  }
}

/* ---------- 清理过期：删除已过期且完成结算的任务与产物 ---------- */
function cleanup() {
  Modal.confirm({
    title: '清理过期任务',
    content: '确认清理已经过期且完成结算的任务与产物？该操作不可恢复。',
    okText: '确认清理', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      actionLoading.value = true
      try {
        const r = await request('/admin/image-tasks/cleanup', { method: 'POST', query: { limit: 100 }, body: { confirm: 'CLEANUP_IMAGE_TASKS' } })
        message.success(`已清理 ${r.removed} 条过期任务`)
        await refresh(true)
      } catch (e) { message.error(`过期任务清理失败：${e.message}`) } finally { actionLoading.value = false }
    },
  })
}

/* ---------- 自动刷新：5 秒；页面隐藏/操作中/重启中时跳过 ---------- */
function resetTimer() {
  if (refreshTimer) clearInterval(refreshTimer)
  refreshTimer = undefined
  if (autoRefresh.value && queueRunning.value && !serviceRestarting.value) {
    refreshTimer = setInterval(() => {
      if (!document.hidden && !loading.value && !actionLoading.value && !serviceRestarting.value && !cfgOpen.value) void refresh(true)
    }, 5000)
  }
}
watch([autoRefresh, queueRunning, serviceRestarting], resetTimer)

onMounted(() => { window.addEventListener('resize', onResize); void refresh(); resetTimer() })
onBeforeUnmount(() => { window.removeEventListener('resize', onResize); if (refreshTimer) clearInterval(refreshTimer) })
</script>

<template>
  <div class="itask">
    <!-- 工具栏：操作组 | 状态组 | 刷新组 -->
    <div class="toolbar">
      <a-button type="primary" @click="cfgOpen = true" :disabled="serviceRestarting">配置</a-button>
      <a-button danger :disabled="actionLoading || serviceRestarting || !runtime?.config.enabled" @click="cleanup">清理过期</a-button>
      <span class="divider" />
      <a-select v-model:value="stateFilter" style="width: 120px" :options="STATE_OPTIONS" :disabled="!queueRunning" @change="selectState" />
      <span class="divider" />
      <span class="switem" :class="{ off: !queueRunning }">
        <a-switch v-model:checked="autoRefresh" size="small" :disabled="!queueRunning" />
        <span>5 秒自动刷新</span>
      </span>
      <a-button :loading="loading" :disabled="serviceRestarting" @click="refresh(false)">{{ loading ? '刷新中' : '刷新' }}</a-button>
      <a-tag :color="queueStatus.color" class="qtag">{{ queueStatus.label }}</a-tag>
      <a-tag v-if="runtimeTag" :color="runtimeTag.color" class="qtag">{{ runtimeTag.label }}</a-tag>
      <span class="grow" />
      <a-button v-if="err" danger size="small" @click="refresh(false)">加载失败，点击重试</a-button>
    </div>

    <!-- 队列关闭提示：desired 已开启但等待重启 -->
    <a-alert
      v-if="configState && !queueRunning" type="warning" show-icon class="band"
      :message="configState.desired.enabled && configState.restart_required ? '配置已开启，正在等待重启' : '镜像任务当前未启用'"
      :description="configState.desired.enabled && configState.restart_required
        ? '配置已经写入文件；重启 tokenav gateway 后才会创建 worker（工作线程）并开始消费队列。普通同步生图仍可照常使用。'
        : '普通同步生图仍按原路径工作。点击「配置」即可选择本机或 S3 / MinIO 存储、运行规模和保留时间。'"
    >
      <template #action>
        <a-button type="primary" size="small" :disabled="serviceRestarting" @click="cfgOpen = true">立即配置</a-button>
      </template>
    </a-alert>

    <!-- 重启中 -->
    <a-alert v-if="serviceRestarting" type="info" show-icon class="band" message="配置已保存，tokenav gateway 正在重启"
      description="页面会自动等待服务恢复并核对实际生效状态，不需要手动刷新。" />

    <!-- 重启失败 / 降级 -->
    <a-alert v-if="restartErrMsg" type="error" show-icon class="band" message="自动重启未完成" :description="restartErrMsg">
      <template #action>
        <a-button size="small" :disabled="serviceRestarting" @click="cfgRef?.retry()">{{ restartDegraded ? '返回配置' : '重新启动并检查' }}</a-button>
      </template>
    </a-alert>

    <a-alert v-if="okMsg" type="success" show-icon class="band" :message="okMsg" />

    <!-- 指标卡 -->
    <div class="stats" v-if="metricCards.length">
      <div v-for="m in metricCards" :key="m.label" class="scard">
        <div class="k">{{ m.label }}</div>
        <div class="v">{{ m.value }}</div>
        <div class="h">{{ m.hint }}</div>
      </div>
    </div>

    <!-- 任务流水 -->
    <a-table
      :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="small"
      :scroll="{ x: 'max-content' }" :pagination="false" :row-class-name="() => 'clickable'"
      :custom-row="(r) => ({ onClick: () => openDetail(r) })"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'task'">
          <div class="cellTask">
            <span class="tid" :title="record.id">{{ shortID(record.id) }}</span>
            <span class="tmodel">{{ record.model }} · {{ record.image_count }} 张</span>
          </div>
        </template>
        <template v-else-if="column.key === 'state'">
          <a-tag :color="stateTone(record.state)" class="stag">{{ stateLabel(record.state) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'progress'">{{ record.progress }}%</template>
        <template v-else-if="column.key === 'amount'">
          <a-tooltip :title="`预留 $${money(record.reserved_amount)} / 实扣 $${money(record.actual_amount)} · ${settleLabel(record.settlement_state)}`">
            <span>${{ money(displayAmount(record)) }}</span>
          </a-tooltip>
        </template>
        <template v-else-if="column.key === 'user'">{{ record.user_id ?? '-' }}</template>
        <template v-else-if="column.key === 'updated'">{{ fmtTime(record.updated_at) }}</template>
      </template>
      <template #emptyText>
        <div class="empty">
          <p class="et">{{ err ? '任务队列读取失败' : queueRunning ? '暂无符合条件的任务' : '任务队列尚未运行' }}</p>
          <p class="ed">{{ err ? err : queueRunning ? '客户端使用 Prefer: respond-async（异步响应头）提交生图请求后，任务会出现在这里。' : '关闭状态不会查询任务接口，配置并重启 tokenav gateway 后任务流水会自动显示。' }}</p>
        </div>
      </template>
    </a-table>

    <div v-if="nextCursor" class="more">
      <a-button size="small" :loading="loadingMore" @click="loadMore">加载更多</a-button>
    </div>

    <!-- 详情 drawer：结算与错误详情、产物下载 -->
    <a-drawer v-model:open="drawerOpen" title="任务详情" :width="narrow ? '100%' : 460" placement="right">
      <template v-if="selected">
        <div class="dhead">
          <span class="tid" :title="selected.id">{{ shortID(selected.id) }}</span>
          <div class="dops">
            <a-tooltip v-if="capabilities && !capabilities.enabled" title="图像任务运行时未启用，无法取消任务">
              <a-button size="small" danger disabled>取消任务</a-button>
            </a-tooltip>
            <a-button
              v-else-if="canCancel(selected)" size="small" danger :loading="actionLoading"
              @click="cancelTask(selected)"
            >取消任务</a-button>
            <span v-if="selected.cancel_requested && (selected.state === 'queued' || selected.state === 'running')" class="dcancel-tip">已请求取消，等待执行线程响应</span>
            <a-button v-if="canReplay(selected)" size="small" :loading="actionLoading" @click="replay(selected)">重放</a-button>
          </div>
        </div>
        <div class="dgrid">
          <div class="drow"><span class="dl">模型</span><span class="dv">{{ selected.model }}</span></div>
          <div class="drow"><span class="dl">任务状态</span><span class="dv"><a-tag :color="stateTone(selected.state)" class="stag">{{ stateLabel(selected.state) }}</a-tag></span></div>
          <div class="drow"><span class="dl">结算状态</span><span class="dv">{{ settleLabel(selected.settlement_state) }}</span></div>
          <div class="drow"><span class="dl">进度</span><span class="dv">{{ selected.progress }}%</span></div>
          <div class="drow"><span class="dl">执行次数</span><span class="dv">{{ selected.attempt }} / {{ selected.max_attempts }}</span></div>
          <div class="drow"><span class="dl">预留 / 实扣</span><span class="dv">${{ money(selected.reserved_amount) }} / ${{ money(selected.actual_amount) }}</span></div>
          <div class="drow"><span class="dl">用户</span><span class="dv">{{ selected.user_id ?? '-' }}</span></div>
          <div class="drow"><span class="dl">创建时间</span><span class="dv">{{ fmtTime(selected.created_at) }}</span></div>
          <div class="drow"><span class="dl">完成时间</span><span class="dv">{{ selected.completed_at ? fmtTime(selected.completed_at) : '—' }}</span></div>
        </div>
        <a-alert v-if="selected.error_message || selected.error_code" type="error" show-icon class="band"
          :message="selected.error_code || '执行失败'" :description="selected.error_message || '请检查上游账号和任务日志。'" />
        <template v-if="selected.artifacts?.length">
          <div class="dsec">产物</div>
          <ImageTaskArtifactPreview :key="selected.id" :task-id="selected.id" :artifacts="selected.artifacts" />
          <div v-for="(a, i) in selected.artifacts" :key="a.sha256 + i" class="art">
            <div class="artname" :title="a.file_name">{{ a.file_name }}</div>
            <div class="artmeta">
              {{ bytes(a.size_bytes) }} · {{ a.content_type }}
              <a v-if="a.download_url" :href="a.download_url" target="_blank" rel="noopener" class="artdl">下载</a>
            </div>
          </div>
        </template>
      </template>
    </a-drawer>

    <!-- 配置弹窗：配置读取/编辑/保存/重启流程全部在 ConfigModal 内 -->
    <ImageTaskConfigModal ref="cfgRef" v-model:open="cfgOpen" @state="onCfgState" @restarting="serviceRestarting = $event" @message="onCfgMessage" @changed="refresh(true)" />
  </div>
</template>

<style scoped>
.itask { height: 100%; display: flex; flex-direction: column; overflow-y: auto; }

/* 工具栏：操作组 | 状态组 | 刷新组，组间 16px */
.toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; }
.toolbar > :nth-child(4) { margin-left: 8px; }
.divider { width: 1px; height: 18px; background: var(--hr); margin: 0 8px; }
.grow { flex: 1; }
.switem { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text2); height: 32px; }
.switem.off { opacity: 0.5; }
.qtag { margin-inline-end: 0; }

.band { margin-bottom: 10px; }

/* 指标卡 */
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 8px; margin-bottom: 12px; }
.scard { padding: 12px 14px; border-radius: 8px; background: var(--card); border: 1px solid var(--hr); }
.scard .k { font-size: 12px; color: var(--text2); }
.scard .v { margin-top: 4px; font-size: 18px; font-weight: 600; }
.scard .h { margin-top: 2px; font-size: 11px; color: var(--text2); opacity: 0.8; }

/* 表格 */
.cellTask { display: flex; flex-direction: column; min-width: 0; }
.cellTask .tid { font-weight: 500; }
.cellTask .tmodel { font-size: 11px; color: var(--text2); }
:deep(.clickable) { cursor: pointer; }
.stag { margin-inline-end: 0; }
.empty { padding: 18px 0; }
.empty .et { font-weight: 500; margin-bottom: 4px; }
.empty .ed { font-size: 12px; color: var(--text2); max-width: 480px; margin: 0 auto; line-height: 1.7; }
.more { text-align: center; padding: 10px 0 2px; }

/* 详情 drawer */
.dops { display: inline-flex; align-items: center; gap: 8px; }
.dcancel-tip { font-size: 11px; color: var(--text2); }
.dhead { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 14px; }
.dgrid { border: 1px solid var(--hr); border-radius: 8px; padding: 4px 14px; margin-bottom: 12px; }
.drow { display: flex; justify-content: space-between; gap: 12px; padding: 9px 0; border-bottom: 1px solid var(--hr); font-size: 12px; }
.drow:last-child { border-bottom: 0; }
.dl { color: var(--text2); flex-shrink: 0; }
.dv { text-align: right; word-break: break-all; font-weight: 500; }
.dsec { font-size: 12px; color: var(--text2); margin: 14px 0 8px; }
.art { border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px; margin-bottom: 8px; }
.artname { font-size: 12px; font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.artmeta { margin-top: 4px; font-size: 11px; color: var(--text2); }
.artdl { margin-left: 8px; }

@media (max-width: 640px) {
  .choiceGrid3, .twoCol, .s3Grid, .advGrid { grid-template-columns: 1fr; }
  .s3Grid .span2 { grid-column: span 1; }
}
</style>
