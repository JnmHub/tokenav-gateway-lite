<script setup>
/* ============================================================
 * 邮件服务卡（对照 dto.SystemSettings 的 SMTP_* 字段 + setting_handler.go 邮件路由）
 * - SMTP：GET /admin/settings 回显；PUT /admin/settings 整卡快照+patch 保存
 * - smtp_password：响应只有 smtp_password_configured 回显位，输入留空 = 不提交（后端保留原值）
 * - 发送测试邮件：POST /admin/settings/send-test-email { email, smtp_* }，
 *   SMTP 字段留空时后端自动回填已保存配置（SendTestEmail）
 * - 邮件模板：GET /admin/settings/email-templates 列表；
 *   GET/PUT /admin/settings/email-templates/:event/:locale；POST .../restore-official
 * ============================================================ */
import { ref, reactive, computed, onMounted } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'
import { MailOutlined, SendOutlined, FileTextOutlined, UndoOutlined, EyeOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  s: { type: Object, required: true }, // 系统设置全量快照（SystemSettings.vue 注入）
  save: { type: Function, required: true }, // (key, patch) => Promise<void> 快照+patch 保存
})

/* ── SMTP 表单 ── */
const saving = ref(false)
// 密码不进 s（响应无该字段），单独暂存，仅输入了新值才提交
const smtpPassword = ref('')

const doSaveSmtp = async () => {
  const patch = {
    smtp_host: String(props.s.smtp_host || '').trim(),
    smtp_port: Number(props.s.smtp_port) || 587,
    smtp_username: String(props.s.smtp_username || '').trim(),
    smtp_from_email: String(props.s.smtp_from_email || '').trim(),
    smtp_from_name: String(props.s.smtp_from_name || '').trim(),
    smtp_use_tls: !!props.s.smtp_use_tls,
  }
  // secret/password 留空不提交：后端在非空时才覆盖，留空保留数据库已存密码
  if (String(smtpPassword.value).trim()) patch.smtp_password = String(smtpPassword.value).trim()
  saving.value = true
  try {
    await props.save('mail', patch)
    smtpPassword.value = ''
  } finally {
    saving.value = false
  }
}

/* ── 发送测试邮件 ── */
const testEmail = ref('')
const testing = ref(false)
const sendTest = async () => {
  const email = String(testEmail.value || '').trim()
  if (!email) {
    message.warning('请先填写接收测试邮件的邮箱')
    return
  }
  testing.value = true
  try {
    // SMTP 各项与表单同步提交；留空字段后端自动回填已保存配置，密码留空沿用原值
    const body = {
      email,
      smtp_host: String(props.s.smtp_host || '').trim(),
      smtp_port: Number(props.s.smtp_port) || 0,
      smtp_username: String(props.s.smtp_username || '').trim(),
      smtp_from_email: String(props.s.smtp_from_email || '').trim(),
      smtp_from_name: String(props.s.smtp_from_name || '').trim(),
      smtp_use_tls: !!props.s.smtp_use_tls,
    }
    if (String(smtpPassword.value).trim()) body.smtp_password = String(smtpPassword.value).trim()
    await request('/admin/settings/send-test-email', { method: 'POST', body })
    message.success(`测试邮件已发送至 ${email}`)
  } catch (e) {
    message.error(`发送失败：${e.message}`)
  } finally {
    testing.value = false
  }
}

/* ── 测试 SMTP 连接（官方入口：/admin/settings/test-smtp，不发送邮件、只验证连通与登录） ── */
const smtpTesting = ref(false)
const testSmtpConnection = async () => {
  smtpTesting.value = true
  try {
    const body = {
      smtp_host: String(props.s.smtp_host || '').trim(),
      smtp_port: Number(props.s.smtp_port) || 0,
      smtp_username: String(props.s.smtp_username || '').trim(),
      smtp_use_tls: !!props.s.smtp_use_tls,
    }
    // 密码留空表示沿用已保存配置；填了就测未保存的新密码
    if (String(smtpPassword.value).trim()) body.smtp_password = String(smtpPassword.value).trim()
    await request('/admin/settings/test-smtp', { method: 'POST', body })
    message.success('SMTP 连接与登录验证通过')
  } catch (e) {
    message.error(`SMTP 测试失败：${e.message}`)
  } finally {
    smtpTesting.value = false
  }
}

