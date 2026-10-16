<script setup>
/* 风控 · 关键词拦截面板（对照旧 RiskControlView.vue 关键词区块）
 * - 屏蔽关键词：textarea 按行编辑（trim/去空/大小写去重，上限 10000，旧 parseBlockedKeywords 语义）
 * - 规则包（keyword_rule_packs）：启停 + 动作（强拦截/只审计）+ 关键词预览；
 *   内置包本地只保存差异（后端行为，前端整体提交即可）
 * - 本地关键词与纯 AI 模式由总配置选择；纯 AI 保留词库管理草稿但不参与判定。
 * - 不提供 local_keyword_only（后台派生状态）和已被后台固定关闭的虚假开关。 */
import { computed, ref, watch } from 'vue'
import { useRequestLifetime } from '../../composables/useRequestLifetime.js'
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
import W12Select from '../design/W12Select.vue'

const props = defineProps({
  cfg: { type: Object, required: true },
  saving: Boolean,
})
const emit = defineEmits(['save'])

const KW_MAX = 10000

const kwsText = ref('')
// 初始化/外部重载时同步 textarea（cfg.blocked_keywords 数组 → 每行一个）
watch(
  () => props.cfg,
  (c) => {
    kwsText.value = Array.isArray(c?.blocked_keywords) ? c.blocked_keywords.join('\n') : ''
  },
  { immediate: true }
)

// 旧 parseBlockedKeywords 语义：按行 trim、去空、小写去重、保序
const parsedKeywords = computed(() => {
  const seen = new Set()
  const out = []
  for (const line of kwsText.value.split(/\r?\n/)) {
    const kw = line.trim()
    if (!kw) continue
    const key = kw.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(kw)
  }
  return out
})
const overLimit = computed(() => parsedKeywords.value.length > KW_MAX)
// 同步至父级草稿，让“保存审查配置”和窗口脏状态也包含尚未单独保存的关键词。
watch(kwsText, () => { if (props.cfg) props.cfg.blocked_keywords = [...parsedKeywords.value] })

const ACTION_OPTIONS = [
  { value: 'keyword_block', label: '强拦截' },
  { value: 'keyword_audit', label: '只审计' },
]

// 规则包关键词预览：默认收起，展示前 12 个
const expanded = ref({})
useRequestLifetime(() => { kwsText.value = ''; expanded.value = {} })
const togglePack = (id) => { expanded.value = { ...expanded.value, [id]: !expanded.value[id] } }
const packKeywords = (p) => {
  const list = p.keywords || []
  return expanded.value[p.id] ? list : list.slice(0, 12)
}

const save = () => {
  if (overLimit.value || props.saving) return
  emit('save', {
    blocked_keywords: parsedKeywords.value,
    keyword_rule_packs: props.cfg.keyword_rule_packs,
    keyword_group_routing_enabled: props.cfg.keyword_group_routing_enabled,
    block_message: props.cfg.block_message, block_status: props.cfg.block_status,
  })
}
</script>

