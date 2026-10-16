<script setup>
/* 仪表盘补充面板容器
 * 把「后端已有 / 旧仪表盘无入口」的 8 个接口集中挂载，避免 Dashboard.vue 超过 600 行上限：
 *   GET  /admin/dashboard/stats                → StatsOverviewPanel（概览指标卡）
 *   GET  /admin/dashboard/realtime             → RealtimePanel（15s 轮询，弹窗打开自动暂停）
 *   GET  /admin/dashboard/api-keys-trend       → ApiKeyTrendPanel（Key 维度趋势表）
 *   POST /admin/dashboard/api-keys-usage       → ApiKeyTrendPanel（勾选行 → 批量 Key 用量）
 *   GET  /admin/dashboard/users-trend          → UserTrendPanel（用户维度趋势表）
 *   POST /admin/dashboard/users-usage          → UserTrendPanel（勾选行 → 批量用户用量）
 *   GET  /admin/dashboard/user-breakdown       → UserBreakdownPanel（用户维度明细表）
 *   POST /admin/dashboard/aggregation/backfill → BackfillPanel（回填聚合，二次确认）
 * 日期范围 / 粒度由 Dashboard.vue 透传，改动日期即各面板各自重载（面板内也有独立刷新按钮）。 */
import StatsOverviewPanel from './StatsOverviewPanel.vue'
import RealtimePanel from './RealtimePanel.vue'
import ApiKeyTrendPanel from './ApiKeyTrendPanel.vue'
import UserTrendPanel from './UserTrendPanel.vue'
import UserBreakdownPanel from './UserBreakdownPanel.vue'
import BackfillPanel from './BackfillPanel.vue'

defineProps({
  startDate: { type: String, default: '' },
  endDate: { type: String, default: '' },
  granularity: { type: String, default: 'day' },
})
</script>

<template>
  <div class="extra">
    <StatsOverviewPanel />
    <RealtimePanel />
    <ApiKeyTrendPanel :start-date="startDate" :end-date="endDate" :granularity="granularity" />
    <UserTrendPanel :start-date="startDate" :end-date="endDate" :granularity="granularity" />
    <UserBreakdownPanel :start-date="startDate" :end-date="endDate" />
    <BackfillPanel :start-date="startDate" :end-date="endDate" />
  </div>
</template>

<style scoped>
.extra { display: flex; flex-direction: column; gap: 12px; }
</style>
