/* ============================================================
 * 系统配置（备份恢复 + 系统版本）专用 API 封装
 * 路由对照（internal/server/routes/admin.go）：
 * - registerBackupRoutes → /admin/backups*（backup_handler.go）
 * - registerSystemRoutes → /admin/system/*（system_handler.go）
 *   两组路由都只在 standard 模式的 RegisterAdminRoutes 中注册；
 *   gateway-lite 模式走 RegisterGatewayLiteAdminRoutes，不含这些路由，
 *   请求会得到 404 —— 组件据此显示「运行模式不提供此功能」降级提示。
 *
 * 为什么不用统一 request()（src/api/index.js）：
 * - 它对 !res.ok 只抛「HTTP 状态」文本，调用方拿不到 404 语义，无法降级；
 * - 它不支持自定义 header（X-Step-Up-Token 一次性授权重放）。
 * 因此这里与 accountsApi.js 同构：原生 fetch + 状态码保留，
 * 错误对象附加 notFound / stepUpRequired / stepUpBlocked 标记。
 *
 * step-up（二次验证）契约：
 * - 高危路由挂 stepUpAuth.ForScope(service.StepUpScopeXxx)，缺一次性
 *   授权时返回 403 { code: "STEP_UP_REQUIRED" }（middleware/step_up.go）；
 * - 前端用 POST /user/totp/step-up { code, action_scope } 换取一次性
 *   token，再带 X-Step-Up-Token 头重放一次（旧 useStepUp.run 语义）。
 * ============================================================ */
import { getToken } from '../../api'

const BASE = '/api/v1'

/** 运行模式降级提示（404 时统一文案，卡片据此渲染而不弹错误） */
export const NOT_FOUND_TIP = '当前运行模式不提供此功能（standard 模式专属）'

function buildUrl(path, query) {
  const url = new URL(BASE + path, window.location.origin)
  if (query) {
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null && v !== '') url.searchParams.set(k, v)
    }
  }
  return url
}

/** 解析网关统一包裹 { code, message, reason, data } 或中间件裸错误体 { code: "STEP_UP_*", message } */
async function parseBody(res) {
  let payload = null
  try { payload = await res.json() } catch { /* 空/非 JSON 体 */ }
  if (payload && typeof payload === 'object' && 'code' in payload) {
    return { code: payload.code, message: payload.message || '', reason: payload.reason || '', data: payload.data }
  }
  return { code: res.ok ? 0 : res.status, message: payload?.message || '', reason: payload?.reason || '', data: payload }
}

/**
 * 统一请求：成功返回包裹中的 data；失败抛 Error 并附加标记——
 * - e.notFound = true：HTTP 404（gateway-lite 模式未注册该路由）→ 卡片降级
 * - e.stepUpRequired = true：403 STEP_UP_REQUIRED → 需 TOTP 换授权后重放
 * - e.stepUpBlocked = 'STEP_UP_*'：其他二次验证失败（未启用 TOTP 等）
 */
