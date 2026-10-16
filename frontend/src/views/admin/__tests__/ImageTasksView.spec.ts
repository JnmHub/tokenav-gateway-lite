import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ImageTasksView from '../ImageTasksView.vue'

const api = vi.hoisted(() => ({
  cleanupImageTasks: vi.fn(),
  getImageTask: vi.fn(),
  getImageTaskConfig: vi.fn(),
  getImageTaskMetrics: vi.fn(),
  listImageTasks: vi.fn(),
  replayImageTask: vi.fn(),
  updateImageTaskConfig: vi.fn(),
}))
const system = vi.hoisted(() => ({ restartService: vi.fn() }))

vi.mock('@/api/admin/imageTasks', () => api)
vi.mock('@/api/admin/system', () => ({
  ...system,
  default: { restartService: system.restartService },
}))

const task = {
  id: '11111111-2222-3333-4444-555555555555',
  state: 'failed',
  settlement_state: 'released',
  progress: 100,
  model: 'gpt-image-1',
  image_count: 1,
  attempt: 3,
  max_attempts: 3,
  reserved_amount: 0.2,
  actual_amount: 0,
  cancel_requested: false,
  error_code: 'upstream_execution_failed',
  error_message: 'The upstream image service failed after retries',
  artifacts: [],
  created_at: '2026-08-02T00:00:00Z',
  updated_at: '2026-08-02T00:01:00Z',
  expires_at: '2026-08-09T00:00:00Z',
}

const metrics = {
  metrics: { queue_depth: 1, workers: 2, running_workers: 0, queue_capacity: 256 },
  config: {
    enabled: true,
    workers: 2,
    queue_capacity: 256,
    batch_size: 16,
    per_user_concurrency: 1,
    max_images_per_task: 4,
    max_attempts: 3,
    execution_timeout: 600_000_000_000,
    claim_ttl: 720_000_000_000,
    poll_interval: 1_000_000_000,
    result_retention: 604_800_000_000_000,
    download_url_ttl: 900_000_000_000,
    max_request_bytes: 1_048_576,
    max_artifact_bytes: 26_214_400,
    max_task_artifact_bytes: 83_886_080,
  },
}

const adminConfig = {
  revision: 'rev-1',
  desired: {
    enabled: true,
    worker_count: 2,
    queue_capacity: 256,
    claim_batch_size: 16,
    per_user_concurrency: 1,
    max_images_per_task: 4,
    max_attempts: 3,
    execution_timeout_seconds: 600,
    claim_ttl_seconds: 900,
    poll_interval_milliseconds: 1000,
    result_retention_days: 7,
    download_url_ttl_seconds: 900,
    max_request_bytes: 1_048_576,
    max_artifact_bytes: 26_214_400,
    max_task_artifact_bytes: 83_886_080,
    storage: {
      type: 'local',
      local_path: './data/image-tasks',
      s3: {
        endpoint: '',
        region: 'auto',
        bucket: '',
        prefix: 'image-tasks',
        force_path_style: false,
        access_key_id_configured: false,
        secret_access_key_configured: false,
      },
    },
  },
  effective_enabled: true,
  encryption_key_configured: true,
  encryption_key_read_only: false,
  restart_required: false,
  config_path: '/opt/tokenav/gateway-lite/config.yaml',
  config_writable: true,
  read_only: false,
  env_overrides: [],
}

function mountView(locale: 'zh-CN' | 'en-US' = 'zh-CN', stubs: Record<string, unknown> = {}) {
  return mount(ImageTasksView, {
    global: {
      plugins: [createI18n({ legacy: false, locale, messages: { 'zh-CN': {}, 'en-US': {} } })],
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        Icon: { template: '<span aria-hidden="true" />' },
        ...stubs,
      },
    },
  })
}

