/**
 * Resolve the account procurement cost shown in admin usage views.
 *
 * A dedicated account_stats_cost is authoritative, including zero. Only
 * legacy rows without an upstream model may fall back to total_cost; a row
 * that records an upstream model but has no resolved procurement price must
 * not expose the customer's (possibly mapped/high-price) charge as cost.
 */
export function resolveAccountBilledCost(row: {
  total_cost?: number | null
  account_stats_cost?: number | null
  account_rate_multiplier?: number | null
  upstream_model?: string | null
}): number {
  let base = 0
  if (row.account_stats_cost != null) {
    base = row.account_stats_cost
  } else if (!row.upstream_model?.trim()) {
    base = row.total_cost ?? 0
  }
  const result = base * (row.account_rate_multiplier ?? 1)
  return Number.isFinite(result) ? result : 0
}
