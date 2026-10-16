<script setup>
/* 本地密钥 v2：列表 + 新增/编辑（含 IP 白/黑名单）+ 行内启停 + 列设置 +
 * 创建后明文一次性弹窗。契约来源（均已核实，不凭猜测）：
 * - 旧实现 frontend/src/views/admin/GatewayLiteLocalKeysView.vue + api/admin/gatewayLiteLocalKeys.ts
 * - 后端 internal/handler/admin/gateway_lite_local_key_handler.go（已对本机 18080 实测）
 * 要点：
 * - 列表 GET  /admin/gateway-lite/local-api-keys → 数组：id/name/key_masked/
 *   group_id/group_name/platform/status/ip_whitelist[]/ip_blacklist[]/last_used_at
 * - 创建 POST 同路径，body 仅 { name, group_id, ip_whitelist[], ip_blacklist[] }
 *   （后端不接受 platform/status，平台由绑定分组决定、新密钥默认 active），
 *   响应含 secret 明文——明文只在创建响应里出现，前端弹一次性展示弹窗
 * - 启停/编辑 PUT :id：指针字段部分更新，行内启停只传 { status: 'active'|'disabled' }
 * - 明文补看 POST :id/reveal → { secret }（需浏览器 JWT 会话，列表保留入口）
 * - IP 规则为字符串数组（IP 或 CIDR，服务端校验格式）；表单用多行文本，
 *   提交时按换行/逗号拆分、去空、去重（与旧版 parseIPRules 行为一致） */
import { ref, computed, onMounted } from 'vue'
import { confirmAction } from '../utils/confirmAction'
import { request } from '../api'
import { message } from 'ant-design-vue'

const loading = ref(false)
const rows = ref([])
const groups = ref([])
const err = ref('')

/* ===== 列定义与显隐设置（缺口：列显示设置） =====
 * 与旧版一致默认隐藏 ID 列；可切换列持久化到 localStorage（Win12 独立 origin，
 * 使用 w12- 前缀 key，与 token/user 命名习惯一致） */
const HIDDEN_KEY = 'w12-local-keys-hidden-columns'
const DEFAULT_HIDDEN = ['id']
const BASE_COLUMNS = [
  { title: 'ID', dataIndex: 'id', key: 'id', width: 70 },
  { title: '名称', dataIndex: 'name', key: 'name', ellipsis: true },
  { title: '密钥', dataIndex: 'key_masked', key: 'key_masked', ellipsis: true },
  { title: '分组', dataIndex: 'group_name', key: 'group_name', width: 110, ellipsis: true },
  { title: '平台', dataIndex: 'platform', key: 'platform', width: 90 },
  { title: '访问限制', key: 'access', width: 110 },
  { title: '最近使用', key: 'last_used', width: 150 },
  { title: '状态', key: 'status', width: 80 },
  { title: '操作', key: 'action', width: 190 },
]
// 名称/密钥/分组/状态/操作是主信息，固定显示；其余列允许显隐切换
const TOGGLABLE = ['id', 'platform', 'access', 'last_used']
const togglableCols = BASE_COLUMNS.filter((c) => TOGGLABLE.includes(c.key))

function loadHiddenCols() {
  try {
    const saved = JSON.parse(localStorage.getItem(HIDDEN_KEY))
    return new Set(Array.isArray(saved) ? saved : DEFAULT_HIDDEN)
  } catch {
    return new Set(DEFAULT_HIDDEN)
  }
}
const hiddenCols = ref(loadHiddenCols())
const columns = computed(() => BASE_COLUMNS.filter((c) => !hiddenCols.value.has(c.key)))

