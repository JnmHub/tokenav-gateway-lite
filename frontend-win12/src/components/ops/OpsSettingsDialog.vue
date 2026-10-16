<script setup>
/* 运维设置弹窗（二期）：OpsSettingsDialog 等价实现
 * 契约来源：旧 OpsSettingsDialog.vue + frontend/src/api/admin/ops.ts
 * - 打开时并行 GET：/admin/ops/runtime/alert（评估间隔）、/admin/ops/email-notification/config
 *   （告警+报告邮件）、/admin/ops/advanced-settings（高级）、/admin/ops/settings/metric-thresholds（4 阈值）
 * - 保存 = 4 个 PUT 并行；保存前防覆盖：
 *   1) 重新 GET advanced-settings，把服务端最新 codex_tail_burst 合并回 payload
 *      （整对象 PUT，缺字段会把后端 *bool 写成 nil，覆盖独立开关）
 *   2) 邮件启用但无收件人时自动禁用（与旧实现一致，不阻断保存）
 * - 高级设置块拆分到 OpsSettingsAdvanced.vue（单文件行数控制） */
import { ref, reactive, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { Switch, Select, InputNumber, Input } from 'ant-design-vue'
import { request } from '../../api'
import OpsSettingsAdvanced from './OpsSettingsAdvanced.vue'

const props = defineProps({ open: { type: Boolean, default: false } })
const emit = defineEmits(['update:open', 'saved'])

const loading = ref(false)
const saving = ref(false)

const runtimeSettings = ref(null)      // { evaluation_interval_seconds, distributed_lock, silencing, thresholds }
const emailConfig = ref(null)          // { alert: {...}, report: {...} }
const advancedSettings = ref(null)     // OpsAdvancedSettings
const metricThresholds = ref({ sla_percent_min: 99.5, ttft_p99_ms_max: 500, request_error_rate_percent_max: 5, upstream_error_rate_percent_max: 5 })
const codexTailBurstSnapshot = ref(undefined) // 打开弹窗时的 Tail-Burst 快照（保存前合并防覆盖）

const loadAllSettings = async () => {
  loading.value = true
  try {
    const [runtime, email, advanced, thresholds] = await Promise.all([
      request('/admin/ops/runtime/alert', { method: 'GET' }),
      request('/admin/ops/email-notification/config', { method: 'GET' }),
      request('/admin/ops/advanced-settings', { method: 'GET' }),
      request('/admin/ops/settings/metric-thresholds', { method: 'GET' }),
    ])
    runtimeSettings.value = runtime
    emailConfig.value = email
    advancedSettings.value = advanced
    codexTailBurstSnapshot.value = advanced.codex_tail_burst ? { ...advanced.codex_tail_burst } : undefined
    // 兼容旧 payload：后端未返回该块时补默认值，保证表单可绑定
    if (advancedSettings.value && !advancedSettings.value.openai_account_quota_auto_pause) {
      advancedSettings.value.openai_account_quota_auto_pause = { default_threshold_5h: 0, default_threshold_7d: 0 }
    }
    metricThresholds.value = {
      sla_percent_min: thresholds?.sla_percent_min ?? 99.5,
      ttft_p99_ms_max: thresholds?.ttft_p99_ms_max ?? 500,
      request_error_rate_percent_max: thresholds?.request_error_rate_percent_max ?? 5,
      upstream_error_rate_percent_max: thresholds?.upstream_error_rate_percent_max ?? 5,
    }
  } catch (e) { message.error(`加载设置失败：${e.message}`) }
  finally { loading.value = false }
}

watch(() => props.open, (v) => { if (v) loadAllSettings() })

/* ---------- 邮件收件人增删（校验 + 小写去重，与旧实现一致） ---------- */
const alertRecipientInput = ref('')
const reportRecipientInput = ref('')
const isValidEmail = (s) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s)
const addRecipient = (target) => {
  if (!emailConfig.value) return
  const raw = (target === 'alert' ? alertRecipientInput.value : reportRecipientInput.value).trim()
  if (!raw) return
  if (!isValidEmail(raw)) { message.error('邮箱格式不正确'); return }
  const normalized = raw.toLowerCase()
  const list = target === 'alert' ? emailConfig.value.alert.recipients : emailConfig.value.report.recipients
  if (!list.includes(normalized)) list.push(normalized)
  if (target === 'alert') alertRecipientInput.value = ''
  else reportRecipientInput.value = ''
}
const removeRecipient = (target, email) => {
  if (!emailConfig.value) return
  const list = target === 'alert' ? emailConfig.value.alert.recipients : emailConfig.value.report.recipients
  const idx = list.indexOf(email)
  if (idx >= 0) list.splice(idx, 1)
}

