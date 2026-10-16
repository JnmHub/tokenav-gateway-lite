<script setup>
/* 分组模型映射编辑器（二期 A）
 * 契约来源：旧 components/admin/group/GroupModelMappingFields.vue +
 *   views/admin/groupsModelMapping.ts + i18n zh.ts modelMapping 段
 *  - 单目标行：源 → 目标（model_mapping）
 *  - 智能分流行：源 + 2~8 个带权目标（weighted_model_mapping，仅 openai）
 *  - 权重 1~100 整数，±10 步进，旁显百分比（按权重占比）
 *  - 分流目标健康点（仅 openai，编辑态加载 /model-route-health），
 *    cooling 目标可手动重置（DELETE /admin/groups/:id/model-route-health）
 */
import { computed } from 'vue'

const props = defineProps({
  rows: { type: Array, default: () => [] },        // GroupModelMappingRow[]
  candidates: { type: Array, default: () => [] },  // 模型候选（models-list-candidates + 映射源）
  loading: { type: Boolean, default: false },
  platform: { type: String, default: '' },
  groupId: { type: Number, default: null },        // 编辑态传入，用于健康徽标与重置
  health: { type: Array, default: () => [] },      // GroupModelRouteHealthView[]
})
const emit = defineEmits(['update:rows', 'reset-health'])

const splitAvailable = computed(() => (props.platform || '').trim().toLowerCase() === 'openai')
const modelOptions = computed(() =>
  [...new Set(props.candidates.map((m) => m.trim()).filter(Boolean))].map((m) => ({ value: m })))

// —— 健康徽标：public/target 小写配对 ——
const healthKey = (p, t) => `${p.trim().toLowerCase()}\u0000${t.trim().toLowerCase()}`
const targetHealth = (p, t) => {
  if (!props.health?.length) return undefined
  const key = healthKey(p, t)
  return props.health.find((v) => healthKey(v.public_model, v.target_model) === key)
}
const healthDotClass = (p, t) => {
  switch (targetHealth(p, t)?.state) {
    case 'cooling': return 'dot bad'
    case 'recovering':
    case 'failing': return 'dot warn'
    default: return 'dot ok'
  }
}
const healthTitle = (p, t) => {
  const v = targetHealth(p, t)
  if (!v) return '正常'
  const seconds = Math.ceil((v.cooldown_remaining_ms ?? 0) / 1000)
  switch (v.state) {
    case 'cooling': return `冷却中，约 ${seconds} 秒后自动恢复`
    case 'recovering': return '恢复中（试探放行）'
    case 'failing': return `连续失败 ${v.consecutive_failures} 次`
    default: return '正常'
  }
}
const coolingCount = (row) =>
  (row.targets ?? []).filter((t) => targetHealth(row.from, t.model)?.state === 'cooling').length

// —— 行操作（不可变更新，与旧组件一致） ——
const writeRows = (rows) => emit('update:rows', rows)
const addRow = () => writeRows([...props.rows, { from: '', to: '' }])
const removeRow = (index) => writeRows(props.rows.filter((_, i) => i !== index))
const updateRow = (index, field, value) => {
  writeRows(props.rows.map((r, i) => (i === index ? { ...r, [field]: typeof value === 'string' ? value : '' } : r)))
}
const convertToSplit = (index) => {
  writeRows(props.rows.map((r, i) => {
    if (i !== index) return r
    // 对照旧组件：已有目标按 70/30 起步，否则 50/50
    const first = r.to.trim()
    const targets = first
      ? [{ model: first, weight: 70 }, { model: '', weight: 30 }]
      : [{ model: '', weight: 50 }, { model: '', weight: 50 }]
    return { from: r.from, to: '', split: true, targets }
  }))
}
const convertToSingle = (index) => {
  writeRows(props.rows.map((r, i) => (i === index ? { from: r.from, to: (r.targets?.[0]?.model ?? '').trim() } : r)))
}
const mapTargets = (rowIndex, fn) => {
  writeRows(props.rows.map((r, i) => (i === rowIndex && r.targets ? { ...r, targets: r.targets.map(fn) } : r)))
}
const updateTarget = (rowIndex, targetIndex, value) => {
  mapTargets(rowIndex, (t, pos) => (pos === targetIndex ? { ...t, model: typeof value === 'string' ? value : '' } : t))
}
const clampWeight = (w) => Math.min(100, Math.max(1, w))
const adjustWeight = (rowIndex, targetIndex, delta) => {
  mapTargets(rowIndex, (t, pos) => (pos === targetIndex ? { ...t, weight: clampWeight(t.weight + delta) } : t))
}
const setWeight = (rowIndex, targetIndex, event) => {
  const parsed = Number.parseInt(event.target.value, 10)
  const clamped = Number.isNaN(parsed) ? 1 : clampWeight(parsed)
  mapTargets(rowIndex, (t, pos) => (pos === targetIndex ? { ...t, weight: clamped } : t))
}
const addTarget = (rowIndex) => {
  writeRows(props.rows.map((r, i) => (i === rowIndex ? { ...r, targets: [...(r.targets ?? []), { model: '', weight: 10 }] } : r)))
}
const removeTarget = (rowIndex, targetIndex) => {
  writeRows(props.rows.map((r, i) => (i === rowIndex && r.targets
    ? { ...r, targets: r.targets.filter((_, pos) => pos !== targetIndex) }
    : r)))
}

