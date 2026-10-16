import { onUnmounted } from 'vue'
import { getSessionEpoch, subscribeSessionReset } from '../api/session.js'

/** 所属业务实例的请求和明文草稿随会话结束；普通关窗也必须中止该实例的请求。 */
export function useRequestLifetime(clear = () => {}) {
  const controller = new AbortController()
  const epoch = getSessionEpoch()
  const cleanup = () => { controller.abort(); clear() }
  const unsubscribe = subscribeSessionReset(cleanup)
  onUnmounted(() => { unsubscribe(); cleanup() })
  return {
    signal: controller.signal,
    sessionEpoch: epoch,
    isAlive: () => !controller.signal.aborted && epoch === getSessionEpoch(),
  }
}