describe('ImageTasksView', () => {
  beforeEach(() => {
    api.cleanupImageTasks.mockReset().mockResolvedValue(0)
    api.getImageTask.mockReset().mockResolvedValue(task)
    api.getImageTaskConfig.mockReset().mockResolvedValue(adminConfig)
    api.getImageTaskMetrics.mockReset().mockResolvedValue(metrics)
    api.listImageTasks.mockReset().mockResolvedValue({ items: [task] })
    api.replayImageTask.mockReset().mockResolvedValue({ ...task, state: 'queued', attempt: 0 })
    api.updateImageTaskConfig.mockReset().mockResolvedValue(adminConfig)
    system.restartService.mockReset().mockResolvedValue(undefined)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('renders queue metrics, task state and safe replay explanation', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('异步图片任务')
    expect(wrapper.text()).toContain('排队任务')
    expect(wrapper.text()).toContain('gpt-image-1')
    expect(wrapper.text()).toContain('失败')
		expect(wrapper.text()).toContain('$0')
		expect(wrapper.text()).not.toContain('$0.2')
    expect(wrapper.text()).not.toContain('oauth_token')
    wrapper.unmount()
  })

  it('shows a stable disabled state without enabling destructive actions', async () => {
    api.getImageTaskConfig.mockResolvedValueOnce({
      ...adminConfig,
      desired: { ...adminConfig.desired, enabled: false },
      effective_enabled: false,
    })
    const wrapper = mountView('en-US')
    await flushPromises()

    expect(wrapper.text()).toContain('Asynchronous image tasks are disabled')
    expect(wrapper.text()).not.toContain('Request failed with status code 503')
    expect(api.getImageTaskMetrics).not.toHaveBeenCalled()
    expect(api.listImageTasks).not.toHaveBeenCalled()
    const cleanup = wrapper.findAll('button').find(button => button.text().includes('Clean expired'))
    expect(cleanup?.attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('replays only after explicit confirmation', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountView()
    await flushPromises()

    const row = wrapper.find('tbody tr')
    await row.trigger('click')
    await flushPromises()
    const replay = wrapper.findAll('button').find(button => button.text().includes('重放'))
    await replay?.trigger('click')
    await flushPromises()

    expect(api.replayImageTask).toHaveBeenCalledWith(task.id)
    expect(wrapper.text()).toContain('任务已重新进入队列')
    wrapper.unmount()
  })

  it('pauses periodic queue refresh while the service restart is pending', async () => {
    vi.useFakeTimers()
    system.restartService.mockReturnValue(new Promise(() => {}))
    api.updateImageTaskConfig.mockResolvedValueOnce({
      ...adminConfig,
      revision: 'rev-2',
      restart_required: true,
    })
    const wrapper = mountView('zh-CN', {
      ImageTaskConfigDialog: {
        props: ['show'],
        emits: ['save'],
        template: '<button v-if="show" data-test="config-save" @click="$emit(\'save\', {})">save</button>',
      },
    })
    await flushPromises()

    const configure = wrapper.findAll('button').find(button => button.text().includes('配置'))
    await configure?.trigger('click')
    await wrapper.get('[data-test="config-save"]').trigger('click')
    await flushPromises()
    expect(api.updateImageTaskConfig).toHaveBeenCalledTimes(1)
    expect(system.restartService).toHaveBeenCalledTimes(1)

    api.getImageTaskMetrics.mockClear()
    api.listImageTasks.mockClear()
    await vi.advanceTimersByTimeAsync(5_000)

    expect(api.getImageTaskMetrics).not.toHaveBeenCalled()
    expect(api.listImageTasks).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('reports an explicit restart HTTP failure without entering recovery polling', async () => {
    system.restartService.mockRejectedValueOnce({ response: { status: 409 } })
    api.updateImageTaskConfig.mockResolvedValueOnce({
      ...adminConfig,
      revision: 'rev-2',
      restart_required: true,
    })
    const wrapper = mountView('zh-CN', {
      ImageTaskConfigDialog: {
        props: ['show'],
        emits: ['save'],
        template: '<button v-if="show" data-test="config-save" @click="$emit(\'save\', {})">save</button>',
      },
    })
    await flushPromises()

    const configure = wrapper.findAll('button').find(button => button.text().includes('配置'))
    await configure?.trigger('click')
    await wrapper.get('[data-test="config-save"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('自动重启未完成')
    expect(api.getImageTaskConfig).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})
