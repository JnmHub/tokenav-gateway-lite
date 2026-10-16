<script setup>
/* ============================================================
 * 镜像任务配置弹窗（自包含）：配置读取/编辑/校验/保存 + 保存后
 * 自动重启网关并轮询恢复。从 ImageTasks.vue 拆出（600 行约束）。
 * 接口契约见 ImageTasks.vue 文件头注释（GET/PUT /admin/image-tasks/config、
 * POST /admin/system/restart）。
 * 对外：v-model:open；emit state(configState)/restarting(bool)/
 *       message({ok,err,degraded})/changed（保存生效后父刷新列表）；
 *       expose retry()（重启失败横幅的「重新启动并检查」）。
 * ============================================================ */
import { ref, computed, watch } from 'vue'
import { request, getToken } from '../../api'
import { message, Modal } from 'ant-design-vue'

const props = defineProps({ open: { type: Boolean, default: false } })
const emit = defineEmits(['update:open', 'state', 'restarting', 'message', 'changed'])

const open = computed({
  get: () => props.open,
  set: (v) => emit('update:open', v),
})
const syncOpen = (v) => emit('update:open', v)

/* ---------- 配置状态 ---------- */
const cfgLoading = ref(false)
const cfgSaving = ref(false)
const cfgError = ref('')
const configState = ref(null)
const draft = ref(null)
const generateKey = ref(false)
const s3AccessKeyID = ref('')
const s3SecretAccessKey = ref('')

/* 重启流程状态（横幅在父页面渲染，状态经 emit 同步） */
const serviceRestarting = ref(false)
const restartErrMsg = ref('')
const restartDegraded = ref(false)
const pendingDesiredEnabled = ref(false)
const okMsg = ref('')

const syncState = () => emit('state', configState.value)
const syncRestarting = () => emit('restarting', serviceRestarting.value)
const syncMessage = () => emit('message', { ok: okMsg.value, err: restartErrMsg.value, degraded: restartDegraded.value })

/* 窄屏适配：配置弹窗宽度 */
const narrow = ref(typeof window !== 'undefined' && window.innerWidth < 640)
const onResize = () => { narrow.value = window.innerWidth < 640 }
if (typeof window !== 'undefined') window.addEventListener('resize', onResize)

/* ---------- 预设档位与枚举 ---------- */
const PRESETS = {
  light: { label: '轻量', hint: '1 线程 · 128 队列', values: { worker_count: 1, queue_capacity: 128, claim_batch_size: 8, per_user_concurrency: 1 } },
  standard: { label: '标准（推荐）', hint: '2 线程 · 256 队列', values: { worker_count: 2, queue_capacity: 256, claim_batch_size: 16, per_user_concurrency: 1 } },
  high: { label: '高吞吐', hint: '4 线程 · 1024 队列', values: { worker_count: 4, queue_capacity: 1024, claim_batch_size: 32, per_user_concurrency: 2 } },
}
const IMAGE_COUNTS = [1, 4, 8, 16]
const RETENTION_DAYS = [1, 7, 30, 90]
const mib = 1024 * 1024

const activePreset = computed(() => {
  const v = draft.value
  if (!v) return ''
  for (const [key, p] of Object.entries(PRESETS)) {
    if (Object.entries(p.values).every(([k, val]) => v[k] === val)) return key
  }
  return ''
})
const applyPreset = (key) => { if (draft.value) Object.assign(draft.value, PRESETS[key].values) }

const encryptionReady = computed(() => Boolean(configState.value?.encryption_key_configured || generateKey.value))
/* 环境变量托管或配置文件不可写时禁止保存（避免假成功） */
const cfgLocked = computed(() => Boolean(configState.value?.read_only || !configState.value?.config_writable))

/* MiB 双向换算（后端单位为字节） */
const maxRequestMiB = computed({ get: () => Number(((draft.value?.max_request_bytes || 0) / mib).toFixed(3)), set: (v) => { if (draft.value) draft.value.max_request_bytes = Math.round(Number(v || 0) * mib) } })
const maxArtifactMiB = computed({ get: () => Number(((draft.value?.max_artifact_bytes || 0) / mib).toFixed(2)), set: (v) => { if (draft.value) draft.value.max_artifact_bytes = Math.round(Number(v || 0) * mib) } })
const maxTaskArtifactMiB = computed({ get: () => Number(((draft.value?.max_task_artifact_bytes || 0) / mib).toFixed(2)), set: (v) => { if (draft.value) draft.value.max_task_artifact_bytes = Math.round(Number(v || 0) * mib) } })