// 目标权重百分比（权重和 <= 0 时显示 0%）
const targetPercent = (row, targetIndex) => {
  const targets = row.targets ?? []
  const total = targets.reduce((sum, t) => sum + (t.weight > 0 ? t.weight : 0), 0)
  if (total <= 0) return 0
  return Math.round((targets[targetIndex].weight / total) * 100)
}

// 生效预览：单目标 → from→to；分流行按权重占比展开
const previewRows = computed(() => {
  const out = []
  for (const row of props.rows) {
    const from = row.from.trim()
    if (!from) continue
    if (row.split) {
      const total = (row.targets ?? []).reduce((s, t) => s + (t.weight > 0 ? t.weight : 0), 0)
      if (total <= 0) continue
      for (const t of row.targets ?? []) {
        if (!t.model.trim()) continue
        out.push({ from, to: `${t.model.trim()} ${Math.round((t.weight / total) * 100)}%` })
      }
    } else if (row.to.trim()) {
      out.push({ from, to: row.to.trim() })
    }
  }
  return out
})
</script>

<template>
  <div class="mm">
    <div class="mm-head">
      <div>
        <div class="mm-t">分组模型映射</div>
        <div class="mm-d">客户请求源模型时，先改写为目标模型，再选择支持目标模型的账号；计费与日志按最终模型链记录。</div>
      </div>
      <a-button size="small" @click="addRow">+ 添加映射</a-button>
    </div>

    <div v-if="rows.length === 0" class="mm-empty">未配置映射时，请求模型保持不变。</div>

    <div v-else class="mm-rows">
      <template v-for="(row, index) in rows" :key="index">
        <!-- 单目标行：源 -> 目标 -->
        <div v-if="!row.split" class="mrow">
          <a-auto-complete
            :value="row.from" :options="modelOptions" style="width: 100%"
            placeholder="客户请求模型" option-filter-prop="value" @change="(v) => updateRow(index, 'from', v)"
          />
          <span class="arrow">→</span>
          <a-auto-complete
            :value="row.to" :options="modelOptions" style="width: 100%"
            placeholder="实际调用模型" option-filter-prop="value" @change="(v) => updateRow(index, 'to', v)"
          />
          <button v-if="splitAvailable" type="button" class="mini-btn" title="转为智能分流（按权重分配多个目标）" @click="convertToSplit(index)">分流</button>
          <a-button size="small" type="text" danger @click="removeRow(index)">删除</a-button>
        </div>

        <!-- 智能分流行：源 + 带权重目标列表 -->
        <div v-else class="srow">
          <div class="srow-head">
            <a-auto-complete
              :value="row.from" :options="modelOptions" style="width: 100%"
              placeholder="客户请求模型" option-filter-prop="value" @change="(v) => updateRow(index, 'from', v)"
            />
            <button type="button" class="mini-btn" title="改回单目标" @click="convertToSingle(index)">单目标</button>
            <a-button size="small" type="text" danger @click="removeRow(index)">删除</a-button>
          </div>
          <div v-for="(target, ti) in row.targets ?? []" :key="ti" class="trow">
            <span :class="healthDotClass(row.from, target.model)" :title="healthTitle(row.from, target.model)" />
            <a-auto-complete
              :value="target.model" :options="modelOptions" style="width: 100%"
              placeholder="实际调用模型" option-filter-prop="value" @change="(v) => updateTarget(index, ti, v)"
            />
            <div class="weight">
              <button type="button" class="wbtn" title="调低权重" @click="adjustWeight(index, ti, -10)">−</button>
              <input :value="target.weight" type="number" min="1" max="100" @change="setWeight(index, ti, $event)">
              <button type="button" class="wbtn" title="调高权重" @click="adjustWeight(index, ti, 10)">＋</button>
              <span class="pct">{{ targetPercent(row, ti) }}%</span>
            </div>
            <div class="trow-ops">
              <button
                v-if="targetHealth(row.from, target.model)?.state === 'cooling' && groupId"
                type="button" class="mini-btn warn" title="重置健康状态，立即恢复该目标"
                @click="emit('reset-health', row.from.trim(), target.model.trim())"
              >重置</button>
              <a-button size="small" type="text" danger @click="removeTarget(index, ti)">删除</a-button>
            </div>
          </div>
          <div class="srow-foot">
            <a-button size="small" :disabled="(row.targets?.length ?? 0) >= 8" @click="addTarget(index)">+ 添加目标</a-button>
            <span v-if="coolingCount(row) > 0" class="cool-tip">{{ coolingCount(row) }} 个目标冷却中，请求将暂时走其余目标</span>
          </div>
        </div>
      </template>
    </div>

    <p v-if="loading" class="mm-hint">正在读取可用模型候选...</p>

    <div v-if="previewRows.length > 0" class="mm-preview">
      <div class="mm-hint" style="margin: 0 0 6px">生效预览</div>
      <div class="chips">
        <code v-for="(r, i) in previewRows" :key="i" class="chip">{{ r.from }} → {{ r.to }}</code>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mm { display: grid; gap: 8px; }
