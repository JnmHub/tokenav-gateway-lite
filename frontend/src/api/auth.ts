/**
 * Gateway-lite authentication API.
 * 轻量网关后台只保留管理员登录、2FA（双因素认证）、退出、当前用户和 token（令牌）刷新。
 */

import { apiClient } from './client'
import type {
  AuthResponse,
  ChangePasswordRequest,
  CurrentUserResponse,
  LoginRequest,
  PublicSettings,
  TotpLogin2FARequest,
  TotpLoginResponse,
} from '@/types'

export type LoginResponse = AuthResponse | TotpLoginResponse

export interface RefreshTokenResponse {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
}

export function isTotp2FARequired(response: LoginResponse): response is TotpLoginResponse {
  return 'requires_2fa' in response && response.requires_2fa === true
}

function setAuthToken(token: string): void {
  localStorage.setItem('auth_token', token)
}

function setRefreshToken(token: string): void {
  localStorage.setItem('refresh_token', token)
}

function setTokenExpiresAt(expiresIn: number): void {
  const expiresAt = Date.now() + expiresIn * 1000
  localStorage.setItem('token_expires_at', String(expiresAt))
}

function getRefreshToken(): string | null {
  return localStorage.getItem('refresh_token')
}

function persistAuthResponse(data: AuthResponse): void {
  setAuthToken(data.access_token)
  if (data.refresh_token) {
    setRefreshToken(data.refresh_token)
  }
  if (data.expires_in) {
    setTokenExpiresAt(data.expires_in)
  }
  localStorage.setItem('auth_user', JSON.stringify(data.user))
}

function clearAuthToken(): void {
  localStorage.removeItem('auth_token')
  localStorage.removeItem('refresh_token')
  localStorage.removeItem('auth_user')
  localStorage.removeItem('token_expires_at')
}

export async function login(credentials: LoginRequest): Promise<LoginResponse> {
  const { data } = await apiClient.post<LoginResponse>('/auth/login', credentials)

  if (!isTotp2FARequired(data)) {
    persistAuthResponse(data)
  }

  return data
}

export async function login2FA(request: TotpLogin2FARequest): Promise<AuthResponse> {
  const { data } = await apiClient.post<AuthResponse>('/auth/login/2fa', request)
  persistAuthResponse(data)
  return data
}

export async function getCurrentUser() {
  return apiClient.get<CurrentUserResponse>('/auth/me')
}

export async function logout(): Promise<void> {
  const refreshToken = getRefreshToken()

  if (refreshToken) {
    try {
      await apiClient.post('/auth/logout', { refresh_token: refreshToken })
    } catch {
      // 即使服务端撤销失败，也必须清理本地登录态。
    }
  }

  clearAuthToken()
}

export async function refreshToken(): Promise<RefreshTokenResponse> {
  const currentRefreshToken = getRefreshToken()
  if (!currentRefreshToken) {
    throw new Error('No refresh token available')
  }

  const { data } = await apiClient.post<RefreshTokenResponse>('/auth/refresh', {
    refresh_token: currentRefreshToken,
  })

  setAuthToken(data.access_token)
  setRefreshToken(data.refresh_token)
  setTokenExpiresAt(data.expires_in)

  return data
}

export async function getPublicSettings(): Promise<PublicSettings> {
  const { data } = await apiClient.get<PublicSettings>('/settings/public')
  return data
}

export async function changePassword(request: ChangePasswordRequest): Promise<{ message: string }> {
  const { data } = await apiClient.put<{ message: string }>('/user/password', request)
  return data
}

export const authAPI = {
  login,
  login2FA,
  getCurrentUser,
  logout,
  refreshToken,
  getPublicSettings,
  changePassword,
}

export default authAPI
