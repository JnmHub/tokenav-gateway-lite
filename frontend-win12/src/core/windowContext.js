import { inject, onScopeDispose, provide } from 'vue'
import { Modal } from 'ant-design-vue'
export const WINDOW_CONTEXT = Symbol('w12-window')
export const useWindowContext = () => inject(WINDOW_CONTEXT, null)

// 草稿由所属窗口统一询问；可登记多个表单，保存状态由业务自行决定。
export function useDirtyGuard(isDirty) {
  const context = useWindowContext()
  if (!context) return
  const unregister = context.registerDirty(isDirty)
  onScopeDispose(unregister)
}
export function useBusyGuard(isBusy) {
  const context = useWindowContext()
  const unregister = context?.registerBusy?.(isBusy)
  if (unregister) onScopeDispose(unregister)
}
export const provideWindowContext = (context) => provide(WINDOW_CONTEXT, context)

export function useDiscardGuard(isDirty, isBusy = () => false) {
  const context = useWindowContext()
  useDirtyGuard(isDirty)
  useBusyGuard(isBusy)
  let pending = null, dialog = null, finish = null, disposed = false
  onScopeDispose(() => { disposed = true; const current = dialog; finish?.(false); current?.destroy(); dialog = null })
  return () => {
    if (disposed || isBusy()) return Promise.resolve(false)
    if (!isDirty()) return Promise.resolve(true)
    if (pending) return pending
    pending = new Promise(resolve => {
      // 每个回调捕获自己的结算函数，旧弹窗动画结束不能结算后来打开的确认。
      const settle = allowed => {
        resolve(allowed && !disposed && !isBusy())
        if (finish === settle) { pending = null; finish = null; dialog = null }
      }
      finish = settle
      dialog = Modal.confirm({ title: '放弃未保存的修改？', content: '已保存的数据不会撤销，当前草稿会丢弃。', getContainer: context?.getContainer,
        wrapClassName: 'w12-design w12-dialog', okText: '放弃修改', cancelText: '继续编辑', onOk: () => settle(true), onCancel: () => settle(false) })
    })
    return pending
  }
}
