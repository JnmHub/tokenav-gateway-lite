import { describe, expect, it } from 'vitest'
import { sanitizeUrl } from '../url'

describe('sanitizeUrl', () => {
  it('允许合法 HTTPS 和显式允许的相对路径', () => {
    expect(sanitizeUrl('https://docs.example.com/path')).toBe('https://docs.example.com/path')
    expect(sanitizeUrl('/logo.png', { allowRelative: true })).toBe('/logo.png')
  })

  it('拒绝脚本协议和协议相对地址', () => {
    expect(sanitizeUrl('javascript:alert(1)', { allowRelative: true })).toBe('')
    expect(sanitizeUrl('//evil.example.com/logo.png', { allowRelative: true })).toBe('')
  })

  it('只在显式允许时接受图片 Data URL', () => {
    const dataUrl = 'data:image/png;base64,AAAA'
    expect(sanitizeUrl(dataUrl)).toBe('')
    expect(sanitizeUrl(dataUrl, { allowDataUrl: true })).toBe(dataUrl)
    expect(sanitizeUrl('data:text/html;base64,AAAA', { allowDataUrl: true })).toBe('')
  })
})
