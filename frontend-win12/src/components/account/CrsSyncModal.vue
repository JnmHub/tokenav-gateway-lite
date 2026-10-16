<script setup>
/* CRS 同步弹窗（三期）——旧实现契约：frontend/src/components/account/SyncFromCrsModal.vue
 * 接口：
 * - POST /admin/accounts/sync/crs/preview {base_url, username, password}
 *     → { new_accounts[], existing_accounts[] }（元素 crs_account_id/kind/name/platform/type）
 * - POST /admin/accounts/sync/crs {base_url, username, password, sync_proxies, selected_account_ids}
 *     → { created, updated, skipped, failed, items[{crs_account_id, kind, name, action, error?}] }
 * 三步流程：输入凭据 → 预览选择（新账号默认全选）→ 执行并展示逐条结果。 */
import { ref, reactive, computed, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['synced'])

const step = ref('input') // input | preview | result
const form = reactive({ base_url: '', username: '', password: '', sync_proxies: true })
const previewing = ref(false)
const syncing = ref(false)
const preview = ref(null)
const selected = ref(new Set())
const result = ref(null)

// 旧 errorItems：失败项 + 非「未勾选」跳过项
const errorItems = computed(() => (result.value?.items || []).filter((i) => i.action === 'failed' || (i.action === 'skipped' && i.error !== 'not selected')))
const hasNewNoneSel = computed(() => !!preview.value && preview.value.new_accounts.length > 0 && selected.value.size === 0)

watch(open, (o) => {
  if (o) {
    step.value = 'input'
    Object.assign(form, { base_url: '', username: '', password: '', sync_proxies: true })
    preview.value = null; selected.value = new Set(); result.value = null
  }
})

const previewCrs = async () => {
  if (!form.base_url.trim() || !form.username.trim() || !form.password.trim()) { message.error('CRS 地址、用户名、密码均为必填'); return }
  previewing.value = true
  try {
    const res = await request('/admin/accounts/sync/crs/preview', {
      method: 'POST',
      body: { base_url: form.base_url.trim(), username: form.username.trim(), password: form.password },
    })
    preview.value = res
    selected.value = new Set((res.new_accounts || []).map((a) => a.crs_account_id)) // 新账号默认全选
    step.value = 'preview'
  } catch (e) {
    message.error(`预览失败：${e.message}`)
  } finally {
    previewing.value = false
  }
}

const syncCrs = async () => {
  if (!form.base_url.trim() || !form.username.trim() || !form.password.trim()) { message.error('CRS 地址、用户名、密码均为必填'); return }
  syncing.value = true
  try {
    const res = await request('/admin/accounts/sync/crs', {
      method: 'POST',
      body: {
        base_url: form.base_url.trim(), username: form.username.trim(), password: form.password,
        sync_proxies: form.sync_proxies,
        selected_account_ids: [...selected.value],
      },
    })
    result.value = res
    step.value = 'result'
    if (res.failed > 0) message.warning(`同步完成（部分失败）：新建 ${res.created}，更新 ${res.updated}，失败 ${res.failed}`)
    else message.success(`同步完成：新建 ${res.created}，更新 ${res.updated}，跳过 ${res.skipped}`)
    emit('synced')
  } catch (e) {
    message.error(`同步失败：${e.message}`)
  } finally {
    syncing.value = false
  }
}

const back = () => { step.value = 'input'; preview.value = null; selected.value = new Set() }
</script>

