/**
 * API Client for JnmGatewayApi Backend
 * Central export point for all API modules
 */

// Re-export the HTTP client
export { apiClient } from './client'

// Auth API
export { authAPI, isTotp2FARequired, type LoginResponse } from './auth'
export { totpAPI } from './totp'

// Gateway admin shared APIs
export { keysAPI } from './keys'
export { usageAPI } from './usage'
export { userGroupsAPI } from './groups'

// Admin APIs
export { adminAPI } from './admin'

// Default export
export { default } from './client'
