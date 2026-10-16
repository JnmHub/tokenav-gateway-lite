import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (name: string) =>
  readFileSync(resolve(process.cwd(), `src/components/account/${name}`), 'utf8')

describe('国产供应商账号创建与编辑闭环', () => {
  it('创建页提供三个原生平台入口和自适应多端点', () => {
    const source = read('CreateAccountModal.vue')
    expect(source).toContain("{ value: 'kimi', label: 'Kimi' }")
    expect(source).toContain("{ value: 'zhipu', label: '智谱 GLM' }")
    expect(source).toContain("{ value: 'deepseek', label: 'DeepSeek' }")
    expect(source).toContain('credentials.account_mode = accountMode.value')
    expect(source).toContain('credentials.api_protocol = apiProtocol.value')
    expect(source).toContain('credentials.api_base_urls = protocolBaseUrls')
  })

  it('编辑页回显并保存账号模式、协议和多端点', () => {
    const source = read('EditAccountModal.vue')
    expect(source).toContain("credentials?.account_mode === 'coding'")
    expect(source).toContain('credentials?.api_base_urls')
    expect(source).toContain('newCredentials.account_mode = editAccountMode.value')
    expect(source).toContain('newCredentials.api_protocol = editApiProtocol.value')
    expect(source).toContain('newCredentials.api_base_urls = protocolBaseUrls')
  })
})
