// URL（页面地址）只恢复白名单应用/对象/标签，不从地址解析组件或执行代码。
export function parseAppLink(href, appIds) {
  const url = new URL(href, 'http://localhost')
  const hash = new URLSearchParams(url.hash.replace(/^#\/?/, '').replace(/^app\//, 'app='))
  const query = new URLSearchParams(url.search)
  let appId = hash.get('app') || query.get('app')
  if (!appId && ['open_error_details', 'open_alert_rules', 'alert_rule_id'].some(key => query.has(key))) appId = 'opsCenter'
  if (!appId && query.has('tab')) appId = 'settings'
  if (!appId) return { appId: null, target: null }
  if (!appIds.includes(appId)) return { error: '此地址指向的应用尚未提供。' }
  const tab = hash.get('tab') || query.get('tab')
  const raw = hash.get('id') || query.get('id')
  const value = raw && /^\d{1,16}$/.test(raw) ? Number(raw) : NaN
  const id = Number.isSafeInteger(value) && value > 0 ? value : null
  return { appId, target: { tab: tab && /^[a-zA-Z][\w-]{0,48}$/.test(tab) ? tab : null, id } }
}
