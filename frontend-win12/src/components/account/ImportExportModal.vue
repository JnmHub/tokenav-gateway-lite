<script setup>
/* 导入/导出弹窗（三期）——旧实现契约：
 * - frontend/src/views/admin/AccountsView.vue 的 handleExportData / openExportDataDialog
 * - frontend/src/components/admin/account/ImportDataModal.vue
 * 导出：GET /admin/accounts/data（ids 或 filters + include_proxies=false），
 *   响应 STEP_UP_REQUIRED 时弹 TOTP 输入（POST /user/totp/step-up {code, action_scope:'accounts.export'}），
 *   拿到一次性 token 后带 X-Step-Up-Token 头重放一次（旧 useStepUp.run 语义）。
 * 导入：文件校验（type ∈ sub2api-data|sub2api-bundle、version==1、accounts/proxies 数组）
 *   → 预览（账号/代理数/版本）→ POST /admin/accounts/data {data, skip_default_group_bind:true}
 *   → 结果汇总 + 逐条错误（旧 ImportDataModal 等价，支持多文件合并）。 */
import { ref, computed, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'
import { exportAccountsData, totpStepUp, STEP_UP_SCOPE_ACCOUNTS_EXPORT } from './accountsApi'
import { InboxOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  selectedIds: { type: Array, default: () => [] },
  filters: { type: Object, default: () => ({}) },
})
const emit = defineEmits(['imported'])

const mode = defineModel('mode', { type: String, default: 'export' }) // export | import
const open = defineModel('open', { type: Boolean, default: false })

// ===== 导出 =====
const exportScope = ref('auto') // auto: 有勾选=选中，否则当前筛选（旧版行为）
const includeProxies = ref(true)
const exporting = ref(false)
const showTotp = ref(false)
const totpCode = ref('')
const totpBusy = ref(false)
const needsSelection = computed(() => exportScope.value === 'selected' || (exportScope.value === 'auto' && props.selectedIds.length > 0))

const doExport = async (stepUpToken) => {
  const ids = needsSelection.value ? props.selectedIds : []
  const data = await exportAccountsData(
    { ids, includeProxies: includeProxies.value, filters: ids.length ? undefined : props.filters },
    stepUpToken,
  )
  // 下载（旧 formatDefaultTimeZoneFileTimestamp 简化为本地时间戳）
  const ts = new Date().toISOString().replace(/[:T]/g, '-').slice(0, 19)
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `sub2api-account-${ts}.json`
  a.click()
  URL.revokeObjectURL(a.href)
  const skipped = data?.skipped_shadows ?? 0
  if (skipped > 0) message.warning(`已导出（跳过 ${skipped} 个影子账号）`)
  else message.success('已导出')
}

const onExport = async () => {
  exporting.value = true
  try {
    await doExport()
    showTotp.value = false
    open.value = false
  } catch (e) {
    if (e.stepUpRequired) { showTotp.value = true; message.info('该操作需要二次验证，请输入 6 位动态验证码') }
    else if (e.stepUpBlocked) message.error(e.message)
    else message.error(`导出失败：${e.message}`)
  } finally {
    exporting.value = false
  }
}

const onTotpVerify = async () => {
  const code = totpCode.value.replace(/\D/g, '')
  if (code.length !== 6) { message.warning('请输入 6 位数字验证码'); return }
  totpBusy.value = true
  try {
    const res = await totpStepUp(code, STEP_UP_SCOPE_ACCOUNTS_EXPORT)
    showTotp.value = false
    totpCode.value = ''
    await doExport(res.token) // 一次性授权只在这次重放中生效（旧 useStepUp 语义）
    open.value = false
  } catch (e) {
    message.error(`验证失败：${e.message}`)
  } finally {
    totpBusy.value = false
  }
}

// ===== 导入 =====
const files = ref([])
const parsed = ref(null)
const preview = ref(null)
const fileErrors = ref([])
const parsing = ref(false)
const importing = ref(false)
const result = ref(null)

const isOpen = (f) => f.name.toLowerCase().endsWith('.json') || f.type === 'application/json'
const readText = async (f) => (typeof f.text === 'function' ? f.text() : new Response(f).text())

