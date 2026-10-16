<script setup>
/* ============================================================
 * 备份恢复卡（供「系统配置」页接入，自取数：onMounted fetch）
 * 接口契约：internal/handler/admin/backup_handler.go + routes/admin.go
 * registerBackupRoutes（standard 模式注册；gateway-lite 模式 404 → 整卡降级）：
 * - GET  /admin/backups            备份列表（无需 step-up）
 * - POST /admin/backups            创建备份（step-up backups.create）
 * - POST /admin/backups/:id/restore 恢复（step-up backups.restore + 管理员密码）
 * - DELETE /admin/backups/:id      删除（step-up backups.delete）
 * - GET  /admin/backups/:id/download-url 下载（step-up backups.download）
 * - GET/PUT /admin/backups/s3-config、POST .../s3-config/test（S3 配置）
 * 恢复/删除都是破坏性操作：a-popconfirm 红色警示二次确认；恢复还需再输入管理员密码。
 * 所有请求 404 时显示「当前运行模式不提供此功能（standard 模式专属）」，不弹错误。
 * ============================================================ */
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  CloudServerOutlined,
  CloudUploadOutlined,
  DeleteOutlined,
  DownloadOutlined,
  InfoCircleOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  UndoOutlined,
} from '@ant-design/icons-vue'
import {
  NOT_FOUND_TIP,
  STEP_UP_SCOPE_BACKUP_CREATE,
  STEP_UP_SCOPE_BACKUP_DELETE,
  STEP_UP_SCOPE_BACKUP_DOWNLOAD,
  STEP_UP_SCOPE_BACKUP_RESTORE,
  STEP_UP_SCOPE_BACKUP_S3_READ,
  STEP_UP_SCOPE_BACKUP_S3_UPDATE,
  createBackup,
  deleteBackup,
  getBackupDownloadUrl,
  getS3Config,
  listBackups,
  restoreBackup,
  testS3Connection,
  totpStepUp,
  updateS3Config,
} from './systemApi'

/* ── 卡片级降级（列表 404 即整卡降级） ── */
const notFound = ref(false)
const loading = ref(false)
const items = ref([])
const listErr = ref('')

/* ── 备份列表 ── */
const columns = [
  { title: '时间', key: 'time', width: 180 },
  { title: '类型', key: 'type', width: 130 },
  { title: '大小', key: 'size', width: 90 },
  { title: '状态', key: 'status', width: 150 },
  { title: '操作', key: 'act', width: 200 },
]

const fmtTime = (iso) => (iso ? new Date(iso).toLocaleString() : '-')
const fmtBytes = (n) => {
  const v = Number(n) || 0
  if (v <= 0) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let x = v
  while (x >= 1024 && i < units.length - 1) { x /= 1024; i += 1 }
  return `${x >= 100 ? Math.round(x) : x.toFixed(1)} ${units[i]}`
}
const triggerText = (t) => (t === 'manual' ? '手动' : t === 'scheduled' ? '定时' : t || '-')
const typeText = (r) => (r.backup_type === 'postgres' ? 'PostgreSQL' : r.backup_type || '-')
const statusMap = {
  pending: { color: 'default', text: '等待中' },
  running: { color: 'processing', text: '进行中' },
  completed: { color: 'success', text: '已完成' },
  failed: { color: 'error', text: '失败' },
}
const restoreStatusMap = {
  running: { color: 'processing', text: '恢复中' },
  completed: { color: 'success', text: '已恢复' },
  failed: { color: 'error', text: '恢复失败' },
}

const hasActive = computed(() =>
  items.value.some((r) => ['pending', 'running'].includes(r.status) || r.restore_status === 'running'),
)

/* 备份/恢复是 202 异步任务：启动后轮询列表，直到没有 pending/running 记录 */
let pollTimer = null
let pollRounds = 0
const stopPoll = () => { if (pollTimer) { clearInterval(pollTimer); pollTimer = null } }
const schedulePoll = () => {
  stopPoll()
  pollRounds = 0
  pollTimer = setInterval(async () => {
    pollRounds += 1
    await loadList(true)
    if (!hasActive.value || pollRounds >= 40) stopPoll() // 最多轮询 2 分钟
  }, 3000)
}
onBeforeUnmount(stopPoll)

