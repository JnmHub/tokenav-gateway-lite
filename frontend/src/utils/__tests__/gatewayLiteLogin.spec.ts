import { beforeEach, describe, expect, it, vi } from 'vitest'

async function loadModule() {
  vi.resetModules()
  return import('../gatewayLiteLogin')
}

describe('gatewayLiteLogin', () => {
  beforeEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/')
    delete window.__GATEWAY_LITE_LOGIN_ENTRY__
  })

  it('remembers only the server-approved private entry', async () => {
    window.history.replaceState({}, '', '/private-console')
    window.__GATEWAY_LITE_LOGIN_ENTRY__ = {
      enabled: true,
      allowed: true,
      title: 'Private Gateway',
      subtitle: 'Authorized only',
    }

    const login = await loadModule()

    expect(login.isGatewayLiteLoginProtectionEnabled()).toBe(true)
    expect(login.isGatewayLitePrivateLoginEntry('/private-console')).toBe(true)
    expect(login.isGatewayLitePrivateLoginEntry('/login')).toBe(false)
    expect(login.getGatewayLiteLoginPath()).toBe('/private-console')
    expect(login.getGatewayLiteLoginPresentation('/private-console')).toEqual({
      title: 'Private Gateway',
      subtitle: 'Authorized only',
    })
  })

  it('does not remember or reveal a denied path', async () => {
    window.history.replaceState({}, '', '/login')
    window.__GATEWAY_LITE_LOGIN_ENTRY__ = { enabled: true, allowed: false }

    const login = await loadModule()

    expect(login.isGatewayLitePrivateLoginEntry('/login')).toBe(false)
    expect(login.getGatewayLiteLoginPresentation('/login')).toBeNull()
    expect(login.getGatewayLiteLoginPath()).toBe('/login')
  })

  it('rejects external or multi-segment redirect values from storage', async () => {
    localStorage.setItem('gateway_lite_private_login_path', 'https://example.com')
    let login = await loadModule()
    expect(login.getGatewayLiteLoginPath()).toBe('/login')

    localStorage.setItem('gateway_lite_private_login_path', '/one/two')
    login = await loadModule()
    expect(login.getGatewayLiteLoginPath()).toBe('/login')
  })
})
