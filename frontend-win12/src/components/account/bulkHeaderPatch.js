const blocked = new Set(['host', 'content-length', 'content-type', 'transfer-encoding', 'connection', 'keep-alive',
  'proxy-authenticate', 'proxy-authorization', 'proxy-connection', 'te', 'trailer', 'upgrade', 'authorization',
  'x-api-key', 'x-goog-api-key', 'cookie', 'accept-encoding', 'sec-websocket-key', 'sec-websocket-version',
  'sec-websocket-extensions', 'sec-websocket-protocol', 'sec-websocket-accept', 'session_id', 'conversation_id',
  'x-codex-turn-state', 'x-codex-turn-metadata', 'chatgpt-account-id', 'x-claude-code-session-id', 'x-client-request-id', 'x-grok-conv-id'])
const namePattern = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/
const invalidValue = /[\x00-\x08\x0a-\x1f\x7f]/
const encoder = new TextEncoder()

/** 与旧 credentialsBuilder 和后台限制一致；金额/身份/会话头不能通过批量业务覆写。 */
export function buildBulkHeaderPatch(enabled, rows) {
  if (!enabled) return { header_override_enabled: false, header_overrides: {} }
  const headers = Object.create(null), seen = new Set()
  for (const row of rows || []) {
    const name = String(row.name || '').trim(), value = String(row.value || '').trim(), lower = name.toLowerCase()
    if (!name && !value) continue
    if (!namePattern.test(name) || name.length > 200) throw new Error('请求头名称无效或超过200字符')
    if (blocked.has(lower)) throw new Error('认证、会话路由或传输请求头不能覆写')
    if (seen.has(lower)) throw new Error('请求头名称重复（不区分大小写）')
    if (invalidValue.test(value) || encoder.encode(value).length > 8192) throw new Error('请求头值含控制字符或超过8192字节')
    seen.add(lower); headers[lower] = value
  }
  if (!seen.size) throw new Error('批量开启覆写至少填写一条请求头；清空请关闭开关')
  if (seen.size > 64) throw new Error('请求头最多64条')
  return { header_override_enabled: true, header_overrides: headers }
}
