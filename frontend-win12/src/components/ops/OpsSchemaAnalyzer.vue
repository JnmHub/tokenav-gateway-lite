<script setup>
/* 请求 Schema 分析器整页（三期）：a-drawer 内整页形态，拆 3 个子组件控制行数
 * 契约来源：旧 frontend/src/views/admin/ops/RequestSchemaAnalyzerView.vue + frontend/src/api/admin/ops.ts
 * - 采集范围（全部/按分组/按 Key）+ 保存调试范围：
 *   PUT /admin/ops/runtime/request-debug { preset, max_entries, scope:{type,value} }
 *   （preset 缺省时按 ttl_seconds 推导 5m/15m/30m；保存即以 enabled=true 开启结构采集，与旧实现一致）
 * - 记录列表勾选 2 条对照；结构详情字段树；结构对照 4 区域 chip + 仅看差异 + 差异四列
 * - GET /admin/ops/runtime/request-debug/records → { items|records }
 * - DELETE /admin/ops/runtime/request-debug/records（清空）
 * - GET /admin/ops/runtime/request-debug（当前配置）
 * - 完整调试 Tab（OpsSchemaFullDebug）：full-config / full-records 全套
 * - 分组 GET /admin/groups/all；本地密钥 GET /admin/gateway-lite/local-api-keys（均经真实接口核实） */
import { ref, computed, watch, onUnmounted } from 'vue'
import dayjs from 'dayjs'
import { message, Modal } from 'ant-design-vue'
import { ReloadOutlined, DeleteOutlined, ExperimentOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'
import OpsSchemaDetail from './OpsSchemaDetail.vue'
import OpsSchemaCompare from './OpsSchemaCompare.vue'
import OpsSchemaFullDebug from './OpsSchemaFullDebug.vue'

const props = defineProps({ open: { type: Boolean, default: false } })
const emit = defineEmits(['update:open'])

/* ---------- 数据 ---------- */
const loading = ref(false)
const saving = ref(false)
const groups = ref([])
const keys = ref([])
const config = ref(null)      // GET /admin/ops/runtime/request-debug → {enabled, expires_at, max_records, scope, ttl_seconds, count}
const records = ref([])

/* selected: 'all' | 'group:<id>' | 'api_key:<id>'（与旧实现同形态） */
const selected = ref('all')
const selectedLabel = computed(() => {
  if (selected.value === 'all') return '全部请求'
  const [kind, rawID] = selected.value.split(':')
  if (kind === 'group') return groups.value.find((g) => String(g.id) === rawID)?.name || '指定分组'
  if (kind === 'api_key') return keys.value.find((k) => String(k.id) === rawID)?.name || '指定 API Key（接口密钥）'
  return '全部请求'
})

/* 按当前配置回填下拉（type=api_key 时 value 是字符串化的 key id） */
const applyScopeFromConfig = (c) => {
  if (c?.scope?.type && c.scope.type !== 'all' && c.scope.value != null && c.scope.value !== '') {
    selected.value = `${c.scope.type}:${c.scope.value}`
  } else {
    selected.value = 'all'
  }
}

const load = async () => {
  loading.value = true
  try {
    const [g, k, c, r] = await Promise.all([
      request('/admin/groups/all', { method: 'GET' }),
      request('/admin/gateway-lite/local-api-keys', { method: 'GET' }),
      request('/admin/ops/runtime/request-debug', { method: 'GET' }),
      request('/admin/ops/runtime/request-debug/records', { method: 'GET' }),
    ])
    groups.value = g || []
    keys.value = k || []
    config.value = c
    records.value = r?.items || r?.records || []
    applyScopeFromConfig(c)
  } catch (e) { message.error(`加载 Schema 分析器失败：${e.message}`) }
  finally { loading.value = false }
}

/* 保存调试范围 = 以 enabled=true 开启结构采集（旧 save() 同语义）：
 * preset 优先取当前配置，缺失时按 ttl_seconds 推导；max_entries 承载 max_records */
const save = async () => {
  if (!config.value || saving.value) return
  saving.value = true
  try {
    const [kind, rawID] = selected.value.split(':')
    const scope = selected.value === 'all'
      ? { type: 'all', value: null }
      : { type: kind, value: kind === 'group' ? Number(rawID) : String(rawID) }
    const ttl = Number(config.value.ttl_seconds) || 900
    const preset = ttl <= 300 ? '5m' : ttl <= 900 ? '15m' : '30m'
    const savedSelection = selected.value // 保存后保留当前选择（旧网关精简响应可能缺 scope，不能因此回填“全部”）
    config.value = await request('/admin/ops/runtime/request-debug', {
      method: 'PUT',
      body: { preset, max_entries: config.value.max_records || 100, scope },
    })
    selected.value = savedSelection
    const r = await request('/admin/ops/runtime/request-debug/records', { method: 'GET' })
    records.value = r?.items || r?.records || []
    message.success('调试范围已保存，结构采集中')
  } catch (e) { message.error(`保存调试范围失败：${e.message}`) }
  finally { saving.value = false }
}

/* 清空记录：确认后 DELETE records */
const clear = () => {
  if (!records.value.length) return
  Modal.confirm({
    title: '清空采集记录',
    content: `将删除当前 ${records.value.length} 条结构记录，不可恢复。确定清空？`,
    okText: '清空', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      try {
        await request('/admin/ops/runtime/request-debug/records', { method: 'DELETE' })
        records.value = []
        compareLeft.value = ''; compareRight.value = ''
        message.success('已清空采集记录')
      } catch (e) { message.error(`清空失败：${e.message}`) }
    },
  })
}