const loadList = async (silent) => {
  if (!silent) loading.value = true
  try {
    const data = await listBackups()
    items.value = data.items
    listErr.value = ''
  } catch (e) {
    if (e.notFound) { notFound.value = true; return } // 整卡降级，不再提示
    listErr.value = e.message
  } finally {
    loading.value = false
  }
}

/* ── 二次验证（step-up）共享流程：首次 403 STEP_UP_REQUIRED → 弹 TOTP → 换 token 重放一次 ── */
const stepUp = reactive({ open: false, code: '', busy: false, scope: '', run: null })
const openStepUp = (scope, run) => {
  stepUp.scope = scope
  stepUp.run = run
  stepUp.code = ''
  stepUp.open = true
}
/* 非 404/step-up 的统一提示；404 用 info（降级提示而非报错弹窗） */
const showActionError = (e, prefix) => {
  if (e.notFound) message.info(NOT_FOUND_TIP)
  else if (e.stepUpBlocked) message.error(e.message || '二次验证不可用')
  else message.error(`${prefix}：${e.message}`)
}
const verifyStepUp = async () => {
  const code = stepUp.code.replace(/\D/g, '')
  if (code.length !== 6) { message.warning('请输入 6 位数字验证码'); return }
  stepUp.busy = true
  try {
    const grant = await totpStepUp(code, stepUp.scope)
    stepUp.open = false
    const run = stepUp.run
    try {
      await run(grant.token) // 一次性授权只在这次重放中生效
      stepUp.run = null
    } catch (e) {
      if (e.stepUpRequired) { stepUp.open = true; message.info('授权已过期，请重新输入验证码') }
      else { stepUp.run = null; showActionError(e, '操作失败') }
    }
  } catch (e) {
    showActionError(e, '验证失败')
  } finally {
    stepUp.busy = false
  }
}

/* ── 创建备份 ── */
const creating = ref(false)
const doCreate = async (token) => {
  creating.value = true
  try {
    // expire_days 不传 → 后端默认 14 天过期（CreateBackupRequest）；202 异步任务
    await createBackup(undefined, token)
    message.success('备份任务已启动')
    await loadList(true)
    schedulePoll()
  } finally {
    creating.value = false
  }
}
const onCreate = async () => {
  try {
    await doCreate()
  } catch (e) {
    if (e.stepUpRequired) openStepUp(STEP_UP_SCOPE_BACKUP_CREATE, doCreate)
    else showActionError(e, '创建备份失败')
  }
}

/* ── 恢复（popconfirm 红色警示 → 管理员密码 → step-up） ── */
const restore = reactive({ open: false, busy: false, record: null, password: '' })
const openRestore = (record) => {
  restore.record = record
  restore.password = ''
  restore.open = true
}
const doRestore = async (token) => {
  restore.busy = true
  try {
    await restoreBackup(restore.record.id, restore.password, token)
    restore.open = false
    message.success('恢复任务已启动，将在后台执行，期间请勿进行其他管理操作')
    await loadList(true)
    schedulePoll()
  } finally {
    restore.busy = false
  }
}
const submitRestore = async () => {
  if (!restore.password) { message.warning('请输入管理员密码'); return }
  try {
    await doRestore()
  } catch (e) {
    if (e.stepUpRequired) openStepUp(STEP_UP_SCOPE_BACKUP_RESTORE, doRestore)
    else showActionError(e, '恢复失败')
  }
}

/* ── 删除 ── */
const doDelete = async (record, token) => {
  await deleteBackup(record.id, token)
  message.success('备份已删除')
  await loadList(true)
}
const onDelete = async (record) => {
  try {
    await doDelete(record)
  } catch (e) {
    if (e.stepUpRequired) openStepUp(STEP_UP_SCOPE_BACKUP_DELETE, (token) => doDelete(record, token))
    else showActionError(e, '删除失败')
  }
}

