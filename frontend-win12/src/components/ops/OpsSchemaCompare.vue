<script setup>
/* 结构对照 Tab（三期 Schema 分析器）：4 区域 chip + 仅看差异 + 差异四列（+/路径/左/右）
 * 契约来源：旧 RequestSchemaAnalyzerView.vue 结构对照区块（GitHub diff 风格逻辑平移）：
 * - fieldMap：headers 区域 = 头名称集合；schema 区域 = path → "type · length"
 * - diff 状态：added（右侧新增）/ removed（左侧删除）/ changed（类型变化）/ same
 * - 树状排序：a.b[0].c 拆段，父字段在前、数组下标按数值排且排在命名段后（旧实现同算法）
 * - 对照结果按（左、右记录）缓存，避免 4 区域重复并集+排序 */
import { computed, ref } from 'vue'

const props = defineProps({
  left: { type: Object, default: null },
  right: { type: Object, default: null },
})

const ALL_PARTS = ['request_headers', 'request_schema', 'response_headers', 'response_schema']
const PART_TITLE = {
  request_headers: '请求头',
  request_schema: '请求内容字段',
  response_headers: '响应头',
  response_schema: '响应内容字段',
}
const compareParts = ref([...ALL_PARTS])
const onlyDiff = ref(true)

const fieldMap = (record, part) => {
  if (!record) return new Map()
  if (part.endsWith('_headers')) {
    return new Map((record[part] || []).map((name) => [name, 'header']))
  }
  const schema = record[part === 'request_schema' ? 'request_schema' : 'response_schema']
  return new Map((schema?.fields || []).map((f) => [f.path, `${f.type}${f.length == null ? '' : ` · ${f.length}`}`]))
}

