<script setup>
import { computed } from 'vue'
import {
  DashboardOutlined, SettingOutlined, TeamOutlined, SwapOutlined, ApartmentOutlined,
  DatabaseOutlined, BarChartOutlined, TagsOutlined, KeyOutlined, SafetyOutlined,
  MonitorOutlined, AuditOutlined, ControlOutlined, FileTextOutlined, PictureOutlined,
  ToolOutlined, SlidersOutlined, ClusterOutlined, AppstoreAddOutlined, AppstoreOutlined,
} from '@ant-design/icons-vue'
const props = defineProps({ app: { type: Object, required: true }, size: { type: Number, default: 32 } })
// 系统功能图标统一 SVG（矢量图）库；品牌图形仍由业务平台字段独立负责。
const icons = { dashboard: DashboardOutlined, settings: SettingOutlined, accounts: TeamOutlined,
  proxies: SwapOutlined, groups: ApartmentOutlined, channels: DatabaseOutlined, usage: BarChartOutlined,
  modelPrices: TagsOutlined, localKeys: KeyOutlined, riskControl: SafetyOutlined, channelMonitor: MonitorOutlined,
  auditLogs: AuditOutlined, opsCenter: ControlOutlined, runtimeSummary: FileTextOutlined,
  imageTasks: PictureOutlined, setupWizard: ToolOutlined, systemSettings: SlidersOutlined,
  controlPlane: ClusterOutlined, plugins: AppstoreAddOutlined }
const icon = computed(() => icons[props.app?.glyph || props.app?.id] || AppstoreOutlined)
</script>
<template><component :is="icon" class="w12-app-icon" :style="{ fontSize: size + 'px' }" aria-hidden="true" /></template>
<style scoped>.w12-app-icon { color: var(--dicon-icon); display: inline-flex; flex: none; }</style>