/* 保存前校验：规则逐条照抄旧 ImageTaskConfigDialog（与后端 ValidateImageTasks 一致） */
const inRange = (v, min, max) => Number.isFinite(v) && v >= min && v <= max
const validationErrors = computed(() => {
  const v = draft.value
  if (!v) return []
  const es = []
  if (!inRange(v.worker_count, 1, 64)) es.push('工作线程必须在 1–64 之间。')
  if (!inRange(v.queue_capacity, 1, 100000)) es.push('队列容量必须在 1–100000 之间。')
  if (!inRange(v.claim_batch_size, 1, 500)) es.push('领取批次必须在 1–500 之间。')
  if (!inRange(v.per_user_concurrency, 1, 16)) es.push('单用户并发必须在 1–16 之间。')
  if (!inRange(v.max_images_per_task, 1, 16)) es.push('单任务图片数必须在 1–16 之间。')
  if (!inRange(v.max_attempts, 1, 10)) es.push('最大尝试次数必须在 1–10 之间。')
  if (!inRange(v.execution_timeout_seconds, 10, 3600)) es.push('执行超时必须在 10–3600 秒之间。')
  if (v.claim_ttl_seconds < v.execution_timeout_seconds + 180 || v.claim_ttl_seconds > 7200) es.push('任务锁定时长必须比执行超时至少多 180 秒，且不能超过 7200 秒。')
  if (!inRange(v.poll_interval_milliseconds, 100, 60000)) es.push('轮询间隔必须在 100–60000 毫秒之间。')
  if (!inRange(v.result_retention_days, 1, 90)) es.push('结果保留必须在 1–90 天之间。')
  if (!inRange(v.download_url_ttl_seconds, 60, 86400)) es.push('下载链接有效期必须在 60–86400 秒之间。')
  if (!inRange(v.max_request_bytes, 16 * 1024, 4 * mib)) es.push('请求体上限必须在 16 KiB–4 MiB 之间。')
  if (!inRange(v.max_artifact_bytes, mib, 100 * mib)) es.push('单图上限必须在 1–100 MiB 之间。')
  if (v.max_task_artifact_bytes < v.max_artifact_bytes || v.max_task_artifact_bytes > 500 * mib) es.push('单任务产物上限不能小于单图上限，且不能超过 500 MiB。')
  if (v.enabled) {
    if (!encryptionReady.value) {
      es.push(configState.value?.encryption_key_read_only
        ? '固定加密密钥由环境变量托管，请先在服务器中配置。'
        : '请先选择「一键生成」固定加密密钥。')
    }
    if (v.storage.type === 'local' && !v.storage.local_path.trim()) es.push('请填写本机存储目录。')
    if (v.storage.type === 's3') {
      if (!v.storage.s3.bucket.trim()) es.push('请填写 S3 / MinIO 存储桶。')
      if (!configState.value?.desired.storage.s3.access_key_id_configured && !s3AccessKeyID.value.trim()) es.push('请填写 Access Key ID。')
      if (!configState.value?.desired.storage.s3.secret_access_key_configured && !s3SecretAccessKey.value) es.push('请填写 Secret Access Key。')
    }
  }
  return es
})
const canSave = computed(() => Boolean(draft.value && !cfgLoading.value && !cfgSaving.value && !cfgLocked.value && validationErrors.value.length === 0))

function cloneDesired(src) {
  return {
    ...src,
    storage: {
      type: src.storage?.type === 's3' ? 's3' : 'local',
      local_path: src.storage?.local_path || './data/image-tasks',
      s3: {
        endpoint: src.storage?.s3?.endpoint || '',
        region: src.storage?.s3?.region || 'auto',
        bucket: src.storage?.s3?.bucket || '',
        prefix: src.storage?.s3?.prefix || 'image-tasks',
        force_path_style: Boolean(src.storage?.s3?.force_path_style),
        access_key_id_configured: Boolean(src.storage?.s3?.access_key_id_configured),
        secret_access_key_configured: Boolean(src.storage?.s3?.secret_access_key_configured),
      },
    },
  }
}
function resetDraft() {
  draft.value = configState.value?.desired ? cloneDesired(configState.value.desired) : null
  generateKey.value = false
  s3AccessKeyID.value = ''
  s3SecretAccessKey.value = ''
  cfgError.value = ''
}

