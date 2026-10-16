/* ============================================================
 * 渠道监控接口封装（Win12 重构版）
 * 契约来源：
 *  - frontend/src/api/admin/channelMonitor.ts（V1 列表/CRUD/run/duplicate 幂等键）
 *  - frontend/src/api/channelMonitorV2.ts（V2 config/snapshot/models/errors/users）
 *  - frontend/src/api/keys.ts + api/groups.ts（「使用我的 Key」数据源）
 *  - 真实后端实测：gateway-lite 模式未注册用户路由（/keys、/groups/rates 返回 404），
 *    故「使用我的 Key」回退本地 Key 接口 /admin/gateway-lite/local-api-keys
 *    （POST :id/reveal → {secret}，已实测核实）。
 * 说明：全局 request() 封装不支持自定义请求头，而监控复制必须携带
 * Idempotency-Key，因此 duplicateMonitor 单独用 fetch 实现（令牌取 getToken()），
 * 幂等键的存取语义与旧实现保持一致。
 * ============================================================ */
import { request, getToken } from '../../api'

/* ---------- V1 主动探测（列表参数：page/page_size/provider/enabled/search） ---------- */

export function listMonitors(query) {
  return request('/admin/channel-monitors', { method: 'GET', query })
}

export function createMonitor(body) {
  return request('/admin/channel-monitors', { method: 'POST', body })
}

/** 更新：body 按需传字段；api_key 空串 = 不修改（后端指针语义），调用方直接省略该字段 */
export function updateMonitor(id, body) {
  return request(`/admin/channel-monitors/${id}`, { method: 'PUT', body })
}

export function deleteMonitor(id) {
  return request(`/admin/channel-monitors/${id}`, { method: 'DELETE' })
}

/** 立即检测。注意：V2 模式下后端返回 403 CHANNEL_MONITOR_ACTIVE_PROBES_RETIRED（已实测），
 *  调用方需把后端 message 原样展示。 */
export function runMonitor(id) {
  return request(`/admin/channel-monitors/${id}/run`, { method: 'POST' })
}

/* ---------- 监控复制（Idempotency-Key 幂等） ----------
 * 服务端直接复制加密后的 API Key；浏览器只提交源监控 ID + 管理员范围幂等键。
 * 幂等键按「管理员ID:监控ID」维度存 sessionStorage，网络结果不明确时保留该键
 * 供安全重试（重试收敛为同一副本），成功后立即清除 —— 与旧 duplicate() 一致。 */

// 进程内缓存：sessionStorage 不可用（无痕模式受限）时兜底复用同一键
const dupMemKeys = new Map()

function currentAdminID() {
  try {
    const raw = localStorage.getItem('w12-user')
    if (!raw) return null
    const u = JSON.parse(raw)
    return u && Number.isSafeInteger(u.id) && u.id > 0 ? String(u.id) : null
  } catch {
    return null
  }
}

function dupStorageKey(id) {
  const admin = currentAdminID()
  return admin ? `sub2api:admin:channel-monitor-duplicate:${admin}:${id}` : null
}

function readStoredKey(key) {
  if (!key) return null
  try { return sessionStorage.getItem(key) ?? null } catch { return null }
}

function storeKey(key, val) {
  if (!key) return
  try { val ? sessionStorage.setItem(key, val) : sessionStorage.removeItem(key) } catch { /* 降级为内存缓存 */ }
}

