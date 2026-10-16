import { request, ApiError } from './index.js'

export function assertSettingsVersion(settings) {
  if (typeof settings?.version !== 'string' || !settings.version) {
    throw new ApiError('后台未返回设置修订，请刷新并确认后端版本', { payload: { reason: 'SETTINGS_VERSION_REQUIRED' } })
  }
  return settings
}

export async function readSettings(options = {}) {
  return assertSettingsVersion(await request('/admin/settings', { ...options, method: 'GET' }))
}

export async function saveSettingsPart(version, fields, options = {}) {
  assertSettingsVersion({ version })
  // 只带当前区块和读取时修订；绝不把旧完整快照重放以绕过冲突。
  return assertSettingsVersion(await request('/admin/settings', {
    ...options, method: 'PATCH', body: { ...fields, version },
  }))
}

export function settingsErrorMessage(error) {
  if (error.reason === 'SETTINGS_VERSION_CONFLICT') return '其他管理员已更新设置，当前草稿已保留。请重新读取最新配置后核对，再保存。'
  if (error.reason === 'SETTINGS_VERSION_REQUIRED') return '缺少设置修订，请重新读取配置后保存。'
  return error.message
}
