/* ============================================================
 * 网关设置共享工具：数值归一化（对照旧 SettingsView.vue 语义）
 * clampInt  → 旧 normalizeNumber：非法回退默认值、向下取整、夹取区间
 * clampNum  → 旧 normalizeDecimal：不取整（仅权重类字段使用）
 * ============================================================ */

export function clampInt(value, fallback, min, max) {
  const n = Number(value)
  if (!Number.isFinite(n)) return fallback
  return Math.min(max, Math.max(min, Math.floor(n)))
}

export function clampNum(value, fallback, min, max) {
  const n = Number(value)
  if (!Number.isFinite(n)) return fallback
  return Math.min(max, Math.max(min, n))
}

/** 逗号/空白分隔的白名单输入 → 去重去空数组（对照旧 parse 逻辑） */
export function parseWhitelist(value) {
  return Array.from(new Set(
    String(value || '')
      .split(/[,\s]+/)
      .map((item) => item.trim())
      .filter(Boolean),
  ))
}
