/* ============================================================
 * 统一确认弹窗（Win12 视觉）
 *
 * 为什么不用 window.confirm：原生对话框不受主题控制、样式与 Win12 无关、
 * 手机上还会被浏览器当成普通对话框处理，且无法做危险操作红色主按钮。
 * 这里统一走 antd Modal.confirm（已接入 core/antdTheme.js 令牌），
 * 返回 Promise<boolean>，调用方 `if (!(await confirmAction({...}))) return`。
 * ============================================================ */
import { Modal } from 'ant-design-vue'

export function confirmAction({ title, content = '', okText = '确认', cancelText = '取消', danger = false } = {}) {
  return new Promise((resolve) => {
    Modal.confirm({
      title: title || '请确认',
      content,
      okText,
      cancelText,
      centered: true,
      // 危险操作：主按钮用低饱和红描边（与设计规范的危险按钮一致）
      okButtonProps: danger ? { danger: true } : undefined,
      onOk: () => resolve(true),
      onCancel: () => resolve(false),
      // 关闭（右上角 X）也按取消处理，避免 Promise 永远悬空
      afterClose: () => resolve(false),
    })
  })
}
