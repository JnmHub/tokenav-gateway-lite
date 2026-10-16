<script setup>
/* 管理员合规确认卡（安全 tab）
 * 接口（轻量网关模式真实注册，此前前端无任何入口）：
 * - GET  /admin/compliance        → { required, version, document_path_zh/en,
 *                                     document_url_zh/en, ack_phrase_zh/en, acknowledgement? }
 * - POST /admin/compliance/accept  body { phrase, language }
 * 语义要点（按后端 internal/service/admin_compliance.go 实现）：
 * 1) required=true 表示当前管理员在**本版本**下尚未确认；确认记录按 admin 用户维度保存，
 *    版本号变化后 required 会重新变回 true，需要再次确认。
 * 2) phrase 必须与所选语言的 ack_phrase 完全一致，否则后端拒绝
 *    （ErrAdminComplianceInvalidPhrase）——因此前端在短语不匹配时直接禁用提交，
 *    避免用户提交后才看到失败。
 * 3) 轻量网关模式不做「未确认就拦截后台」的处理（见 routes 注释），这里只提供
 *    查看与确认入口，保持与官方后台一致的能力，不改变运行行为。
 */
import { ref, computed, onMounted } from 'vue'
import { message } from 'ant-design-vue'
import { request as apiRequest, isCancellation } from '../../api'
import { useRequestLifetime } from '../../composables/useRequestLifetime'
import { useDraftBaseline, requireFields } from '../../composables/useDraftBaseline'
import { createLatestRequest } from '../../api/latestRequest'

const loading = ref(false)
const submitting = ref(false)
const err = ref('')
const status = ref(null)

const language = ref('zh')
const phrase = ref('')
const lane = createLatestRequest()
const lifetime = useRequestLifetime(() => { lane.cancel(); status.value = null; phrase.value = ''; err.value = '' })
useDraftBaseline(() => ({ phrase: phrase.value }), () => submitting.value).accept()
const request = (path, options) => apiRequest(path, { ...options, ...lifetime, signal: options?.signal || lifetime.signal })
const readStatus = value => {
  requireFields(value, ['required', 'version'])
  if (typeof value.required !== 'boolean' || typeof value.version !== 'string') throw new Error('后台返回的确认状态无效')
  return value
}

const phraseOf = (lang) => (lang === 'zh' ? status.value?.ack_phrase_zh : status.value?.ack_phrase_en) || ''
const expected = computed(() => phraseOf(language.value))
const matched = computed(() => !!expected.value && phrase.value.trim() === expected.value.trim())
const ack = computed(() => status.value?.acknowledgement || null)

const load = async () => {
  if (submitting.value || !lifetime.isAlive()) return
  const turn = lane.begin()
  loading.value = true
  err.value = ''
  try {
    const updated = readStatus(await request('/admin/compliance', { method: 'GET', signal: AbortSignal.any([turn.signal, lifetime.signal]) }))
    if (turn.isCurrent() && lifetime.isAlive()) status.value = updated
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) err.value = e.message
  } finally {
    if (turn.isCurrent() && lifetime.isAlive()) loading.value = false
  }
}

const submit = async () => {
  if (submitting.value || !matched.value || !lifetime.isAlive()) return
  const submitted = phrase.value
  lane.cancel(); loading.value = false
  submitting.value = true
  try {
    const updated = readStatus(await request('/admin/compliance/accept', {
      method: 'POST',
      body: { phrase: phrase.value.trim(), language: language.value },
    }))
    if (updated.required) throw new Error('后台尚未确认接受本版本')
    status.value = updated
    err.value = ''
    if (phrase.value === submitted) phrase.value = ''
    message.success('已完成本版本的合规确认')
  } catch (e) {
    if (!isCancellation(e) && lifetime.isAlive()) message.error(`确认失败：${e.message}`)
  } finally {
    submitting.value = false
  }
}

const fillPhrase = () => { phrase.value = expected.value }

const fmtTime = (t) => {
  if (!t) return '—'
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return String(t)
  return d.toLocaleString('zh-CN')
}

onMounted(load)
</script>

<template>
  <div class="card">
    <div class="card-title">管理员合规确认</div>
    <div class="card-sub">确认记录按管理员账号保存；文档版本升级后需要重新确认。</div>
    <a-spin :spinning="loading" size="small">
      <div v-if="err" class="cmp-err">{{ err }}</div>
      <template v-else-if="status">
        <div class="form">
          <div class="frow"><label>当前版本</label><span class="static mono">{{ status.version || '-' }}</span></div>
          <div class="frow">
            <label>确认状态</label>
            <div class="fmain">
              <a-tag :color="status.required ? 'red' : 'green'">{{ status.required ? '待确认' : '已确认' }}</a-tag>
              <span v-if="ack" class="static">确认时间：{{ fmtTime(ack.accepted_at) }}（IP {{ ack.ip_address || '—' }}）</span>
            </div>
          </div>
          <div class="frow">
            <label>合规文档</label>
            <div class="fmain">
              <a v-if="status.document_url_zh" :href="status.document_url_zh" target="_blank" rel="noopener">中文文档</a>
              <span v-else class="static mono">{{ status.document_path_zh || '—' }}</span>
              <a v-if="status.document_url_en" :href="status.document_url_en" target="_blank" rel="noopener">English</a>
              <span v-else-if="status.document_path_en" class="static mono">{{ status.document_path_en }}</span>
            </div>
          </div>

          <template v-if="status.required">
            <div class="frow">
              <label>确认语言</label>
              <div class="fmain">
                <a-radio-group v-model:value="language" size="small">
                  <a-radio-button value="zh">中文</a-radio-button>
                  <a-radio-button value="en">English</a-radio-button>
                </a-radio-group>
              </div>
            </div>
            <div class="frow">
              <label>确认短语</label>
              <div class="fmain">
                <span class="static cmp-phrase">{{ expected }}</span>
                <a-button size="small" @click="fillPhrase">填入</a-button>
              </div>
            </div>
            <div class="frow">
              <label>输入确认短语</label>
              <a-input v-model:value="phrase" class="fld" :placeholder="`请完整粘贴上方短语（${language === 'zh' ? '中文' : 'English'}）`" />
            </div>
            <div class="cmp-note">短语必须与上方内容完全一致（含标点），否则后端会拒绝确认。</div>
          </template>
        </div>
        <div class="savebar">
          <a-button v-if="status.required" type="primary" class="save" :disabled="!matched" :loading="submitting" @click="submit">确认接受</a-button>
          <a-button v-else @click="load">刷新状态</a-button>
        </div>
      </template>
    </a-spin>
  </div>
</template>

<style scoped>
@import './settings.css';

.cmp-err { font-size: 13px; color: #d13438; padding: 8px 0; }
.cmp-phrase { font-family: Consolas, monospace; font-size: 12px; word-break: break-all; max-width: 520px; }
.cmp-note { font-size: 12px; color: var(--text2); padding-left: 8px; }
.fmain { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.fmain a { font-size: 13px; }
</style>
