import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(currentDir, '../SettingsView.vue'), 'utf8')

describe('SettingsView admin API key reveal', () => {
  it('offers click-to-view, hide and copy without persisting plaintext', () => {
    expect(source).toContain('data-testid="admin-api-key-reveal"')
    expect(source).toContain('data-testid="admin-api-key-copy"')
    expect(source).toContain('adminAPI.settings.revealAdminApiKey()')
    expect(source).toContain('adminApiKeyPlaintext.value = "";')
    expect(source).toContain('await navigator.clipboard.writeText(key)')
    expect(source).not.toContain('localStorage.setItem("adminApiKey')
    expect(source).not.toContain('sessionStorage.setItem("adminApiKey')
    expect(source).not.toContain('管理员密钥只会完整显示一次')
  })
})
