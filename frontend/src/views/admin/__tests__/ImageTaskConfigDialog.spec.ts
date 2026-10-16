import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'

import ImageTaskConfigDialog from '../image-tasks/ImageTaskConfigDialog.vue'

const desired = {
  enabled: false,
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
      access_key_id_configured: false,
      secret_access_key_configured: false,
    },
  },
}

function mountDialog(overrides: Record<string, unknown> = {}) {
  return mount(ImageTaskConfigDialog, {
    props: {
      show: true,
      loading: false,
      saving: false,
      error: '',
      config: {
        revision: 'rev-1',
        desired,
        effective_enabled: false,
        encryption_key_configured: false,
        encryption_key_read_only: false,
        restart_required: false,
        config_path: '/opt/tokenav/gateway-lite/config.yaml',
        config_writable: true,
        read_only: false,
        env_overrides: [],
        ...overrides,
      },
    },
    global: {
      plugins: [createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {} } })],
      stubs: {
        BaseDialog: { props: ['show', 'title'], template: '<section v-if="show"><h2>{{ title }}</h2><slot /><footer><slot name="footer" /></footer></section>' },
        Icon: { template: '<span aria-hidden="true" />' },
      },
    },
  })
}

describe('ImageTaskConfigDialog', () => {
  it('requires an explicit one-click persistent key before enabling', async () => {
    const wrapper = mountDialog()
    await wrapper.get('[role="switch"]').trigger('click')

    expect(wrapper.text()).toContain('启用前需要固定加密密钥')
    expect(wrapper.text()).toContain('请先选择“一键生成”固定加密密钥')
    const save = wrapper.findAll('button').find(button => button.text().includes('保存并重启'))
    expect(save?.attributes('disabled')).toBeDefined()

    const generate = wrapper.findAll('button').find(button => button.text().includes('一键生成'))
    await generate?.trigger('click')
    expect(save?.attributes('disabled')).toBeUndefined()
    await save?.trigger('click')

    const payload = wrapper.emitted('save')?.[0]?.[0] as Record<string, unknown>
    expect(payload).toMatchObject({
      expected_revision: 'rev-1',
      generate_encryption_key: true,
      desired: { enabled: true },
    })
    expect(payload).not.toHaveProperty('encryption_key')
    wrapper.unmount()
  })

  it('keeps configured S3 credentials when password fields are left blank', async () => {
    const s3Desired = {
      ...desired,
      enabled: true,
      storage: {
        type: 's3' as const,
        local_path: './data/image-tasks',
        s3: {
          endpoint: 'https://s3.example.com',
          region: 'auto',
          bucket: 'images',
          prefix: 'image-tasks',
          force_path_style: true,
          access_key_id_configured: true,
          secret_access_key_configured: true,
        },
      },
    }
    const wrapper = mountDialog({ desired: s3Desired, encryption_key_configured: true })

    const credentialInputs = wrapper.findAll('input').filter(input => input.attributes('placeholder') === '已配置，留空不修改')
    expect(credentialInputs).toHaveLength(2)
    expect(wrapper.text()).not.toContain('existing-secret')
    const save = wrapper.findAll('button').find(button => button.text().includes('保存并重启'))
    await save?.trigger('click')

    const payload = wrapper.emitted('save')?.[0]?.[0] as { desired: { storage: { s3: Record<string, unknown> } } }
    expect(payload.desired.storage.s3.access_key_id).toBe('')
    expect(payload.desired.storage.s3.secret_access_key).toBe('')
    wrapper.unmount()
  })
})