/* ---------- 记录选择与对照 ---------- */
const expandedId = ref(null)   // 结构详情当前记录
const activeTab = ref('detail')
const compareLeft = ref('')
const compareRight = ref('')
const selectedRecord = computed(() => records.value.find((r) => r.id === expandedId.value) || records.value[0])

const selectRecord = (id) => {
  expandedId.value = expandedId.value === id ? null : id
  activeTab.value = 'detail'
}
const toggleCompare = (id) => {
  if (compareLeft.value === id) compareLeft.value = ''
  else if (compareRight.value === id) compareRight.value = ''
  else if (!compareLeft.value) compareLeft.value = id
  else if (!compareRight.value) compareRight.value = id
  // 选齐两条自动跳到对照 Tab，省一次点击（旧实现同交互）
  if (compareLeft.value && compareRight.value) activeTab.value = 'compare'
}
const leftRecord = computed(() => records.value.find((r) => r.id === compareLeft.value))
const rightRecord = computed(() => records.value.find((r) => r.id === compareRight.value))

const fmtTime = (t) => (t ? dayjs(t).format('MM-DD HH:mm:ss') : '-')
const fmtStatus = (n) => n || '—'

const drawerWidth = ref(window.innerWidth < 760 ? '100%' : '94%')
const onWinResize = () => { drawerWidth.value = window.innerWidth < 760 ? '100%' : '94%' }
window.addEventListener('resize', onWinResize)
onUnmounted(() => window.removeEventListener('resize', onWinResize))

const close = () => emit('update:open', false)
/* 抽屉打开即加载（内容随 open 卸载，关闭即销毁子组件状态） */
watch(() => props.open, (v) => { if (v) { activeTab.value = 'detail'; expandedId.value = null; load() } })
</script>

