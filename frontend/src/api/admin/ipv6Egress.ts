import { apiClient } from '../client'

// IPv6 出口自动管理：本机网卡全局 IPv6 → 源绑定测通 → 注册本地 CONNECT 代理。
// 配置通过管理界面热更新（持久化于网关 data/ipv6_egress.json），无需重启服务。
export interface IPv6EgressConfig {
  enabled: boolean
  listen_host: string
  listen_port: number
  probe_target: string
  sync_interval_seconds: number
  name_prefix: string
  max_addresses: number
}

export interface IPv6EgressEntry {
  address: string
  status: 'ok' | 'failed'
  latency_ms: number
  error?: string
  proxy_id?: number
  proxy_url?: string
  last_check: string
}

export interface IPv6EgressStatus {
  config: IPv6EgressConfig
  enabled: boolean
  running: boolean
  last_scan?: string
  entries: IPv6EgressEntry[]
  ok_count: number
}

export interface IPv6EgressConfigUpdate {
  enabled?: boolean
  listen_host?: string
  listen_port?: number
  probe_target?: string
  sync_interval_seconds?: number
  name_prefix?: string
  max_addresses?: number
}

function normalizeConfig(value: unknown): IPv6EgressConfig {
  const cfg = (value || {}) as Partial<IPv6EgressConfig>
  return {
    enabled: Boolean(cfg.enabled),
    listen_host: cfg.listen_host || '127.0.0.1',
    listen_port: Number(cfg.listen_port) || 32600,
    probe_target: cfg.probe_target || 'chatgpt.com:443',
    sync_interval_seconds: Number(cfg.sync_interval_seconds) || 300,
    name_prefix: cfg.name_prefix || 'ipv6',
    max_addresses: Number(cfg.max_addresses) || 512,
  }
}

function normalizeStatus(value: unknown): IPv6EgressStatus {
  const raw = (value || {}) as Partial<IPv6EgressStatus> & { config?: unknown }
  return {
    config: normalizeConfig(raw.config),
    enabled: Boolean(raw.enabled),
    running: Boolean(raw.running),
    last_scan: raw.last_scan || '',
    entries: Array.isArray(raw.entries) ? raw.entries : [],
    ok_count: Number(raw.ok_count) || 0,
  }
}

export async function getStatus(): Promise<IPv6EgressStatus> {
  const { data } = await apiClient.get('/admin/ipv6-egress')
  return normalizeStatus(data)
}

export async function updateConfig(payload: IPv6EgressConfigUpdate): Promise<IPv6EgressStatus> {
  const { data } = await apiClient.put('/admin/ipv6-egress', payload)
  return normalizeStatus(data)
}

export async function rescan(): Promise<IPv6EgressStatus> {
  const { data } = await apiClient.post('/admin/ipv6-egress/rescan')
  return normalizeStatus(data)
}

export default { getStatus, updateConfig, rescan }
