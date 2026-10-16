import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const groupsViewSource = readFileSync(resolve(currentDir, '../GroupsView.vue'), 'utf8')
const typesSource = readFileSync(resolve(currentDir, '../../../types/index.ts'), 'utf8')
const zhLocaleSource = readFileSync(resolve(currentDir, '../../../i18n/locales/zh.ts'), 'utf8')
const enLocaleSource = readFileSync(resolve(currentDir, '../../../i18n/locales/en.ts'), 'utf8')

describe('GroupsView fallback model', () => {
  it('在创建和编辑表单中提交同一个兜底模型字段', () => {
    expect(groupsViewSource).toMatch(/v-if="createForm\.platform === 'openai'"[\s\S]{0,3000}v-model\.trim="createForm\.fallback_model"/)
    expect(groupsViewSource).toMatch(/v-if="editForm\.platform === 'openai'"[\s\S]{0,3000}v-model\.trim="editForm\.fallback_model"/)
    expect(groupsViewSource).toContain('v-model.trim="createForm.fallback_model"')
    expect(groupsViewSource).toContain('v-model.trim="editForm.fallback_model"')
    expect(groupsViewSource).toContain('fallback_model: ""')
  })

  it('编辑时回显并在关闭弹窗后清空兜底模型', () => {
    expect(groupsViewSource).toContain('editForm.fallback_model = group.fallback_model || ""')
    expect(groupsViewSource).toContain('createForm.fallback_model = ""')
    expect(groupsViewSource).toContain('editForm.fallback_model = ""')
  })

  it('在接口类型和中英文提示中保持明确语义', () => {
    expect(typesSource).toContain('fallback_model: string')
    expect(typesSource).toContain('fallback_model?: string')
    expect(zhLocaleSource).toContain("fallbackModel: 'OpenAI 分组兜底模型'")
    expect(zhLocaleSource).toContain('仅 OpenAI 文本请求在原模型无可用账号时尝试一次')
    expect(enLocaleSource).toContain("fallbackModel: 'OpenAI group fallback model'")
    expect(enLocaleSource).toContain('original model has no available account')
  })
})
