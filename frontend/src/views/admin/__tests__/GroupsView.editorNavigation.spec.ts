import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const groupsViewSource = readFileSync(resolve(currentDir, '../GroupsView.vue'), 'utf8')
const navigationSource = readFileSync(resolve(currentDir, '../../../components/admin/group/GroupEditorSectionNav.vue'), 'utf8')

describe('GroupsView editor navigation', () => {
  it('uses the same four focused sections for create and edit', () => {
    expect(groupsViewSource).toContain('<GroupEditorSectionNav')
    expect(groupsViewSource).toContain('v-model="createEditorSection"')
    expect(groupsViewSource).toContain('v-model="editEditorSection"')
    expect(groupsViewSource).toContain('create-group-editor-basics')
    expect(groupsViewSource).toContain('create-group-editor-models')
    expect(groupsViewSource).toContain('create-group-editor-media')
    expect(groupsViewSource).toContain('create-group-editor-advanced')
    expect(groupsViewSource).toContain('edit-group-editor-basics')
    expect(groupsViewSource).toContain('edit-group-editor-models')
    expect(groupsViewSource).toContain('edit-group-editor-media')
    expect(groupsViewSource).toContain('edit-group-editor-advanced')
  })

  it('starts each new session in basics and keeps the full-width editor usable', () => {
    expect(groupsViewSource).toContain('width="extra-wide"')
    expect(groupsViewSource).toContain('createEditorSection.value = "basics"')
    expect(groupsViewSource).toContain('editEditorSection.value = "basics"')
    expect(groupsViewSource).toContain('lg:grid-cols-[13rem_minmax(0,1fr)]')
  })

  it('keeps a horizontal touch-friendly navigator and a desktop sticky navigator', () => {
    expect(navigationSource).toContain('overflow-x-auto')
    expect(navigationSource).toContain('lg:sticky')
    expect(navigationSource).toContain('role="tablist"')
    expect(navigationSource).toContain('role="tab"')
  })
})