const fieldDepth = (path) => Math.min(8, (String(path).match(/[.[]/g) || []).length)

function pathSegments(path) {
  return String(path).replace(/\[(\d+)\]/g, '.$1').split('.').filter(Boolean)
    .map((seg) => (/^\d+$/.test(seg) ? Number(seg) : seg))
}
function compareSegments(a, b) {
  const len = Math.min(a.length, b.length)
  for (let i = 0; i < len; i++) {
    const av = a[i]; const bv = b[i]
    if (av === bv) continue
    const aNum = typeof av === 'number'; const bNum = typeof bv === 'number'
    if (aNum && bNum) return av - bv
    if (aNum !== bNum) return aNum ? 1 : -1
    return String(av).localeCompare(String(bv))
  }
  return a.length - b.length
}
function rows(part) {
  const l = fieldMap(props.left, part)
  const r = fieldMap(props.right, part)
  const out = [...new Set([...l.keys(), ...r.keys()])].map((path) => ({
    path,
    depth: fieldDepth(path),
    left: l.get(path) || '',
    right: r.get(path) || '',
    state: !l.has(path) ? 'added' : !r.has(path) ? 'removed' : l.get(path) !== r.get(path) ? 'changed' : 'same',
  }))
  out.sort((x, y) => compareSegments(pathSegments(x.path), pathSegments(y.path)))
  return out
}

/* 按（左、右记录）整体缓存一次，模板多次调用不再重复计算 */
const cache = computed(() => {
  const c = {}
  if (!props.left || !props.right) return c
  for (const p of ALL_PARTS) c[p] = rows(p)
  return c
})
const partRows = (p) => cache.value[p] || []
const partStats = (p) => {
  const rs = partRows(p)
  return {
    added: rs.filter((r) => r.state === 'added').length,
    removed: rs.filter((r) => r.state === 'removed').length,
    changed: rs.filter((r) => r.state === 'changed').length,
    same: rs.filter((r) => r.state === 'same').length,
  }
}
const togglePart = (p) => {
  compareParts.value = compareParts.value.includes(p)
    ? compareParts.value.filter((x) => x !== p)
    : [...compareParts.value, p]
}
/* 路径拆分显示：弱化前缀 + 强调末段（hover title 给完整路径） */
const splitPath = (path) => {
  const segs = String(path).replace(/\[(\d+)\]/g, '.$1').split('.').filter(Boolean)
  if (segs.length <= 1) return { prefix: '', last: path }
  return { prefix: segs.slice(0, -1).join('.') + '.', last: segs[segs.length - 1] }
}
const shortPath = (p) => (p && p.length > 34 ? `${p.slice(0, 34)}…` : p || '-')
const visibleRows = (p) => (onlyDiff.value ? partRows(p).filter((r) => r.state !== 'same') : partRows(p))
const GUT = { added: '+', removed: '−', changed: '~', same: '' }
</script>

<template>
  <div class="cmp">
    <template v-if="left && right">
      <!-- 4 区域 chip（+n/-n/~n 摘要）+ 仅看差异 -->
      <div class="bar">
        <button
          v-for="p in ALL_PARTS" :key="p" class="pch" :class="{ on: compareParts.includes(p) }"
          @click="togglePart(p)"
        >
          {{ PART_TITLE[p] }}
          <span v-if="compareParts.includes(p)" class="pst">
            <template v-if="partStats(p).added">+{{ partStats(p).added }}</template>
            <template v-if="partStats(p).removed"> -{{ partStats(p).removed }}</template>
            <template v-if="partStats(p).changed"> ~{{ partStats(p).changed }}</template>
            <template v-if="!partStats(p).added && !partStats(p).removed && !partStats(p).changed">一致</template>
          </span>
        </button>
        <label class="only"><a-checkbox v-model:checked="onlyDiff" />仅看差异</label>
      </div>

      <div v-for="p in compareParts" :key="p" class="diff">
        <div class="diff-h">
          <span class="diff-t">{{ PART_TITLE[p] }}</span>
          <span class="side"><i class="dot" />{{ shortPath(left.path) }}</span>
          <span class="side"><i class="dot r" />{{ shortPath(right.path) }}</span>
        </div>
        <div class="grid head">
          <span />
          <span>字段路径</span>
          <span>左侧（旧）</span>
          <span>右侧（新）</span>
        </div>
        <template v-if="partRows(p).length">
          <div v-if="onlyDiff && partStats(p).same" class="same-tip" @click="onlyDiff = false">
            … {{ partStats(p).same }} 个字段完全一致（点击展开）
          </div>
          <div v-for="row in visibleRows(p)" :key="`${p}-${row.path}`" class="grid row" :class="row.state">
            <span class="gut">{{ GUT[row.state] }}</span>
            <code class="path" :style="{ paddingLeft: `${4 + row.depth * 12}px` }" :title="row.path">
              <span v-if="splitPath(row.path).prefix" class="prefix">{{ splitPath(row.path).prefix }}</span>{{ splitPath(row.path).last }}
            </code>
            <span class="cell old">{{ row.left || '—' }}</span>
            <span class="cell new">{{ row.right || '—' }}</span>
          </div>
        </template>
        <div v-else class="none">两次请求均没有可展示的结构</div>
      </div>

      <div class="legend">
        <span><i class="lg" style="background: #16a34a" />右侧新增</span>
        <span><i class="lg" style="background: #dc2626" />左侧删除</span>
        <span><i class="lg" style="background: #d97706" />类型变化</span>
      </div>
    </template>
    <div v-else class="none" style="padding: 40px 0; text-align: center">
      在左侧记录列表勾选两条请求即可对照<template v-if="!left && !right">（尚未勾选）</template>
    </div>
  </div>
</template>

<style scoped>
.cmp { padding: 12px 14px; }
.bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.pch { display: inline-flex; align-items: center; gap: 5px; border: 1px solid var(--hr); border-radius: 6px; background: rgba(127, 127, 127, 0.05); padding: 4px 10px; font-size: 11px; color: var(--text2); cursor: pointer; transition: all 0.15s ease; }
.pch:hover { color: inherit; }
.pch.on { border-color: rgba(59, 130, 246, 0.5); background: rgba(59, 130, 246, 0.1); color: #3b82f6; }
.pst { font-family: Consolas, monospace; font-size: 10px; opacity: 0.9; }
.only { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; font-size: 11px; color: var(--text2); cursor: pointer; }
.diff { border: 1px solid var(--hr); border-radius: 8px; overflow: hidden; margin-bottom: 10px; }
.diff-h { display: flex; align-items: center; gap: 12px; border-bottom: 1px solid var(--hr); background: rgba(127, 127, 127, 0.05); padding: 7px 12px; font-size: 12px; }
.diff-t { font-weight: 500; }
.side { display: inline-flex; align-items: center; gap: 5px; font-size: 10px; color: var(--text2); min-width: 0; }
.dot { width: 7px; height: 7px; flex: none; border-radius: 999px; background: #8a8a8a; }
.dot.r { background: #3b82f6; }
.grid { display: grid; align-items: baseline; grid-template-columns: 24px minmax(180px, 1.15fr) minmax(90px, 0.55fr) minmax(90px, 0.55fr); gap: 0 10px; padding: 3px 12px; font-size: 11px; }
.grid.head { border-bottom: 1px solid var(--hr); padding: 6px 12px; font-size: 10px; font-weight: 500; color: var(--text2); }
.gut { text-align: center; font-family: Consolas, monospace; font-weight: 700; }
.path { min-width: 0; overflow-wrap: anywhere; font-family: Consolas, monospace; }
.prefix { color: var(--text2); font-size: 10px; }
.cell { font-family: Consolas, monospace; color: var(--text2); overflow-wrap: anywhere; }
.row.added { background: rgba(22, 163, 74, 0.08); }
.row.added .gut, .row.added .cell.new { color: #16a34a; }
.row.removed { background: rgba(220, 38, 38, 0.08); }
.row.removed .gut, .row.removed .cell.old { color: #dc2626; }
.row.changed { background: rgba(217, 119, 6, 0.09); }
.row.changed .gut { color: #d97706; }
.row.same .path { color: var(--text2); }
.same-tip { cursor: pointer; border-bottom: 1px solid var(--hr); padding: 5px 12px; font-size: 10px; color: var(--text2); }
.same-tip:hover { color: inherit; }
.none { padding: 10px 12px; text-align: center; font-size: 11px; color: var(--text2); }
.legend { display: flex; gap: 14px; font-size: 10px; color: var(--text2); }
.lg { display: inline-block; width: 8px; height: 8px; border-radius: 2px; margin-right: 4px; }
@media (max-width: 720px) { .grid { grid-template-columns: 20px minmax(120px, 1fr) minmax(70px, 0.55fr) minmax(70px, 0.55fr); } }
</style>
