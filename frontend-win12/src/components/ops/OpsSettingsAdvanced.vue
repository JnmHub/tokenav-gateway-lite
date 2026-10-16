<script setup>
/* 运维设置弹窗 - 高级设置块（数据保留/预聚合/OpenAI 自动暂停/忽略错误/自动刷新/面板显示）
 * 直接读写父级传入的 advancedSettings 响应式对象（与旧 OpsSettingsDialog 同一份数据） */
import { computed } from 'vue'
import { Switch, Select, InputNumber, Input, Checkbox } from 'ant-design-vue'

const props = defineProps({ advanced: { type: Object, required: true } })

const props_ = props // 模板直接用 advanced

/* OpenAI 自动暂停：后端存 0~1 分数，UI 按 0~100 百分比展示（0 = 不启用） */
const pct = (key) => computed({
  get: () => {
    const v = props.advanced.openai_account_quota_auto_pause?.[key]
    return v && v > 0 ? Math.round(v * 1000) / 10 : null
  },
  set: (val) => {
    if (!props.advanced.openai_account_quota_auto_pause) return
    props.advanced.openai_account_quota_auto_pause[key] = val != null && val > 0 ? val / 100 : 0
  },
})
const pause5h = pct('default_threshold_5h')
const pause7d = pct('default_threshold_7d')

const REFRESH_OPTS = [
  { value: 15, label: '15 秒' }, { value: 30, label: '30 秒' }, { value: 60, label: '60 秒' },
]

/* 5 个忽略错误 Toggle（字段名自旧 OpsAdvancedSettings 核实） */
const IGNORE_TOGGLES = [
  { field: 'ignore_count_tokens_errors', label: '忽略 count_tokens 错误', hint: '启用后，count_tokens 请求的错误将不会写入错误日志。' },
  { field: 'ignore_context_canceled', label: '忽略客户端断连错误', hint: '启用后，客户端主动断开连接（context canceled）的错误将不会写入错误日志。' },
  { field: 'ignore_no_available_accounts', label: '忽略无可用账号错误', hint: '启用后，"No available accounts" 错误将不会写入错误日志（不推荐，这通常是配置问题）。' },
  { field: 'ignore_invalid_api_key_errors', label: '忽略无效 API Key 错误', hint: '启用后，无效或缺失 API Key 的错误将不会写入错误日志。' },
  { field: 'ignore_insufficient_balance_errors', label: '忽略余额不足错误', hint: '启用后，账号余额不足（Insufficient balance）的错误将不会写入错误日志。' },
]
</script>

<template>
  <div class="adv">
    <!-- 数据保留策略 -->
    <div class="blk">
      <div class="blk-t">数据保留策略</div>
      <div class="row">
        <div class="row-main"><span class="lb">启用数据清理</span></div>
        <Switch v-model:checked="advanced.data_retention.cleanup_enabled" size="small" />
      </div>
      <div v-if="advanced.data_retention.cleanup_enabled" class="row">
        <span class="lb">清理计划（Cron）</span>
        <Input v-model:value="advanced.data_retention.cleanup_schedule" size="small" placeholder="0 2 * * *" style="width: 160px" />
      </div>
      <div class="row3">
        <label>错误日志保留天数<InputNumber v-model:value="advanced.data_retention.error_log_retention_days" :min="0" :max="365" size="small" style="width: 100%" /></label>
        <label>分钟指标保留天数<InputNumber v-model:value="advanced.data_retention.minute_metrics_retention_days" :min="0" :max="365" size="small" style="width: 100%" /></label>
        <label>小时指标保留天数<InputNumber v-model:value="advanced.data_retention.hourly_metrics_retention_days" :min="0" :max="365" size="small" style="width: 100%" /></label>
      </div>
      <div class="hint">建议保留 7-90 天；填 0 表示每次定时清理时清空所有历史</div>
    </div>

    <!-- 预聚合任务 -->
    <div class="blk">
      <div class="blk-t">预聚合任务</div>
      <div class="row">
        <div class="row-main">
          <span class="lb">启用预聚合任务</span>
          <span class="hint">预聚合可提升长时间窗口查询性能</span>
        </div>
        <Switch v-model:checked="advanced.aggregation.aggregation_enabled" size="small" />
      </div>
    </div>

    <!-- OpenAI 账号配额自动暂停 -->
    <div class="blk">
      <div class="blk-t">OpenAI 账号配额自动暂停</div>
      <div class="hint">当 OpenAI 账号 5h / 7d 用量达到阈值时，调度会自动跳过该账号；账号级阈值优先于全局默认值。</div>
      <div class="row2">
        <label>默认 5h 用量阈值 (%)<InputNumber v-model:value="pause5h" :min="0" :max="100" :step="0.1" size="small" style="width: 100%" /></label>
        <label>默认 7d 用量阈值 (%)<InputNumber v-model:value="pause7d" :min="0" :max="100" :step="0.1" size="small" style="width: 100%" /></label>
      </div>
      <div class="hint">取值 0-100，留空或 0 表示不启用全局默认阈值。</div>
    </div>

    <!-- 错误过滤：5 个忽略错误 Toggle -->
    <div class="blk">
      <div class="blk-t">错误过滤</div>
      <div v-for="t in IGNORE_TOGGLES" :key="t.field" class="row">
        <div class="row-main">
          <span class="lb">{{ t.label }}</span>
          <span class="hint">{{ t.hint }}</span>
        </div>
        <Switch v-model:checked="advanced[t.field]" size="small" />
      </div>
    </div>

    <!-- 自动刷新 -->
    <div class="blk">
      <div class="blk-t">自动刷新</div>
      <div class="row">
        <div class="row-main">
          <span class="lb">启用自动刷新</span>
          <span class="hint">自动刷新仪表板数据，启用后会定期拉取最新数据。</span>
        </div>
        <Switch v-model:checked="advanced.auto_refresh_enabled" size="small" />
      </div>
      <div v-if="advanced.auto_refresh_enabled" class="row">
        <span class="lb">刷新间隔</span>
        <Select v-model:value="advanced.auto_refresh_interval_seconds" :options="REFRESH_OPTS" size="small" style="width: 100px" />
      </div>
    </div>

    <!-- 仪表盘卡片显示：2 个 Toggle -->
    <div class="blk">
      <div class="blk-t">仪表盘卡片</div>
      <div class="row">
        <div class="row-main">
          <span class="lb">展示告警事件</span>
          <span class="hint">控制运维监控仪表盘中告警事件卡片是否显示，默认开启。</span>
        </div>
        <Checkbox v-model:checked="advanced.display_alert_events" />
      </div>
      <div class="row">
        <div class="row-main">
          <span class="lb">展示 OpenAI Token 请求统计</span>
          <span class="hint">控制运维监控仪表盘中 OpenAI Token 请求统计卡片是否显示，默认关闭。</span>
        </div>
        <Checkbox v-model:checked="advanced.display_openai_token_stats" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.adv { display: flex; flex-direction: column; gap: 14px; }
.blk { border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px; }
.blk-t { font-size: 12px; font-weight: 500; margin-bottom: 8px; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 5px 0; }
.row-main { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.lb { font-size: 12px; }
.hint { font-size: 11px; color: var(--text2); }
.row2, .row3 { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 10px; margin-top: 8px; }
.row2 label, .row3 label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text2); }
</style>