const MIN_SEV_OPTS = [
  { value: '', label: '全部级别' },
  { value: 'critical', label: '严重（critical）' },
  { value: 'warning', label: '警告（warning）' },
  { value: 'info', label: '信息（info）' },
]

/* ---------- 校验（规则与旧 validation 相同） ---------- */
const validation = computed(() => {
  const errors = []
  if (runtimeSettings.value) {
    const s = runtimeSettings.value.evaluation_interval_seconds
    if (!Number.isFinite(s) || s < 1 || s > 86400) errors.push('评估间隔必须在 1 到 86400 秒之间')
  }
  if (advancedSettings.value) {
    const { error_log_retention_days, minute_metrics_retention_days, hourly_metrics_retention_days } = advancedSettings.value.data_retention
    if (error_log_retention_days < 0 || error_log_retention_days > 365) errors.push('保留天数必须在 0-365 天之间（0 = 每次清理时清空所有）')
    if (minute_metrics_retention_days < 0 || minute_metrics_retention_days > 365) errors.push('保留天数必须在 0-365 天之间（0 = 每次清理时清空所有）')
    if (hourly_metrics_retention_days < 0 || hourly_metrics_retention_days > 365) errors.push('保留天数必须在 0-365 天之间（0 = 每次清理时清空所有）')
    const { default_threshold_5h, default_threshold_7d } = advancedSettings.value.openai_account_quota_auto_pause || {}
    if (default_threshold_5h < 0 || default_threshold_5h > 1 || default_threshold_7d < 0 || default_threshold_7d > 1) errors.push('OpenAI 配额自动暂停阈值必须在 0-100 之间')
  }
  const th = metricThresholds.value
  if (th.sla_percent_min != null && (th.sla_percent_min < 0 || th.sla_percent_min > 100)) errors.push('SLA最低百分比必须在0-100之间')
  if (th.ttft_p99_ms_max != null && th.ttft_p99_ms_max < 0) errors.push('TTFT P99最大值必须大于等于0')
  if (th.request_error_rate_percent_max != null && (th.request_error_rate_percent_max < 0 || th.request_error_rate_percent_max > 100)) errors.push('请求错误率最大值必须在0-100之间')
  if (th.upstream_error_rate_percent_max != null && (th.upstream_error_rate_percent_max < 0 || th.upstream_error_rate_percent_max > 100)) errors.push('上游错误率最大值必须在0-100之间')
  return { valid: errors.length === 0, errors }
})