/* ── 下载（预签名 URL；大备份是分卷 parts） ── */
const doDownload = async (record, token) => {
  const data = await getBackupDownloadUrl(record.id, token)
  const urls = data?.parts?.length ? data.parts.map((p) => p.url) : data?.url ? [data.url] : []
  if (!urls.length) { message.warning('该备份没有可下载的文件'); return }
  urls.forEach((u, i) => setTimeout(() => {
    const a = document.createElement('a')
    a.href = u
    a.target = '_blank'
    a.rel = 'noopener'
    a.click()
  }, i * 400))
  message.success(urls.length > 1 ? `已开始下载（${urls.length} 个分卷）` : '已开始下载')
}
const onDownload = async (record) => {
  try {
    await doDownload(record)
  } catch (e) {
    if (e.stepUpRequired) openStepUp(STEP_UP_SCOPE_BACKUP_DOWNLOAD, (token) => doDownload(record, token))
    else showActionError(e, '获取下载链接失败')
  }
}

/* ── S3 存储配置（GET 回 presence 位，凭据留空 = 保留原值） ── */
const s3 = reactive({
  loaded: false,
  loading: false,
  stepUpRequired: false,
  err: '',
  accessKeyIdConfigured: false,
  secretAccessKeyConfigured: false,
  saving: false,
  testing: false,
})
const s3form = reactive({
  endpoint: '',
  region: '',
  bucket: '',
  prefix: '',
  force_path_style: false,
  access_key_id: '',
  secret_access_key: '',
})
const fillS3Form = (cfg) => {
  s3form.endpoint = cfg.endpoint || ''
  s3form.region = cfg.region || ''
  s3form.bucket = cfg.bucket || ''
  s3form.prefix = cfg.prefix || ''
  s3form.force_path_style = Boolean(cfg.force_path_style)
  // 凭据不回显（服务端只回配置位），留空即保存原值
  s3form.access_key_id = ''
  s3form.secret_access_key = ''
  s3.accessKeyIdConfigured = Boolean(cfg.access_key_id_configured)
  s3.secretAccessKeyConfigured = Boolean(cfg.secret_access_key_configured)
}
const loadS3 = async (token) => {
  s3.loading = true
  s3.err = ''
  try {
    fillS3Form(await getS3Config(token))
    s3.loaded = true
    s3.stepUpRequired = false
  } catch (e) {
    if (e.notFound) { notFound.value = true; return }
    if (e.stepUpRequired) { s3.stepUpRequired = true; return }
    s3.err = e.message
  } finally {
    s3.loading = false
  }
}
const s3Payload = () => ({
  endpoint: s3form.endpoint.trim(),
  region: s3form.region.trim(),
  bucket: s3form.bucket.trim(),
  prefix: s3form.prefix.trim(),
  force_path_style: s3form.force_path_style,
  access_key_id: s3form.access_key_id.trim(), // 留空 = 保留原值（后端约定）
  secret_access_key: s3form.secret_access_key, // 留空 = 保留原值（后端约定）
})
const saveS3 = async (token) => {
  if (!s3form.endpoint.trim() || !s3form.bucket.trim()) { message.warning('Endpoint 与 Bucket 为必填项'); return }
  if (!s3.accessKeyIdConfigured && !s3form.access_key_id.trim()) { message.warning('Access Key ID 未配置，请填写'); return }
  if (!s3.secretAccessKeyConfigured && !s3form.secret_access_key) { message.warning('Secret Access Key 未配置，请填写'); return }
  s3.saving = true
  try {
    fillS3Form(await updateS3Config(s3Payload(), token))
    message.success('S3 配置已保存')
  } finally {
    s3.saving = false
  }
}
const onSaveS3 = async () => {
  try {
    await saveS3()
  } catch (e) {
    if (e.stepUpRequired) openStepUp(STEP_UP_SCOPE_BACKUP_S3_UPDATE, saveS3)
    else showActionError(e, '保存失败')
  }
}
const testS3 = async (token) => {
  s3.testing = true
  try {
    // 后端对连接失败也返回 HTTP 200 + { ok:false, message }（脱敏结论）
    const res = await testS3Connection(s3Payload(), token)
    if (res?.ok) message.success('S3 连接成功')
    else message.warning(res?.message || 'S3 连接失败，请检查 Endpoint、Bucket 与凭据')
  } finally {
    s3.testing = false
  }
}
const onTestS3 = async () => {
  try {
    await testS3()
  } catch (e) {
    if (e.stepUpRequired) openStepUp(STEP_UP_SCOPE_BACKUP_S3_UPDATE, testS3)
    else showActionError(e, '测试连接失败')
  }
}

