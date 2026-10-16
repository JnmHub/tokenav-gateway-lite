import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(currentDir, '../GroupsView.vue'), 'utf8')

describe('GroupsView composite image permission', () => {
  it('创建和编辑聚合分组时都展示生图权限', () => {
    expect(source).toContain("createForm.platform === 'grok' ||\n            createForm.platform === 'composite'")
    expect(source).toContain("editForm.platform === 'grok' ||\n            editForm.platform === 'composite'")
  })
})
