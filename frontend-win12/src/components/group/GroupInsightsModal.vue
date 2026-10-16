<script setup>
import W12Dialog from '../design/W12Dialog.vue'
import W12Table from '../design/W12Table.vue'
/* 分组洞察弹窗：把两个「后端已注册但前端没有入口」的只读接口补上 UI。
 * - GET /admin/groups/:id/effective-models → { group_id, models, effective_models,
 *       configured_models, reported, version }
 *   语义：只按「分组已绑定账号 + 已保存的模型映射」推算客户可见模型集合，
 *   不猜运行时报错（后端注释明确）。因此「生效模型」与「已配置模型」要分开展示，
 *   两者不一致时提示差异，管理员据此判断名单是否漏配。
 * - GET /admin/groups/:id/api-keys?page&page_size → { items, total, page, page_size, pages }
 *   语义：该分组下的 API Key 列表（按分组归属查询，不是全量 Key）。
 * 说明：分组统计接口 /admin/groups/:id/stats 在本方与官方后端都是占位实现
 * （恒返回 total_api_keys=0 等零值，源码含 TODO），故**不**为它做入口，避免死入口。
 */
import { ref, watch, computed } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../../api/index.js'
import { useGroupDialogRequests } from './useGroupDialogRequests.js'
import { isCancellation } from '../../api/session.js'

const props = defineProps({
  open: { type: Boolean, default: false },
  group: { type: Object, default: null },
})
const emit = defineEmits(['update:open'])
const requests = useGroupDialogRequests(props)

const tab = ref('models')

/* ===== 生效模型 ===== */
const modelsLoading = ref(false)
const models = ref([])          // effective/models：客户可见的生效模型
const configured = ref([])      // configured_models：分组上已保存的名单
const modelErr = ref('')

const configuredSet = computed(() => new Set(configured.value || []))
const effectiveSet = computed(() => new Set(models.value || []))
// 已配置但未生效（通常是没有可用账号承载）：重点提示项
const configuredOnly = computed(() => (configured.value || []).filter((m) => !effectiveSet.value.has(m)))
// 生效但不在配置名单里（继承/默认放行的模型）
const effectiveOnly = computed(() => (models.value || []).filter((m) => !configuredSet.value.has(m)))

const loadModels = async () => {
  if (!props.open || !props.group?.id) return
  const turn = requests.begin('models')
  modelsLoading.value = true
  modelErr.value = ''
  try {
    const d = await request(`/admin/groups/${turn.groupId}/effective-models`, { method: 'GET', signal: turn.signal })
    if (!turn.isCurrent()) return
    models.value = d?.effective_models || d?.models || []
    configured.value = d?.configured_models || []
  } catch (e) {
    if (!turn.isCurrent() || isCancellation(e)) return
    modelErr.value = e.message
    models.value = []
    configured.value = []
  } finally {
    if (turn.isCurrent()) modelsLoading.value = false
  }
}

/* ===== 分组内 API Key ===== */
const keysLoading = ref(false)
const keys = ref([])
const keyTotal = ref(0)
const keyPage = ref(1)
const keyPageSize = ref(20)
const keyErr = ref('')

const keyColumns = [
  { title: '名称', dataIndex: 'name', key: 'name', ellipsis: true },
  { title: 'Key', key: 'key', width: 190 },
  { title: '用户 ID', dataIndex: 'user_id', key: 'user_id', width: 90 },
  { title: '状态', dataIndex: 'status', key: 'status', width: 80 },
  { title: '当前并发', dataIndex: 'current_concurrency', key: 'cc', width: 90 },
  { title: '最后使用', key: 'last_used_at', width: 170 },
]

// 密钥掩码：只留头尾，避免在列表里完整泄露
const maskKey = (k) => {
  const s = String(k || '')
  if (s.length <= 12) return s ? '••••••' : '—'
  return `${s.slice(0, 8)}……${s.slice(-4)}`
}
const fmtTime = (t) => {
  if (!t) return '—'
  const d = new Date(t)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '—'
  return d.toLocaleString('zh-CN')
}

const loadKeys = async () => {
  if (!props.open || !props.group?.id) return
  const turn = requests.begin('keys')
  keysLoading.value = true
  keyErr.value = ''
  try {
    const d = await request(`/admin/groups/${turn.groupId}/api-keys`, {
      method: 'GET', signal: turn.signal,
      query: { page: keyPage.value, page_size: keyPageSize.value },
    })
    if (!turn.isCurrent()) return
    // 只在内存保留掩码，短密钥也不能原文回显；弹窗关闭后立即清除列表。
    keys.value = (d?.items || []).map(item => ({ ...item, key: maskKey(item.key) }))
    keyTotal.value = d?.total || 0
  } catch (e) {
    if (!turn.isCurrent() || isCancellation(e)) return
    keyErr.value = e.message
    keys.value = []
    keyTotal.value = 0
  } finally {
    if (turn.isCurrent()) keysLoading.value = false
  }
}

