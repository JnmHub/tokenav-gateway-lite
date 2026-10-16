import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const groupsViewSource = readFileSync(resolve(currentDir, '../GroupsView.vue'), 'utf8')
const localKeysViewSource = readFileSync(resolve(currentDir, '../GatewayLiteLocalKeysView.vue'), 'utf8')
const zhLocaleSource = readFileSync(resolve(currentDir, '../../../i18n/locales/zh.ts'), 'utf8')
const enLocaleSource = readFileSync(resolve(currentDir, '../../../i18n/locales/en.ts'), 'utf8')

describe('GroupsView local-only group control', () => {
  it('keeps control-plane publishing enabled by default and restores the safe default on reset', () => {
    expect(groupsViewSource).toContain('local_only: false')
    expect(groupsViewSource).toContain('createForm.local_only = false')
    expect(groupsViewSource).toContain('editForm.local_only = false')
  })

  it('hydrates existing state and exposes accessible switches in both forms', () => {
    expect(groupsViewSource).toContain('editForm.local_only = group.local_only ?? false')
    expect(groupsViewSource).toContain(':aria-checked="createForm.local_only"')
    expect(groupsViewSource).toContain(':aria-checked="editForm.local_only"')
    expect(groupsViewSource).toContain('createForm.local_only = !createForm.local_only')
    expect(groupsViewSource).toContain('editForm.local_only = !editForm.local_only')
  })

  it('marks local-only groups in the always-visible name cell', () => {
    expect(groupsViewSource).toContain('v-if="row.local_only"')
    expect(groupsViewSource).toContain('admin.groups.columns.localOnly')
  })

  it('marks local-only choices when creating a gateway-local API key', () => {
    expect(localKeysViewSource).toContain("group.local_only ? ` · ${localText('仅本地', 'Local only')}` : ''")
  })

  it('explains the boundary in both supported languages', () => {
    expect(zhLocaleSource).toContain("localOnly: '仅本地使用（不推送主站）'")
    expect(zhLocaleSource).toContain('只能绑定轻量网关本地创建的 API 密钥')
    expect(enLocaleSource).toContain("localOnly: 'Local only (do not publish to control plane)'")
    expect(enLocaleSource).toContain('only API keys created locally on this gateway can bind to it')
  })
})
