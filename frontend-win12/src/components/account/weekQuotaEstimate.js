/* 周额度总额预估：用窗口内已产生的账号采购成本 / 周占比，反推 100% 对应多少钱。
 *
 * 边界条件（重要）：
 * - 只在占比整除到 1% 档变化时重算，同一档沿用上次总额，避免金额随每次请求抖动；
 * - 缓存键必须带窗口周期指纹（resets_at）。否则跨周重置后，若占比恰好与上周相同，
 *   会沿用上周总额，得出完全错误的金额；
 * - 占比不足 1% 无法可靠反推（放大误差过大），不预估；
 * - 还没有窗口计费（cost 为 0）时不预估；
 * - 金额口径是「账号采购成本」（WindowStats.cost），不是用户实扣（user_cost），
 *   调用方必须在界面上写清楚，避免被理解成用户余额。
 */

const KEY = 'w12-week-quota-est-v2'

function readMap() {
  try {
    const raw = JSON.parse(localStorage.getItem(KEY) || '{}')
    return raw && typeof raw === 'object' ? raw : {}
  } catch { return {} }
}

function writeMap(map) {
  try { localStorage.setItem(KEY, JSON.stringify(map)) } catch { /* 隐私模式/存储满时不影响本次展示 */ }
}

/** 窗口周期指纹：同一 resets_at 视为同一周期；缺失时退化为固定串（此时不做跨周区分）。 */
function windowToken(resetsAt) {
  if (!resetsAt) return 'no-reset'
  const ms = new Date(resetsAt).getTime()
  // 分钟级对齐，避免上游毫秒抖动把同一周期当成两个周期而反复重算。
  return Number.isFinite(ms) ? `r${Math.round(ms / 60000)}` : String(resetsAt)
}

/**
 * @param {number|string} accountId 账号 ID
 * @param {number} utilization 窗口占比（0-100+）
 * @param {number} usedCost 窗口内账号采购成本
 * @param {string|null} resetsAt 窗口重置时间（周期指纹来源）
 * @returns {{ready: boolean, reason?: string, pct?: number, used?: number, total?: number, remaining?: number, refreshed?: boolean}}
 */
export function estimateWeekQuota(accountId, utilization, usedCost, resetsAt = null) {
  if (!accountId) return { ready: false, reason: 'noid' }
  const pct = Math.floor(Number(utilization) || 0)
  const used = Number(usedCost) || 0
  const id = `${accountId}:${windowToken(resetsAt)}`
  const map = readMap()
  const prev = map[id]

  if (pct < 1) return { ready: false, reason: 'lt1', pct, used }
  if (used <= 0) return { ready: false, reason: 'nocost', pct, used }

  if (!prev || Number(prev.pct) !== pct || !(Number(prev.total) > 0)) {
    const total = used / (pct / 100)
    map[id] = { pct, used, total, at: Date.now() }
    writeMap(map)
    return { ready: true, pct, used, total, remaining: Math.max(0, total - used), refreshed: true }
  }
  return {
    ready: true,
    pct,
    used,
    total: Number(prev.total),
    remaining: Math.max(0, Number(prev.total) - used),
    refreshed: false,
  }
}

/** 清理某账号的历史周期估算，避免 localStorage 无限增长。 */
export function forgetWeekQuota(accountId) {
  if (!accountId) return
  const prefix = `${accountId}:`
  const map = readMap()
  let changed = false
  for (const key of Object.keys(map)) {
    if (key.startsWith(prefix)) { delete map[key]; changed = true }
  }
  if (changed) writeMap(map)
}
