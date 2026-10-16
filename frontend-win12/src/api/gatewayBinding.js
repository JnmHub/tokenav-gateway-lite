import { request, requestWithMeta } from './index.js'

const base = '/admin/gateway-binding'
export const bindingApi = {
  status: options => request(`${base}/status`, { method: 'GET', ...options }),
  probe: (body, options) => request(`${base}/probe`, { body, ...options }),
  verify: (body, options) => request(`${base}/verify`, { body, ...options }),
  register: (body, options) => requestWithMeta(`${base}/register`, { body, ...options }),
  check: options => request(`${base}/disconnect-check`, { body: {}, ...options }),
  disconnect: options => request(`${base}/disconnect`, { body: { confirm: true }, ...options }),
  cancel: options => request(`${base}/cancel`, { body: {}, ...options }),
  retry: options => request(`${base}/retry`, { body: {}, ...options }),
}

export const bindingPhaseText = phase => ({ unbound: '尚未绑定', registering: '等待完成登记', activating: '正在建立连接', bound: '已绑定', draining: '正在完成旧主站结算', blocked: '暂不能断开' }[phase] || '状态未知')
export function bindingErrorText(error) {
  const code = error?.message || ''
  return ({
    unauthorized: '主站对接密钥无效，请从主站设置重新复制。',
    binding_url_invalid: '请输入有效的 HTTP 或 HTTPS 主站地址。',
    binding_public_url_invalid: '请输入客户可访问的 HTTP 或 HTTPS 节点地址。',
    binding_control_plane_unreachable: '无法连接主站，请检查地址与网络后重试。',
    binding_capability_not_supported: '该主站未提供新版网关绑定能力，请先更新主站。',
    binding_node_conflict: '节点身份已登记且资料不一致，不能覆盖原节点。',
    binding_registration_pending_retry_same_destination: '上次登记结果尚未确认，请保留相同节点资料重试。',
    binding_restart_unavailable: '当前服务未接入有序重启，请由运维按正常启动方式运行后重试。',
    binding_response_invalid: '主站响应格式不正确，请确认地址及主站版本。',
    binding_state_conflict: '绑定状态已变化，请刷新后重试。',
  })[code] || code || '操作未完成，请重试。'
}
export function bindingIsConfirmed(status, expectedId = '') {
  return status?.phase === 'bound' && Boolean(status.binding_id) && (!expectedId || status.binding_id === expectedId) && status.handshake_at > 0 && status.snapshot_at > 0
}
export function bindingRequiresGate(status) {
  return !status || Boolean(status.first_binding_required)
}
export async function waitForBinding(read, expectedId, { signal, delay = ms => new Promise(resolve => setTimeout(resolve, ms)), attempts = 60, onStatus = () => {} } = {}) {
  for (let i = 0; i < attempts; i++) {
    if (signal?.aborted) throw new DOMException('已取消', 'AbortError')
    try {
      const status = await read()
      onStatus(status)
      if (bindingIsConfirmed(status, expectedId)) return status
      if (status.binding_id && status.binding_id !== expectedId) throw new Error('binding_state_conflict')
    } catch (error) {
      if (signal?.aborted || error?.status === 401 || error?.message === 'binding_state_conflict') throw error
      // A graceful restart briefly closes the listener. Persisted registration
      // is only accepted after the new process confirms the same binding id.
    }
    await delay(1500)
  }
  throw new Error('连接仍未确认，可稍后刷新或重试连接；节点资料已保存。')
}