// 旧 validatePayload：type/version/结构校验
const validate = (data) => {
  if (!data || typeof data !== 'object' || Array.isArray(data)) throw new Error('文件结构无效（需要对象）')
  if (data.type && !['sub2api-data', 'sub2api-bundle'].includes(data.type)) throw new Error(`不支持的 type：${data.type}`)
  if (data.version != null && data.version !== 1) throw new Error(`不支持的版本：v${data.version}`)
  if (!Array.isArray(data.accounts) || !Array.isArray(data.proxies)) throw new Error('缺少 accounts/proxies 数组')
  return data
}

// 旧 mergeDataPayloads：多文件合并
const merge = (list) => list.length === 1 ? list[0] : {
  type: list.find((x) => x.type)?.type,
  version: list.find((x) => x.version != null)?.version,
  exported_at: new Date().toISOString(),
  proxies: list.flatMap((x) => x.proxies),
  accounts: list.flatMap((x) => x.accounts),
}

const selectFiles = async (list) => {
  const incoming = Array.from(list || [])
  const jsons = incoming.filter(isOpen)
  if (!jsons.length) { message.error('请选择 JSON 文件'); return }
  files.value = jsons
  parsed.value = null; preview.value = null; fileErrors.value = []; result.value = null
  parsing.value = true
  try {
    const ok = []
    for (const f of jsons) {
      try { ok.push(validate(JSON.parse(await readText(f)))) }
      catch (e) { fileErrors.value.push({ name: f.name, message: e instanceof SyntaxError ? 'JSON 解析失败' : e.message }) }
    }
    if (!ok.length) { message.error('没有可用的导入文件'); return }
    parsed.value = merge(ok)
    preview.value = {
      accounts: parsed.value.accounts.length,
      proxies: parsed.value.proxies.length,
      version: parsed.value.version ? `v${parsed.value.version}` : '旧版格式',
    }
  } finally {
    parsing.value = false
  }
}

const onImport = async () => {
  if (!parsed.value) { message.error('请先选择并解析文件'); return }
  importing.value = true
  try {
    const res = await request('/admin/accounts/data', {
      method: 'POST',
      body: { data: parsed.value, skip_default_group_bind: true }, // 旧版固定跳过默认分组绑定
    })
    result.value = res
    const p = { account_created: res.account_created, account_failed: res.account_failed, proxy_created: res.proxy_created, proxy_reused: res.proxy_reused, proxy_failed: res.proxy_failed }
    if (res.account_failed > 0 || res.proxy_failed > 0) {
      message.warning(`导入完成（部分失败）：账号 成功${p.account_created}/失败${p.account_failed}，代理 成功${p.proxy_created + p.proxy_reused}/失败${p.proxy_failed}`)
    } else {
      message.success(`导入成功：新增账号 ${p.account_created}，代理 ${p.proxy_created + p.proxy_reused}`)
      emit('imported')
      open.value = false
    }
  } catch (e) {
    message.error(`导入失败：${e.message}`)
  } finally {
    importing.value = false
  }
}

watch(open, (o) => {
  if (o) {
    showTotp.value = false; totpCode.value = ''
    files.value = []; parsed.value = null; preview.value = null; fileErrors.value = []; result.value = null
    includeProxies.value = true; exportScope.value = 'auto'
  }
})
</script>

