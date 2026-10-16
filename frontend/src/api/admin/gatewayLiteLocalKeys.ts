import { apiClient } from '../client'

export interface GatewayLiteLocalKey {
  id: number
  name: string
  key_masked: string
  secret?: string
  group_id: number
  group_name: string
  platform: string
  status: 'active' | 'disabled'
  ip_whitelist: string[]
  ip_blacklist: string[]
  last_used_at?: string | null
  created_at: string
  updated_at: string
}

export interface CreateGatewayLiteLocalKeyRequest {
  name: string
  group_id: number
  ip_whitelist: string[]
  ip_blacklist: string[]
}

export interface UpdateGatewayLiteLocalKeyRequest {
  name?: string
  group_id?: number
  status?: 'active' | 'disabled'
  ip_whitelist?: string[]
  ip_blacklist?: string[]
}

type GatewayLiteLocalKeyWire = Omit<GatewayLiteLocalKey, 'ip_whitelist' | 'ip_blacklist'> & {
  ip_whitelist?: unknown
  ip_blacklist?: unknown
}

function normalizeIPRules(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string')
}

export function normalizeGatewayLiteLocalKey(value: unknown): GatewayLiteLocalKey {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new TypeError('Invalid gateway-lite local API key response')
  }
  const item = value as GatewayLiteLocalKeyWire
  return {
    ...item,
    ip_whitelist: normalizeIPRules(item.ip_whitelist),
    ip_blacklist: normalizeIPRules(item.ip_blacklist),
  }
}

export function normalizeGatewayLiteLocalKeyList(value: unknown): GatewayLiteLocalKey[] {
  if (!Array.isArray(value)) return []
  return value
    .filter((item) => item !== null && typeof item === 'object' && !Array.isArray(item))
    .map(normalizeGatewayLiteLocalKey)
}

export async function list(): Promise<GatewayLiteLocalKey[]> {
	const { data } = await apiClient.get<GatewayLiteLocalKeyWire[] | null>('/admin/gateway-lite/local-api-keys')
	return normalizeGatewayLiteLocalKeyList(data)
}

export async function create(payload: CreateGatewayLiteLocalKeyRequest): Promise<GatewayLiteLocalKey> {
	const { data } = await apiClient.post<GatewayLiteLocalKeyWire>('/admin/gateway-lite/local-api-keys', payload)
	return normalizeGatewayLiteLocalKey(data)
}

export async function reveal(id: number): Promise<{ secret: string }> {
	const { data } = await apiClient.post<{ secret: string }>(`/admin/gateway-lite/local-api-keys/${id}/reveal`)
	return data
}

export async function update(id: number, payload: UpdateGatewayLiteLocalKeyRequest): Promise<GatewayLiteLocalKey> {
	const { data } = await apiClient.put<GatewayLiteLocalKeyWire>(`/admin/gateway-lite/local-api-keys/${id}`, payload)
	return normalizeGatewayLiteLocalKey(data)
}

export async function remove(id: number): Promise<void> {
  await apiClient.delete(`/admin/gateway-lite/local-api-keys/${id}`)
}

export default { list, create, reveal, update, remove }
