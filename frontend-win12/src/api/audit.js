import { request, ApiError } from './index.js'

export async function clearAuditLogs(code, options = {}) {
  const headers = new Headers(options.headers)
  const trimmed = String(code || '').trim()
  if (trimmed) {
    const grant = await request('/user/totp/step-up', {
      ...options, method: 'POST', body: { code: trimmed, action_scope: 'audit_logs.clear' },
    })
    if (grant?.verified !== true || typeof grant.token !== 'string' || !grant.token) {
      throw new ApiError('二次验证未返回有效授权，请重新验证', { payload: { reason: 'STEP_UP_INVALID_GRANT' } })
    }
    headers.set('X-Step-Up-Token', grant.token)
  }
  // 票据只存在于本次调用，不缓存或自动重试；重复消费由后端明确拒绝。
  return request('/admin/audit-logs/clear', { ...options, method: 'POST', headers })
}
