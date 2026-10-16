import { ref } from 'vue'
import { useDirtyGuard, useBusyGuard } from '../core/windowContext.js'

export const cloneDraft = (value) => value == null ? value : JSON.parse(JSON.stringify(value))
export const sameDraft = (a, b) => JSON.stringify(a) === JSON.stringify(b)

/** 基线只记录后台已确认的数据；回填前记录续改，失败时绝不把草稿当作已保存。 */
export function useDraftBaseline(getDraft, isBusy) {
  const baseline = ref(null)
  const edits = () => {
    const current = getDraft()
    if (!current || !baseline.value) return {}
    return Object.fromEntries(Object.entries(current).filter(([key, value]) => !sameDraft(value, baseline.value[key])))
  }
  useDirtyGuard(() => baseline.value !== null && !sameDraft(getDraft(), baseline.value))
  useBusyGuard(isBusy)
  return {
    baseline, edits,
    accept(value = getDraft()) { baseline.value = cloneDraft(value) },
    acceptPart(fields) { baseline.value = { ...baseline.value, ...cloneDraft(fields) } },
    clear() { baseline.value = null },
  }
}

/** 缺字段是未知合同，不能用空值构造一次破坏性的全量保存。 */
export function requireFields(value, fields) {
  if (!value || typeof value !== 'object' || fields.some(key => !Object.hasOwn(value, key) || value[key] === undefined)) {
    throw new Error('后台返回的配置不完整，请重新读取后再保存')
  }
  return value
}
export function retainEdits(current, submitted) {
  return Object.fromEntries(Object.entries(current || {}).filter(([key, value]) => !sameDraft(value, submitted[key])))
}
