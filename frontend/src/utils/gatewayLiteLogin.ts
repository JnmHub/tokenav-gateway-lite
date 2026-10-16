const STORAGE_KEY = 'gateway_lite_private_login_path'
const privatePathPattern = /^\/[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/

function normalizePath(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) return '/'
  const withSlash = trimmed.startsWith('/') ? trimmed : `/${trimmed}`
  return withSlash.length > 1 ? withSlash.replace(/\/+$/, '') : withSlash
}

function sanitizePrivatePath(value: string | null | undefined): string | null {
  if (!value) return null
  const normalized = normalizePath(value)
  return privatePathPattern.test(normalized) ? normalized : null
}

const injectedEntry = window.__GATEWAY_LITE_LOGIN_ENTRY__
const initialEntryPath = injectedEntry?.enabled === true && injectedEntry.allowed === true
  ? sanitizePrivatePath(window.location.pathname)
  : null

if (initialEntryPath) {
  try {
    localStorage.setItem(STORAGE_KEY, initialEntryPath)
  } catch {
    // Private browsing or storage policy must not block the login page.
  }
}

export function isGatewayLiteLoginProtectionEnabled(): boolean {
  return injectedEntry?.enabled === true
}

export function isGatewayLitePrivateLoginEntry(path: string): boolean {
  return initialEntryPath !== null && normalizePath(path) === initialEntryPath
}

export function getGatewayLiteLoginPresentation(path: string): { title: string; subtitle: string } | null {
  if (!isGatewayLitePrivateLoginEntry(path)) return null
  return {
    title: injectedEntry?.title?.trim() || '',
    subtitle: injectedEntry?.subtitle?.trim() || '',
  }
}

export function rememberGatewayLiteLoginPath(path: string): void {
  const normalized = sanitizePrivatePath(path)
  if (!normalized) return
  try {
    localStorage.setItem(STORAGE_KEY, normalized)
  } catch {
    // Storage is an ergonomic redirect aid, not an authentication dependency.
  }
}

export function getGatewayLiteLoginPath(): string {
  try {
    return sanitizePrivatePath(localStorage.getItem(STORAGE_KEY)) || '/login'
  } catch {
    return '/login'
  }
}

export function redirectToGatewayLiteLogin(replace = true): void {
  const destination = getGatewayLiteLoginPath()
  if (normalizePath(window.location.pathname) === destination && isGatewayLitePrivateLoginEntry(destination)) {
    return
  }
  if (replace) {
    window.location.replace(destination)
  } else {
    window.location.assign(destination)
  }
}