<template>
  <a-drawer :open="open" :width="drawerWidth" title="请求 Schema 分析器" @close="close">
    <div class="sa">
      <!-- 顶部：采集范围 + 保存调试范围 + 刷新/清空 -->
      <div class="card top">
        <div class="top-head">
          <ExperimentOutlined class="top-i" />
          <div class="top-t">
            <div class="top-name">字段结构分析</div>
            <div class="sub">只查看字段路径、类型和请求头名称，不展示任何字段值</div>
          </div>
          <div class="top-btns">
            <a-button size="small" :loading="loading" @click="load"><template #icon><ReloadOutlined /></template>刷新</a-button>
            <a-button size="small" danger :disabled="!records.length" @click="clear"><template #icon><DeleteOutlined /></template>清空</a-button>
          </div>
        </div>
        <div v-if="config" class="top-form">
          <div class="f-scope">
            <label>采集范围</label>
            <a-select v-model:value="selected" size="small" style="width: 100%">
              <a-select-option value="all">全部请求</a-select-option>
              <a-select-opt-group label="按分组">
                <a-select-option v-for="g in groups" :key="`g-${g.id}`" :value="`group:${g.id}`">{{ g.name }}（{{ g.platform }}）</a-select-option>
              </a-select-opt-group>
              <a-select-opt-group v-if="keys.length" label="按 API Key（接口密钥）">
                <a-select-option v-for="k in keys" :key="`k-${k.id}`" :value="`api_key:${k.id}`">{{ k.name }} · {{ k.key_masked }}</a-select-option>
              </a-select-opt-group>
            </a-select>
          </div>
          <div class="f-hint">
            <span class="strong">{{ selectedLabel }}</span>
            <span class="dim">· 采集 {{ config.ttl_seconds ? `${Math.round(config.ttl_seconds / 60)} 分钟` : '15 分钟' }}，最多 {{ config.max_records }} 条</span>
            <a-tag v-if="config.enabled" color="green" style="margin: 0 0 0 8px">采集中</a-tag>
            <a-tag v-else style="margin: 0 0 0 8px">已关闭</a-tag>
          </div>
          <a-button size="small" type="primary" :loading="saving" @click="save">保存调试范围</a-button>
        </div>
      </div>

      <!-- 主区：左记录列表 + 右工作台 -->
      <div class="main">
        <aside class="card rec">
          <div class="rec-h">
            <div>
              <div class="rec-t">采集记录</div>
              <div class="sub">{{ records.length }} 条 · 勾选两条对照</div>
            </div>
          </div>
          <div v-if="loading" class="empty">加载中…</div>
          <div v-else-if="!records.length" class="empty">暂无记录，先保存调试范围并向目标范围发送请求</div>
          <div v-else class="rec-scroll">
            <article
              v-for="record in records" :key="record.id" class="rec-item"
              :class="{ 'rec-active': selectedRecord?.id === record.id }" @click="selectRecord(record.id)"
            >
              <div class="rec-row">
                <a-checkbox
                  :checked="compareLeft === record.id || compareRight === record.id"
                  @click.stop @change="toggleCompare(record.id)"
                />
                <div class="rec-body">
                  <div class="rec-line">
                    <span class="method">{{ record.method }}</span>
                    <span class="path" :title="record.path">{{ record.path }}</span>
                  </div>
                  <div class="rec-meta">
                    <span :class="(record.response_status || 0) < 400 ? 'ok' : 'err'">{{ fmtStatus(record.response_status) }}</span>
                    <span>{{ record.duration_ms || 0 }}ms</span>
                    <span>{{ fmtTime(record.created_at) }}</span>
                  </div>
                </div>
                <span v-if="compareLeft === record.id || compareRight === record.id" class="side-tag">{{ compareLeft === record.id ? '左' : '右' }}</span>
              </div>
            </article>
          </div>
        </aside>

        <main class="card ws">
          <div class="tabs">
            <button class="tab" :class="{ on: activeTab === 'detail' }" @click="activeTab = 'detail'">结构详情</button>
            <button class="tab" :class="{ on: activeTab === 'compare', hint: !leftRecord || !rightRecord }" @click="activeTab = 'compare'">
              结构对照<span v-if="leftRecord && rightRecord" class="tab-n">2</span>
            </button>
            <button class="tab" :class="{ on: activeTab === 'full' }" @click="activeTab = 'full'">完整调试</button>
          </div>

          <OpsSchemaDetail v-if="activeTab === 'detail'" :record="selectedRecord" />
          <OpsSchemaCompare v-else-if="activeTab === 'compare'" :left="leftRecord" :right="rightRecord" />
          <OpsSchemaFullDebug v-else :keys="keys" />
        </main>
      </div>
    </div>
  </a-drawer>
