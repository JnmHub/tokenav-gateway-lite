/* 分组模块请求全部使用统一请求层，支持自定义请求头与会话取消。
 * 契约来源（唯一权威）：frontend/src/api/admin/groups.ts + 后端
 *   internal/server/routes/admin.go（路由）+ internal/handler/admin/group_handler.go（参数）
 * 已核实接口：
 * - GET  /admin/groups?page&page_size&platform&status&is_exclusive&search&sort_by&sort_order
 * - GET  /admin/groups/all?platform
 * - GET/PUT/DELETE /admin/groups/:id/rate-multipliers（PUT/DELETE body { entries:[{user_id, rate_multiplier}] }）
 * - PUT/DELETE /admin/groups/:id/rpm-overrides（body { entries:[{user_id, rpm_override}] }）
 * - POST /admin/groups/:id/accounts/bind（body { account_ids, confirm_mixed_channel_risk }）
 * - GET/POST /admin/groups/:id/composite-routes
 * - PUT/DELETE /admin/groups/:id/composite-routes/:routeId
 * - PUT  /admin/groups/:id/composite-routes/batch-status（body { route_ids, enabled }）
 * - POST /admin/groups/:id/composite-routes/preview（body { model, endpoint, candidate, editing_route_id }）
 * - GET  /admin/groups/:id/models-list-candidates?platform=composite
 * - PUT  /admin/groups/sort-order（body { updates:[{id, sort_order}] }）
 * - GET  /admin/groups/capacity-summary
 * - GET  /admin/groups/usage-summary
 * - POST /admin/groups/:id/duplicate（header Idempotency-Key）
 * - GET  /admin/users?page&page_size&search（用户搜索；当前区域网关构建可能未启用，失败按旧前端静默降级）
 * ============================================================ */
import { request } from '../../api/index.js'

/** 列表：筛选 + 服务端排序（参数名已对照后端 List handler 核实） */
export function listGroups(page, pageSize, filters = {}, signal) {
  const query = { page, page_size: pageSize }
  for (const k of ['platform', 'status', 'is_exclusive', 'search', 'sort_by', 'sort_order']) {
    if (filters[k] !== undefined && filters[k] !== null && filters[k] !== '') query[k] = filters[k]
  }
  return request('/admin/groups', { method: 'GET', query, signal })
}

/** 全量分组（排序弹窗用；按 sort_order 排序交给调用方） */
export function getAllGroups(signal) {
  return request('/admin/groups/all', { method: 'GET', signal })
}

/** 用户倍率：列表（含 rpm_override 字段，RPM 弹窗共用此接口做过滤） */
export function getRateMultipliers(id, signal) {
  return request(`/admin/groups/${id}/rate-multipliers`, { method: 'GET', signal })
}
/** 用户倍率：批量保存（全量覆盖 rate_multiplier，保留 rpm_override） */
export function batchSetRateMultipliers(id, entries, signal) {
  return request(`/admin/groups/${id}/rate-multipliers`, { method: 'PUT', body: { entries }, signal })
}
/** 用户倍率：清空（保留 rpm_override） */
export function clearRateMultipliers(id, signal) {
  return request(`/admin/groups/${id}/rate-multipliers`, { method: 'DELETE', signal })
}
/** RPM 覆写：批量保存（全量覆盖 rpm_override，保留 rate_multiplier） */
export function batchSetRPMOverrides(id, entries, signal) {
  return request(`/admin/groups/${id}/rpm-overrides`, { method: 'PUT', body: { entries }, signal })
}
/** RPM 覆写：清空（保留 rate_multiplier） */
export function clearRPMOverrides(id, signal) {
  return request(`/admin/groups/${id}/rpm-overrides`, { method: 'DELETE', signal })
}

/** 用户搜索（弹窗添加用户用；?search=&page_size=10，失败返回空列表由调用方提示） */
export function searchUsers(search, pageSize = 10, signal) {
  return request('/admin/users', { method: 'GET', query: { page: 1, page_size: pageSize, search }, signal })
}

/** 账号搜索（快速绑定用；与旧 QuickBindAccountsModal 相同参数：平台过滤 + 活跃 + 优先级升序） */
export function searchAccounts(page, pageSize, { platform, search }, signal) {
  return request('/admin/accounts', {
    method: 'GET', signal,
    query: {
      page, page_size: pageSize,
      // composite 聚合分组不限定平台（旧实现：platform === 'composite' ? undefined : platform）
      platform: platform && platform !== 'composite' ? platform : undefined,
      status: 'active',
      sort_by: 'priority', sort_order: 'asc',
      search: search || undefined,
    },
  })
}

/** 快速绑定账号到分组（保留账号的其他分组绑定） */
export function bindAccounts(id, accountIds, confirmMixedChannelRisk = false, signal) {
  return request(`/admin/groups/${id}/accounts/bind`, {
    method: 'POST', signal,
    body: { account_ids: accountIds, confirm_mixed_channel_risk: confirmMixedChannelRisk },
  })
}

// ===== 聚合路由（composite 平台分组专用） =====
export function listCompositeRoutes(id, signal) {
  return request(`/admin/groups/${id}/composite-routes`, { method: 'GET', signal })
}
export function createCompositeRoute(id, route, signal) {
  return request(`/admin/groups/${id}/composite-routes`, { method: 'POST', body: route, signal })
}
export function updateCompositeRoute(id, routeId, route, signal) {
  return request(`/admin/groups/${id}/composite-routes/${routeId}`, { method: 'PUT', body: route, signal })
}
export function deleteCompositeRoute(id, routeId, signal) {
  return request(`/admin/groups/${id}/composite-routes/${routeId}`, { method: 'DELETE', signal })
}
export function batchSetCompositeRoutesEnabled(id, routeIds, enabled, signal) {
  return request(`/admin/groups/${id}/composite-routes/batch-status`, { method: 'PUT', body: { route_ids: routeIds, enabled }, signal })
}
export function previewCompositeRoute(id, payload, signal) {
  return request(`/admin/groups/${id}/composite-routes/preview`, { method: 'POST', body: payload, signal })
}
/** 常用模型候选（models-list-candidates，platform=composite） */
export function getModelsListCandidates(id, platform = 'composite', signal) {
  return request(`/admin/groups/${id}/models-list-candidates`, { method: 'GET', query: { platform }, signal })
}

/** 保存分组排序（updates: [{id, sort_order}]，sort-order 为固定路径必须在 :id 路由之前） */
export function updateSortOrder(updates, signal) {
  return request('/admin/groups/sort-order', { method: 'PUT', body: { updates }, signal })
}

/** 容量汇总：并发/会话/RPM（字段已对照后端类型核实：group_id, concurrency_*, sessions_*, rpm_*, ingress_requests_used） */
export function getCapacitySummary(signal) {
  return request('/admin/groups/capacity-summary', { method: 'GET', signal })
}
/** 用量汇总：今日/累计 请求与成本 */
export function getUsageSummary(signal) {
  return request('/admin/groups/usage-summary', { method: 'GET', signal })
}

/** 复制分组每次使用新幂等键，且由统一请求层取消注销时的旧请求。 */
export function duplicateGroup(id, signal) {
  const uuid = globalThis.crypto?.randomUUID?.() || `dup-${Date.now()}-${Math.random().toString(36).slice(2)}`
  return request(`/admin/groups/${id}/duplicate`, { method: 'POST', headers: { 'Idempotency-Key': uuid }, signal })
}
