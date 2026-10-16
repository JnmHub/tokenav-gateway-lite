/* 账号所有 JSON 请求共用公共鉴权/头/会话路径，导出二次验证和 ETag 语义保持不变。 */
import { request, requestWithMeta, getSessionEpoch, subscribeSessionReset } from '../../api/index.js'
import { cancellationError } from '../../api/session.js'

export async function listWithEtag(page, pageSize, filters, etag, options = {}) {
  const res = await requestWithMeta('/admin/accounts', {
    ...options, method: 'GET', query: { page, page_size: pageSize, ...filters },
    headers: etag ? { 'If-None-Match': etag } : undefined, allowNotModified: true,
  })
  return { notModified: res.notModified, etag: res.headers.get('ETag') || (res.notModified ? etag : null), data: res.data }
}

export async function exportAccountsData(options = {}, stepUpToken) {
  const query = {}
  if (options.ids?.length) query.ids = options.ids.join(',')
  else if (options.filters) for (const [key, value] of Object.entries(options.filters)) if (value) query[key] = value
  if (options.includeProxies === false) query.include_proxies = 'false'
  return request('/admin/accounts/data', {
    method: 'GET', query, headers: stepUpToken ? { 'X-Step-Up-Token': stepUpToken } : undefined,
  })
}

export const totpStepUp = (code, actionScope) => request('/user/totp/step-up', {
  method: 'POST', body: { code, action_scope: actionScope },
})
export const STEP_UP_SCOPE_ACCOUNTS_EXPORT = 'accounts.export'

// 行内用量队列最多三请求；缓存、进行中去重及待队列均不可跨管理员会话。
let usageRunning = 0
const usageWait = []
const USAGE_MAX = 3
const usageCache = new Map()
const usageInflight = new Map()
const USAGE_CACHE_TTL = 5 * 60 * 1000
subscribeSessionReset(() => {
  usageCache.clear(); usageInflight.clear()
  for (const item of usageWait.splice(0)) item.reject(cancellationError())
})
export function readUsageCache(key) {
  const item = usageCache.get(key)
  if (!item) return null
  if (item.epoch !== getSessionEpoch() || Date.now() - item.at >= USAGE_CACHE_TTL) { usageCache.delete(key); return null }
  return item.data
}
export function writeUsageCache(key, data, expectedEpoch = getSessionEpoch()) {
  if (expectedEpoch !== getSessionEpoch()) return false
  usageCache.set(key, { data, at: Date.now(), epoch: expectedEpoch })
  return true
}
export function readUsageInflight(key) { return usageInflight.get(key) || null }
export function trackUsageInflight(key, promise) {
  usageInflight.set(key, promise)
  // 旧请求结束不能删除新会话的同名进行中条目。
  const remove = () => { if (usageInflight.get(key) === promise) usageInflight.delete(key) }
  promise.then(remove, remove)
}
function pumpUsage() {
  if (usageRunning >= USAGE_MAX) return
  const next = usageWait.shift()
  if (!next) return
  usageRunning += 1
  Promise.resolve().then(() => {
    if (next.epoch !== getSessionEpoch()) throw cancellationError()
    return next.fn()
  }).then((result) => {
    if (next.epoch !== getSessionEpoch()) next.reject(cancellationError())
    else next.resolve(result)
  }, next.reject).finally(() => { usageRunning -= 1; pumpUsage() })
}
export function enqueueUsage(fn) {
  return new Promise((resolve, reject) => {
    usageWait.push({ fn, resolve, reject, epoch: getSessionEpoch() })
    pumpUsage()
  })
}

export function getAccountUsage(id, { source = 'active', force = false, ...options } = {}) {
  return request(`/admin/accounts/${id}/usage`, { ...options, method: 'GET', query: { source, force: force ? 'true' : undefined } })
}
// 主动刷新/真正消耗重置券保留各自操作边界，调用方须确认消耗。
export const refreshOpenAIQuota = (id) => request(`/admin/openai/accounts/${id}/quota/refresh`, { method: 'POST' })
export const resetOpenAIQuota = (id) => request(`/admin/openai/accounts/${id}/reset-quota`, { method: 'POST' })
export const getBatchTodayStats = (accountIds) => request('/admin/accounts/today-stats/batch', { method: 'POST', body: { account_ids: accountIds } })
export const syncUpstreamModels = (id) => request(`/admin/accounts/${id}/models/sync-upstream`, { method: 'POST' })
export const syncUpstreamModelsPreview = (params) => request('/admin/accounts/models/sync-upstream-preview', { method: 'POST', body: params })
export const getAntigravityDefaultModelMapping = () => request('/admin/accounts/antigravity/default-model-mapping', { method: 'GET' })
