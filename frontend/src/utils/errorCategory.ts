export type ErrorCategory =
  | 'auth'
  | 'rate_limit'
  | 'quota'
  | 'invalid_request'
  | 'service_unavailable'
  | 'upstream'
  | 'internal'
  | 'cyber'
  | 'other'

// 与后端 service.MapUserErrorCategory 保持同一口径，分类码可稳定用于筛选和展示。
export function mapErrorCategory(phase?: string | null, errorType?: string | null): ErrorCategory {
  switch ((phase || '').toLowerCase()) {
    case 'auth':
      return 'auth'
    case 'routing':
      return 'service_unavailable'
    case 'upstream':
    case 'network':
      return 'upstream'
    case 'internal':
      return 'internal'
    case 'request':
      switch ((errorType || '').toLowerCase()) {
        case 'rate_limit_error':
          return 'rate_limit'
        case 'billing_error':
        case 'subscription_error':
          return 'quota'
        case 'invalid_request_error':
          return 'invalid_request'
        case 'cyber_policy':
          return 'cyber'
      }
  }
  return 'other'
}

export const ERROR_CATEGORY_BADGE_CLASSES: Record<ErrorCategory, string> = {
  auth: 'bg-blue-50 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-300 dark:ring-blue-500/30',
  rate_limit: 'bg-purple-50 text-purple-700 ring-purple-600/20 dark:bg-purple-900/30 dark:text-purple-300 dark:ring-purple-500/30',
  quota: 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-300 dark:ring-amber-500/30',
  invalid_request: 'bg-yellow-50 text-yellow-700 ring-yellow-600/20 dark:bg-yellow-900/30 dark:text-yellow-300 dark:ring-yellow-500/30',
  service_unavailable: 'bg-orange-50 text-orange-700 ring-orange-600/20 dark:bg-orange-900/30 dark:text-orange-300 dark:ring-orange-500/30',
  upstream: 'bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-300 dark:ring-red-500/30',
  internal: 'bg-gray-100 text-gray-700 ring-gray-600/20 dark:bg-dark-700 dark:text-gray-200 dark:ring-dark-500/30',
  cyber: 'bg-rose-50 text-rose-700 ring-rose-600/20 dark:bg-rose-900/30 dark:text-rose-300 dark:ring-rose-500/30',
  other: 'bg-slate-50 text-slate-700 ring-slate-600/20 dark:bg-slate-800 dark:text-slate-300 dark:ring-slate-500/30'
}

export const FILTERABLE_ERROR_CATEGORIES: Exclude<ErrorCategory, 'other'>[] = [
  'auth',
  'rate_limit',
  'quota',
  'invalid_request',
  'service_unavailable',
  'upstream',
  'internal',
  'cyber'
]