async function loadConfig() {
  cfgLoading.value = true
  cfgError.value = ''
  try {
    configState.value = await request('/admin/image-tasks/config', { method: 'GET' })
    syncState()
  } catch (e) { cfgError.value = e.message } finally { cfgLoading.value = false }
}
/* 弹窗每次打开、或配置 revision 变化（保存/恢复后）时重置草稿 */
watch(() => props.open, (o) => { if (o) loadConfig() })
watch([() => props.open, () => configState.value?.revision], ([o]) => { if (o) resetDraft() }, { immediate: true })

/* 组装 PUT 请求体（字段全集照旧实现；S3 密钥留空表示不修改已配置值） */
function buildPayload() {
  const v = draft.value
  return {
    expected_revision: configState.value.revision,
    generate_encryption_key: generateKey.value,
    desired: {
      enabled: v.enabled,
      worker_count: v.worker_count,
      queue_capacity: v.queue_capacity,
      claim_batch_size: v.claim_batch_size,
      per_user_concurrency: v.per_user_concurrency,
      max_images_per_task: v.max_images_per_task,
      max_attempts: v.max_attempts,
      execution_timeout_seconds: v.execution_timeout_seconds,
      claim_ttl_seconds: v.claim_ttl_seconds,
      poll_interval_milliseconds: v.poll_interval_milliseconds,
      result_retention_days: v.result_retention_days,
      download_url_ttl_seconds: v.download_url_ttl_seconds,
      max_request_bytes: v.max_request_bytes,
      max_artifact_bytes: v.max_artifact_bytes,
      max_task_artifact_bytes: v.max_task_artifact_bytes,
      storage: {
        type: v.storage.type,
        local_path: v.storage.local_path.trim(),
        s3: {
          endpoint: v.storage.s3.endpoint.trim(),
          region: v.storage.s3.region.trim() || 'auto',
          bucket: v.storage.s3.bucket.trim(),
          prefix: v.storage.s3.prefix.trim() || 'image-tasks',
          force_path_style: v.storage.s3.force_path_style,
          access_key_id: s3AccessKeyID.value.trim() || '',
          secret_access_key: s3SecretAccessKey.value || '',
        },
      },
    },
  }
}

/* 危险操作确认：保存会先写配置文件，再自动重启整个 tokenav gateway */
function submitConfig() {
  if (!canSave.value) return
  const payload = buildPayload()
  Modal.confirm({
    title: '保存并重启 tokenav gateway？',
    content: '配置将写入 config.yaml，随后网关自动重启以加载新配置。重启期间（通常 10–60 秒）所有经过网关的请求都会中断，正在执行的任务可能失败。确认继续？',
    okText: '保存并重启', okType: 'danger', cancelText: '取消',
    onOk: () => saveConfig(payload),
  })
}

async function saveConfig(payload) {
  cfgSaving.value = true
  cfgError.value = ''
  restartErrMsg.value = ''
  restartDegraded.value = false
  okMsg.value = ''
  syncMessage()
  try {
    configState.value = await request('/admin/image-tasks/config', { method: 'PUT', body: payload })
    syncState()
    pendingDesiredEnabled.value = configState.value.desired.enabled
    syncOpen(false)
    if (configState.value.restart_required) {
      await restartAndWaitForRecovery(pendingDesiredEnabled.value)
    } else {
      okMsg.value = '镜像任务配置已保存并已生效。'
      syncMessage()
      emit('changed')
    }
  } catch (e) {
    cfgError.value = e.message
    message.error(`保存配置失败：${e.message}`)
  } finally {
    cfgSaving.value = false
  }
}

/* ============================================================
 * 重启 → 轮询恢复
 * 1) POST /admin/system/restart：服务开始退出时连接中断属正常现象，
 *    只有带 HTTP 状态的明确错误才视为重启失败；
 * 2) 先等 1.5 秒，再在 60 秒窗口内轮询 /admin/image-tasks/config：
 *    - 配置接口走 /api 代理（dev 与生产 embed 都可用；/health 在 dev
 *      代理下不可达，故不用）；
 *    - 仅健康恢复还不够：必须确认新进程已加载目标配置
 *      （!restart_required 且 effective_enabled === 期望值）才算成功；
 *    - 断连/超时按预期重试，每次请求 2.5 秒超时；
 *    - degraded_reason 表示图片任务启动失败但网关其余功能正常，
 *      系统已安全保持关闭，此时报错并允许返回配置页。
 * ============================================================ */
