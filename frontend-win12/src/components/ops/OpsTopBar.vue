<script setup>
/* 运维中心顶栏：时间范围(5m~24h/自定义起止弹窗) + 平台/分组筛选 + 自动刷新(开关/15-30-60s/倒计时) + 手动刷新
 * 契约来源：旧 frontend/src/views/admin/ops/components/OpsDashboardHeader.vue
 * - 时间范围枚举：'5m'|'30m'|'1h'|'6h'|'24h'|'custom'（custom 需另传 start_time/end_time）
 * - 平台目录：CONCRETE_PLATFORM_OPTIONS（8 平台 + 全部）
 * - 分组数据源：GET /admin/groups/all → [{ id, name, platform }]，按平台过滤 */
import { ref, computed, onMounted } from 'vue'
import dayjs from 'dayjs'
import { message } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const props = defineProps({
  timeRange: { type: String, default: '1h' },
  customStart: { type: String, default: '' },
  customEnd: { type: String, default: '' },
  platform: { type: String, default: '' },
  groupId: { type: Number, default: null },
  autoRefresh: { type: Boolean, default: false },
  refreshInterval: { type: Number, default: 30 },
  countdown: { type: Number, default: 0 },
  loading: { type: Boolean, default: false },
})
const emit = defineEmits([
  'update:timeRange', 'update:platform', 'update:groupId',
  'update:autoRefresh', 'update:refreshInterval', 'refresh',
])

/* 平台目录：与旧 constants/platforms.ts CONCRETE_PLATFORM_OPTIONS 一致 */
const PLATFORMS = [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' },
  { value: 'grok', label: 'Grok' },
  { value: 'kimi', label: 'Kimi' },
  { value: 'zhipu', label: '智谱 GLM' },
  { value: 'deepseek', label: 'DeepSeek' },
]

const timeRangeOpts = [
  { value: '5m', label: '近 5 分钟' },
  { value: '30m', label: '近 30 分钟' },
  { value: '1h', label: '近 1 小时' },
  { value: '6h', label: '近 6 小时' },
  { value: '24h', label: '近 24 小时' },
  { value: 'custom', label: '自定义…' },
]

const onTimeRange = (v) => {
  if (v === 'custom') { openCustom() }
  else emit('update:timeRange', v)
}

/* 自定义起止弹窗：默认回填最近 1 小时，确认后以 ISO 串上抛 */
const customOpen = ref(false)
const customRange = ref([])
const openCustom = () => {
  const end = dayjs()
  // value-format 为字符串形态，绑定值保持与之一致
  customRange.value = [end.subtract(1, 'hour').format('YYYY-MM-DD HH:mm:ss'), end.format('YYYY-MM-DD HH:mm:ss')]
  customOpen.value = true
}
const customLabel = computed(() => {
  if (props.timeRange !== 'custom' || !props.customStart || !props.customEnd) return '自定义…'
  const f = (t) => String(t).replace('T', ' ').slice(5, 16)
  return `自定义 ${f(props.customStart)} ~ ${f(props.customEnd)}`
})
const customOpts = computed(() => timeRangeOpts.map((o) =>
  o.value === 'custom' ? { ...o, label: customLabel.value } : o))
const confirmCustom = () => {
  const [s, e] = customRange.value || []
  if (!s || !e) return
  if (dayjs(e).isBefore(dayjs(s))) { message.warning('结束时间不能早于开始时间'); return }
  customOpen.value = false
  emit('update:timeRange', { start: dayjs(s).toISOString(), end: dayjs(e).toISOString() })
}

/* 分组下拉：GET /admin/groups/all，按所选平台过滤（旧 Header 同逻辑） */
const groups = ref([])
onMounted(async () => {
  try {
    const list = await request('/admin/groups/all', { method: 'GET' })
    groups.value = (list || []).map((g) => ({ id: g.id, name: g.name, platform: g.platform }))
  } catch { groups.value = [] }
})
const groupOpts = computed(() => {
  const filtered = props.platform ? groups.value.filter((g) => g.platform === props.platform) : groups.value
  return [{ value: null, label: '全部分组' }, ...filtered.map((g) => ({ value: g.id, label: g.name }))]
})
const onPlatform = (v) => {
  emit('update:platform', v || '')
  // 平台切换后当前分组可能不属于新平台，与旧 Header 一致自动清空
  if (props.groupId != null) {
    const g = groups.value.find((x) => x.id === props.groupId)
    if (!v || (g && g.platform !== v)) emit('update:groupId', null)
  }
}

const INTERVALS = [
  { value: 15, label: '15s' },
  { value: 30, label: '30s' },
  { value: 60, label: '60s' },
]
</script>

<template>
  <div class="topbar">
    <a-select
      :value="timeRange === 'custom' ? 'custom' : timeRange" :options="customOpts" size="small"
      style="width: 190px" @change="onTimeRange"
    />
    <a-select
      :value="platform || ''" size="small" style="width: 130px"
      :options="[{ value: '', label: '全部平台' }, ...PLATFORMS]" @change="onPlatform"
    />
    <a-select
      :value="groupId" :options="groupOpts" size="small" style="width: 150px"
      :placeholder="'全部分组'" @change="(v) => emit('update:groupId', v)"
    />
    <div class="sp"></div>
    <span class="auto-label">自动刷新</span>
    <a-switch
      :checked="autoRefresh" size="small"
      @change="(v) => emit('update:autoRefresh', !!v)"
    />
    <a-segmented
      :value="refreshInterval" :options="INTERVALS" size="small"
      :disabled="!autoRefresh" @change="(v) => emit('update:refreshInterval', v)"
    />
    <span v-if="autoRefresh && countdown > 0" class="countdown">{{ countdown }}s</span>
    <a-button size="small" :loading="loading" @click="emit('refresh')">
      <template #icon><ReloadOutlined /></template>
    </a-button>

    <a-modal v-model:open="customOpen" title="自定义时间范围" ok-text="确定" cancel-text="取消" @ok="confirmCustom">
      <div style="display: flex; align-items: center; gap: 8px; padding: 8px 0">
        <span style="color: var(--text2); font-size: 13px">起止时间</span>
        <a-range-picker
          v-model:value="customRange" show-time format="YYYY-MM-DD HH:mm"
          value-format="YYYY-MM-DD HH:mm:ss" style="flex: 1"
        />
      </div>
    </a-modal>
  </div>
</template>

<style scoped>
.topbar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.sp { flex: 1; }
.auto-label { font-size: 12px; color: var(--text2); }
.countdown { min-width: 28px; text-align: center; font-size: 12px; color: var(--text2); font-variant-numeric: tabular-nums; }
</style>