<template>
  <!-- 导出 -->
  <a-modal v-model:open="open" :title="mode === 'export' ? '导出账号数据' : '导入账号数据'" width="520px" :footer="null">
    <template v-if="mode === 'export'">
      <a-form layout="vertical" size="small">
        <a-form-item label="导出范围">
          <a-radio-group v-model:value="exportScope">
            <a-radio value="auto">自动（有勾选=选中 {{ selectedIds.length }} 个，否则当前筛选）</a-radio>
            <a-radio value="selected">仅选中的 {{ selectedIds.length }} 个</a-radio>
            <a-radio value="filtered">当前筛选全部</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item label="附带代理配置（proxies）">
          <a-switch v-model:checked="includeProxies" size="small" />
        </a-form-item>
        <a-form-item>
          <a-alert type="info" show-icon message="导出包含账号凭据（脱敏形态），属于敏感操作；后端要求二次验证时会先要求输入动态验证码。" />
        </a-form-item>
        <div class="foot">
          <a-button @click="open = false">取消</a-button>
          <a-button type="primary" :loading="exporting" @click="onExport">导出 JSON</a-button>
        </div>
      </a-form>
      <div v-if="showTotp" class="totp">
        <div class="ttitle">二次验证（TOTP）</div>
        <div class="dim" style="margin-bottom: 8px">输入认证器 6 位动态码以继续导出（accounts.export）</div>
        <div class="tinputs">
          <a-input v-model:value="totpCode" :maxlength="6" size="large" placeholder="000000" style="width: 160px; text-align: center; letter-spacing: 8px; font-family: Consolas, monospace" @pressEnter="onTotpVerify" />
        </div>
        <div class="foot">
          <a-button size="small" @click="showTotp = false">取消验证</a-button>
          <a-button size="small" type="primary" :loading="totpBusy" @click="onTotpVerify">验证并导出</a-button>
        </div>
      </div>
    </template>

    <!-- 导入 -->
    <template v-else>
      <a-alert type="warning" show-icon message="导入会创建/更新账号与代理（凭据以文件内容为准），请谨慎操作。" style="margin-bottom: 12px" />
      <a-upload-dragger
        :show-upload-list="false" accept=".json,application/json" :before-upload="() => false"
        @change="(info) => selectFiles(info.fileList.map((f) => f.originFileObj || f))"
      >
        <p class="ant-upload-drag-icon"><InboxOutlined /></p>
        <p class="ant-upload-text">点击或拖拽 JSON 文件到此处（支持多文件合并）</p>
      </a-upload-dragger>
      <div v-if="parsing" class="dim" style="padding: 8px 0">解析中…</div>
      <div v-if="preview || fileErrors.length" class="pv">
        <div class="ptitle">预览</div>
        <div v-if="preview" class="pgrid">
          <div class="pcell"><span>账号</span><b>{{ preview.accounts }}</b></div>
          <div class="pcell"><span>代理</span><b>{{ preview.proxies }}</b></div>
          <div class="pcell"><span>版本</span><b>{{ preview.version }}</b></div>
        </div>
        <div v-if="fileErrors.length" class="ferr">
          <div v-for="fe in fileErrors" :key="fe.name">{{ fe.name }} — {{ fe.message }}</div>
        </div>
      </div>
      <div v-if="result" class="pv">
        <div class="ptitle">导入结果</div>
        <div class="dim">
          账号：成功 {{ result.account_created }} / 失败 {{ result.account_failed }}；
          代理：新建 {{ result.proxy_created }} / 复用 {{ result.proxy_reused }} / 失败 {{ result.proxy_failed }}
        </div>
        <div v-if="(result.errors || []).length" class="ferr" style="max-height: 180px">
          <div v-for="(e, i) in result.errors" :key="i" class="mono">{{ e.kind }} {{ e.name || e.proxy_key || '-' }} — {{ e.message }}</div>
        </div>
        <div class="foot"><a-button type="primary" @click="open = false">关闭</a-button></div>
      </div>
      <div v-else class="foot">
        <a-button @click="open = false">取消</a-button>
        <a-button type="primary" :disabled="!parsed || parsing" :loading="importing" @click="onImport">开始导入</a-button>
      </div>
    </template>
  </a-modal>
</template>

<style scoped>
.foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
.totp { margin-top: 14px; padding: 12px; border-radius: 8px; background: var(--card); }
.ttitle { font-weight: 500; font-size: 13px; margin-bottom: 6px; }
.tinputs { display: flex; justify-content: center; }
.dim { color: var(--text2); font-size: 12px; }
.mono { font-family: Consolas, monospace; }
.pv { margin-top: 12px; padding: 10px 12px; border-radius: 8px; background: var(--card); }
.ptitle { font-weight: 500; font-size: 13px; margin-bottom: 8px; }
.pgrid { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 8px; }
.pcell { display: flex; justify-content: space-between; padding: 6px 10px; border-radius: 6px; background: var(--card); font-size: 13px; }
.pcell span { color: var(--text2); }
.ferr { margin-top: 8px; padding: 8px 10px; border-radius: 6px; background: rgba(207, 48, 48, 0.08); color: #c13438; font-size: 12px; overflow: auto; }
</style>
