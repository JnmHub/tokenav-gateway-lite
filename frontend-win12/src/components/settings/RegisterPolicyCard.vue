<script setup>
/* ============================================================
 * 注册与登录策略卡（对照 dto.SystemSettings 注册/登录协议字段）
 * - registration_enabled / email_verify_enabled / registration_email_suffix_whitelist
 * - invitation_code_enabled / password_reset_enabled / promo_code_enabled
 * - login_agreement_enabled / login_agreement_mode / login_agreement_documents
 *   （字段名以 settings.go 为准；documents = [{id,title,content_md}]，
 *    id 留空由后端保留原值/生成；启用协议时后端要求至少一份文档）
 * 保存走 SystemSettings.vue 快照+patch。
 * ============================================================ */
import { ref, computed } from 'vue'
import { message } from 'ant-design-vue'
import { PlusOutlined, DeleteOutlined, ArrowUpOutlined, ArrowDownOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  s: { type: Object, required: true },
  save: { type: Function, required: true },
})

const saving = ref(false)

// 邮箱后缀白名单：a-select tags 模式直接绑定数组（逗号/回车分词）
const suffixes = computed(() => {
  if (!Array.isArray(props.s.registration_email_suffix_whitelist)) props.s.registration_email_suffix_whitelist = []
  return props.s.registration_email_suffix_whitelist
})

// 登录协议文档：直接编辑快照数组；标题与正文都为空的行提交前剔除（后端会拒绝全空标题行）
const docs = computed(() => {
  if (!Array.isArray(props.s.login_agreement_documents)) props.s.login_agreement_documents = []
  return props.s.login_agreement_documents
})
const addDoc = () => docs.value.push({ id: '', title: '', content_md: '' })
const removeDoc = (i) => docs.value.splice(i, 1)
const moveDoc = (i, dir) => {
  const j = i + dir
  if (j < 0 || j >= docs.value.length) return
  const arr = docs.value
  ;[arr[i], arr[j]] = [arr[j], arr[i]]
}

const doSave = async () => {
  if (props.s.login_agreement_enabled && docs.value.every((d) => !String(d.title || '').trim() && !String(d.content_md || '').trim())) {
    message.warning('启用登录协议时至少需要一份协议文档')
    return
  }
  saving.value = true
  try {
    const patch = {
      registration_enabled: !!props.s.registration_enabled,
      email_verify_enabled: !!props.s.email_verify_enabled,
      invitation_code_enabled: !!props.s.invitation_code_enabled,
      password_reset_enabled: !!props.s.password_reset_enabled,
      promo_code_enabled: !!props.s.promo_code_enabled,
      registration_email_suffix_whitelist: suffixes.value.map((v) => String(v || '').trim()).filter(Boolean),
      login_agreement_enabled: !!props.s.login_agreement_enabled,
      login_agreement_mode: props.s.login_agreement_mode === 'checkbox' ? 'checkbox' : 'modal',
      // 全空文档行不上传（后端把空列表视为保留原值）
      login_agreement_documents: docs.value
        .map((d) => ({ id: String(d.id || ''), title: String(d.title || '').trim(), content_md: String(d.content_md || '') }))
        .filter((d) => d.title || d.content_md),
    }
    await props.save('regpolicy', patch)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="card-title">注册与登录策略</div>
    <div class="form">
      <div class="frow">
        <label>开放注册</label>
        <div class="fmain"><a-switch v-model:checked="s.registration_enabled" /></div>
      </div>
      <div class="frow">
        <label>邮箱验证</label>
        <div class="fmain"><a-switch v-model:checked="s.email_verify_enabled" /><span class="static">注册/换绑需邮箱验证码</span></div>
      </div>
      <div class="frow frow-col">
        <label>邮箱后缀白名单</label>
        <div class="fm-col">
          <a-select v-model:value="suffixes" mode="tags" class="fld-full" :token-separators="[',', ' ', ';']"
            :max-tag-count="6" placeholder="留空 = 不限制；例如 @qq.com、@gmail.com" :open="false" />
        </div>
      </div>
      <div class="frow">
        <label>邀请码注册</label>
        <div class="fmain"><a-switch v-model:checked="s.invitation_code_enabled" /></div>
      </div>
      <div class="frow">
        <label>找回密码</label>
        <div class="fmain"><a-switch v-model:checked="s.password_reset_enabled" /></div>
      </div>
      <div class="frow">
        <label>兑换码</label>
        <div class="fmain"><a-switch v-model:checked="s.promo_code_enabled" /></div>
      </div>
    </div>

    <div class="divider" />

    <div class="card-title">登录协议</div>
    <div class="form">
      <div class="frow">
        <label>启用登录协议</label>
        <div class="fmain">
          <a-switch v-model:checked="s.login_agreement_enabled" />
          <span class="static">用户登录时需确认协议（modal = 弹窗 / checkbox = 勾选）</span>
        </div>
      </div>
      <div class="frow">
        <label>确认方式</label>
        <div class="fmain">
          <a-select v-model:value="s.login_agreement_mode" class="fld-n" :disabled="!s.login_agreement_enabled"
            :options="[{ value: 'modal', label: '弹窗确认' }, { value: 'checkbox', label: '勾选确认' }]" />
        </div>
      </div>
      <div v-for="(d, i) in docs" :key="d.id || i" class="frow frow-col">
        <label>协议文档 {{ i + 1 }}</label>
        <div class="fm-col docbox">
          <div class="docrow">
            <a-input v-model:value="d.title" class="fld-full" placeholder="文档标题（必填）" :maxlength="80" />
            <a-button size="small" type="text" :disabled="i === 0" @click="moveDoc(i, -1)"><ArrowUpOutlined /></a-button>
            <a-button size="small" type="text" :disabled="i === docs.length - 1" @click="moveDoc(i, 1)"><ArrowDownOutlined /></a-button>
            <a-button size="small" type="text" danger @click="removeDoc(i)"><DeleteOutlined /></a-button>
          </div>
          <a-textarea v-model:value="d.content_md" class="fld-full mono" :rows="4" auto-size placeholder="Markdown 正文" />
        </div>
      </div>
      <div class="frow">
        <label>协议文档</label>
        <div class="fmain">
          <a-button size="small" :disabled="!s.login_agreement_enabled" @click="addDoc"><PlusOutlined /> 添加文档</a-button>
          <span v-if="s.login_agreement_updated_at" class="static">最近更新：{{ s.login_agreement_updated_at }}</span>
        </div>
      </div>
    </div>

    <div class="savebar">
      <a-button type="primary" class="save" :loading="saving" @click="doSave">保存注册与登录策略</a-button>
    </div>
  </div>
</template>

<style scoped>
@import './settings.css';
.docbox { display: flex; flex-direction: column; gap: 6px; width: 100%; }
.docrow { display: flex; align-items: center; gap: 4px; }
.docrow .ant-input { flex: 1; }
.divider { border-top: 1px solid var(--hr); margin: 12px -16px; opacity: 0.6; }
</style>
