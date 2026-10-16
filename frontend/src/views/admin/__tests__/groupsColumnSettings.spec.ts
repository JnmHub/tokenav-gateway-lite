import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const groupsViewSource = readFileSync(resolve(currentDir, '../GroupsView.vue'), 'utf-8')

describe('groups table column settings', () => {
  it('persists hidden columns and keeps required columns visible', () => {
    expect(groupsViewSource).toContain('const HIDDEN_COLUMNS_KEY = "groups-hidden-columns"')
    expect(groupsViewSource).toContain('const DEFAULT_HIDDEN_COLUMNS = ["id"]')
    expect(groupsViewSource).toContain('const COLUMN_SETTINGS_VERSION = 3')
    expect(groupsViewSource).toContain('2: ["id"]')
    expect(groupsViewSource).toContain('3: ["usage"]')
    expect(groupsViewSource).toContain('const REQUIRED_COLUMN_KEYS = new Set(["name", "actions"])')
    expect(groupsViewSource).toContain(':columns="visibleColumns"')
    expect(groupsViewSource).toContain('localStorage.setItem(HIDDEN_COLUMNS_KEY')
  })

  it('exposes localized column settings labels', () => {
    expect(groupsViewSource).toContain('admin.groups.columnSettings')
    expect(groupsViewSource).toContain('admin.groups.viewColumns')
  })

  it('offers an ID column that is hidden by default and rendered in the existing table style', () => {
    expect(groupsViewSource).toContain('{ key: "id", label: t("admin.groups.columns.id"), sortable: true }')
    expect(groupsViewSource).toContain('<template #cell-id="{ value }">')
  })
})
