import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(currentDir, '../SettingsView.vue'), 'utf8')

describe('SettingsView custom client error message', () => {
  it('uses one optional text field and explains the stable machine fields', () => {
    expect(source).toContain('v-model="form.gateway_custom_error_message"')
    expect(source).toContain('maxlength="500"')
    expect(source).toContain('留空时返回真实错误')
    expect(source).toContain('HTTP 状态码和错误代码保持真实')
  })

  it('loads and saves through the existing settings endpoint payload', () => {
    expect(source).toContain('gateway_custom_error_message: settings.gateway_custom_error_message || ""')
    expect(source).toContain('gateway_custom_error_message: form.gateway_custom_error_message.trim()')
  })
})
