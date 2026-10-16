<script setup>
import { MoreOutlined, CopyOutlined, DeleteOutlined, DollarOutlined, ThunderboltOutlined, UserAddOutlined, ApartmentOutlined, BarChartOutlined, FormOutlined } from '@ant-design/icons-vue'
import W12Button from '../design/W12Button.vue'
defineProps({ group: Object, duplicating: Boolean })
const emit = defineEmits(['edit', 'more'])
</script>
<template><div class="group-row-actions">
  <W12Button variant="text" compact :aria-label="`编辑${group.name}`" title="编辑" @click="emit('edit', group)"><FormOutlined /></W12Button>
  <a-dropdown :trigger="['click']"><W12Button variant="text" compact :aria-label="`${group.name}更多操作`" title="更多操作"><MoreOutlined /></W12Button><template #overlay>
    <a-menu @click="({ key }) => emit('more', { key }, group)">
      <a-menu-item key="rates"><DollarOutlined />用户倍率</a-menu-item><a-menu-item key="rpm"><ThunderboltOutlined />RPM（每分钟请求）覆写</a-menu-item>
      <a-menu-item key="bind"><UserAddOutlined />绑定账号</a-menu-item><a-menu-item key="ins"><BarChartOutlined />生效模型 / 接口密钥列表</a-menu-item>
      <a-menu-item v-if="group.platform === 'composite'" key="routes"><ApartmentOutlined />聚合路由</a-menu-item><a-menu-item key="dup" :disabled="duplicating"><CopyOutlined />复制分组</a-menu-item>
      <a-menu-divider /><a-menu-item key="del" danger><DeleteOutlined />删除</a-menu-item>
    </a-menu>
  </template></a-dropdown>
</div></template>
<style scoped>.group-row-actions { display: flex; gap: 4px; align-items: center; justify-content: flex-end; }</style>
