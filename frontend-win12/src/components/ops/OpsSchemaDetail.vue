<script setup>
/* 结构详情 Tab（三期 Schema 分析器）：请求/响应字段树 + 请求头名称 chip
 * 契约来源：旧 RequestSchemaAnalyzerView.vue 结构详情区块
 * 字段结构：{ content_type?, bytes, truncated?, fields?: [{ path, type, length? }] } */
import { computed } from 'vue'
import dayjs from 'dayjs'

const props = defineProps({ record: { type: Object, default: null } })

/* 路径中的点号与数组下标代表一层嵌套，用于保留树状阅读层级（旧实现同规则，上限 8 层） */
const fieldDepth = (path) => Math.min(8, (String(path).match(/[.[]/g) || []).length)
const entries = (schema) => schema?.fields || []
const fmtTime = (t) => (t ? dayjs(t).format('YYYY-MM-DD HH:mm:ss') : '-')
const prettyPath = computed(() => `${props.record?.method || ''} ${props.record?.path || ''}`.trim())
</script>

<template>
  <div class="dt">
    <template v-if="record">
      <div class="dt-head">
        <div class="dt-path" :title="record.path">
          <span class="method">{{ record.method }}</span>
          <span class="mono">{{ record.path }}</span>
        </div>
        <div class="dt-meta">
          <span>{{ fmtTime(record.created_at) }}</span>
          <span v-if="record.api_key_ref || record.api_key_id" class="dim"> · key {{ record.api_key_ref || `#${record.api_key_id}` }}</span>
          <span v-if="record.group_name" class="dim"> · {{ record.group_name }}</span>
          <a-tag :color="(record.response_status || 0) < 400 ? 'green' : 'red'" style="margin: 0 0 0 8px">{{ record.response_status || '—' }}</a-tag>
        </div>
      </div>

      <div class="two">
        <div class="subcard">
          <div class="sc-h">请求结构 <span class="dim">{{ record.request_schema?.bytes || 0 }} bytes<template v-if="record.request_schema?.truncated"> · 已截断</template></span></div>
          <div class="fields">
            <div v-for="f in entries(record.request_schema)" :key="`rq-${f.path}`" class="field" :style="{ paddingLeft: `${8 + fieldDepth(f.path) * 16}px` }">
              <code class="fp">{{ f.path }}</code>
              <span class="type">{{ f.type }}<template v-if="f.length != null"> · {{ f.length }}</template></span>
            </div>
            <p v-if="!entries(record.request_schema).length" class="none">无字段</p>
          </div>
        </div>
        <div class="subcard">
          <div class="sc-h">响应结构 <span class="dim">{{ record.response_schema?.bytes || 0 }} bytes<template v-if="record.response_schema?.truncated"> · 已截断</template></span></div>
          <div class="fields">
            <div v-for="f in entries(record.response_schema)" :key="`rs-${f.path}`" class="field" :style="{ paddingLeft: `${8 + fieldDepth(f.path) * 16}px` }">
              <code class="fp">{{ f.path }}</code>
              <span class="type">{{ f.type }}<template v-if="f.length != null"> · {{ f.length }}</template></span>
            </div>
            <p v-if="!entries(record.response_schema).length" class="none">无字段</p>
          </div>
        </div>
      </div>

      <div class="subcard">
        <div class="sc-h">请求头（仅名称）</div>
        <div class="chips">
          <span v-for="h in record.request_headers || []" :key="h" class="chip">{{ h }}</span>
          <span v-if="!(record.request_headers || []).length" class="dim">无</span>
        </div>
        <div class="sc-h" style="margin-top: 10px">响应头（仅名称）</div>
        <div class="chips">
          <span v-for="h in record.response_headers || []" :key="h" class="chip">{{ h }}</span>
          <span v-if="!(record.response_headers || []).length" class="dim">无</span>
        </div>
      </div>
    </template>
    <div v-else class="none" style="padding: 40px 0; text-align: center">暂无结构记录</div>
  </div>
</template>

<style scoped>
.dt { padding: 12px 14px; }
.dt-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 10px; }
.method { flex: none; font-family: Consolas, monospace; font-size: 10px; background: rgba(59, 130, 246, 0.12); color: #3b82f6; border-radius: 4px; padding: 1px 6px; margin-right: 6px; }
.dt-path { min-width: 0; font-size: 12px; }
.mono { font-family: Consolas, monospace; overflow-wrap: anywhere; }
.dt-meta { margin-left: auto; font-size: 11px; color: var(--text2); display: flex; align-items: center; flex-wrap: wrap; }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.subcard { border: 1px solid var(--hr); border-radius: 8px; background: rgba(127, 127, 127, 0.04); padding: 10px 12px; min-width: 0; }
.sc-h { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 11px; font-weight: 500; color: var(--text2); margin-bottom: 6px; }
.fields { max-height: 420px; overflow-y: auto; }
.field { display: flex; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid var(--hr); padding: 5px 0; font-size: 12px; }
.field:last-of-type { border-bottom: 0; }
.fp { min-width: 0; overflow-wrap: anywhere; font-family: Consolas, monospace; }
.fp::before { content: '├─'; margin-right: 6px; color: var(--text2); }
.field:last-of-type .fp::before { content: '└─'; }
.type { flex: none; font-family: Consolas, monospace; font-size: 10px; background: rgba(59, 130, 246, 0.1); color: #3b82f6; border-radius: 4px; padding: 1px 6px; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { border: 1px solid var(--hr); border-radius: 4px; background: rgba(127, 127, 127, 0.06); padding: 2px 8px; font-family: Consolas, monospace; font-size: 10px; color: var(--text2); }
.dim { font-size: 10px; color: var(--text2); }
.none { font-size: 12px; color: var(--text2); }
@media (max-width: 900px) { .two { grid-template-columns: 1fr; } }
</style>