async function fetchConfigOnce(timeoutMs) {
  const ctl = new AbortController()
  const timer = setTimeout(() => ctl.abort(), timeoutMs)
  try {
    const headers = { 'Content-Type': 'application/json' }
    const token = getToken()
    if (token) headers.Authorization = `Bearer ${token}`
    const res = await fetch('/api/v1/admin/image-tasks/config', { method: 'GET', headers, cache: 'no-store', signal: ctl.signal })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const payload = await res.json()
    if (payload && typeof payload === 'object' && 'code' in payload) {
      if (payload.code !== 0) throw new Error(payload.message || `HTTP ${res.status}`)
      return payload.data
    }
    return payload
  } finally {
    clearTimeout(timer)
  }
}
const delay = (ms) => new Promise((r) => setTimeout(r, ms))

async function restartAndWaitForRecovery(expectedEnabled) {
  if (serviceRestarting.value) return
  serviceRestarting.value = true
  restartErrMsg.value = ''
  syncRestarting()
  try {
    try {
      await request('/admin/system/restart', { method: 'POST' })
    } catch (e) {
      // 网络级错误（Failed to fetch 等）= 服务正在退出，属预期；HTTP 状态错误才是真失败
      if (/HTTP \d+/.test(e.message)) throw e
    }
    const recovered = await waitForServiceRecovery(expectedEnabled)
    configState.value = recovered
    syncState()
    okMsg.value = 'tokenav gateway 已重启，镜像任务配置已生效。'
    syncMessage()
    emit('changed')
  } catch (e) {
    restartErrMsg.value = /超时/.test(e.message) ? e.message : (e.message || '配置已经保存，但服务未在 60 秒内恢复，请检查网关状态后重试。')
    syncMessage()
  } finally {
    serviceRestarting.value = false
    syncRestarting()
  }
}

async function waitForServiceRecovery(expectedEnabled) {
  await delay(1500)
  const deadline = Date.now() + 60000
  while (Date.now() < deadline) {
    try {
      const current = await fetchConfigOnce(Math.min(2500, Math.max(1, deadline - Date.now())))
      if (current.degraded_reason) {
        configState.value = current
        syncState()
        restartDegraded.value = true
        syncMessage()
        throw new Error(`镜像任务启动失败，但 tokenav gateway 其余功能正常，系统已安全保持关闭：${current.degraded_reason}`)
      }
      if (!current.restart_required && current.effective_enabled === expectedEnabled) return current
    } catch (e) {
      if (restartDegraded.value) throw e
      // 重启窗口内连接失败是预期状态，继续轮询
    }
    const remaining = deadline - Date.now()
    if (remaining > 0) await delay(Math.min(1000, remaining))
  }
  throw new Error('服务恢复超时，配置仍保留在配置文件中。')
}

async function retry() {
  restartErrMsg.value = ''
  if (restartDegraded.value) {
    // 降级：队列被安全关闭，回配置页查看 degraded 原因
    restartDegraded.value = false
    syncMessage()
    syncOpen(true)
    await loadConfig()
    return
  }
  await restartAndWaitForRecovery(pendingDesiredEnabled.value)
}
defineExpose({ retry })
</script>