const onKeyTableChange = (pg) => {
  keyPage.value = pg.current
  keyPageSize.value = pg.pageSize
  loadKeys()
}

const close = () => emit('update:open', false)

// 弹窗每次打开（或切换分组）都重新拉数据，避免看到上一个分组的残留
watch(() => [props.open, props.group?.id], ([o]) => {
  models.value = []; configured.value = []; keys.value = []; keyTotal.value = 0
  modelsLoading.value = false; keysLoading.value = false
  if (!o) return
  tab.value = 'models'
  keyPage.value = 1
  loadModels()
  loadKeys()
}, { immediate: true })

const reload = () => {
  if (tab.value === 'models') loadModels()
  else loadKeys()
}
</script>

<template>
  <W12Dialog
    :open="open"
    :title="`分组洞察 · ${group?.name || ''}`"
    :footer="null"
    width="900px"
    @cancel="close"
  >
    <a-tabs v-model:activeKey="tab">
      <a-tab-pane key="models" tab="生效模型">
        <a-spin :spinning="modelsLoading">
          <div v-if="modelErr" class="ins-err">{{ modelErr }}</div>
          <template v-else>
            <div class="ins-bar">
              <span class="ins-tag">生效 {{ models.length }}</span>
              <span class="ins-tag">已配置 {{ configured.length }}</span>
              <span class="ins-grow" />
              <a-button size="small" @click="loadModels">刷新</a-button>
            </div>
            <div v-if="configuredOnly.length" class="ins-warn">
              已配置但未生效（{{ configuredOnly.length }}）：通常是没有可用账号承载这些模型 ——
              {{ configuredOnly.slice(0, 12).join('、') }}{{ configuredOnly.length > 12 ? ' …' : '' }}
            </div>
            <div v-if="effectiveOnly.length" class="ins-note">
              生效但不在配置名单里（{{ effectiveOnly.length }}）：{{ effectiveOnly.slice(0, 12).join('、') }}{{ effectiveOnly.length > 12 ? ' …' : '' }}
            </div>
            <div v-if="!models.length" class="ins-empty">该分组当前没有生效模型（未绑定可用账号或未配置模型名单）。</div>
            <div v-else class="ins-chips">
              <a-tag v-for="m in models" :key="m" class="ins-chip" :color="configuredSet.has(m) ? 'blue' : 'default'">{{ m }}</a-tag>
            </div>
          </template>
        </a-spin>
      </a-tab-pane>

      <a-tab-pane key="keys" tab="API Key 列表">
        <a-spin :spinning="keysLoading">
          <div v-if="keyErr" class="ins-err">{{ keyErr }}</div>
          <W12Table
            v-else
            :columns="keyColumns"
            :data-source="keys"
            row-key="id"
            size="small"
            :scroll="{ x: 'max-content' }"
            :pagination="{ current: keyPage, pageSize: keyPageSize, total: keyTotal, showSizeChanger: true, pageSizeOptions: ['10', '20', '50'] }"
            @change="onKeyTableChange"
          >
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'key'"><code class="ins-key">{{ record.key }}</code></template>
              <template v-else-if="column.key === 'status'">
                <a-tag :color="record.status === 'active' ? 'green' : 'red'" class="ins-chip">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
              </template>
              <template v-else-if="column.key === 'cc'">{{ record.current_concurrency ?? 0 }}</template>
              <template v-else-if="column.key === 'last_used_at'">{{ fmtTime(record.last_used_at) }}</template>
            </template>
          </W12Table>
        </a-spin>
      </a-tab-pane>
    </a-tabs>

    <div class="ins-foot">
      <a-button size="small" @click="reload">重新加载</a-button>
      <a-button size="small" type="primary" @click="close">关闭</a-button>
    </div>
  </W12Dialog>
</template>

<style scoped>
.ins-bar { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.ins-grow { flex: 1; }
.ins-tag { font-size: 12px; color: var(--text2); background: rgba(127, 127, 127, 0.1); border-radius: 4px; padding: 1px 8px; }
.ins-warn { font-size: 12px; color: #9a5b00; background: rgba(154, 91, 0, 0.08); border-radius: 8px; padding: 8px 10px; margin-bottom: 8px; }
.ins-note { font-size: 12px; color: var(--text2); margin-bottom: 8px; }
.ins-empty { font-size: 13px; color: var(--text2); padding: 16px 0; }
.ins-chips { display: flex; flex-wrap: wrap; gap: 6px; max-height: 380px; overflow-y: auto; padding: 2px; }
.ins-chip { border-radius: 4px; margin: 0; }
.ins-key { font-family: Consolas, monospace; font-size: 12px; }
.ins-err { font-size: 13px; color: #d13438; padding: 12px 0; }
.ins-foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--hr); }
</style>
