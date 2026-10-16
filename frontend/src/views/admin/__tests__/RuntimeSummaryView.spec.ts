import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import RuntimeSummaryView from '../RuntimeSummaryView.vue'

const { getGatewayRuntimeSummary } = vi.hoisted(() => ({
  getGatewayRuntimeSummary: vi.fn(),
}))

vi.mock('@/api/admin/runtimeSummary', () => ({
  getGatewayRuntimeSummary,
}))

const runtimeFixture = {
  service: { name: 'gateway-lite', version: 'v-test', runtime: 'go-test', started_at: '2026-08-02T00:00:00Z', uptime_seconds: 3600, source: 'runtime_memory' },
  database: { provider: 'postgresql', max_open: 80, max_idle: 20, max_lifetime_minutes: 30, max_idle_time_minutes: 5, quota_flusher_enabled: true, quota_flush_interval_ms: 20, quota_flush_batch_size: 500, source: 'startup_config' },
  cache: { provider: 'redis', pool_size: 256, min_idle: 16, dial_timeout_seconds: 2, read_timeout_seconds: 1, write_timeout_seconds: 1, tls_enabled: false, source: 'startup_config' },
  billing: { preset: 'standard', revision: 3, critical_workers: 8, critical_active_workers: 8, critical_capacity: 4096, critical_pending: 0, critical_full_total: 0, critical_fallback_total: 0, critical_fallback_fail_total: 0, best_effort_workers: 4, best_effort_active_workers: 4, best_effort_capacity: 1024, best_effort_pending: 0, best_effort_deferred: 0, best_effort_merged_total: 0, best_effort_fallback_total: 0, best_effort_fallback_failed: 0, best_effort_dropped_total: 0, ordering_wait_total: 0, reload_total: 0, write_timeout_ms: 1500, source: 'runtime_memory_and_startup_config' },
  usage: { local_workers: 12, local_running: 2, local_capacity: 3000, local_pending: 0, local_completed: 500, local_failed: 0, local_dropped: 0, report_workers: 6, report_batch_size: 80, local_overflow_mode: 'sync', source: 'runtime_memory_and_startup_config' },
  timeouts: { control_plane_ms: 300, response_header_seconds: 60, openai_response_header_seconds: 0, openai_first_output_seconds: 45, openai_high_reasoning_output_seconds: 180, stream_data_interval_seconds: 180, image_stream_data_interval_seconds: 900, source: 'startup_config' },
  websocket: { configured_enabled: true, force_http: false, effective_enabled: true, mode_router_v2: false, ingress_mode: 'ctx_pool', client_first_message_seconds: 30, inter_turn_idle_seconds: 300, max_connections_per_account: 4, source: 'startup_config' },
  scheduling: { advanced_enabled: true, advanced_setting_available: true, sticky_escape_enabled: true, sticky_escape_ttft_ms: 3000, sticky_escape_error_rate: 0.2, fallback_queue_enabled: true, fallback_max_waiting: 500, fallback_wait_ms: 15000, fallback_selection_mode: 'last_used', load_batch_enabled: true, load_batch_cache_ttl_ms: 100, source: 'database_setting_and_startup_config' },
  read_only: true,
}

function mountView(locale: 'zh-CN' | 'en-US') {
  const i18n = createI18n({ legacy: false, locale, messages: { 'zh-CN': {}, 'en-US': {} } })
  return mount(RuntimeSummaryView, {
    global: {
      plugins: [i18n],
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        Icon: { template: '<span aria-hidden="true" />' },
        BillingCacheRuntimeCard: { template: '<section>billing-cache-runtime-card</section>' },
        OpenAISchedulerRuntimePolicyCard: { template: '<section>scheduler-policy-card</section>' },
      },
    },
  })
}

describe('RuntimeSummaryView', () => {
  beforeEach(() => {
    getGatewayRuntimeSummary.mockReset()
    getGatewayRuntimeSummary.mockResolvedValue(runtimeFixture)
  })

  it('renders the Chinese read-only runtime summary without credential fields', async () => {
    const wrapper = mountView('zh-CN')
    await flushPromises()

    expect(wrapper.text()).toContain('运行配置')
    expect(wrapper.text()).toContain('PostgreSQL 连接池')
    expect(wrapper.text()).toContain('计费缓存队列')
    expect(wrapper.text()).toContain('billing-cache-runtime-card')
    expect(wrapper.text()).toContain('调度与计费队列可热更新')
    expect(wrapper.text()).toContain('只读')
    expect(wrapper.text()).not.toContain('password')
    expect(wrapper.text()).not.toContain('do-not-leak')
  })

  it('renders complete English labels when the locale changes', async () => {
    const wrapper = mountView('en-US')
    await flushPromises()

    expect(wrapper.text()).toContain('Runtime configuration')
    expect(wrapper.text()).toContain('Billing cache queues')
    expect(wrapper.text()).toContain('Advanced scheduling')
    expect(wrapper.text()).toContain('Read-only')
  })
})
