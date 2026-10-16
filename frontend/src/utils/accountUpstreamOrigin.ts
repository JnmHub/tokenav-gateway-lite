import type { Account } from '@/types'

type AccountOriginInput = Pick<Account, 'type' | 'credentials'>

// 账号列表只展示 URL origin（协议、主机、端口）。路径、查询参数、
// fragment 和 userinfo 可能含上游密钥，不能进入 href 或 DOM。
export const accountUpstreamOrigin = (account: AccountOriginInput): string | null => {
  if (account.type !== 'apikey') return null
  const raw = account.credentials?.base_url
  if (typeof raw !== 'string' || raw.trim() === '') return null

  try {
    const parsed = new URL(raw.trim())
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null
    return parsed.origin
  } catch {
    return null
  }
}