export async function duplicateMonitor(id) {
  const skey = dupStorageKey(id)
  let idem = (skey && (dupMemKeys.get(skey) || readStoredKey(skey))) || null
  if (!idem) {
    const uuid = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(36).slice(2)}`
    idem = `channel-monitor-duplicate-${currentAdminID() ?? 'unknown-admin'}-${id}-${uuid}`
  }
  if (skey) { dupMemKeys.set(skey, idem); storeKey(skey, idem) }

  const url = new URL(`/api/v1/admin/channel-monitors/${id}/duplicate`, window.location.origin)
  const res = await fetch(url, { method: 'POST', headers: { Authorization: `Bearer ${getToken()}` } })

  if (res.status === 401) throw new Error('登录已失效，请重新登录')
  if (!res.ok) {
    // 失败保留幂等键：同一操作重试仍复用，避免网络重试产生多个副本
    let tip = `请求失败（HTTP ${res.status}）`
    try {
      const j = await res.clone().json()
      if (j && j.message) tip = j.message
    } catch { /* 响应体可能非 JSON */ }
    throw new Error(tip)
  }

  const payload = await res.json()
  const data = payload && typeof payload === 'object' && 'code' in payload ? payload.data : payload
  // 只有确认成功才清除幂等键
  if (skey) { dupMemKeys.delete(skey); storeKey(skey, null) }
  return data
}

/* ---------- 公共设置（V1/V2 模式、系统默认检测间隔） ---------- */

export function getPublicSettings() {
  return request('/settings/public', { method: 'GET' })
}

/* ---------- 请求模板（GET /admin/channel-monitor-templates，支持 provider/api_mode 过滤） ----------
 * 响应 { items: [{ id, name, provider, api_mode, description, extra_headers,
 *   body_override_mode, body_override, associated_monitors }] }，见
 * internal/handler/admin/channel_monitor_template_handler.go。监控表单的下拉
 * 选项与模板管理器共用本接口。 */
export function listMonitorTemplates(query) {
  return request('/admin/channel-monitor-templates', { method: 'GET', query })
}


/* 检测历史：GET /admin/channel-monitors/:id/history?model=&limit=
 * 响应 { items: [{ id, model, status, latency_ms, ping_latency_ms, message, checked_at }] }，
 * 字段见 internal/handler/admin/channel_monitor_handler.go channelMonitorHistoryItemResponse。 */
export function getMonitorHistory(id, query) {
  return request(`/admin/channel-monitors/${id}/history`, { method: 'GET', query })
}

/* ---------- V2 被动统计（读取接口在 v1 模式下被后端 403 保护） ---------- */

export function getV2Config() {
  return request('/admin/channel-monitor-v2/config', { method: 'GET' })
}

/** 保存聚合设置：body 为完整 config（含 version，后端按版本号做并发冲突检测 409） */
export function updateV2Config(body) {
  return request('/admin/channel-monitor-v2/config', { method: 'PUT', body })
}

export function getV2Snapshot(range) {
  return request('/admin/channel-monitor-v2/snapshot', { method: 'GET', query: { range } })
}

export function getV2Models(range) {
  return request('/admin/channel-monitor-v2/models', { method: 'GET', query: { range } })
}

export function getV2Errors(range) {
  return request('/admin/channel-monitor-v2/errors', { method: 'GET', query: { range } })
}

export function getV2Users(range) {
  return request('/admin/channel-monitor-v2/users', { method: 'GET', query: { range } })
}

/* V2 维度选项：GET /admin/channel-monitor-v2/dimensions?range=
 * 响应 { platforms: [{value,label,platform?,request_count}], groups: [{id,name,platform?,request_count}],
 *   models: [{value,label,platform?,request_count}] }（见 service.ChannelMonitorV2Dimensions）。 */
export function getV2Dimensions(range) {
  return request('/admin/channel-monitor-v2/dimensions', { method: 'GET', query: { range } })
}

/* V2 矩阵：GET /admin/channel-monitor-v2/matrix?range=&group_by=&platform=&group_id=&model=
 * group_by 枚举：platform / platform_group（后端默认）/ platform_model / platform_group_model
 * 响应 { group_by, coverage, items: [{ platform, group_id?, group_name?, model?,
 *   metrics, health, buckets: [{bucket_start, metrics, health}] }] }。 */
export function getV2Matrix(query) {
  return request('/admin/channel-monitor-v2/matrix', { method: 'GET', query })
}

/* ---------- 「使用我的 Key」数据源 ----------
 * 首选用户接口（旧契约）：GET /keys?status=active + GET /groups/rates。
 * gateway-lite 部署实测 404 → 回退 /admin/gateway-lite/local-api-keys。 */

export function listMyKeys() {
  return request('/keys', { method: 'GET', query: { page: 1, page_size: 100, status: 'active' } })
}

export function getMyGroupRates() {
  return request('/groups/rates', { method: 'GET' })
}

export function listLocalKeys() {
  return request('/admin/gateway-lite/local-api-keys', { method: 'GET' })
}

/** 揭示本地 Key 明文：响应 {secret}（已实测核实） */
export function revealLocalKey(id) {
  return request(`/admin/gateway-lite/local-api-keys/${id}/reveal`, { method: 'POST' })
}