export async function apiFetch(path, { method = 'GET', body, headers, query } = {}) {
  const h = { Authorization: `Bearer ${getToken()}` }
  if (headers) Object.assign(h, headers)
  if (body !== undefined) h['Content-Type'] = 'application/json'
  const res = await fetch(buildUrl(path, query), {
    method,
    headers: h,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (res.status === 404) {
    const e = new Error(NOT_FOUND_TIP)
    e.notFound = true
    e.status = 404
    throw e
  }
  const payload = await parseBody(res)
  if (payload.code !== 0) {
    // step-up 错误体是中间件裸体 { code: "STEP_UP_*" }，业务错误体 reason 存放原因码
    const marker = [payload.reason, payload.code].find((v) => typeof v === 'string' && v.startsWith('STEP_UP')) || ''
    const e = new Error(payload.message || `请求失败（HTTP ${res.status}）`)
    e.status = res.status
    if (marker === 'STEP_UP_REQUIRED') { e.stepUpRequired = true; throw e }
    if (marker.startsWith('STEP_UP')) { e.stepUpBlocked = marker; throw e }
    throw e
  }
  return payload.data
}

/** TOTP step-up 二次验证：验证码换一次性授权 token（同 accountsApi.totpStepUp） */
export async function totpStepUp(code, actionScope) {
  return apiFetch('/user/totp/step-up', {
    method: 'POST',
    body: { code, action_scope: actionScope },
  }) // → { verified, token, expires_in }
}

/* ── step-up 作用域（internal/service/step_up.go 常量） ── */
export const STEP_UP_SCOPE_BACKUP_CREATE = 'backups.create'
export const STEP_UP_SCOPE_BACKUP_DOWNLOAD = 'backups.download'
export const STEP_UP_SCOPE_BACKUP_RESTORE = 'backups.restore'
export const STEP_UP_SCOPE_BACKUP_DELETE = 'backups.delete'
export const STEP_UP_SCOPE_BACKUP_S3_READ = 'backups.s3.read'
export const STEP_UP_SCOPE_BACKUP_S3_UPDATE = 'backups.s3.update'

/* ============================================================
 * 备份 / 恢复（backup_handler.go + registerBackupRoutes）
 * ============================================================ */

/** GET /admin/backups/s3-config（step-up: backups.s3.read）→ BackupS3ConfigView：
 *  凭据只回 presence 位（access_key_id_configured / secret_access_key_configured），不回明文 */
export function getS3Config(stepUpToken) {
  return apiFetch('/admin/backups/s3-config', {
    headers: stepUpToken ? { 'X-Step-Up-Token': stepUpToken } : undefined,
  })
}

/** PUT /admin/backups/s3-config（step-up: backups.s3.update）→ 更新后 BackupS3ConfigView。
 *  后端约定：access_key_id / secret_access_key 留空 = 保留原值（backup_service.UpdateS3Config） */
export function updateS3Config(cfg, stepUpToken) {
  return apiFetch('/admin/backups/s3-config', {
    method: 'PUT',
    body: cfg,
    headers: stepUpToken ? { 'X-Step-Up-Token': stepUpToken } : undefined,
  })
}

/** POST /admin/backups/s3-config/test（step-up: backups.s3.update）→ { ok, code?, message }。
 *  后端对连接失败也返回 HTTP 200 + ok:false（脱敏结论），空凭据自动复用已保存值 */
export function testS3Connection(cfg, stepUpToken) {
  return apiFetch('/admin/backups/s3-config/test', {
    method: 'POST',
    body: cfg,
    headers: stepUpToken ? { 'X-Step-Up-Token': stepUpToken } : undefined,
  })
}

/** POST /admin/backups（step-up: backups.create）→ 202 Accepted，data = BackupRecord。
 *  expireDays：nil=默认 14 天过期，0=永不过期（CreateBackupRequest.expire_days） */
export function createBackup(expireDays, stepUpToken) {
  const body = expireDays === undefined ? {} : { expire_days: expireDays }
  return apiFetch('/admin/backups', {
    method: 'POST',
    body,
    headers: stepUpToken ? { 'X-Step-Up-Token': stepUpToken } : undefined,
  })
}

/** GET /admin/backups → { items: BackupRecord[] }（无需 step-up） */
export async function listBackups() {
  const data = await apiFetch('/admin/backups')
  return { items: data?.items || [] }
}

/** GET /admin/backups/:id → BackupRecord（单条详情；列表页暂未展示详情，接口保留备扩展） */
export function getBackup(id) {
  return apiFetch(`/admin/backups/${encodeURIComponent(id)}`)
}

/** DELETE /admin/backups/:id（step-up: backups.delete）→ { deleted: true } */
export function deleteBackup(id, stepUpToken) {
  return apiFetch(`/admin/backups/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: stepUpToken ? { 'X-Step-Up-Token': stepUpToken } : undefined,
  })
}

/** POST /admin/backups/:id/restore（step-up: backups.restore）→ 202 Accepted，data = BackupRecord。
 *  body 必须带管理员密码 { password }（handler 校验后才开始恢复任务） */
export function restoreBackup(id, password, stepUpToken) {
  return apiFetch(`/admin/backups/${encodeURIComponent(id)}/restore`, {
    method: 'POST',
    body: { password },
    headers: stepUpToken ? { 'X-Step-Up-Token': stepUpToken } : undefined,
  })
}

/** GET /admin/backups/:id/download-url（step-up: backups.download）→ BackupDownloadResponse：
 *  { url } 单文件 或 { parts: [{ index, size_bytes, url }] } 大备份分卷（预签名 URL） */
export function getBackupDownloadUrl(id, stepUpToken) {
  return apiFetch(`/admin/backups/${encodeURIComponent(id)}/download-url`, {
    headers: stepUpToken ? { 'X-Step-Up-Token': stepUpToken } : undefined,
  })
}

/* ============================================================
 * 系统版本 / 更新（system_handler.go + registerSystemRoutes）
 * ============================================================ */

/** GET /admin/system/version → { version } */
export async function getVersion() {
  const data = await apiFetch('/admin/system/version')
  return { version: data?.version || '' }
}

/** GET /admin/system/check-updates?force=true → UpdateInfo：
 *  { current_version, latest_version, has_update, release_info?, cached, warning?, build_type } */
export function checkUpdates(force) {
  return apiFetch('/admin/system/check-updates', { query: force ? { force: 'true' } : undefined })
}

/** POST /admin/system/update → { message, need_restart } 或已最新 { already_up_to_date: true }。
 *  handler 已实现（PerformUpdate，下载并替换二进制），未挂 step-up，仅有幂等键保护 */
export function performUpdate() {
  return apiFetch('/admin/system/update', { method: 'POST' })
}

/** POST /admin/system/rollback → { message, need_restart }（幂等键保护，未挂 step-up） */
export function rollback() {
  return apiFetch('/admin/system/rollback', { method: 'POST' })
}

/* POST /admin/system/restart（重启 systemd 服务）handler 已实现，但属于运维域操作，
 * 不在版本卡范围内，由主会话决定接入位置。 */
