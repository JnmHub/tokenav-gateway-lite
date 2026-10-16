<script setup>
/* 「使用我的 Key」选择器（对照旧 MonitorKeyPickerDialog.vue）
 * 数据源双通道（均为已核实契约）：
 *  1) 用户接口：GET /keys?status=active + GET /groups/rates（旧 api/keys.ts、api/groups.ts）
 *     过滤：status=active、未过期、且 group.platform === 当前表单 provider
 *  2) gateway-lite 回退：该部署模式未注册用户路由（实测 404），改用
 *     GET /admin/gateway-lite/local-api-keys（active + platform 过滤），
 *     选中后再 POST :id/reveal 换取明文 {secret}（已实测）
 * 两个来源都以行点击回调 emit('pick', 明文key)，由表单回填 api_key。 */
import { ref, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { listMyKeys, getMyGroupRates, listLocalKeys, revealLocalKey } from './api'
import { PROVIDER_LABELS } from './constants'

const props = defineProps({
  open: { type: Boolean, default: false },
  provider: { type: String, default: 'anthropic' },
})
const emit = defineEmits(['close', 'pick'])

const loading = ref(false)
const source = ref('user') // 'user' = 用户 Key 接口；'local' = 网关本地 Key
const rows = ref([])
const groupRates = ref({}) // group_id → 用户自定义费率（来源 GET /groups/rates）
const search = ref('')
const revealingId = ref(null)

/* 打开时重置搜索并按需拉取（provider 变化后重算过滤，无需重新拉全量） */
watch(() => props.open, (open) => {
  if (!open) return
  search.value = ''
  if (rows.value.length === 0) load()
})

async function load() {
  loading.value = true
  try {
    try {
      // 首选：用户 Key 接口（旧实现契约）
      const [res, rates] = await Promise.all([listMyKeys(), getMyGroupRates()])
      const items = res.items || []
      const now = Date.now()
      rows.value = items.filter((k) => {
        if (k.status !== 'active') return false
        if (k.expires_at && new Date(k.expires_at).getTime() <= now) return false
        return true
      })
      groupRates.value = rates || {}
      source.value = 'user'
    } catch (e) {
      // 回退：gateway-lite 模式（/keys 404）走本地 Key 接口
      if (!/HTTP 404|404/.test(e.message || '')) throw e
      const items = (await listLocalKeys()) || []
      rows.value = items.filter((k) => k.status === 'active')
      groupRates.value = {}
      source.value = 'local'
    }
  } catch (e) {
    message.error(`加载 Key 列表失败：${e.message}`)
    rows.value = []
  } finally {
    loading.value = false
  }
}

/* 本地 Key 接口的 platform 字段即分组平台；用户 Key 在 group.platform */
const platformOf = (k) => (source.value === 'user' ? k.group?.platform : k.platform)

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  return rows.value.filter((k) => {
    if (platformOf(k) !== props.provider) return false
    if (!q) return true
    const group = source.value === 'user' ? k.group?.name || '' : k.group_name || ''
    return (k.name || '').toLowerCase().includes(q) || (k.key || '').toLowerCase().includes(q) || group.toLowerCase().includes(q)
  })
})

const columns = computed(() => [
  { title: '名称', dataIndex: 'name', key: 'name', ellipsis: true },
  { title: 'Key', key: 'key', width: 150 },
  source.value === 'user'
    ? { title: '并发', dataIndex: 'current_concurrency', key: 'conc', width: 70 }
    : { title: '分组', dataIndex: 'group_name', key: 'group', width: 140, ellipsis: true },
  { title: '分组费率', key: 'rate', width: 90 },
])

/* 用户 Key 直接带明文（旧 k.key）；本地 Key 只回掩码，需 reveal 换明文 */
async function pick(row) {
  if (source.value === 'user') {
    emit('pick', row.key)
    return
  }
  revealingId.value = row.id
  try {
    const data = await revealLocalKey(row.id)
    emit('pick', data.secret)
  } catch (e) {
    message.error(`获取 Key 明文失败：${e.message}`)
  } finally {
    revealingId.value = null
  }
}
</script>

<template>
  <a-modal
    :open="open"
    :title="`选择我的 API Key（${PROVIDER_LABELS[provider] || provider}）`"
    width="640px"
    :footer="null"
    wrap-class-name="monitor-key-picker-modal"
    @cancel="emit('close')"
  >
    <p class="pick-hint">
      仅显示当前账号处于「启用」状态且未过期的 Key，并按表单平台（{{ PROVIDER_LABELS[provider] || provider }}）过滤。
    </p>
    <a-input v-model:value="search" placeholder="搜索名称 / Key / 分组" allow-clear style="margin-bottom: 10px" />

    <a-table
      :columns="columns"
      :data-source="filtered"
      :loading="loading"
      row-key="id"
      size="small"
      :pagination="false"
      :scroll="{ y: 320 }"
      class="pick-table"
      :custom-row="(r) => ({ onClick: () => pick(r), style: 'cursor:pointer' })"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'key'">
          <span class="mono">{{ source === 'user' ? record.key : record.key_masked }}</span>
        </template>
        <template v-else-if="column.key === 'group'">
          {{ record.group_name || '—' }}
        </template>
        <template v-else-if="column.key === 'rate'">
          <template v-if="source === 'user' && record.group && groupRates[record.group.id] != null">
            ×{{ groupRates[record.group.id] }}
          </template>
          <template v-else>—</template>
        </template>
      </template>
      <template #emptyText>
        <span class="pick-empty">{{ loading ? '正在加载…' : '没有可用的启用状态 Key' }}</span>
      </template>
    </a-table>

    <div class="pick-foot">
      <a-button size="small" @click="emit('close')">取消</a-button>
    </div>
  </a-modal>
</template>

<style scoped>
.pick-hint { font-size: 12px; color: var(--text2); margin: 0 0 10px; }
.mono { font-family: Consolas, monospace; font-size: 12px; }
.pick-empty { font-size: 12px; color: var(--text2); }
.pick-foot { display: flex; justify-content: flex-end; margin-top: 12px; }
.pick-table :deep(.ant-table-row:hover) { background: var(--hover-b); }
</style>