.mm-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.mm-t { font-size: 13px; }
.mm-d { font-size: 12px; color: var(--text2); margin-top: 2px; line-height: 1.6; }
.mm-empty { border: 1px dashed var(--hr); border-radius: 8px; padding: 14px; text-align: center; font-size: 12px; color: var(--text2); }
.mm-rows { display: grid; gap: 8px; }
.mrow { display: grid; grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr) auto auto; gap: 8px; align-items: center; padding: 8px; border: 1px solid var(--hr); border-radius: 8px; background: var(--card); }
.arrow { color: var(--text2); }
.mini-btn { border: none; background: transparent; color: var(--text2); font-size: 12px; cursor: pointer; padding: 4px 8px; border-radius: 6px; white-space: nowrap; }
.mini-btn:hover { background: var(--bg70); color: var(--text); }
.mini-btn.warn { color: #b7791f; }
.srow { border: 1px solid color-mix(in srgb, var(--accent) 35%, transparent); background: color-mix(in srgb, var(--accent) 6%, transparent); border-radius: 8px; padding: 10px; display: grid; gap: 8px; }
.srow-head { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; gap: 8px; align-items: center; }
.trow { display: grid; grid-template-columns: auto minmax(0, 1fr) auto auto; gap: 8px; align-items: center; padding: 6px 8px; border: 1px solid var(--hr); border-radius: 8px; background: var(--card); }
.dot { width: 10px; height: 10px; border-radius: 50%; display: inline-block; }
.dot.ok { background: #2f9e63; }
.dot.warn { background: #e8a33d; }
.dot.bad { background: #d13438; }
.weight { display: flex; align-items: center; gap: 2px; }
.weight input { width: 52px; height: 28px; text-align: center; border: 1px solid var(--stroke-ctl); border-radius: var(--r-ctl); background: var(--surface-card); color: inherit; font-size: 12px; }
.weight input:hover { border-color: var(--accent); }
.weight input:focus { outline: none; border-color: var(--accent); }
.wbtn { width: 24px; height: 24px; border: none; border-radius: 6px; background: transparent; color: var(--text2); cursor: pointer; font-size: 13px; line-height: 1; }
.wbtn:hover { background: var(--bg70); color: var(--text); }
.pct { width: 42px; text-align: right; font-size: 12px; color: var(--text2); }
.trow-ops { display: flex; align-items: center; gap: 2px; }
.srow-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.cool-tip { font-size: 12px; color: #b7791f; }
.mm-hint { font-size: 12px; color: var(--text2); }
.mm-preview { border-radius: 8px; background: var(--card); padding: 10px; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { font-size: 11.5px; border: 1px solid var(--hr); border-radius: 6px; padding: 2px 8px; background: var(--bg70); }
/* 所属弹窗宽度决定换行；桌面350px窄窗不能依赖整个视口的手机媒体查询。 */
@container w12-dialog (max-width: 600px) {
  .mm-head, .srow-foot { flex-wrap: wrap; }
  .mrow { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .mrow > .ant-select { grid-column: 1 / -1; min-width: 0; }
  .arrow { display: none; }
  .srow-head { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .srow-head > .ant-select { grid-column: 1 / -1; min-width: 0; }
  .trow { grid-template-columns: 10px minmax(0, 1fr); }
  .trow > .ant-select { min-width: 0; }
  .weight { grid-column: 1 / -1; flex-wrap: wrap; justify-content: flex-end; gap: 4px; }
  .trow-ops { grid-column: 1 / -1; justify-content: flex-end; }
  .wbtn { width: 32px; height: 32px; }
  .mini-btn { min-height: 32px; }
  .weight input { height: 32px; }
  .chip { overflow-wrap: anywhere; min-width: 0; }
}
@media (max-width: 640px) {
  .mm-head, .srow-foot { flex-wrap: wrap; }
}
</style>
