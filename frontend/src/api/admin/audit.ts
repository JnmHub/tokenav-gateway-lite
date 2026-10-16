import { apiClient } from '../client'

export type OperationAuditResult = 'pending' | 'success' | 'failure' | 'denied'

export interface OperationAuditLog {
  id: number
  created_at: string
  actor_user_id?: number
  actor_email?: string
  actor_role?: string
  auth_method?: string
  action: string
  resource_type?: string
  resource_id?: string
  result: OperationAuditResult | string
  method?: string
  path?: string
  request_id?: string
  client_ip?: string
  ua_hash?: string
  status_code: number
  latency_ms: number
  summary?: Record<string, unknown>
  extra?: Record<string, unknown>
}

export interface OperationAuditListResponse {
  items: OperationAuditLog[]
  next_cursor?: number
}

export interface OperationAuditListParams {
  before_id?: number
  limit?: number
  actor_user_id?: number
  actor_email?: string
  action?: string
  resource_type?: string
  resource_id?: string
  result?: string
  start_time?: string
  end_time?: string
}

export async function list(params: OperationAuditListParams = {}): Promise<OperationAuditListResponse> {
  const { data } = await apiClient.get<OperationAuditListResponse>('/admin/audit-logs', { params })
  return data
}

export async function getById(id: number): Promise<OperationAuditLog> {
  const { data } = await apiClient.get<OperationAuditLog>(`/admin/audit-logs/${id}`)
  return data
}

export async function clear(stepUpToken?: string): Promise<{ deleted: number }> {
  const { data } = await apiClient.post<{ deleted: number }>(
    '/admin/audit-logs/clear',
    {},
    { headers: stepUpToken ? { 'X-Step-Up-Token': stepUpToken } : undefined }
  )
  return data
}

export const auditAPI = {
  list,
  getById,
  clear
}

export default auditAPI
