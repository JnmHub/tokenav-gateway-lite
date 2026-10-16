import { pricingFormToRequest } from './pricing.js'
import { isCancellation } from '../../api/session.js'

export class GroupSaveError extends Error {
  constructor(stage, cause, { created = null, pricingSaved = false } = {}) {
    super(cause.message, { cause })
    this.name = 'GroupSaveError'
    this.stage = stage
    this.created = created
    this.pricingSaved = pricingSaved
  }
}

// 两个接口不具备跨请求事务：明确已落库阶段，失败时不能再创建副本或误启用无价格草稿。
export async function saveGroupChanges({ request, form, body, pricing, onPricingSaved }) {
  if (form.id) {
    await request(`/admin/groups/${form.id}/pricing`, { method: 'PUT', body: pricingFormToRequest(pricing, form.platform, form.id) })
    onPricingSaved?.()
    try { await request(`/admin/groups/${form.id}`, { method: 'PUT', body }) }
    catch (cause) {
      if (isCancellation(cause)) throw cause
      throw new GroupSaveError('base', cause, { pricingSaved: true })
    }
    return null
  }
  const created = await request('/admin/groups', { method: 'POST', body: { ...body, status: pricing.enabled ? 'inactive' : form.status } })
  if (!pricing.enabled) return created
  try {
    await request(`/admin/groups/${created.id}/pricing`, { method: 'PUT', body: pricingFormToRequest(pricing, form.platform, created.id) })
  } catch (cause) {
    if (isCancellation(cause)) throw cause
    throw new GroupSaveError('pricing', cause, { created })
  }
  // 管理员主动选择停用时，价格完成也保持停用；只有明确启用才执行最后一步。
  if (form.status === 'active') {
    try { await request(`/admin/groups/${created.id}`, { method: 'PUT', body: { status: 'active' } }) }
    catch (cause) {
      if (isCancellation(cause)) throw cause
      throw new GroupSaveError('activation', cause, { created, pricingSaved: true })
    }
  }
  return created
}
