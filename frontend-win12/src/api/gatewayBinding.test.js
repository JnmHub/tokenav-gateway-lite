import test from 'node:test'
import assert from 'node:assert/strict'
import { bindingIsConfirmed, bindingRequiresGate, waitForBinding, bindingErrorText } from './gatewayBinding.js'

test('首次绑定门禁不把已保存或202启用中当作完成', () => {
  assert.equal(bindingRequiresGate(null), true)
  assert.equal(bindingRequiresGate({ first_binding_required: true, phase: 'activating' }), true)
  assert.equal(bindingIsConfirmed({ phase: 'activating', binding_id: 'one', handshake_at: 1, snapshot_at: 1 }), false)
  assert.equal(bindingIsConfirmed({ phase: 'bound', binding_id: 'one', handshake_at: 0, snapshot_at: 1 }), false)
  assert.equal(bindingIsConfirmed({ phase: 'bound', binding_id: 'other', handshake_at: 1, snapshot_at: 1 }, 'one'), false)
})
test('等待重启允许监听器瞬断，只接受原bindingid真实握手和快照', async () => {
  const states = [new TypeError('network'), { phase: 'activating', binding_id: 'one' }, { phase: 'bound', binding_id: 'one', handshake_at: 3, snapshot_at: 4 }]
  const read = async () => { const next = states.shift(); if (next instanceof Error) throw next; return next }
  const status = await waitForBinding(read, 'one', { delay: async () => {}, attempts: 3 })
  assert.equal(status.snapshot_at, 4)
})
test('其他bindingid、登录过期和取消不会继续轮询', async () => {
  await assert.rejects(waitForBinding(async () => ({ phase: 'bound', binding_id: 'other' }), 'one', { delay: async () => {} }), /binding_state_conflict/)
  await assert.rejects(waitForBinding(async () => { throw Object.assign(new Error('登录已失效'), { status: 401 }) }, 'one', { delay: async () => {} }), /登录已失效/)
  const controller = new AbortController(); controller.abort()
  await assert.rejects(waitForBinding(async () => ({}), 'one', { signal: controller.signal, delay: async () => {} }), { name: 'AbortError' })
})
test('启用超时不伪报成功，错误密钥不显示内部日志', async () => {
  await assert.rejects(waitForBinding(async () => ({ phase: 'activating', binding_id: 'one' }), 'one', { delay: async () => {}, attempts: 2 }), /连接仍未确认/)
  assert.match(bindingErrorText(new Error('unauthorized')), /主站对接密钥无效/)
})
