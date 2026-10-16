import test from 'node:test'
import assert from 'node:assert/strict'
import { saveGroupChanges, GroupSaveError } from './groupSave.js'
import { createPricingFormState } from './pricing.js'

const customPricing = () => ({ ...createPricingFormState(), enabled: true })

test('编辑先保存价格再主体，主体失败明确价格已落库', async () => {
  const calls = [], cause = new Error('主体版本冲突')
  let pricingSaved = false
  const request = async (path, options) => { calls.push([path, options.method]); if (path.endsWith('/7')) throw cause }
  await assert.rejects(saveGroupChanges({ request, form: { id: 7, platform: 'openai' }, body: { name: '修改' }, pricing: customPricing(), onPricingSaved: () => { pricingSaved = true } }), error => error instanceof GroupSaveError && error.stage === 'base' && error.pricingSaved && error.cause === cause)
  assert.equal(pricingSaved, true)
  assert.deepEqual(calls, [['/admin/groups/7/pricing', 'PUT'], ['/admin/groups/7', 'PUT']])
})

test('新增价格失败只留下停用草稿，携带草稿ID供重新编辑', async () => {
  const calls = [], created = { id: 8, name: '草稿', platform: 'openai', status: 'inactive' }
  const request = async (path, options) => { calls.push([path, options]); if (path === '/admin/groups') return created; throw new Error('价格不可用') }
  await assert.rejects(saveGroupChanges({ request, form: { id: 0, platform: 'openai', status: 'active' }, body: { name: '草稿' }, pricing: customPricing() }), error => error.stage === 'pricing' && error.created === created && !error.pricingSaved)
  assert.equal(calls.length, 2)
  assert.equal(calls[0][1].body.status, 'inactive')
})

test('新增成功按停用草稿、价格、激活顺序完成，激活失败不冒充价格失败', async () => {
  const calls = [], created = { id: 8, name: '草稿' }
  const request = async (path, options) => { calls.push([path, options]); if (path === '/admin/groups') return created; if (path === '/admin/groups/8') throw new Error('启用失败') }
  await assert.rejects(saveGroupChanges({ request, form: { id: 0, platform: 'openai', status: 'active' }, body: {}, pricing: customPricing() }), error => error.stage === 'activation' && error.pricingSaved && error.created === created)
  assert.deepEqual(calls.map(([path]) => path), ['/admin/groups', '/admin/groups/8/pricing', '/admin/groups/8'])
  assert.equal(calls[2][1].body.status, 'active')
})

test('管理员选择停用时即使价格保存成功也不激活', async () => {
  const calls = []
  const request = async (path, options) => { calls.push([path, options]); if (path === '/admin/groups') return { id: 8 } }
  await saveGroupChanges({ request, form: { id: 0, platform: 'openai', status: 'inactive' }, body: {}, pricing: customPricing() })
  assert.deepEqual(calls.map(([path]) => path), ['/admin/groups', '/admin/groups/8/pricing'])
})

test('会话取消不包装成保存失败也不继续写后续阶段', async () => {
  const error = Object.assign(new Error('取消'), { name: 'AbortError' }), calls = []
  const request = async path => { calls.push(path); throw error }
  await assert.rejects(saveGroupChanges({ request, form: { id: 7, platform: 'openai' }, body: {}, pricing: customPricing() }), cause => cause === error)
  assert.deepEqual(calls, ['/admin/groups/7/pricing'])
})
