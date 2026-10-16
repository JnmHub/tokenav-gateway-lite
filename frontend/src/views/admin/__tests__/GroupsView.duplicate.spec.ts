import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const groupsViewSource = readFileSync(resolve(currentDir, '../GroupsView.vue'), 'utf8')

describe('GroupsView duplicate flow', () => {
  it('提供防双击复制入口和包含平台、账号数量的确认摘要', () => {
    expect(groupsViewSource).toContain('data-testid="group-duplicate"')
    expect(groupsViewSource).toContain(':disabled="duplicatingGroupIds.has(row.id)"')
    expect(groupsViewSource).toContain(':message="duplicateConfirmMessage"')
    expect(groupsViewSource).toContain('platform: group.platform')
    expect(groupsViewSource).toContain('count: group.account_count ?? 0')
  })

  it('复制成功后刷新列表并打开停用副本编辑页', () => {
    expect(groupsViewSource).toContain('const duplicate = await adminAPI.groups.duplicate(source.id)')
    expect(groupsViewSource).toContain('await loadGroups()')
    expect(groupsViewSource).toContain('await handleEdit(duplicate)')
    expect(groupsViewSource).toContain('duplicateSuccess')
  })

  it('错误提示使用统一 API 错误提取且始终释放防重状态', () => {
    expect(groupsViewSource).toContain('extractApiErrorMessage(error, t("admin.groups.duplicateFailed"))')
    expect(groupsViewSource).toContain('duplicatingGroupIds.delete(source.id)')
  })
})
