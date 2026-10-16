// 请求层与外壳共用会话代际，不依赖 Pinia，避免 stores → api → stores 循环。
let epoch = 0
const listeners = new Set()
const activeRequests = new Set()

export const getSessionEpoch = () => epoch

export function subscribeSessionReset(listener) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export const onSessionReset = subscribeSessionReset

export function cancellationError(reason = '请求已取消') {
  return new DOMException(reason, 'AbortError')
}

export function resetRequestSession(reason = 'session-reset') {
  epoch += 1
  // 先使代际失效，再取消请求；即使网络层已经收到响应，也不能写回新会话。
  for (const controller of activeRequests) controller.abort(cancellationError('登录会话已切换'))
  activeRequests.clear()
  for (const listener of listeners) listener({ epoch, reason })
  return epoch
}

export function createRequestScope(signal, expectedEpoch = epoch) {
  const controller = new AbortController()
  const abort = () => controller.abort(cancellationError())
  if (signal?.aborted) abort()
  else signal?.addEventListener('abort', abort, { once: true })
  activeRequests.add(controller)
  const assertCurrent = () => {
    if (controller.signal.aborted || expectedEpoch !== epoch) throw cancellationError()
  }
  return {
    signal: controller.signal,
    assertCurrent,
    dispose() {
      activeRequests.delete(controller)
      signal?.removeEventListener('abort', abort)
    },
  }
}

export const isCancellation = (error) => error?.name === 'AbortError'
