import type { PublicSettings } from '@/types'

declare global {
  interface Window {
    __APP_CONFIG__?: PublicSettings
    __GATEWAY_LITE_LOGIN_ENTRY__?: {
      enabled: boolean
      allowed: boolean
      title?: string
      subtitle?: string
    }
  }
}

export {}