/* ── 邮件模板预览（官方入口：/admin/settings/email-template-preview，用样本变量渲染不落库） ── */
const preview = reactive({ open: false, loading: false, subject: '', html: '' })
const previewTemplate = async () => {
  if (!tpl.event || !tpl.locale) {
    message.warning('请先选择事件与语言')
    return
  }
  preview.loading = true
  try {
    const d = await request('/admin/settings/email-template-preview', {
      method: 'POST',
      // variables 留空：服务端会用该事件的安全样本变量渲染（见 service.PreviewTemplate）
      body: { event: tpl.event, locale: tpl.locale, subject: tpl.subject, html: tpl.html, variables: {} },
    })
    preview.subject = d?.subject || ''
    preview.html = d?.html || d?.body || ''
    preview.open = true
  } catch (e) {
    message.error(`预览失败：${e.message}`)
  } finally {
    preview.loading = false
  }
}

/* ── 邮件模板管理 ── */
const tpl = reactive({
  loading: false,
  events: [],
  locales: [],
  event: '',
  locale: '',
  detailLoading: false,
  subject: '',
  html: '',
  isCustom: false,
  updatedAt: '',
  saving: false,
  restoring: false,
})

const tplEventOptions = computed(() => tpl.events.map((e) => ({ value: e.value, label: e.label || e.value })))
const tplLocaleOptions = computed(() => tpl.locales.map((v) => ({ value: v, label: v })))
const hasTemplate = computed(() => !!(tpl.event && tpl.locale))

const loadTemplates = async () => {
  tpl.loading = true
  try {
    const d = await request('/admin/settings/email-templates', { method: 'GET' })
    tpl.events = d?.events || []
    tpl.locales = d?.locales || []
    // 默认选中第一个事件与语言，减少一次点击
    if (!tpl.event && tpl.events.length) tpl.event = tpl.events[0].value
    if (!tpl.locale && tpl.locales.length) tpl.locale = tpl.locales[0]
  } catch (e) {
    message.error(`邮件模板加载失败：${e.message}`)
  } finally {
    tpl.loading = false
  }
}

const loadTemplateDetail = async () => {
  if (!hasTemplate.value) return
  tpl.detailLoading = true
  try {
    const d = await request(
      `/admin/settings/email-templates/${encodeURIComponent(tpl.event)}/${encodeURIComponent(tpl.locale)}`,
      { method: 'GET' },
    )
    tpl.subject = d?.subject || ''
    tpl.html = d?.html || ''
    tpl.isCustom = !!d?.is_custom
    tpl.updatedAt = d?.updated_at || ''
  } catch (e) {
    message.error(`模板读取失败：${e.message}`)
  } finally {
    tpl.detailLoading = false
  }
}

const saveTemplate = async () => {
  tpl.saving = true
  try {
    await request(
      `/admin/settings/email-templates/${encodeURIComponent(tpl.event)}/${encodeURIComponent(tpl.locale)}`,
      { method: 'PUT', body: { subject: tpl.subject, html: tpl.html } },
    )
    message.success('邮件模板已保存')
    await loadTemplateDetail()
  } catch (e) {
    message.error(`模板保存失败：${e.message}`)
  } finally {
    tpl.saving = false
  }
}

// 恢复官方模板：删除覆盖并回填内置模板
const restoreOfficial = async () => {
  tpl.restoring = true
  try {
    await request(
      `/admin/settings/email-templates/${encodeURIComponent(tpl.event)}/${encodeURIComponent(tpl.locale)}/restore-official`,
      { method: 'POST' },
    )
    message.success('已恢复官方模板')
    await loadTemplateDetail()
  } catch (e) {
    message.error(`恢复失败：${e.message}`)
  } finally {
    tpl.restoring = false
  }
}

onMounted(loadTemplates)
</script>

