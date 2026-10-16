/**
 * 将网关返回的入口实时请求数转换为可展示的值。
 *
 * 该指标是观测数据；旧网关、缓存响应或异常响应可能不带这个字段。此时返回
 * null，让界面展示“—”，不能把未知状态误导为真实的零请求。
 */
export function normalizeGroupIngressRequests(value: unknown): number | null {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
    return null
  }

  return value
}