onMounted(async () => {
  await loadList()
  if (!notFound.value) await loadS3()
})
</script>

<template>
  <div class="card backup-card">
    <!-- 运行模式降级：gateway-lite 未注册备份路由（404） -->
    <div v-if="notFound" class="na">
      <InfoCircleOutlined class="na-icon" />
      <div class="na-text">{{ NOT_FOUND_TIP }}</div>
      <div class="na-sub">数据库备份恢复由 standard 模式提供，当前运行模式未注册相关接口。</div>
    </div>

    <template v-else>
      <div class="head">
        <div>
          <div class="card-title">备份与恢复</div>
          <div class="card-sub">数据库备份到 S3 兼容对象存储，支持手动与定时任务。</div>
        </div>
        <a-space :size="8">
          <a-button size="small" :loading="loading" @click="loadList()">
            <template #icon><ReloadOutlined /></template>
            刷新
          </a-button>
          <a-button type="primary" size="small" :loading="creating" @click="onCreate">
            <template #icon><CloudUploadOutlined /></template>
            立即备份
          </a-button>
        </a-space>
      </div>

      <div v-if="listErr" class="hint" style="margin-bottom: 8px">
        备份列表读取失败：{{ listErr }}
        <a class="retry" @click="loadList()">重试</a>
      </div>

      <!-- 备份列表：小表格（时间/类型/大小/状态/操作），窄屏横向滚动 -->
      <a-table
        class="tbl" :columns="columns" :data-source="items" row-key="id" size="small"
        :loading="loading && !items.length" :scroll="{ x: 'max-content' }"
        :pagination="{ pageSize: 8, size: 'small', showSizeChanger: false, hideOnSinglePage: true }"
        :locale="{ emptyText: '暂无备份记录' }"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'time'">
            <div>{{ fmtTime(record.started_at) }}</div>
            <div class="dim">{{ record.file_name }}</div>
          </template>
          <template v-else-if="column.key === 'type'">
            <div>{{ typeText(record) }}</div>
            <div class="dim">{{ triggerText(record.triggered_by) }}</div>
          </template>
          <template v-else-if="column.key === 'size'">{{ fmtBytes(record.size_bytes) }}</template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="statusMap[record.status]?.color || 'default'" style="margin: 0">
              {{ statusMap[record.status]?.text || record.status }}
            </a-tag>
            <a-tag
              v-if="record.restore_status" :color="restoreStatusMap[record.restore_status]?.color"
              style="margin: 4px 0 0"
            >
              {{ restoreStatusMap[record.restore_status]?.text || record.restore_status }}
            </a-tag>
            <div v-if="record.status === 'failed' && record.error_message" class="dim err-dim" :title="record.error_message">
              {{ record.error_message }}
            </div>
          </template>
          <template v-else-if="column.key === 'act'">
            <a-space :size="4" wrap>
              <!-- 恢复：红色警示二次确认（确认后再输入管理员密码） -->
              <a-popconfirm
                title="确认恢复该备份？" ok-text="确认恢复" cancel-text="取消"
                :ok-button-props="{ danger: true }" @confirm="openRestore(record)"
              >
                <template #description>
                  恢复将用该备份覆盖当前数据库，不可撤销。<br />确认后还需输入管理员密码。
                </template>
                <a-button size="small" danger :disabled="record.status !== 'completed'">
                  <template #icon><UndoOutlined /></template>
                  恢复
                </a-button>
              </a-popconfirm>
              <a-button size="small" :disabled="record.status !== 'completed'" @click="onDownload(record)">
                <template #icon><DownloadOutlined /></template>
                下载
              </a-button>
              <!-- 删除：红色警示二次确认 -->
              <a-popconfirm
                title="确认删除该备份？" ok-text="确认删除" cancel-text="取消"
                :ok-button-props="{ danger: true }" @confirm="onDelete(record)"
              >
                <template #description>删除后备份文件将从对象存储移除，不可恢复。</template>
                <a-button size="small" danger type="text">
                  <template #icon><DeleteOutlined /></template>
                  删除
                </a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>

      <div class="hr-line" />

      <!-- S3 存储配置 -->
      <div class="head">
        <div>
          <div class="card-title"><CloudServerOutlined /> S3 存储配置</div>
          <div class="card-sub">备份文件上传到 S3 兼容对象存储（如 Cloudflare R2，Region 填 auto）。</div>
        </div>
      </div>

      <!-- 读取需要 step-up（backups.s3.read）：提供验证入口而不是报错 -->
      <div v-if="s3.stepUpRequired" class="hint verify-hint">
        <SafetyCertificateOutlined /> 读取存储配置需要二次验证
        <a-button size="small" @click="openStepUp(STEP_UP_SCOPE_BACKUP_S3_READ, loadS3)">输入验证码读取</a-button>
      </div>
      <div v-else-if="s3.err" class="hint">
        存储配置读取失败：{{ s3.err }} <a class="retry" @click="loadS3()">重试</a>
      </div>
      <a-spin v-else :spinning="s3.loading && !s3.loaded" size="small">
        <div v-if="s3.loaded" class="form">
          <div class="frow">
            <label>Endpoint</label>
            <a-input v-model:value="s3form.endpoint" class="fld" placeholder="https://<account>.r2.cloudflarestorage.com" />
          </div>
          <div class="frow">
            <label>Region</label>
            <a-input v-model:value="s3form.region" class="fld" placeholder="R2 填 auto" />
          </div>
          <div class="frow">
            <label>Bucket</label>
            <a-input v-model:value="s3form.bucket" class="fld" />
          </div>
          <div class="frow">
            <label>Key 前缀</label>
            <a-input v-model:value="s3form.prefix" class="fld" placeholder="backups/" />
          </div>
          <div class="frow">
            <label>Access Key ID</label>
            <div class="fmain">
              <a-input v-model:value="s3form.access_key_id" class="fld"
                :placeholder="s3.accessKeyIdConfigured ? '已配置，留空表示不修改' : '未配置'" />
              <span class="state" :class="{ ok: s3.accessKeyIdConfigured }">{{ s3.accessKeyIdConfigured ? '已配置' : '未配置' }}</span>
            </div>
          </div>
          <div class="frow">
            <label>Secret Access Key</label>
            <div class="fmain">
              <a-input-password v-model:value="s3form.secret_access_key" class="fld" autocomplete="new-password"
                :placeholder="s3.secretAccessKeyConfigured ? '已配置，留空表示不修改' : '未配置'" />
              <span class="state" :class="{ ok: s3.secretAccessKeyConfigured }">{{ s3.secretAccessKeyConfigured ? '已配置' : '未配置' }}</span>
            </div>
          </div>
          <div class="frow">
            <label>Path Style 寻址</label>
            <div class="fmain">
              <a-switch v-model:checked="s3form.force_path_style" />
              <span class="state">自建 MinIO 等兼容存储需要开启</span>
            </div>
          </div>
        </div>
      </a-spin>

      <div v-if="s3.loaded" class="savebar">
        <a-button :loading="s3.testing" @click="onTestS3">测试连接</a-button>
        <a-button type="primary" class="save" :loading="s3.saving" @click="onSaveS3">保存配置</a-button>
      </div>
    </template>

    <!-- 二次验证（step-up）弹窗：验证码换一次性授权 token -->
    <a-modal
      :open="stepUp.open" title="二次验证" :footer="null" :mask-closable="false" :keyboard="false"
      :closable="!stepUp.busy" width="360px" @cancel="stepUp.open = false"
    >
      <div class="totp">
        <div class="hint">该操作需要管理员双因素验证，请输入认证器中的 6 位动态验证码。</div>
        <a-input
          v-model:value="stepUp.code" size="large" :maxlength="6" inputmode="numeric"
          placeholder="6 位验证码" class="totp-input" @press-enter="verifyStepUp"
        />
        <div class="totp-btns">
          <a-button :disabled="stepUp.busy" @click="stepUp.open = false">取消</a-button>
          <a-button type="primary" :loading="stepUp.busy" @click="verifyStepUp">验证</a-button>
        </div>
      </div>
    </a-modal>

    <!-- 恢复弹窗：管理员密码（handler 要求 body.password） -->
    <a-modal
      v-model:open="restore.open" title="恢复备份" width="440px" ok-text="确认恢复" cancel-text="取消"
      :confirm-loading="restore.busy" :ok-button-props="{ danger: true }" @ok="submitRestore"
    >
      <a-alert
        type="error" show-icon
        message="危险操作"
        description="恢复将清空并覆盖当前数据库，操作不可撤销，服务可能短暂中断。"
        style="margin-bottom: 12px"
      />
      <div v-if="restore.record" class="restore-info">
        <div><span class="dim">备份时间</span>{{ fmtTime(restore.record.started_at) }}</div>
        <div><span class="dim">文件名</span>{{ restore.record.file_name || '-' }}</div>
        <div><span class="dim">大小</span>{{ fmtBytes(restore.record.size_bytes) }}</div>
      </div>
      <div class="restore-pwd">
        <div class="hint">为防止误操作，请输入当前管理员密码以继续：</div>
        <a-input-password
          v-model:value="restore.password" autocomplete="current-password"
          placeholder="管理员密码" @press-enter="submitRestore"
        />
      </div>
    </a-modal>
  </div>
