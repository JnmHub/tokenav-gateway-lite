<script setup>
/* ============================================================
 * 主模型列「附加模型」小徽标 + hover 逐模型状态表
 * （等价旧 components/admin/monitor/MonitorPrimaryModelCell.vue）
 *
 * 数据源：GET /admin/channel-monitors 列表行内联 extra_models_status，
 * 字段 { model, status, latency_ms } —— 见
 * internal/handler/dto/channel_monitor.go（注意：list dto 没有 message，
 * message 只在 /run 与 /history 结果里，故「消息」列按需渲染，后端
 * 未来补字段时自动展示）。
 *
 * 徽标底色按附加模型最差状态降级：failed/error → 红，degraded → 橙，
 * 其余中性；配置了 extra_models 但还没检测过时展示「尚未检测」提示。
 * ============================================================ */
import { computed } from 'vue'
import { statusLabel, statusColor } from './constants'

const props = defineProps({
  row: { type: Object, required: true },
})

const extraStatus = computed(() => props.row?.extra_models_status || [])

const worstLevel = computed(() => {
  const list = extraStatus.value
  if (list.some((m) => m.status === 'failed' || m.status === 'error')) return 'bad'
  if (list.some((m) => m.status === 'degraded')) return 'warn'
  return 'ok'
})

const hasMessage = computed(() => extraStatus.value.some((m) => m.message))
</script>

<template>
  <a-popover placement="bottomLeft" trigger="hover">
    <template #content>
      <div class="xem">
        <div class="xem-head">附加模型（{{ extraStatus.length }}）</div>
        <div v-if="!extraStatus.length" class="xem-empty">已配置附加模型，尚未检测，暂无逐模型状态。</div>
        <table v-else class="xem-table">
          <thead>
            <tr>
              <th>模型</th>
              <th>状态</th>
              <th class="xem-r">延迟</th>
              <th v-if="hasMessage">消息</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(m, i) in extraStatus" :key="`${m.model}:${i}`">
              <td class="xem-model">{{ m.model }}</td>
              <td><a-tag :color="statusColor(m.status)" class="xem-tag">{{ statusLabel(m.status) }}</a-tag></td>
              <td class="xem-lat xem-r">{{ m.latency_ms != null ? `${m.latency_ms}ms` : '—' }}</td>
              <td v-if="hasMessage" class="xem-msg" :title="m.message || ''">{{ m.message || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
    <span class="xem-badge" :class="worstLevel" @click.stop>+{{ (row.extra_models || []).length }}</span>
  </a-popover>
</template>

<style scoped>
/* 徽标：18px 高小胶囊，低饱和半透明底（Win12 状态标签 4px 圆角） */
.xem-badge {
  display: inline-flex; align-items: center; height: 18px; padding: 0 6px;
  border-radius: 4px; font-size: 11px; line-height: 1; cursor: default;
  border: 1px solid transparent; flex-shrink: 0;
}
.xem-badge.ok { color: var(--text2); background: var(--hover-b); border-color: var(--hr); }
.xem-badge.warn { color: #9a6700; background: rgba(217, 119, 6, 0.14); border-color: rgba(217, 119, 6, 0.35); }
.xem-badge.bad { color: #c42b1c; background: rgba(196, 43, 28, 0.12); border-color: rgba(196, 43, 28, 0.35); }
.xem-badge:hover { filter: brightness(1.05); }

/* 弹层内容（popover 挂 body，但 slot 内容带本组件 scope id，scoped 样式可命中） */
.xem { min-width: 300px; }
.xem-head { font-size: 12.5px; font-weight: 500; margin-bottom: 8px; }
.xem-empty { font-size: 12px; color: var(--text2); }
.xem-table { width: 100%; border-collapse: collapse; font-size: 12px; }
.xem-table th {
  text-align: left; font-weight: 500; color: var(--text2); padding: 2px 10px 6px 0;
  border-bottom: 1px solid var(--hr); white-space: nowrap;
}
.xem-table td { padding: 5px 10px 5px 0; vertical-align: middle; }
.xem-table tbody tr + tr td { border-top: 1px solid color-mix(in srgb, var(--hr) 45%, transparent); }
.xem-model { font-family: Consolas, monospace; font-size: 12px; max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.xem-tag { margin: 0; }
.xem-lat { color: var(--text2); white-space: nowrap; }
.xem-r { text-align: right; }
.xem-msg { color: var(--text2); max-width: 160px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
