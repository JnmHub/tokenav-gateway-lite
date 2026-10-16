import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get, post, put } }))

import {
  cleanupImageTasks,
  getImageTaskConfig,
  getImageTask,
  getImageTaskMetrics,
  listImageTasks,
  replayImageTask,
  updateImageTaskConfig,
} from '@/api/admin/imageTasks'

describe('admin image task api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
  })

  it('uses bounded list parameters and unwraps task responses', async () => {
    get.mockResolvedValueOnce({ data: { items: [], next_cursor: 'next' } })
    await expect(listImageTasks({ state: 'failed', limit: 50 })).resolves.toEqual({ items: [], next_cursor: 'next' })
    expect(get).toHaveBeenCalledWith('/admin/image-tasks', { params: { state: 'failed', limit: 50 } })

    get.mockResolvedValueOnce({ data: { task: { id: 'task/a' } } })
    await expect(getImageTask('task/a')).resolves.toEqual({ id: 'task/a' })
    expect(get).toHaveBeenLastCalledWith('/admin/image-tasks/task%2Fa')
  })

  it('replays and cleans up through explicit administrator actions', async () => {
    post.mockResolvedValueOnce({ data: { task: { id: 'task-1', state: 'queued' } } })
    await expect(replayImageTask('task-1')).resolves.toMatchObject({ state: 'queued' })
		expect(post).toHaveBeenLastCalledWith('/admin/image-tasks/task-1/replay', { confirm: 'REPLAY_IMAGE_TASK' })

    post.mockResolvedValueOnce({ data: { removed: 7 } })
    await expect(cleanupImageTasks(25)).resolves.toBe(7)
		expect(post).toHaveBeenLastCalledWith('/admin/image-tasks/cleanup', { confirm: 'CLEANUP_IMAGE_TASKS' }, { params: { limit: 25 } })
  })

  it('loads queue metrics from the isolated image worker endpoint', async () => {
    get.mockResolvedValueOnce({ data: { metrics: { queue_depth: 0 }, config: { enabled: true } } })
    await expect(getImageTaskMetrics()).resolves.toMatchObject({ config: { enabled: true } })
    expect(get).toHaveBeenCalledWith('/admin/image-tasks/metrics')
  })

  it('reads and atomically updates the restart-bound admin config', async () => {
    const config = {
      revision: 'rev-1',
      desired: { enabled: false },
      effective_enabled: false,
      encryption_key_configured: false,
      restart_required: false,
      config_path: '/opt/tokenav/gateway-lite/config.yaml',
    }
    get.mockResolvedValueOnce({ data: config })
    await expect(getImageTaskConfig()).resolves.toEqual(config)
    expect(get).toHaveBeenLastCalledWith('/admin/image-tasks/config')

    const payload = {
      expected_revision: 'rev-1',
      generate_encryption_key: true,
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
          type: 'local' as const,
          local_path: './data/image-tasks',
          s3: {
            endpoint: '',
            region: 'auto',
            bucket: '',
            prefix: 'image-tasks',
            force_path_style: false,
            access_key_id: '',
            secret_access_key: '',
          },
        },
      },
    }
    put.mockResolvedValueOnce({ data: { ...config, revision: 'rev-2', restart_required: true } })
    await expect(updateImageTaskConfig(payload)).resolves.toMatchObject({ revision: 'rev-2', restart_required: true })
    expect(put).toHaveBeenLastCalledWith('/admin/image-tasks/config', payload)
  })
})