<template>
  <div class="card">
    <div class="card-title"><MailOutlined class="ticon" /> 邮件服务（SMTP）</div>
    <div class="form">
      <div class="frow">
        <label>SMTP 服务器</label>
        <div class="fmain"><a-input v-model:value="s.smtp_host" class="fld" placeholder="smtp.example.com" /></div>
      </div>
      <div class="frow">
        <label>端口</label>
        <div class="fmain"><a-input-number v-model:value="s.smtp_port" :min="1" :max="65535" class="fld-n" /></div>
      </div>
      <div class="frow">
        <label>用户名</label>
        <div class="fmain"><a-input v-model:value="s.smtp_username" class="fld" placeholder="发信账号" /></div>
      </div>
      <div class="frow">
        <label>密码</label>
        <div class="fmain">
          <a-input-password v-model:value="smtpPassword" class="fld" autocomplete="new-password"
            :placeholder="s.smtp_password_configured ? '已配置，留空保留原密码' : '未配置'" />
          <span class="static">{{ s.smtp_password_configured ? '已配置' : '未配置' }}</span>
        </div>
      </div>
      <div class="frow">
        <label>发件邮箱</label>
        <div class="fmain"><a-input v-model:value="s.smtp_from_email" class="fld" placeholder="noreply@example.com" /></div>
      </div>
      <div class="frow">
        <label>发件人名称</label>
        <div class="fmain"><a-input v-model:value="s.smtp_from_name" class="fld" /></div>
      </div>
      <div class="frow">
        <label>使用 TLS</label>
        <div class="fmain"><a-switch v-model:checked="s.smtp_use_tls" /><span class="static">加密连接（端口 465/587 常用）</span></div>
      </div>
      <div class="frow">
        <label>测试邮箱</label>
        <div class="fmain">
          <a-input v-model:value="testEmail" class="fld" placeholder="接收测试邮件的邮箱" @pressEnter="sendTest" />
          <a-button :loading="smtpTesting" @click="testSmtpConnection">测试 SMTP 连接</a-button>
          <a-button :loading="testing" @click="sendTest"><SendOutlined /> 发送测试邮件</a-button>
        </div>
      </div>
    </div>
    <div class="savebar">
      <a-button type="primary" class="save" :loading="saving" @click="doSaveSmtp">保存邮件配置</a-button>
    </div>

    <div class="divider" />

    <div class="card-title"><FileTextOutlined class="ticon" /> 通知邮件模板</div>
    <div class="form">
      <div class="frow">
        <label>事件</label>
        <div class="fmain">
          <a-select v-model:value="tpl.event" class="fld" :options="tplEventOptions" :loading="tpl.loading"
            show-search option-filter-prop="label" placeholder="选择邮件事件" @change="loadTemplateDetail" />
        </div>
      </div>
      <div class="frow">
        <label>语言</label>
        <div class="fmain">
          <a-select v-model:value="tpl.locale" class="fld-n" :options="tplLocaleOptions" placeholder="语言" @change="loadTemplateDetail" />
          <a-tag v-if="hasTemplate" :color="tpl.isCustom ? 'blue' : 'default'" class="tstate">
            {{ tpl.isCustom ? '自定义模板' : '官方模板' }}
          </a-tag>
          <span v-if="tpl.updatedAt" class="static">更新于 {{ tpl.updatedAt }}</span>
        </div>
      </div>
      <div class="frow">
        <label>邮件主题</label>
        <div class="fmain"><a-input v-model:value="tpl.subject" class="fld-full" :disabled="!hasTemplate" /></div>
      </div>
      <div class="frow frow-col">
        <label>HTML 正文</label>
        <div class="fm-col">
          <a-textarea v-model:value="tpl.html" class="fld-full mono" :rows="8" :disabled="!hasTemplate"
            placeholder="选择事件与语言后加载模板内容" />
        </div>
      </div>
    </div>
    <div class="savebar">
      <a-popconfirm title="将删除自定义覆盖并恢复官方内置模板，确认？" @confirm="restoreOfficial">
        <a-button danger :disabled="!hasTemplate || !tpl.isCustom" :loading="tpl.restoring">
          <UndoOutlined /> 恢复官方模板
        </a-button>
      </a-popconfirm>
      <a-button :disabled="!hasTemplate" :loading="preview.loading" @click="previewTemplate"><EyeOutlined /> 预览</a-button>
      <a-button type="primary" class="save" :disabled="!hasTemplate" :loading="tpl.saving" @click="saveTemplate">
        保存模板
      </a-button>
    </div>
  </div>

  <!-- 邮件模板预览（POST /admin/settings/email-template-preview：服务端用安全样本变量渲染，不落库、不发送） -->
  <a-modal v-model:open="preview.open" title="邮件模板预览" :footer="null" width="720px">
    <a-spin :spinning="preview.loading">
      <div class="pv-sub">主题</div>
      <div class="pv-subject">{{ preview.subject || '（未返回主题）' }}</div>
      <div class="pv-sub">正文</div>
      <iframe class="pv-frame" :srcdoc="preview.html" sandbox="" title="邮件正文预览" />
      <div class="pv-hint">预览使用服务端提供的安全样本变量，不会保存或发送邮件。</div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
@import './settings.css';
.ticon { margin-right: 6px; color: var(--text2); }
.tstate { flex: none; }
.divider { border-top: 1px solid var(--hr); margin: 12px -16px; opacity: 0.6; }
.pv-sub { font-size: 12px; color: var(--text2); margin: 8px 0 4px; }
.pv-subject { font-size: 13px; font-weight: 500; }
.pv-frame { width: 100%; height: 360px; border: 1px solid var(--hr); border-radius: 8px; background: #fff; }
.pv-hint { font-size: 12px; color: var(--text2); margin-top: 8px; }
</style>
