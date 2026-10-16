/* 网关后台统一请求层：头、取消、会话隔离、结构化业务错误和条件读取共用同一路径。 */
import { createRequestScope, getSessionEpoch, resetRequestSession } from './session.js'
export { getSessionEpoch, subscribeSessionReset, onSessionReset, resetRequestSession, isCancellation } from './session.js'

const BASE = '/api/v1'

export function getToken() {
  return localStorage.getItem('w12-token') || ''
}

export function setToken(token) {
  const next = token || ''
  if (getToken() === next) return
  if (next) localStorage.setItem('w12-token', next)
  else localStorage.removeItem('w12-token')
  // 登录替换及退出都使旧代请求失效，外壳订阅此模块同步清理用户与窗口。
  resetRequestSession(next ? 'token-changed' : 'token-cleared')
}

export class ApiError extends Error {
  constructor(message, { status = 0, payload, headers } = {}) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = payload?.code
    this.reason = payload?.reason || payload?.error?.reason || ''
    this.details = payload?.details ?? payload?.error?.details ?? payload?.data
    this.requestId = headers?.get('X-Request-ID') || payload?.request_id || ''
    this.payload = payload
    const marker = [this.reason, this.code].find((value) => typeof value === 'string' && value.startsWith('STEP_UP'))
    if (marker === 'STEP_UP_REQUIRED') this.stepUpRequired = true
    else if (marker) this.stepUpBlocked = marker
  }
}

function isEnvelope(payload) {
  return payload && typeof payload === 'object' && !Array.isArray(payload) && 'code' in payload
}

function errorMessage(payload, status) {
  const supplied = payload?.message || payload?.error?.message || (typeof payload?.error === 'string' ? payload.error : '')
  return supplied || `请求失败（HTTP ${status}）`
}

/** 默认返回 data；需要 ETag/304 的调用者用 requestWithMeta，不另写原生 fetch 绕过会话。 */
export async function requestWithMeta(path, {
  method = 'POST', body, query, headers: suppliedHeaders, signal,
  sessionEpoch = getSessionEpoch(), allowNotModified = false,
} = {}) {
  const scope = createRequestScope(signal, sessionEpoch)
  const token = getToken()
  try {
    scope.assertCurrent()
    const url = new URL(BASE + path, window.location.origin)
    if (query) for (const [key, value] of Object.entries(query)) {
      if (value !== undefined && value !== null) url.searchParams.set(key, value)
    }
    const headers = new Headers(suppliedHeaders)
    if (body !== undefined && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
    if (token) headers.set('Authorization', `Bearer ${token}`)
    const res = await fetch(url, {
      method, headers, signal: scope.signal,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
    scope.assertCurrent()
    if (res.status === 304 && allowNotModified) {
      return { data: null, status: 304, headers: res.headers, notModified: true }
    }
    const text = res.status === 204 ? '' : await res.text()
    scope.assertCurrent()
    let payload = null
    if (text) {
      try { payload = JSON.parse(text) } catch { /* 网关/上游可能返回非 JSON 错误，不能吞掉状态。 */ }
    }
    // 上游透传 401 没有网关 code 包裹，不撤销管理员会话；旧会话响应已在上方拦截。
    if (res.status === 401 && token && isEnvelope(payload)) {
      const error = new ApiError(payload.message || '登录已失效，请重新登录', { status: 401, payload, headers: res.headers })
      setToken(null)
      localStorage.removeItem('w12-user')
      window.dispatchEvent(new CustomEvent('w12-unauthorized'))
      throw error
    }
    if (!res.ok || (isEnvelope(payload) && payload.code !== 0)) {
      throw new ApiError(errorMessage(payload, res.status), { status: res.status, payload, headers: res.headers })
    }
    if (text && payload === null) throw new ApiError('接口返回了无法解析的数据', { status: res.status, headers: res.headers })
    return { data: isEnvelope(payload) ? payload.data : payload, status: res.status, headers: res.headers, notModified: false }
  } finally {
    scope.dispose()
  }
}

export async function request(path, options) {
  return (await requestWithMeta(path, options)).data
}