</template>

<style scoped>
.card { background: var(--card); border: 1px solid var(--hr); border-radius: 10px; }
.top { padding: 12px 14px; margin-bottom: 10px; }
.top-head { display: flex; align-items: flex-start; gap: 10px; }
.top-i { font-size: 18px; color: #3b82f6; margin-top: 2px; }
.top-name { font-size: 13px; font-weight: 500; }
.top-btns { margin-left: auto; display: flex; gap: 8px; }
.top-form { display: flex; align-items: flex-end; gap: 12px; margin-top: 10px; flex-wrap: wrap; }
.f-scope { display: flex; flex-direction: column; gap: 4px; min-width: 260px; max-width: 380px; flex: 1; }
.f-scope label { font-size: 11px; color: var(--text2); }
.f-hint { display: flex; align-items: center; font-size: 12px; padding-bottom: 4px; flex-wrap: wrap; }
.strong { font-weight: 500; }
.dim { color: var(--text2); font-size: 11px; }
.sub { font-size: 11px; color: var(--text2); margin-top: 2px; }
.main { display: grid; grid-template-columns: minmax(280px, 360px) minmax(0, 1fr); gap: 10px; align-items: start; }
.rec { display: flex; flex-direction: column; max-height: calc(100vh - 210px); }
.rec-h { padding: 10px 12px; border-bottom: 1px solid var(--hr); }
.rec-t { font-size: 13px; font-weight: 500; }
.rec-scroll { overflow-y: auto; }
.rec-item { padding: 8px 12px; border-bottom: 1px solid var(--hr); cursor: pointer; transition: background 0.15s ease; }
.rec-item:last-child { border-bottom: 0; }
.rec-item:hover { background: rgba(127, 127, 127, 0.07); }
.rec-item.rec-active { background: rgba(59, 130, 246, 0.08); box-shadow: inset 2px 0 0 #3b82f6; }
.rec-row { display: flex; align-items: flex-start; gap: 8px; }
.rec-body { min-width: 0; flex: 1; }
.rec-line { display: flex; align-items: center; gap: 6px; min-width: 0; }
.method { flex: none; font-family: Consolas, monospace; font-size: 10px; background: rgba(59, 130, 246, 0.12); color: #3b82f6; border-radius: 4px; padding: 1px 6px; }
.path { font-family: Consolas, monospace; font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rec-meta { display: flex; gap: 8px; font-size: 10px; color: var(--text2); margin-top: 3px; }
.ok { color: #16a34a; }
.err { color: #dc2626; }
.side-tag { flex: none; font-size: 10px; font-weight: 600; color: #3b82f6; background: rgba(59, 130, 246, 0.14); border-radius: 4px; padding: 1px 6px; }
.ws { min-height: 420px; }
.tabs { display: flex; gap: 4px; padding: 8px 10px 0; border-bottom: 1px solid var(--hr); }
.tab { border: 0; background: transparent; font-size: 12px; color: var(--text2); padding: 7px 12px; cursor: pointer; border-radius: 6px 6px 0 0; border-bottom: 2px solid transparent; }
.tab:hover { color: inherit; background: rgba(127, 127, 127, 0.07); }
.tab.on { color: #3b82f6; border-bottom-color: #3b82f6; font-weight: 500; }
.tab.hint { opacity: 0.6; }
.tab-n { margin-left: 6px; background: rgba(59, 130, 246, 0.16); border-radius: 999px; padding: 0 6px; font-size: 10px; }
.empty { padding: 24px 12px; text-align: center; font-size: 12px; color: var(--text2); }
@media (max-width: 900px) {
  .main { grid-template-columns: 1fr; }
  .rec { max-height: 320px; }
}
</style>
