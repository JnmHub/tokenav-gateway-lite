import { request } from './index.js'
import { cancellationError, getSessionEpoch } from './session.js'
import { requireFields } from '../composables/useDraftBaseline.js'

// 后端仍全量接收非指针字段；同页多个标签写入先串行读取最新配置，只覆盖本区。
// 该接口没有服务端修订，无法保证不同管理员/不同标签页同时读写的原子性。
export const gatewayConnectionFields = ['region', 'gateway_code', 'redis_prefix', 'control_plane_url', 'control_plane_timeout_ms',
  'runtime_health_interval_seconds', 'runtime_active_window_seconds', 'config_sync_interval_seconds',
  'cache_invalidation_interval_seconds', 'usage_queue_pending_alert_threshold', 'usage_queue_dead_alert_threshold']
export const gatewayLoginFields = ['admin_login_path', 'login_title', 'login_subtitle']
export const assertGatewayConfig = (data) => requireFields(data, [...gatewayConnectionFields, ...gatewayLoginFields,
  'allow_live', 'control_plane_token_configured', 'admin_sync_key_configured', 'legacy_snapshot_scheduler_authority'])
let writes = Promise.resolve()
export function saveGatewayConfig(part, options = {}) {
  const epoch = options.sessionEpoch ?? getSessionEpoch()
  const assertCurrent = () => { if (options.signal?.aborted || epoch !== getSessionEpoch()) throw cancellationError() }
  const work = writes.catch(() => {}).then(async () => {
    assertCurrent()
    const latest = assertGatewayConfig(await request('/admin/gateway-lite/config', { ...options, sessionEpoch: epoch, method: 'GET' }))
    const body = Object.fromEntries(gatewayConnectionFields.map(key => [key, latest[key]]))
    Object.assign(body, part)
    assertCurrent()
    return assertGatewayConfig(await request('/admin/gateway-lite/config', { ...options, sessionEpoch: epoch, method: 'PUT', body }))
  })
  writes = work.catch(() => {})
  return work
}
