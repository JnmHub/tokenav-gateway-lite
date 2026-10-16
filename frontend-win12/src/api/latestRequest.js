import { getSessionEpoch, cancellationError, isCancellation } from './session.js'

/** 查询以最后一次调用为准：取消旧网络请求，并防止忽略取消的替身/迟到任务写回。 */
export function createLatestRequest() {
  let sequence = 0
  let controller = null
  let disposed = false
  return {
    begin() {
      controller?.abort(cancellationError())
      controller = new AbortController()
      const current = ++sequence
      const epoch = getSessionEpoch()
      const active = controller
      return {
        signal: active.signal,
        isCurrent: () => !disposed && current === sequence && epoch === getSessionEpoch() && !active.signal.aborted,
      }
    },
    cancel() {
      sequence += 1
      controller?.abort(cancellationError())
      controller = null
    },
    dispose() {
      this.cancel()
      disposed = true
    },
  }
}

export async function runLatest(lane, task, { apply, error, finish } = {}) {
  const turn = lane.begin()
  try {
    const result = await task(turn.signal)
    if (turn.isCurrent()) apply?.(result)
    return turn.isCurrent() ? result : undefined
  } catch (cause) {
    if (turn.isCurrent() && !isCancellation(cause)) error?.(cause)
  } finally {
    if (turn.isCurrent()) finish?.()
  }
}