function toggleCol(key) {
  // 重建 Set 保证 computed 列与勾选标记同步刷新
  const next = new Set(hiddenCols.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  hiddenCols.value = next
  try { localStorage.setItem(HIDDEN_KEY, JSON.stringify([...next])) } catch { /* 禁用存储时仅当前会话生效 */ }
}

/* ===== 启用徽章（缺口：N-enabled） ===== */
const activeCount = computed(() => rows.value.filter((r) => r.status === 'active').length)

/* ===== 新增/编辑 ===== */
const editOpen = ref(false)
const editSaving = ref(false)
// 表单内 IP 规则为多行文本；active 为布尔，提交时映射为 status
const editForm = ref({ id: 0, name: '', group_id: undefined, active: true, ip_whitelist: '', ip_blacklist: '' })
const revealed = ref({}) // id -> secret（行内「显示明文」暂存展示）
// 创建成功的一次性明文弹窗
const secretOpen = ref(false)
const createdSecret = ref('')

const groupOptions = computed(() => groups.value.map((g) => ({
  value: g.id,
  // 与旧版一致：选项里带平台与「仅本地」标记，绑定分组即确定平台
  label: `${g.name} · ${g.platform || '-'}${g.local_only ? ' · 仅本地' : ''}`,
})))

const parseIPRules = (text) =>
  Array.from(new Set(String(text || '').split(/[\n,]/).map((s) => s.trim()).filter(Boolean)))

const openCreate = () => {
  editForm.value = { id: 0, name: '', group_id: groups.value[0]?.id, active: true, ip_whitelist: '', ip_blacklist: '' }
  editOpen.value = true
}
const openEdit = (r) => {
  editForm.value = {
    id: r.id,
    name: r.name,
    group_id: r.group_id,
    active: (r.status || 'active') === 'active',
    ip_whitelist: (r.ip_whitelist || []).join('\n'),
    ip_blacklist: (r.ip_blacklist || []).join('\n'),
  }
  editOpen.value = true
}

const save = async () => {
  const f = editForm.value
  if (!f.name.trim() || !f.group_id) { message.error('名称与分组为必填项'); return }
  editSaving.value = true
  try {
    // 创建请求体与后端 gatewayLiteLocalKeyCreateRequest 对齐：不带 status/platform
    const payload = {
      name: f.name.trim(),
      group_id: f.group_id,
      ip_whitelist: parseIPRules(f.ip_whitelist),
      ip_blacklist: parseIPRules(f.ip_blacklist),
    }
    let res
    if (f.id) {
      res = await request(`/admin/gateway-lite/local-api-keys/${f.id}`, {
        method: 'PUT',
        body: { ...payload, status: f.active ? 'active' : 'disabled' },
      })
      message.success('本地密钥已更新')
    } else {
      res = await request('/admin/gateway-lite/local-api-keys', { method: 'POST', body: payload })
    }
    editOpen.value = false
    // 明文仅在创建响应中返回（secret 字段），弹一次性展示弹窗
    if (!f.id && res?.secret) {
      createdSecret.value = res.secret
      secretOpen.value = true
    }
    await load()
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    editSaving.value = false
  }
}

const copyCreated = async () => {
  try {
    await navigator.clipboard.writeText(createdSecret.value)
    message.success('已复制到剪贴板')
  } catch {
    message.warning('复制失败，请手动选中复制')
  }
}

/* ===== 行内快捷启停（缺口：Toggle）=====
 * PUT 指针字段部分更新：只传 status 即可，不影响名称/IP 规则 */
const togglingId = ref(0)
const toggleStatus = async (r) => {
  const next = r.status === 'active' ? 'disabled' : 'active'
  togglingId.value = r.id
  try {
    const updated = await request(`/admin/gateway-lite/local-api-keys/${r.id}`, {
      method: 'PUT',
      body: { status: next },
    })
    const i = rows.value.findIndex((x) => x.id === r.id)
    if (i >= 0 && updated) rows.value[i] = { ...rows.value[i], ...updated }
  } catch (e) {
    // 未更新 rows，开关自动回滚到原状态
    message.error(`修改状态失败：${e.message}`)
  } finally {
    togglingId.value = 0
  }
}

const reveal = async (r) => {
  try {
    const res = await request(`/admin/gateway-lite/local-api-keys/${r.id}/reveal`, { method: 'POST' })
    revealed.value = { ...revealed.value, [r.id]: res.secret }
    message.success('已获取明文，请立即复制保存')
  } catch (e) {
    message.error(e.message)
  }
}

const remove = async (r) => {
  if (!(await confirmAction({ title: '删除本地密钥', content: `确认删除本地密钥「${r.name}」？删除后该密钥立即且永久失效，无法恢复。`, okText: '删除', danger: true }))) return
  try {
    await request(`/admin/gateway-lite/local-api-keys/${r.id}`, { method: 'DELETE' })
    await load()
  } catch (e) {
    message.error(`删除失败：${e.message}`)
  }
}

const fmtTime = (v) => (v ? new Date(v).toLocaleString('zh-CN', { hour12: false }) : '从未使用')

const load = async () => {
  loading.value = true
  try {
    const data = await request('/admin/gateway-lite/local-api-keys', { method: 'GET' })
    rows.value = Array.isArray(data) ? data : []
    err.value = ''
  } catch (e) {
    err.value = e.message
    rows.value = []
  } finally {
    loading.value = false
  }
}
const loadGroups = async () => {
  // /admin/groups/all 不带参数只返回启用分组（与旧版 getAll 一致），本地密钥只能绑定启用分组
  try { groups.value = await request('/admin/groups/all', { method: 'GET' }) } catch { /* 不阻塞 */ }
}
onMounted(() => { load(); loadGroups() })
</script>

<template>
  <div class="lkeys">
    <div class="toolbar">
      <a-button type="primary" @click="openCreate">+ 新增本地密钥</a-button>
      <!-- 启用徽章：N/M 启用（对应旧版 activeCount 徽章的增强形态） -->
      <a-tag color="green" class="badge">{{ activeCount }}/{{ rows.length }} 启用</a-tag>
      <span class="total">共 {{ rows.length }} 个本地密钥</span>
      <a-button v-if="err" size="small" danger @click="load">加载失败，点击重试</a-button>
      <span v-if="err" class="total err">{{ err }}</span>
      <span class="spacer" />
      <a-dropdown :trigger="['click']">
        <a-button size="small">列设置</a-button>
        <template #overlay>
          <a-menu>
            <a-menu-item v-for="c in togglableCols" :key="c.key" @click="toggleCol(c.key)">
              <span class="colmark">{{ hiddenCols.has(c.key) ? '' : '✓' }}</span>{{ c.title }}
            </a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
    </div>
    <a-table
      :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="small" :scroll="{ x: 'max-content' }"
      :pagination="false"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'id'">
          <span class="mono">#{{ record.id }}</span>
        </template>
        <template v-else-if="column.key === 'key_masked'">
          <span class="mono">{{ revealed[record.id] || record.key_masked }}</span>
          <a-button v-if="!revealed[record.id]" size="small" type="link" @click="reveal(record)">显示明文</a-button>
        </template>
        <template v-else-if="column.key === 'access'">
          <!-- 访问限制：白/黑名单条数，白名单为空 = 不限（与旧版展示一致） -->
          <div class="acl">白名单 {{ (record.ip_whitelist || []).length || '不限' }}</div>
          <div class="acl">黑名单 {{ (record.ip_blacklist || []).length || 0 }}</div>
        </template>
        <template v-else-if="column.key === 'last_used'">
          <span class="acl">{{ fmtTime(record.last_used_at) }}</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tooltip :title="record.status === 'active' ? '点击停用' : '点击启用'">
            <a-switch
              size="small" :checked="record.status === 'active'" :loading="togglingId === record.id"
              @change="toggleStatus(record)"
            />
          </a-tooltip>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-space>
            <a-button size="small" type="primary" @click="openEdit(record)">编辑</a-button>
            <a-button size="small" danger @click="remove(record)">删除</a-button>
          </a-space>
        </template>
      </template>
    </a-table>

    <!-- 新增/编辑弹窗：补 IP 白/黑名单；平台由分组决定，不提供 platform 编辑 -->
    <a-modal v-model:open="editOpen" :title="editForm.id ? '编辑本地密钥' : '新增本地密钥'" :confirm-loading="editSaving" ok-text="保存" cancel-text="取消" @ok="save">
      <a-form layout="vertical" size="small">
        <a-form-item label="名称" required>
          <a-input v-model:value="editForm.name" :maxlength="100" placeholder="例如：本机自动化" />
        </a-form-item>
        <a-form-item label="绑定本地分组" required>
          <a-select
            v-model:value="editForm.group_id" show-search option-filter-prop="label"
            :options="groupOptions" placeholder="选择一个启用分组"
          />
        </a-form-item>
        <a-form-item v-if="editForm.id">
          <div class="switchrow">
            <div>
              <div>启用密钥</div>
              <div class="hint">停用后下一次请求立即返回鉴权失败</div>
            </div>
            <a-switch v-model:checked="editForm.active" size="small" />
          </div>
        </a-form-item>
        <a-form-item label="IP 白名单">
          <a-textarea v-model:value="editForm.ip_whitelist" :rows="3" placeholder="每行一个 IP 或 CIDR；留空表示不限" />
        </a-form-item>
        <a-form-item label="IP 黑名单">
          <a-textarea v-model:value="editForm.ip_blacklist" :rows="3" placeholder="每行一个 IP 或 CIDR" />
        </a-form-item>
        <a-alert
          v-if="!editForm.id" type="warning" show-icon
          message="创建成功后仅弹窗展示一次明文，请立即复制保存"
        />
      </a-form>
    </a-modal>

    <!-- 明文一次性展示弹窗：禁遮罩/Esc 关闭，防止误触丢明文；关闭后请走列表「显示明文」补看 -->
    <a-modal
      :open="secretOpen" title="本地密钥已创建" :mask-closable="false" :closable="false" :keyboard="false"
      :width="520"
    >
      <a-alert
        type="warning" show-icon class="secret-warn"
        message="这是唯一一次完整展示，关闭后无法再次查看，请立即复制保存"
      />
      <code class="secret-code">{{ createdSecret }}</code>
      <template #footer>
        <a-button @click="secretOpen = false">我已保存，关闭</a-button>
        <a-button type="primary" @click="copyCreated">复制密钥</a-button>
      </template>
    </a-modal>
  </div>
</template>

<style scoped>
.lkeys { height: 100%; overflow-y: auto; }
.toolbar { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; }
.spacer { flex: 1; }
.badge { margin-inline-end: 0; }
.total { font-size: 12px; color: var(--text2); }
.total.err { color: #d13438; }
.mono { font-family: Consolas, monospace; font-size: 12px; }
.acl { font-size: 12px; color: var(--text2); line-height: 18px; }
.colmark { display: inline-block; width: 16px; }
.switchrow { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.hint { font-size: 12px; color: var(--text2); }
.secret-warn { margin-bottom: 12px; }
.secret-code {
  display: block; padding: 10px 12px; border-radius: 6px; word-break: break-all; user-select: all;
  background: var(--card, rgba(128, 128, 128, 0.08)); border: 1px solid var(--hr, rgba(128, 128, 128, 0.2));
  font-family: Consolas, monospace; font-size: 12px;
}
</style>

