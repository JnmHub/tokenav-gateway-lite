<script setup>
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
/* ============================================================
 * 基础 tab：站点信息 + 后台体验（对照旧 SettingsView.vue「基础」tab）
 * 接口：GET/PATCH /admin/settings（部分更新，只提交本分区字段）
 * 归一化：分页大小 5-1000、分页选项逐个 clamp 后去重升序
 * ============================================================ */
import { ref, onMounted } from 'vue'
import { isCancellation } from '../../api'
import { settingsErrorMessage } from '../../api/settings'
import { useSettingsContract } from '../../composables/useSettingsContract'
import { createLatestRequest } from '../../api/latestRequest'
import { useDraftBaseline, requireFields, retainEdits } from '../../composables/useDraftBaseline'
import { message } from 'ant-design-vue'
import { clampInt, parseWhitelist } from './fields'

const loading = ref(true)
const saving = ref(false)
const err = ref('')
const s = ref(null)
const loadLane = createLatestRequest()
const contract = useSettingsContract(() => { loadLane.cancel(); draft.clear(); s.value = null; err.value = '' })
const draft = useDraftBaseline(() => s.value && Object.fromEntries(Object.entries(s.value).filter(([key]) => ['site_name', 'site_logo', 'site_subtitle', 'api_base_url', 'contact_info', 'doc_url', 'backend_mode_enabled', 'hide_ccs_import_button', 'table_default_page_size', 'table_page_size_options_input'].includes(key))), () => saving.value)
const { conflict } = contract

const load = async () => {
  if (saving.value) return
  const turn = loadLane.begin()
  loading.value = true
  try {
    const data = await contract.read({ signal: turn.signal })
    requireFields(data, ['site_name', 'site_logo', 'site_subtitle', 'api_base_url', 'contact_info', 'doc_url', 'backend_mode_enabled', 'hide_ccs_import_button', 'table_default_page_size', 'table_page_size_options'])
    if (!turn.isCurrent() || !contract.lifetime.isAlive()) return
    const edits = draft.edits()
    s.value = data
    apply(data)
    draft.accept()
    Object.assign(s.value, edits)
    err.value = ''
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) err.value = e.message
  } finally {
    if (turn.isCurrent() && contract.lifetime.isAlive()) loading.value = false
  }
}

// 保存：只带本分区字段；分页选项规范化后回填（默认值必须在选项内，否则取第一项）
const save = async () => {
  if (saving.value || !s.value) return
  saving.value = true
  loadLane.cancel(); loading.value = false
  try {
    const submitted = JSON.parse(JSON.stringify(s.value))
    const f = submitted
    const options = parseWhitelist(String(f.table_page_size_options_input || ''))
      .map((v) => clampInt(Number(v), 0, 5, 1000))
      .filter((v) => v > 0)
    const pageSize = options.includes(clampInt(f.table_default_page_size, 20, 5, 1000))
      ? clampInt(f.table_default_page_size, 20, 5, 1000)
      : (options[0] || 20)
    const body = {
      site_name: String(f.site_name || '').trim(),
      site_logo: String(f.site_logo || '').trim(),
      site_subtitle: String(f.site_subtitle || '').trim(),
      api_base_url: String(f.api_base_url || '').trim(),
      contact_info: String(f.contact_info || '').trim(),
      doc_url: String(f.doc_url || '').trim(),
      backend_mode_enabled: f.backend_mode_enabled !== false,
      hide_ccs_import_button: Boolean(f.hide_ccs_import_button),
      table_default_page_size: pageSize,
      table_page_size_options: options.length ? options : [10, 20, 50, 100],
    }
    const updated = await contract.save(body)
    const current = s.value
    requireFields(updated, Object.keys(body))
    const stillEditing = retainEdits(current, submitted)
    apply(updated)
    draft.accept()
    Object.assign(s.value, stillEditing)
    message.success('基础信息已保存')
  } catch (e) {
    if (!isCancellation(e)) message.error(`保存失败：${settingsErrorMessage(e)}`)
  } finally {
    saving.value = false
  }
}

// 回显：把服务端规范化后的值写回表单（对照旧 applySettings）
const apply = (d) => {
  if (!d || !s.value) return
  for (const k of ['site_name', 'site_logo', 'site_subtitle', 'api_base_url', 'contact_info', 'doc_url']) {
    s.value[k] = d[k] || ''
  }
  s.value.backend_mode_enabled = d.backend_mode_enabled !== false
  s.value.hide_ccs_import_button = Boolean(d.hide_ccs_import_button)
  s.value.table_default_page_size = clampInt(d.table_default_page_size, 20, 5, 1000)
  const opts = Array.isArray(d.table_page_size_options) && d.table_page_size_options.length ? d.table_page_size_options : [10, 20, 50, 100]
  s.value.table_page_size_options_input = opts.join(', ')
}

onMounted(load)
</script>

<template>
  <a-spin :spinning="loading">
    <div class="sw w12-design">
      <div v-if="err" class="card"><W12Button @click="load" variant="danger">加载失败：{{ err }}（点击重试）</W12Button></div>
      <a-alert v-if="conflict" :message="conflict" type="warning" show-icon>
        <template #action><W12Button :disabled="saving" @click="load">重新读取</W12Button></template>
      </a-alert>
      <template v-if="s">
        <div class="card">
          <div class="card-title">基础信息</div>
          <div class="card-sub">只保留网关后台需要展示和复用的站点配置。</div>
          <div class="form">
            <div class="frow"><label>站点名称</label><W12Input v-model:value="s.site_name" class="fld" placeholder="JnmGatewayApi" /></div>
            <div class="frow"><label>站点副标题</label><W12Input v-model:value="s.site_subtitle" class="fld" /></div>
            <div class="frow"><label>API 基础地址</label><W12Input v-model:value="s.api_base_url" class="fld" placeholder="https://api.example.com" /></div>
            <div class="frow"><label>文档地址</label><W12Input v-model:value="s.doc_url" class="fld" placeholder="https://docs.example.com" /></div>
            <div class="frow"><label>Logo 地址</label><W12Input v-model:value="s.site_logo" class="fld-full" /></div>
            <div class="frow"><label>联系方式</label><a-textarea v-model:value="s.contact_info" class="fld-full" :rows="3" auto-size /></div>
          </div>
        </div>
        <div class="card">
          <div class="card-title">后台体验</div>
          <div class="form">
            <div class="frow"><label>后台模式</label><a-switch v-model:checked="s.backend_mode_enabled" /><span class="static" style="margin-left: 12px">启用后以后台管理体验为主</span></div>
            <div class="frow"><label>隐藏 CCS 导入按钮</label><a-switch v-model:checked="s.hide_ccs_import_button" /></div>
            <div class="frow"><label>默认分页大小</label><a-input-number v-model:value="s.table_default_page_size" :min="5" :max="1000" class="fld-n" /></div>
            <div class="frow"><label>分页选项</label><W12Input v-model:value="s.table_page_size_options_input" class="fld" placeholder="10, 20, 50, 100" /></div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="saving" @click="save">保存基础设置</W12Button></div>
        </div>
      </template>
    </div>
  </a-spin>
</template>

<style scoped>
@import './settings.css';
</style>