/* ---------- 保存：4 PUT 并行 + 防覆盖合并 ---------- */
const saveAllSettings = async () => {
  if (!validation.value.valid) { message.error(validation.value.errors[0]); return }
  saving.value = true
  try {
    // 启用但无收件人 → 自动禁用（旧实现同策略，不阻断保存）
    if (emailConfig.value) {
      if (emailConfig.value.alert.enabled && emailConfig.value.alert.recipients.length === 0) emailConfig.value.alert.enabled = false
      if (emailConfig.value.report.enabled && emailConfig.value.report.recipients.length === 0) emailConfig.value.report.enabled = false
    }
    // 整对象 PUT 防覆盖：先拉服务端最新 advanced-settings，合并 codex_tail_burst 快照回 payload
    let advancedPayload = null
    if (advancedSettings.value) {
      let latestTailBurst = codexTailBurstSnapshot.value
      try {
        const latest = await request('/admin/ops/advanced-settings', { method: 'GET' })
        latestTailBurst = latest.codex_tail_burst ?? latestTailBurst
      } catch { /* 读取失败保留打开弹窗时快照，避免静默清空独立开关 */ }
      advancedPayload = { ...advancedSettings.value, ...(latestTailBurst ? { codex_tail_burst: { ...latestTailBurst } } : {}) }
    }
    await Promise.all([
      runtimeSettings.value
        ? request('/admin/ops/runtime/alert', { method: 'PUT', body: runtimeSettings.value }) : Promise.resolve(),
      emailConfig.value
        ? request('/admin/ops/email-notification/config', { method: 'PUT', body: emailConfig.value }) : Promise.resolve(),
      advancedPayload
        ? request('/admin/ops/advanced-settings', { method: 'PUT', body: advancedPayload }) : Promise.resolve(),
      request('/admin/ops/settings/metric-thresholds', { method: 'PUT', body: metricThresholds.value }),
    ])
    message.success('运维监控设置保存成功')
    emit('saved')
    emit('update:open', false)
  } catch (e) { message.error(`保存设置失败：${e.message}`) }
  finally { saving.value = false }
}

const close = () => emit('update:open', false)
</script>