<template>
  <div class="card wide w12-design">
    <div class="card-title">关键词拦截</div>
    <div class="kw-hint">
      {{ cfg.keyword_blocking_mode === 'ai_only' ? '当前编辑选择纯 AI，保存后这些关键词、规则包和分组路由不参与 AI 判定；保留配置供切回本地关键词使用。' : '本地关键词不出网；自定义关键词与规则包合并进本地匹配器，自定义关键词优先命中。' }}
    </div>
    <div class="row"><span>关键词强拦截后按分组切组放行</span><a-switch v-model:checked="cfg.keyword_group_routing_enabled" :disabled="cfg.keyword_blocking_mode === 'ai_only' || saving" /></div>

    <div class="row top">
      <span>屏蔽关键词（每行一个）</span>
      <div class="kw-editor">
        <a-textarea v-model:value="kwsText" :rows="8" placeholder="每行一个关键词，命中即强拦截" />
        <div class="kw-meta" :class="{ over: overLimit }">
          {{ parsedKeywords.length }} 个（去重后） / 上限 {{ KW_MAX }}
          <span v-if="overLimit"> · 超出上限，请删减后再保存</span>
        </div>
        <W12Button variant="primary" compact :loading="saving" :disabled="overLimit || saving" @click="save">保存关键词</W12Button>
      </div>
    </div>

    <div class="row top">
      <span>自定义拦截提示 / 状态码</span>
      <div class="inline">
        <W12Input v-model:value="cfg.block_message" placeholder="拦截提示文案" style="flex: 1; min-width: 0" />
        <a-input-number v-model:value="cfg.block_status" :min="400" :max="599" style="width: 110px; flex: none" />
      </div>
    </div>

    <div v-if="(cfg.keyword_rule_packs || []).length" class="packs">
      <div class="packs-title">规则包（{{ (cfg.keyword_rule_packs || []).length }}）</div>
      <div class="packs-list">
        <div v-for="p in cfg.keyword_rule_packs" :key="p.id" class="pack">
        <div class="pack-head">
          <div class="pack-name">
            <span class="nm">{{ p.name }}</span>
            <a-tag v-if="p.built_in" color="default">内置</a-tag>
            <a-tag :color="p.action === 'keyword_block' ? 'red' : 'blue'">
              {{ p.action === 'keyword_block' ? '强拦截' : '只审计' }}
            </a-tag>
          </div>
          <div class="pack-ctl">
            <W12Select :value="p.action" :options="ACTION_OPTIONS" :disabled="saving" size="small" style="width: 96px"
              @change="(v) => { p.action = v; save() }" />
            <a-switch :checked="p.enabled !== false" size="small" :disabled="saving"
              @change="(v) => { p.enabled = v; save() }" />
          </div>
        </div>
        <div class="pack-desc">{{ p.description }}</div>
        <div class="pack-kws">
          <span v-for="k in packKeywords(p)" :key="k" class="kw-tag" :title="k">{{ k }}</span>
          <W12Button v-if="(p.keywords || []).length > 12" variant="text" compact class="more"
            @click="togglePack(p.id)">
            {{ expanded[p.id] ? '收起' : `等 ${(p.keywords || []).length} 个` }}
          </W12Button>
          <span v-if="!(p.keywords || []).length" class="kw-empty">无关键词</span>
        </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
@import '../settings/settings.css';
.kw-hint { font-size: 12px; color: var(--txt2); margin-bottom: 10px; }
.row { display: flex; align-items: center; justify-content: flex-end; gap: 12px; margin-bottom: 10px; }
.row > span:first-child { flex: 1; text-align: right; color: var(--txt2); }
/* 半宽列里长标签（如「屏蔽关键词（每行一个）」）禁逐字竖排，允许整词换行后右对齐 */
.row > span:first-child { line-height: 1.5; overflow-wrap: break-word; min-width: 96px; }
.row.top { align-items: flex-start; }
.row.top > span:first-child { padding-top: 4px; max-width: 45%; flex: none; }
.inline { display: flex; gap: 8px; min-width: 0; flex: 0 1 420px; }
.kw-editor { display: flex; flex-direction: column; gap: 6px; width: min(420px, 100%); min-width: 0; }
.kw-meta { font-size: 12px; color: var(--txt2); }
.kw-meta.over { color: #d13438; }
.kw-editor :deep(.ant-btn) { align-self: flex-end; }
.packs { margin-top: 12px; border-top: 1px solid var(--hr); padding-top: 12px; }
/* 规则包数量多（11 包 × 12 词条）会撑出数千像素高，限高内部滚动 */
.packs-list { max-height: 380px; overflow-y: auto; padding-right: 4px; }
.packs-title { font-weight: 500; margin-bottom: 8px; }
.pack { border: 1px solid var(--hr); border-radius: var(--r-panel, 10px); padding: 10px 12px; margin-bottom: 8px; }
.pack-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.pack-name { display: flex; align-items: center; gap: 6px; min-width: 0; }
.pack-name .nm { font-weight: 500; }
.pack-ctl { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
.pack-desc { font-size: 12px; color: var(--txt2); margin: 4px 0 6px; }
.pack-kws { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
.kw-tag { background: var(--card); border-radius: 4px; padding: 1px 6px; font-size: 12px; font-family: ui-monospace, monospace; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.kw-empty { font-size: 12px; color: var(--txt3); }
.more { padding: 0 4px; }
@container w12-app (max-width: 620px) {
  .row, .row.top { flex-wrap: wrap; justify-content: flex-start; }
  .row > span:first-child, .row.top > span:first-child { flex-basis: 100%; max-width: 100%; text-align: left; }
  .kw-editor, .inline { width: 100%; flex-basis: 100%; }
  .pack-head, .pack-name { flex-wrap: wrap; }
}
</style>
