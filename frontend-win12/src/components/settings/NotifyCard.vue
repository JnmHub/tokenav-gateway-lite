<script setup>
/* ============================================================
 * 通知卡（对照 dto.SystemSettings 余额/订阅/账号限额通知字段）
 * - balance_low_notify_enabled / balance_low_notify_threshold / balance_low_notify_recharge_url
 * - subscription_expiry_notify_enabled
 * - account_quota_notify_enabled / account_quota_notify_emails
 *   NotifyEmailEntry = { email, disabled, verified }（dto/notify_email_entry.go）；
 *   请求传数组 = 整体替换（UpdateSettingsRequest.AccountQuotaNotifyEmails *[] 指针语义）
 * 保存走 SystemSettings.vue 快照+patch，未触字段不会被清零。
 * ============================================================ */
import { ref, computed } from 'vue'
import { PlusOutlined, DeleteOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  s: { type: Object, required: true },
  save: { type: Function, required: true },
})

const saving = ref(false)

// 额外通知邮箱直接编辑快照数组（引用绑定）；verified 是用户侧验证状态，管理端只读展示
const emails = computed(() => {
  if (!Array.isArray(props.s.account_quota_notify_emails)) props.s.account_quota_notify_emails = []
  return props.s.account_quota_notify_emails
})
const addEmail = () => emails.value.push({ email: '', disabled: false, verified: false })
const removeEmail = (i) => emails.value.splice(i, 1)

const doSave = async () => {
  saving.value = true
  try {
    const patch = {
      balance_low_notify_enabled: !!props.s.balance_low_notify_enabled,
      balance_low_notify_threshold: Number(props.s.balance_low_notify_threshold) || 0,
      balance_low_notify_recharge_url: String(props.s.balance_low_notify_recharge_url || '').trim(),
      subscription_expiry_notify_enabled: !!props.s.subscription_expiry_notify_enabled,
      account_quota_notify_enabled: !!props.s.account_quota_notify_enabled,
      // 整体替换语义：过滤掉未填邮箱的空行，避免脏数据入库
      account_quota_notify_emails: emails.value
        .map((e) => ({ email: String(e.email || '').trim(), disabled: !!e.disabled, verified: !!e.verified }))
        .filter((e) => e.email),
    }
    await props.save('notify', patch)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="card-title">通知提醒</div>
    <div class="form">
      <div class="frow">
        <label>余额不足提醒</label>
        <div class="fmain">
          <a-switch v-model:checked="s.balance_low_notify_enabled" />
          <a-input-number v-model:value="s.balance_low_notify_threshold" :min="0" class="fld-n"
            :disabled="!s.balance_low_notify_enabled" placeholder="阈值" />
          <span class="static">余额低于阈值时邮件提醒用户</span>
        </div>
      </div>
      <div class="frow">
        <label>充值跳转地址</label>
        <div class="fmain">
          <a-input v-model:value="s.balance_low_notify_recharge_url" class="fld-full"
            :disabled="!s.balance_low_notify_enabled" placeholder="https://..." />
        </div>
      </div>
      <div class="frow">
        <label>订阅到期提醒</label>
        <div class="fmain"><a-switch v-model:checked="s.subscription_expiry_notify_enabled" /></div>
      </div>
      <div class="frow">
        <label>账号限额通知</label>
        <div class="fmain"><a-switch v-model:checked="s.account_quota_notify_enabled" /></div>
      </div>
      <div v-for="(e, i) in emails" :key="i" class="frow">
        <label>{{ i === 0 ? '额外通知邮箱' : ' ' }}</label>
        <div class="fmain">
          <a-input v-model:value="e.email" class="fld" placeholder="user@example.com" />
          <a-switch v-model:checked="e.disabled" checked-children="停用" un-checked-children="启用" />
          <a-tag :color="e.verified ? 'green' : 'default'">{{ e.verified ? '已验证' : '未验证' }}</a-tag>
          <a-button type="text" size="small" danger @click="removeEmail(i)"><DeleteOutlined /></a-button>
        </div>
      </div>
      <div class="frow">
        <label>{{ emails.length ? ' ' : '额外通知邮箱' }}</label>
        <div class="fmain">
          <a-button size="small" :disabled="!s.account_quota_notify_enabled" @click="addEmail">
            <PlusOutlined /> 添加邮箱
          </a-button>
          <span class="static">管理员额外接收账号限额通知的邮箱</span>
        </div>
      </div>
    </div>
    <div class="savebar">
      <a-button type="primary" class="save" :loading="saving" @click="doSave">保存通知设置</a-button>
    </div>
  </div>
</template>

<style scoped>
@import './settings.css';
</style>
