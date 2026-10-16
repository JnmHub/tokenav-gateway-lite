<script setup>
/* 六个设置分区仍各自读取和保存；已访问子树保留草稿，退出会话由 Desktop 卸载。 */
import { ref, watch } from 'vue'
import { lazyApp } from '../core/lazyApp'
import { useWindowContext } from '../core/windowContext'
defineProps({ win: Object })
const TABS = [
  { key: 'general', label: '基础', comp: lazyApp(() => import('../components/settings/GeneralTab.vue')) },
  { key: 'features', label: '功能', comp: lazyApp(() => import('../components/settings/FeaturesTab.vue')) },
  { key: 'controlPlane', label: '主站', comp: lazyApp(() => import('../components/settings/ControlPlaneTab.vue')) },
  { key: 'security', label: '安全', comp: lazyApp(() => import('../components/settings/SecurityTab.vue')) },
  { key: 'gateway', label: '网关', comp: lazyApp(() => import('../components/settings/GatewayTab.vue')) },
  { key: 'policy', label: '策略', comp: lazyApp(() => import('../components/settings/PolicyTab.vue')) },
]
const activeTab = ref('gateway'), visited = ref(new Set(['gateway']))
const context = useWindowContext()
const onTabChange = (key) => { activeTab.value = key; visited.value = new Set([...visited.value, key]) }
watch(() => context?.target?.value, (target) => {
  if (TABS.some(tab => tab.key === target?.tab)) onTabChange(target.tab)
}, { immediate: true })
</script>
<template>
  <div class="settings w12-design">
    <a-tabs :active-key="activeTab" class="settings-tabs" @change="onTabChange">
      <a-tab-pane v-for="tab in TABS" :key="tab.key" :tab="tab.label">
        <component :is="tab.comp" v-if="visited.has(tab.key)" />
      </a-tab-pane>
    </a-tabs>
  </div>
</template>
<style scoped>
.settings { height: 100%; min-height: 0; display: flex; flex-direction: column; container: w12-settings / inline-size; }
.settings-tabs { flex: 1; min-height: 0; }
.settings-tabs :deep(.ant-tabs-nav) { flex: none; margin-bottom: 16px; }
.settings-tabs :deep(.ant-tabs-tab) { padding: 8px 12px; margin-left: 0; font-size: 13px; }
.settings-tabs :deep(.ant-tabs-content-holder) { overflow: auto; min-height: 0; }
.settings-tabs :deep(.ant-tabs-content) { min-height: 100%; }
.settings-tabs :deep(.ant-tabs-nav-wrap) { overflow-x: auto; }
@container w12-app (max-width: 600px) { .settings-tabs :deep(.ant-tabs-tab) { padding: 8px; } }
</style>
