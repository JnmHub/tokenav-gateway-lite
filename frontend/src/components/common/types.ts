/**
 * Common component types
 */

export interface Column {
  key: string
  label: string
  sortable?: boolean
  /** Fixed desktop width in pixels when DataTable fixedLayout is enabled. */
  width?: number
  class?: string
  formatter?: (value: any, row: any) => string
}
