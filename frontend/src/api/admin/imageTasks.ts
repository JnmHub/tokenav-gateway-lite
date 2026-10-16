import { apiClient } from '../client'

export type ImageTaskState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'expired'

export interface ImageTaskArtifact {
  file_name: string
  content_type: string
  size_bytes: number
  sha256: string
  download_url?: string
}

export interface ImageTask {
  id: string
  user_id?: number
  state: ImageTaskState
  settlement_state: 'pending' | 'settled' | 'released'
  progress: number
  model: string
  image_count: number
  attempt: number
  max_attempts: number
  reserved_amount: number
  actual_amount: number
  cancel_requested: boolean
  error_code?: string
  error_message?: string
  artifacts: ImageTaskArtifact[]
  created_at: string
  updated_at: string
  started_at?: string
  completed_at?: string
  expires_at: string
}

export interface ImageTaskListResponse {
  items: ImageTask[]
  next_cursor?: string
}

export interface ImageTaskRuntimeConfig {
  enabled: boolean
  workers: number
  queue_capacity: number
  batch_size: number
  per_user_concurrency: number
  max_images_per_task: number
  max_attempts: number
  execution_timeout: number
  claim_ttl: number
  poll_interval: number
  result_retention: number
  download_url_ttl: number
  max_request_bytes: number
  max_artifact_bytes: number
  max_task_artifact_bytes: number
}

export interface ImageTaskMetricsResponse {
  metrics: {
    queue_depth: number
    workers: number
    running_workers: number
    queue_capacity: number
  }
  config: ImageTaskRuntimeConfig
}

export type ImageTaskStorageType = 'local' | 's3'

export interface ImageTaskS3ConfigView {
  endpoint: string
  region: string
  bucket: string
  prefix: string
  force_path_style: boolean
  access_key_id_configured: boolean
  secret_access_key_configured: boolean
}

export interface ImageTaskStorageConfigView {
  type: ImageTaskStorageType
  local_path: string
  s3: ImageTaskS3ConfigView
}

export interface ImageTaskDesiredConfig {
  enabled: boolean
  worker_count: number
  queue_capacity: number
  claim_batch_size: number
  per_user_concurrency: number
  max_images_per_task: number
  max_attempts: number
  execution_timeout_seconds: number
  claim_ttl_seconds: number
  poll_interval_milliseconds: number
  result_retention_days: number
  download_url_ttl_seconds: number
  max_request_bytes: number
  max_artifact_bytes: number
  max_task_artifact_bytes: number
  storage: ImageTaskStorageConfigView
}

/**
 * desired 是 config.yaml 中等待生效的配置，effective_enabled 是当前进程真实状态。
 * 两者分开显示，避免管理员保存后误以为 worker（工作线程）已经启动。
 */
export interface ImageTaskAdminConfig {
  revision: string
  desired: ImageTaskDesiredConfig
  effective_enabled: boolean
  encryption_key_configured: boolean
  encryption_key_read_only: boolean
  restart_required: boolean
  config_path: string
  config_writable: boolean
  read_only: boolean
  env_overrides: string[]
  degraded_reason?: string
}

export interface UpdateImageTaskS3Config {
  endpoint: string
  region: string
  bucket: string
  prefix: string
  force_path_style: boolean
  access_key_id?: string
  secret_access_key?: string
}

export interface UpdateImageTaskDesiredConfig extends Omit<ImageTaskDesiredConfig, 'storage'> {
  storage: {
    type: ImageTaskStorageType
    local_path: string
    s3: UpdateImageTaskS3Config
  }
}

export interface UpdateImageTaskConfigRequest {
  expected_revision: string
  generate_encryption_key: boolean
  desired: UpdateImageTaskDesiredConfig
}

export async function listImageTasks(params: {
  state?: string
  limit?: number
  cursor?: string
} = {}): Promise<ImageTaskListResponse> {
  const response = await apiClient.get<ImageTaskListResponse>('/admin/image-tasks', { params })
  return response.data
}

export async function getImageTask(id: string): Promise<ImageTask> {
  const response = await apiClient.get<{ task: ImageTask }>(`/admin/image-tasks/${encodeURIComponent(id)}`)
  return response.data.task
}

export async function replayImageTask(id: string): Promise<ImageTask> {
	const response = await apiClient.post<{ task: ImageTask }>(`/admin/image-tasks/${encodeURIComponent(id)}/replay`, {
		confirm: 'REPLAY_IMAGE_TASK',
	})
  return response.data.task
}

export async function cleanupImageTasks(limit = 100): Promise<number> {
	const response = await apiClient.post<{ removed: number }>('/admin/image-tasks/cleanup', {
		confirm: 'CLEANUP_IMAGE_TASKS',
	}, {
    params: { limit },
  })
  return response.data.removed
}

export async function getImageTaskMetrics(): Promise<ImageTaskMetricsResponse> {
  const response = await apiClient.get<ImageTaskMetricsResponse>('/admin/image-tasks/metrics')
  return response.data
}

export async function getImageTaskConfig(signal?: AbortSignal): Promise<ImageTaskAdminConfig> {
  const response = signal
    ? await apiClient.get<ImageTaskAdminConfig>('/admin/image-tasks/config', { signal })
    : await apiClient.get<ImageTaskAdminConfig>('/admin/image-tasks/config')
  return response.data
}

export async function updateImageTaskConfig(
  config: UpdateImageTaskConfigRequest,
): Promise<ImageTaskAdminConfig> {
  const response = await apiClient.put<ImageTaskAdminConfig>('/admin/image-tasks/config', config)
  return response.data
}