<template>
  <a-modal v-model:open="open" title="从 CRS 同步账号" width="600px" :footer="null">
    <!-- 第一步：凭据 -->
    <a-form v-if="step === 'input'" layout="vertical" size="small">
      <a-alert type="warning" show-icon message="CRS（Claude Relay Service）需 v1.1.0+；同步会更新已存在账号的凭据与状态。" style="margin-bottom: 10px" />
      <a-form-item label="CRS 地址" required><a-input v-model:value="form.base_url" placeholder="https://your-crs.example.com" /></a-form-item>
      <div class="two">
        <a-form-item label="用户名" required><a-input v-model:value="form.username" autocomplete="username" /></a-form-item>
        <a-form-item label="密码" required><a-input-password v-model:value="form.password" autocomplete="current-password" /></a-form-item>
      </div>
      <a-form-item><a-checkbox v-model:checked="form.sync_proxies">同时同步代理配置</a-checkbox></a-form-item>
      <div class="foot">
        <a-button @click="open = false">取消</a-button>
        <a-button type="primary" :loading="previewing" @click="previewCrs">预览</a-button>
      </div>
    </a-form>

    <!-- 第二步：预览选择 -->
    <div v-else-if="step === 'preview' && preview" class="pv">
      <div v-if="(preview.existing_accounts || []).length" class="sec">
        <div class="sec-title">已存在（将更新凭据，{{ preview.existing_accounts.length }}）</div>
        <div class="list dim">
          <div v-for="a in preview.existing_accounts" :key="a.crs_account_id">
            <a-tag class="mini">{{ a.platform }} / {{ a.type }}</a-tag>{{ a.name }}
          </div>
        </div>
      </div>
      <div v-if="(preview.new_accounts || []).length" class="sec">
        <div class="sec-title">
          新账号（{{ preview.new_accounts.length }}）
          <a @click.prevent="selected = new Set(preview.new_accounts.map((x) => x.crs_account_id))">全选</a>
          <a @click.prevent="selected = new Set()">清空</a>
        </div>
        <div class="list sel">
          <a-checkbox
            v-for="a in preview.new_accounts" :key="a.crs_account_id"
            :checked="selected.has(a.crs_account_id)"
            @change="() => { const s = new Set(selected); s.has(a.crs_account_id) ? s.delete(a.crs_account_id) : s.add(a.crs_account_id); selected = s }"
          >
            <a-tag color="green" class="mini">{{ a.platform }} / {{ a.type }}</a-tag>{{ a.name }}
          </a-checkbox>
        </div>
        <div class="dim" style="margin-top: 6px">已选 {{ selected.size }} 个新账号</div>
      </div>
      <div v-if="!(preview.new_accounts || []).length" class="dim" style="padding: 8px 0">
        没有需要新建的账号{{ (preview.existing_accounts || []).length ? `，将更新 ${preview.existing_accounts.length} 个已有账号` : '' }}
      </div>
      <div class="foot">
        <a-button :disabled="syncing" @click="back">返回</a-button>
        <a-button type="primary" :loading="syncing" :disabled="hasNewNoneSel" @click="syncCrs">开始同步</a-button>
      </div>
    </div>

    <!-- 第三步：结果 -->
    <div v-else-if="step === 'result' && result" class="pv">
      <div class="sec-title">同步结果</div>
      <div class="dim">新建 {{ result.created }} · 更新 {{ result.updated }} · 跳过 {{ result.skipped }} · 失败 {{ result.failed }}</div>
      <div v-if="errorItems.length" class="ferr">
        <div v-for="(i, idx) in errorItems" :key="idx" class="mono">{{ i.kind }} {{ i.crs_account_id }} {{ i.name }} — {{ i.action }}{{ i.error ? `：${i.error}` : '' }}</div>
      </div>
      <div class="foot"><a-button type="primary" @click="open = false">关闭</a-button></div>
    </div>
  </a-modal>
</template>

<style scoped>
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 0 12px; }
.foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
.pv { display: flex; flex-direction: column; gap: 10px; }
.sec-title { font-weight: 500; font-size: 13px; margin-bottom: 6px; display: flex; gap: 10px; align-items: center; }
.sec-title a { font-size: 11px; font-weight: 400; cursor: pointer; }
.list { max-height: 170px; overflow: auto; display: flex; flex-direction: column; gap: 4px; font-size: 12px; }
.list.sel { padding: 6px 8px; border: 1px solid var(--hr); border-radius: 8px; }
.dim { color: var(--text2); font-size: 12px; }
.mini { font-size: 10px; line-height: 16px; padding: 0 4px; margin: 0; }
.ferr { padding: 8px 10px; border-radius: 6px; background: rgba(207, 48, 48, 0.08); color: #c13438; font-size: 12px; max-height: 180px; overflow: auto; }
.mono { font-family: Consolas, monospace; }
</style>
