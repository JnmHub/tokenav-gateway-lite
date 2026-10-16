<script setup>
/* ============================================================
 * 用量明细行展开详情（Win12 规范：标签+值小网格、圆角 4/8px、
 * @ant-design/icons-vue 线性图标、禁 emoji、无厚重阴影）
 * 数据与文案构建见 ./usageDetailFormat.js（字段契约已对照后端核实），
 * 本组件只做纯渲染；主列保持 10 列不变，低频审计字段全部收进展开区。
 * ============================================================ */
import { computed } from 'vue'
import { buildDetailGroups } from './usageDetailFormat'

const props = defineProps({
  // GET /admin/usage 返回的单行 AdminUsageLog
  record: { type: Object, default: null },
})
const groups = computed(() => buildDetailGroups(props.record))
</script>

<template>
  <div class="udetail">
    <div v-for="g in groups" :key="g.title" class="dg">
      <div class="dg-title">{{ g.title }}</div>
      <div class="dg-grid">
        <div v-for="row in g.rows" :key="row.k" class="kv">
          <span class="k">{{ row.k }}</span>
          <span class="v" :class="[row.mono ? 'mono' : '', row.cls]">{{ row.v }}</span>
          <!-- 切组链高亮标记：group_rerouted=true 时在生效分组后追加「已切组」 -->
          <span v-if="row.badge" class="badge">{{ row.badge }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.udetail { display: flex; flex-direction: column; gap: 10px; padding: 4px 2px; }
.dg-title {
  display: flex; align-items: center; gap: 6px;
  font-size: 12px; font-weight: 500; color: var(--text); margin-bottom: 6px;
}
/* 组标题左侧主题色小竖条（与导航选中态同一视觉语义） */
.dg-title::before {
  content: ''; width: 3px; height: 12px; border-radius: 2px;
  background: color-mix(in srgb, var(--theme-2) 72%, transparent);
}
/* 标签+值小网格：自适应列宽，窄屏退化为单列 */
.dg-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 2px 20px; }
.kv { display: flex; align-items: baseline; gap: 8px; min-width: 0; font-size: 12px; line-height: 22px; }
.kv .k { flex: none; color: var(--text2); }
.kv .v { min-width: 0; word-break: break-all; color: var(--text); }
.mono { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 11.5px; }
/* 「已切组」状态标记：20px 高、圆角 4px、低饱和半透明底（Win12 状态标签规范） */
.badge {
  flex: none; align-self: center; font-size: 10px; line-height: 18px;
  padding: 0 6px; border-radius: 4px;
  background: rgba(217, 119, 6, 0.14); color: #d18a2d;
}
.hl { color: #2f9e63; }
.warn { color: #d18a2d; }
.bad { color: #dc2626; }
@media (max-width: 720px) {
  .dg-grid { grid-template-columns: 1fr; }
}
</style>
