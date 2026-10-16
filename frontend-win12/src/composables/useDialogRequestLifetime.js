import { watch } from 'vue'
import { cancellationError } from '../api/session.js'
import { useRequestLifetime } from './useRequestLifetime.js'

/** 一个弹窗可反复打开；每次打开/换目标都要换取消信号，旧任务不能写入新弹窗。 */
export function useDialogRequestLifetime(isOpen, clear, target = () => null) {
  let controller = new AbortController()
  controller.abort()
  let serial = 0
  const cancel = () => { serial += 1; controller.abort(); clear() }
  const lifetime = useRequestLifetime(cancel)
  const begin = () => {
    cancel()
    if (lifetime.isAlive()) controller = new AbortController()
  }
  watch([isOpen, target], ([open]) => { if (open) begin(); else cancel() }, { immediate: true, flush: 'sync' })
  return {
    begin, cancel,
    capture() {
      const turn = serial
      const signal = AbortSignal.any([controller.signal, lifetime.signal])
      const isCurrent = () => turn === serial && !signal.aborted && lifetime.isAlive() && isOpen()
      return {
        signal, sessionEpoch: lifetime.sessionEpoch, isCurrent,
        assertCurrent() { if (!isCurrent()) throw cancellationError() },
      }
    },
  }
}