</template>

<style scoped>
@import './settings.css';

/* 任务规范：区块标题 500（settings.css 默认 600，这里按 Win12 卡片规范覆盖） */
.card-title { font-weight: 500; }
.card-sub { color: var(--text2); }

.head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }

.hr-line { height: 1px; background: var(--hr); opacity: 0.55; margin: 14px 0; }

/* 备份小表格：Win12 表格规范（无竖向分隔线、行 hover 由 antd 主题保证） */
.tbl { margin-top: 8px; }
.tbl :deep(.ant-table-thead > tr > th) { font-weight: 500; }
.tbl :deep(.ant-table-cell) { padding-top: 6px; padding-bottom: 6px; }
.dim { font-size: 11.5px; color: var(--text2); margin-top: 2px; }
.err-dim { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.retry { margin-left: 6px; cursor: pointer; }
.state { font-size: 12px; color: var(--text2); }
.state.ok { color: #3f9b62; }
.verify-hint { display: flex; align-items: center; gap: 10px; padding: 6px 0 10px; }

/* 二次验证弹窗 */
.totp { display: flex; flex-direction: column; gap: 12px; }
.totp-input { text-align: center; letter-spacing: 6px; font-size: 18px; }
.totp-btns { display: flex; justify-content: flex-end; gap: 8px; }

/* 恢复弹窗 */
.restore-info { display: flex; flex-direction: column; gap: 4px; font-size: 13px; margin-bottom: 12px; }
.restore-info .dim { margin-right: 10px; margin-top: 0; }
.restore-pwd { display: flex; flex-direction: column; gap: 6px; }

/* 运行模式降级提示 */
.na { padding: 34px 16px; text-align: center; }
.na-icon { font-size: 28px; color: var(--text2); }
.na-text { margin-top: 10px; font-size: 13px; }
.na-sub { margin-top: 4px; font-size: 12px; color: var(--text2); }

/* 手机端：表单单列（settings.css 媒体查询处理 frow），标题行纵向堆叠 */
@media (max-width: 760px) {
  .head { flex-direction: column; }
  .head > a-space { align-self: flex-end; }
}
</style>
