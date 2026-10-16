import { request } from '../../api/index.js'

/* 账号配额路由：静态导入确保请求在点击时取得所属实例和会话，避免导入迟到再发请求。 */
export const quotaPath = (r, suffix, method, options = {}) => {
  const base = r.platform === 'grok' ? `/admin/grok/accounts/${r.id}` : `/admin/openai/accounts/${r.id}`
  return request(`${base}/${suffix}`, { ...options, method: suffix === 'quota' ? 'GET' : (method || 'POST') })
}
