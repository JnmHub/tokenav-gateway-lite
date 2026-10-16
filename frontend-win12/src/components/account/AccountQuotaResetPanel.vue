<script setup>
/* 配额固定重置组（账号编辑器子面板，600 行约束拆分）
 * 旧实现契约：frontend/src/components/account/QuotaLimitCard.vue 的重置区 +
 * EditAccountModal.vue 5374-5394 行 payload 语义。
 * 挂载条件（父组件控制）：type/category∈{apikey, bedrock}——后端 dto/mappers.go
 * 只对 IsAPIKeyOrBedrock 回读 quota_*reset* 字段；固定重置在日/周配额上限生效时
 * 参与周期计算（service/account.go IsDailyQuotaPeriodExpired 等）。
 * extra 键写入语义（fixed=写键，否则删键；后端 ValidateQuotaResetConfig 校验
 * hour 0-23、day 0-6（0=周日）、tz 必须 IANA 名称）：
 * - quota_daily_reset_mode='fixed' + quota_daily_reset_hour
 * - quota_weekly_reset_mode='fixed' + quota_weekly_reset_day + quota_weekly_reset_hour
 * - 任一 fixed 时写 quota_reset_timezone（缺省按 UTC） */
import { computed } from 'vue'

const dailyMode = defineModel('dailyMode', { type: String, default: '' }) // '' | rolling | fixed（'' 展示为滚动）
const dailyHour = defineModel('dailyHour', { type: Number, default: 0 })
const weeklyMode = defineModel('weeklyMode', { type: String, default: '' })
const weeklyDay = defineModel('weeklyDay', { type: Number, default: 1 })
const weeklyHour = defineModel('weeklyHour', { type: Number, default: 0 })
const timezone = defineModel('timezone', { type: String, default: '' }) // '' = UTC

const modeOptions = [
  { value: '', label: '滚动窗口（默认）' },
  { value: 'fixed', label: '固定时间重置' },
]
// 小时下拉 0-23（旧 hourOptions）
const hourOptions = Array.from({ length: 24 }, (_, i) => ({ value: i, label: `${String(i).padStart(2, '0')}:00` }))
// 星期下拉（旧 dayOptions：周一..周六、周日；值 0=周日，1=周一）
const dayOptions = [
  { value: 1, label: '周一' }, { value: 2, label: '周二' }, { value: 3, label: '周三' },
  { value: 4, label: '周四' }, { value: 5, label: '周五' }, { value: 6, label: '周六' },
  { value: 0, label: '周日' },
]
// 常见时区清单（旧 QuotaLimitCard timezoneOptions）+ UTC + 本地
const COMMON_TZ = [
  'Asia/Shanghai', 'Asia/Tokyo', 'Asia/Seoul', 'Asia/Singapore', 'Asia/Kolkata', 'Asia/Dubai',
  'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Europe/Moscow',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles', 'America/Sao_Paulo',
  'Australia/Sydney', 'Pacific/Auckland',
]
const localTz = (() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || '' } catch { return '' } })()
const tzOptions = computed(() => {
  const opts = [{ value: '', label: 'UTC（默认）' }]
  if (localTz && localTz !== 'UTC') opts.push({ value: localTz, label: `本地（${localTz}）` })
  for (const tz of COMMON_TZ) {
    if (tz === localTz) continue // 本地已在上面单列
    opts.push({ value: tz, label: tz })
  }
  return opts
})
const anyFixed = computed(() => dailyMode.value === 'fixed' || weeklyMode.value === 'fixed')
</script>

<template>
  <div class="aqr">
    <div class="grp-t">配额固定重置</div>
    <div class="frow"><span class="flabel">日配额重置</span>
      <div class="fctl">
        <a-select v-model:value="dailyMode" :options="modeOptions" style="width: 170px" />
        <template v-if="dailyMode === 'fixed'">
          <a-select v-model:value="dailyHour" :options="hourOptions" style="width: 110px" />
          <span class="hint">每天固定时刻清零日配额用量</span>
        </template>
        <span v-else class="hint">自首次用量起 24 小时滚动重置</span>
      </div>
    </div>
    <div class="frow"><span class="flabel">周配额重置</span>
      <div class="fctl">
        <a-select v-model:value="weeklyMode" :options="modeOptions" style="width: 170px" />
        <template v-if="weeklyMode === 'fixed'">
          <a-select v-model:value="weeklyDay" :options="dayOptions" style="width: 100px" />
          <a-select v-model:value="weeklyHour" :options="hourOptions" style="width: 110px" />
          <span class="hint">每周固定星期/时刻清零周配额用量</span>
        </template>
        <span v-else class="hint">自首次用量起 7 天滚动重置</span>
      </div>
    </div>
    <div v-if="anyFixed" class="frow"><span class="flabel">重置时区</span>
      <div class="fctl">
        <a-select v-model:value="timezone" :options="tzOptions" style="width: 240px" show-search option-filter-prop="label" />
        <span class="hint">固定时刻按此时区解释，缺省按 UTC</span>
      </div>
    </div>
    <div v-else class="frow"><span class="flabel"></span>
      <div class="fctl"><span class="hint">仅在设置了日/周配额上限的账号上，固定重置参与周期计算</span></div>
    </div>
  </div>
</template>

<style scoped>
.grp-t { font-size: 12px; font-weight: 600; color: var(--txt2); padding: 6px 8px 2px; }
.aqr { width: 100%; }
</style>