<template>
  <a-modal :open="open" title="运维监控设置" width="760px" :confirm-loading="saving" ok-text="保存" cancel-text="取消" @ok="saveAllSettings" @cancel="close">
    <a-spin :spinning="loading">
      <div v-if="runtimeSettings && emailConfig && advancedSettings">
        <!-- 校验面板 -->
        <div v-if="!validation.valid" class="val-panel">
          <div class="val-t">请先修正以下问题</div>
          <ul><li v-for="m in validation.errors" :key="m">{{ m }}</li></ul>
        </div>

        <!-- 数据采集 -->
        <div class="blk">
          <div class="blk-t">数据采集</div>
          <div class="th-grid">
            <label>评估间隔（秒）<InputNumber v-model:value="runtimeSettings.evaluation_interval_seconds" :min="1" :max="86400" size="small" style="width: 100%" /></label>
          </div>
          <div class="hint">检测任务的执行频率，建议保持默认</div>
        </div>

        <!-- 告警邮件 -->
        <div class="blk">
          <div class="row">
            <span class="lb">开启预警</span>
            <Switch v-model:checked="emailConfig.alert.enabled" size="small" />
          </div>
          <template v-if="emailConfig.alert.enabled">
            <div class="add-row">
              <Input v-model:value="alertRecipientInput" type="email" size="small" placeholder="输入邮箱地址" style="flex: 1" @press-enter="addRecipient('alert')" />
              <a-button size="small" @click="addRecipient('alert')">添加</a-button>
            </div>
            <div class="chips">
              <span v-for="email in emailConfig.alert.recipients" :key="email" class="chip">
                {{ email }}<a class="chip-x" @click="removeRecipient('alert', email)">×</a>
              </span>
            </div>
            <div class="hint">若为空，系统将使用第一个管理员邮箱作为默认收件人</div>
            <div class="row" style="margin-top: 8px">
              <span class="lb">最低级别</span>
              <Select v-model:value="emailConfig.alert.min_severity" :options="MIN_SEV_OPTS" size="small" style="width: 150px" />
            </div>
          </template>
        </div>

        <!-- 报告邮件 -->
        <div class="blk">
          <div class="row">
            <span class="lb">开启评估报告</span>
            <Switch v-model:checked="emailConfig.report.enabled" size="small" />
          </div>
          <template v-if="emailConfig.report.enabled">
            <div class="add-row">
              <Input v-model:value="reportRecipientInput" type="email" size="small" placeholder="输入邮箱地址" style="flex: 1" @press-enter="addRecipient('report')" />
              <a-button size="small" @click="addRecipient('report')">添加</a-button>
            </div>
            <div class="chips">
              <span v-for="email in emailConfig.report.recipients" :key="email" class="chip">
                {{ email }}<a class="chip-x" @click="removeRecipient('report', email)">×</a>
              </span>
            </div>
            <div class="rep-grid">
              <div class="row">
                <span class="lb">每日摘要</span>
                <Switch v-model:checked="emailConfig.report.daily_summary_enabled" size="small" />
              </div>
              <Input v-if="emailConfig.report.daily_summary_enabled" v-model:value="emailConfig.report.daily_summary_schedule" size="small" placeholder="0 9 * * *" />
              <div class="row">
                <span class="lb">每周摘要</span>
                <Switch v-model:checked="emailConfig.report.weekly_summary_enabled" size="small" />
              </div>
              <Input v-if="emailConfig.report.weekly_summary_enabled" v-model:value="emailConfig.report.weekly_summary_schedule" size="small" placeholder="0 9 * * 1" />
            </div>
            <div class="hint">发送时间使用 Cron 语法；留空将使用默认值</div>
          </template>
        </div>

        <!-- 指标阈值 4 项 -->
        <div class="blk">
          <div class="blk-t">指标阈值配置</div>
          <div class="hint" style="margin-bottom: 8px">配置各项指标的告警阈值，超出阈值时将以红色显示</div>
          <div class="th-grid">
            <label>SLA最低百分比（默认 99.5）<InputNumber v-model:value="metricThresholds.sla_percent_min" :min="0" :max="100" :step="0.1" size="small" style="width: 100%" /></label>
            <label>TTFT P99最大值（毫秒，默认 500）<InputNumber v-model:value="metricThresholds.ttft_p99_ms_max" :min="0" :step="50" size="small" style="width: 100%" /></label>
            <label>请求错误率最大值（%，默认 5）<InputNumber v-model:value="metricThresholds.request_error_rate_percent_max" :min="0" :max="100" :step="0.1" size="small" style="width: 100%" /></label>
            <label>上游错误率最大值（%，默认 5）<InputNumber v-model:value="metricThresholds.upstream_error_rate_percent_max" :min="0" :max="100" :step="0.1" size="small" style="width: 100%" /></label>
          </div>
        </div>

        <!-- 高级设置（折叠，子组件） -->
        <a-collapse ghost size="small">
          <a-collapse-panel key="adv" header="高级设置">
            <OpsSettingsAdvanced :advanced="advancedSettings" />
          </a-collapse-panel>
        </a-collapse>
      </div>
      <div v-else-if="!loading" class="hint" style="padding: 24px 0; text-align: center">设置加载失败，请关闭后重试</div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.blk { border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px; margin-bottom: 10px; }
.blk-t { font-size: 12px; font-weight: 500; margin-bottom: 6px; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 4px 0; }
.lb { font-size: 12px; }
.hint { font-size: 11px; color: var(--text2); }
.add-row { display: flex; gap: 8px; margin-top: 6px; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; }
.chip { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; background: rgba(59, 130, 246, 0.12); color: #3b82f6; border-radius: 12px; padding: 2px 10px; }
.chip-x { cursor: pointer; font-size: 13px; }
.rep-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 4px 16px; margin-top: 8px; align-items: center; }
.th-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 10px; margin-top: 4px; }
.th-grid label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text2); }
.val-panel { background: rgba(217, 119, 6, 0.08); border: 1px solid rgba(217, 119, 6, 0.3); border-radius: 8px; padding: 8px 12px; font-size: 12px; color: #d97706; margin-bottom: 10px; }
.val-t { font-weight: 500; }
.val-panel ul { margin: 4px 0 0 18px; }
@media (max-width: 640px) { .rep-grid, .th-grid { grid-template-columns: 1fr; } }
</style>