<template>
  <a-modal :open="open" title="镜像任务配置" :width="narrow ? '100%' : 760" :footer="null" :mask-closable="false" @update:open="syncOpen">
    <a-spin :spinning="cfgLoading">
      <div v-if="!draft || !configState" class="cfgFail">
        <p>配置读取失败</p>
        <p class="cfgFailMsg">{{ cfgError || '请检查 tokenav gateway 状态后重试。' }}</p>
        <a-button size="small" @click="loadConfig">重试</a-button>
      </div>
      <div v-else class="cfgBody">
        <!-- 状态卡 -->
        <div class="statusCards">
          <div class="statusCard">
            <span class="dot" :class="configState.effective_enabled ? 'ok' : 'warn'" />
            <div><p>当前进程</p><strong>{{ configState.effective_enabled ? '队列运行中' : '队列未运行' }}</strong></div>
          </div>
          <div class="statusCard">
            <span class="dot" :class="encryptionReady ? 'ok' : 'warn'" />
            <div><p>固定加密密钥</p><strong>{{ encryptionReady ? '已准备' : '尚未配置' }}</strong></div>
          </div>
          <div class="statusCard">
            <span class="dot pri" />
            <div><p>产物存储</p><strong>{{ draft.storage.type === 's3' ? 'S3 / MinIO' : '本机目录' }}</strong></div>
          </div>
        </div>

        <a-alert v-if="cfgError && !cfgLoading" type="error" show-icon class="band" message="保存失败" :description="cfgError" />
        <a-alert v-if="cfgLocked" type="warning" show-icon class="band"
          :message="configState.read_only ? '当前配置由环境变量托管，只能查看' : '配置文件当前不可写，只能查看'"
          :description="configState.read_only ? '请在服务器环境变量中修改后重启 tokenav gateway，页面不会提供无效的保存按钮。' : '请先修复配置文件的所属用户或写入权限，再返回此页面保存。'" />
        <a-alert v-if="configState.degraded_reason" type="error" show-icon class="band" message="当前配置处于降级状态" :description="configState.degraded_reason" />

        <fieldset class="cfgForm" :disabled="cfgLocked || cfgSaving">
          <!-- 启用 -->
          <div class="settingRow">
            <div>
              <h4>启用镜像任务</h4>
              <p>关闭时普通同步生图不受影响；保存不会自动替你打开此开关。</p>
            </div>
            <a-switch v-model:checked="draft.enabled" />
          </div>
          <a-alert v-if="draft.enabled && !configState.encryption_key_configured" type="warning" class="band" message="启用前需要固定加密密钥"
            description="点击一次即可让服务端在保存时安全生成；密钥原文不会返回到浏览器。">
            <template #action>
              <a-button size="small" :type="generateKey ? 'default' : 'primary'" :disabled="configState.encryption_key_read_only" @click="generateKey = !generateKey">
                {{ configState.encryption_key_read_only ? '请在环境变量中配置' : generateKey ? '已选择保存时生成' : '一键生成' }}
              </a-button>
            </template>
          </a-alert>

          <div class="hr" />

          <!-- 运行规模：预设档位 -->
          <h4 class="secTitle">运行规模</h4>
          <p class="secHint">直接选择常用档位，后续仍可在高级设置里微调。</p>
          <div class="choiceGrid3">
            <button v-for="(p, key) in PRESETS" :key="key" type="button" class="choiceCard" :class="{ active: activePreset === key }" @click="applyPreset(key)">
              <span class="cct">{{ p.label }}</span>
              <small>{{ p.hint }}</small>
            </button>
          </div>
          <div class="twoCol">
            <div>
              <p class="fLabel">单任务最多图片</p>
              <div class="presetBtns">
                <button v-for="v in IMAGE_COUNTS" :key="v" type="button" class="presetBtn" :class="{ active: draft.max_images_per_task === v }" @click="draft.max_images_per_task = v">{{ v }}</button>
              </div>
            </div>
            <div>
              <p class="fLabel">结果保留天数</p>
              <div class="presetBtns">
                <button v-for="v in RETENTION_DAYS" :key="v" type="button" class="presetBtn" :class="{ active: draft.result_retention_days === v }" @click="draft.result_retention_days = v">{{ v }} 天</button>
              </div>
            </div>
          </div>

          <div class="hr" />

          <!-- 产物存储 -->
          <h4 class="secTitle">产物存储</h4>
          <p class="secHint">单节点可用本机目录；多个网关共同消费队列时必须使用 S3 / MinIO。</p>
          <div class="choiceGrid2">
            <button type="button" class="choiceCard" :class="{ active: draft.storage.type === 'local' }" @click="draft.storage.type = 'local'">
              <span class="cct">本机存储</span>
              <small>适合单台 tokenav gateway，配置最简单</small>
            </button>
            <button type="button" class="choiceCard" :class="{ active: draft.storage.type === 's3' }" @click="draft.storage.type = 's3'">
              <span class="cct">S3 / MinIO</span>
              <small>适合集群，共享图片产物</small>
            </button>
          </div>
          <div v-if="draft.storage.type === 'local'" class="fieldRow">
            <span class="fLabel">本机目录</span>
            <div class="pathRow">
              <a-input v-model:value="draft.storage.local_path" placeholder="./data/image-tasks" />
              <a-button :disabled="cfgSaving" @click="draft.storage.local_path = './data/image-tasks'">使用默认目录</a-button>
            </div>
          </div>
          <div v-else class="s3Grid">
            <div class="fieldRow span2">
              <span class="fLabel">Endpoint（服务地址）</span>
              <a-input v-model:value="draft.storage.s3.endpoint" placeholder="https://s3.example.com" />
            </div>
            <div class="fieldRow">
              <span class="fLabel">Region（区域）</span>
              <a-input v-model:value="draft.storage.s3.region" placeholder="auto" />
            </div>
            <div class="fieldRow">
              <span class="fLabel">Bucket（存储桶）</span>
              <a-input v-model:value="draft.storage.s3.bucket" />
            </div>
            <div class="fieldRow">
              <span class="fLabel">Prefix（目录前缀）</span>
              <a-input v-model:value="draft.storage.s3.prefix" placeholder="image-tasks" />
            </div>
            <div class="settingRow boxed">
              <div><h4>Path style</h4><p>MinIO（对象存储）通常需要开启。</p></div>
              <a-switch v-model:checked="draft.storage.s3.force_path_style" />
            </div>
            <div class="fieldRow">
              <span class="fLabel">Access Key ID</span>
              <a-input v-model:value="s3AccessKeyID" autocomplete="off" :placeholder="configState.desired.storage.s3.access_key_id_configured ? '已配置，留空不修改' : '未配置'" />
            </div>
            <div class="fieldRow">
              <span class="fLabel">Secret Access Key</span>
              <a-input-password v-model:value="s3SecretAccessKey" autocomplete="new-password" :placeholder="configState.desired.storage.s3.secret_access_key_configured ? '已配置，留空不修改' : '未配置'" />
            </div>
          </div>

          <!-- 高级设置：12 个数字字段（后端单位：秒/毫秒/天/字节） -->
          <a-collapse ghost class="adv">
            <a-collapse-panel key="adv" header="高级设置">
              <p class="secHint">默认值适合大多数节点；只有明确需要时再调整。</p>
              <div class="advGrid">
                <div class="fieldRow"><span class="fLabel">工作线程 (1–64)</span><a-input-number v-model:value="draft.worker_count" :min="1" :max="64" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">队列容量 (1–100000)</span><a-input-number v-model:value="draft.queue_capacity" :min="1" :max="100000" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">领取批次 (1–500)</span><a-input-number v-model:value="draft.claim_batch_size" :min="1" :max="500" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">单用户并发 (1–16)</span><a-input-number v-model:value="draft.per_user_concurrency" :min="1" :max="16" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">最大尝试次数 (1–10)</span><a-input-number v-model:value="draft.max_attempts" :min="1" :max="10" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">轮询间隔（毫秒）</span><a-input-number v-model:value="draft.poll_interval_milliseconds" :min="100" :max="60000" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">执行超时（秒）</span><a-input-number v-model:value="draft.execution_timeout_seconds" :min="10" :max="3600" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">任务锁定时长（秒）</span><a-input-number v-model:value="draft.claim_ttl_seconds" :min="190" :max="7200" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">下载链接有效期（秒）</span><a-input-number v-model:value="draft.download_url_ttl_seconds" :min="60" :max="86400" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">请求体上限（MiB）</span><a-input-number v-model:value="maxRequestMiB" :min="0.016" :max="4" :step="0.25" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">单图上限（MiB）</span><a-input-number v-model:value="maxArtifactMiB" :min="1" :max="100" style="width:100%" /></div>
                <div class="fieldRow"><span class="fLabel">单任务产物上限（MiB）</span><a-input-number v-model:value="maxTaskArtifactMiB" :min="1" :max="500" style="width:100%" /></div>
              </div>
            </a-collapse-panel>
          </a-collapse>

          <!-- 校验错误 -->
          <a-alert v-if="validationErrors.length" type="warning" show-icon class="band" message="还需要处理以下配置">
            <template #description>
              <ul class="valList"><li v-for="m in validationErrors" :key="m">{{ m }}</li></ul>
            </template>
          </a-alert>

          <div class="cfgNote">
            <p><strong>配置文件：</strong><span class="mono">{{ configState.config_path || 'config.yaml' }}</span></p>
            <p class="warnLine">点击「保存并重启」后会先安全写入配置；写入成功后页面自动重启 tokenav gateway 并等待恢复（约 10–60 秒，期间请求中断）。</p>
          </div>
        </fieldset>

        <div class="cfgFooter">
          <a-button :disabled="cfgSaving" @click="syncOpen(false)">取消</a-button>
          <a-button type="primary" danger :disabled="!canSave" :loading="cfgSaving" @click="submitConfig">
            {{ cfgSaving ? '保存中' : '保存并重启' }}
          </a-button>
        </div>
      </div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.cfgBody { min-width: 0; }
