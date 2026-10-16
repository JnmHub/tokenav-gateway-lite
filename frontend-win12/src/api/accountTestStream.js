import { getToken, ApiError, setToken } from './index.js'
import { createRequestScope, getSessionEpoch } from './session.js'

/** SSE（持续事件流）保持作用域直到最后一个片段读取完，而非只保护响应头。 */
export async function readAccountTestStream(accountId, body, options, onEvent) {
  const scope = createRequestScope(options?.signal, options?.sessionEpoch ?? getSessionEpoch())
  let reader
  const abortReader = () => { reader?.cancel().catch(() => {}) }
  scope.signal.addEventListener('abort', abortReader, { once: true })
  try {
    scope.assertCurrent()
    const token = getToken()
    const response = await fetch(new URL(`/api/v1/admin/accounts/${accountId}/test`, window.location.origin), {
      method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify(body), signal: scope.signal,
    })
    scope.assertCurrent()
    if (!response.ok) {
      const payload = await response.json().catch(() => null)
      scope.assertCurrent()
      if (response.status === 401 && payload && 'code' in payload) setToken(null)
      throw new ApiError(payload?.message || `请求失败（HTTP ${response.status}）`, { status: response.status, payload, headers: response.headers })
    }
    if (!response.body) throw new Error('后台未返回测试事件流')
    reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    const consume = (line) => {
      if (!line.startsWith('data:')) return
      let event
      try { event = JSON.parse(line.slice(5).trim()) } catch { return }
      scope.assertCurrent()
      onEvent(event)
    }
    while (true) {
      const { done, value } = await reader.read()
      scope.assertCurrent()
      buffer += done ? decoder.decode() : decoder.decode(value, { stream: true })
      const lines = buffer.split(/\r?\n/)
      buffer = lines.pop() || ''
      for (const line of lines) consume(line)
      if (done) { if (buffer) consume(buffer); break }
    }
  } finally {
    scope.signal.removeEventListener('abort', abortReader)
    if (reader) { await reader.cancel().catch(() => {}); reader.releaseLock() }
    scope.dispose()
  }
}