.cfgFail { text-align: center; padding: 40px 0; }
.cfgFailMsg { color: var(--text2); font-size: 12px; margin: 6px 0 12px; }
.statusCards { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 8px; margin-bottom: 12px; }
.statusCard { display: flex; align-items: flex-start; gap: 8px; border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px; background: var(--card); }
.statusCard p { font-size: 11px; color: var(--text2); }
.statusCard strong { font-size: 13px; font-weight: 500; }
.dot { width: 10px; height: 10px; border-radius: 50%; margin-top: 4px; flex-shrink: 0; }
.dot.ok { background: #4caf78; }
.dot.warn { background: #e8a23c; }
.dot.pri { background: var(--ant-color-primary, #2563eb); }
.cfgForm { border: 0; padding: 0; margin: 0; min-width: 0; }
.settingRow { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 8px 0; }
.settingRow h4, .secTitle { font-size: 13px; font-weight: 500; }
.settingRow p, .secHint { font-size: 12px; color: var(--text2); margin-top: 2px; line-height: 1.6; }
.settingRow.boxed { border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px; }
.hr { height: 1px; background: var(--hr); margin: 14px 0; }
.choiceGrid3 { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; margin-top: 10px; }
.choiceGrid2 { display: grid; grid-template-columns: repeat(2, 1fr); gap: 8px; margin-top: 10px; }
.choiceCard { display: flex; flex-direction: column; justify-content: center; gap: 2px; min-height: 64px; border: 1px solid var(--hr); border-radius: 8px; background: transparent; padding: 10px 14px; text-align: left; cursor: pointer; transition: border-color 0.15s, background 0.15s; }
.choiceCard:hover { border-color: var(--text2); }
.choiceCard .cct { font-size: 13px; font-weight: 500; }
.choiceCard small { font-size: 11px; color: var(--text2); }
.choiceCard.active { border-color: var(--ant-color-primary, #2563eb); background: color-mix(in srgb, var(--ant-color-primary, #2563eb) 10%, transparent); }
.twoCol { display: grid; grid-template-columns: repeat(2, 1fr); gap: 16px; margin-top: 12px; }
.fLabel { font-size: 12px; color: var(--text2); display: block; margin-bottom: 6px; }
.presetBtns { display: flex; flex-wrap: wrap; gap: 6px; }
.presetBtn { min-height: 30px; border: 1px solid var(--hr); border-radius: 8px; background: transparent; padding: 0 12px; font-size: 12px; cursor: pointer; color: inherit; transition: border-color 0.15s, background 0.15s; }
.presetBtn:hover { border-color: var(--text2); }
.presetBtn.active { border-color: var(--ant-color-primary, #2563eb); background: color-mix(in srgb, var(--ant-color-primary, #2563eb) 10%, transparent); }
.fieldRow { margin-top: 10px; min-width: 0; }
.pathRow { display: flex; gap: 8px; }
.s3Grid { display: grid; grid-template-columns: repeat(2, 1fr); column-gap: 16px; }
.s3Grid .span2 { grid-column: span 2; }
.adv { margin-top: 8px; }
.advGrid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px 16px; }
.valList { margin: 4px 0 0; padding-left: 18px; }
.valList li { margin-bottom: 2px; }
.cfgNote { border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px; font-size: 12px; margin-top: 12px; }
.cfgNote .mono { font-family: Consolas, monospace; word-break: break-all; }
.cfgNote .warnLine { color: #b8860b; margin-top: 4px; line-height: 1.6; }
.cfgFooter { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; padding-top: 12px; border-top: 1px solid var(--hr); }
.band { margin-bottom: 10px; }

@media (max-width: 640px) {
  .choiceGrid3, .twoCol, .s3Grid, .advGrid { grid-template-columns: 1fr; }
  .s3Grid .span2 { grid-column: span 1; }
}
</style>
