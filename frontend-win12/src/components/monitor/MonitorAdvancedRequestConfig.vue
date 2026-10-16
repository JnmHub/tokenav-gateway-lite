<script setup>
/* ============================================================
 * 高级请求配置（对照旧 MonitorAdvancedRequestConfig.vue 等价迁移）
 *
 * 三块能力：
 *  1. extra_headers：key-value 多行编辑，失焦提交；Header 名校验镜像后端
 *     validateExtraHeaders（RFC 7230 token + hop-by-hop 黑名单，见
 *     internal/service/channel_monitor_template_service.go）。
 *  2. body_override_mode：off / merge / replace 三选一（后端 oneof；
 *     validateBodyModeParams 要求 merge/replace 必须带非空 body）。
 *  3. body_override：JSON 文本域，失焦解析为对象提交（null = 无覆盖）。
 *
 * validate() 供父组件保存前调用：先把未失焦的行/文本提交掉，再返回
 * 首个错误文案（空串 = 通过），避免「点了保存没走 blur」漏校验。
 * ============================================================ */
import { ref, watch, computed } from 'vue'
import { PlusOutlined, CloseOutlined } from '@ant-design/icons-vue'
import {
  HEADER_NAME_RE, FORBIDDEN_HEADERS, BODY_OVERRIDE_MODES, bodyOverrideModeMeta,
  API_MODE_CHAT_COMPLETIONS, API_MODE_RESPONSES,
} from './constants'

const props = defineProps({
  provider: { type: String, default: 'anthropic' },
  apiMode: { type: String, default: API_MODE_CHAT_COMPLETIONS },
  extraHeaders: { type: Object, default: () => ({}) },
  bodyOverrideMode: { type: String, default: 'off' },
  bodyOverride: { type: Object, default: null },
})
const emit = defineEmits(['update:extraHeaders', 'update:bodyOverrideMode', 'update:bodyOverride'])

/* ---------- Headers key-value 行 ---------- */
const headerRows = ref(toRows(props.extraHeaders))
const headersError = ref('')

/* 外部重置时（切换平台 / 应用模板）同步行；同值不回写避免行重排（对照旧 watch） */
watch(() => props.extraHeaders, (v) => {
  if (!isSameHeaderMap(toMap(headerRows.value), v)) headerRows.value = toRows(v)
  headersError.value = ''
})

function toRows(h) {
  const entries = Object.entries(h || {})
  if (!entries.length) return [{ name: '', value: '' }]
  return entries.map(([name, value]) => ({ name, value }))
}
function toMap(rows) {
  const out = {}
  for (const r of rows) {
    const name = r.name.trim()
    if (name === '') continue
    out[name] = r.value
  }
  return out
}
function isSameHeaderMap(a, b) {
  const ak = Object.keys(a)
  const bk = Object.keys(b || {})
  if (ak.length !== bk.length) return false
  return ak.every((k) => a[k] === b[k])
}

function commitHeaders() {
  /* 空白行 = 占位新行，不报错；半填行视为编辑中，也不报错。
   * 只有明确非法的名字在这里拦下，黑名单头留给保存时后端兜底。 */
  for (const row of headerRows.value) {
    const name = row.name.trim()
    if (name === '') continue
    if (!HEADER_NAME_RE.test(name)) {
      headersError.value = `Header 名不合法：「${name}」（不能含空格、冒号等特殊字符）`
      return
    }
    if (FORBIDDEN_HEADERS.includes(name.toLowerCase())) {
      headersError.value = `Header「${name}」为受保护头（Host/Content-Length 等），不允许覆盖`
      return
    }
  }
  headersError.value = ''
  emit('update:extraHeaders', toMap(headerRows.value))
}

function addRow() { headerRows.value.push({ name: '', value: '' }) }

function removeRow(i) {
  headerRows.value.splice(i, 1)
  if (!headerRows.value.length) headerRows.value.push({ name: '', value: '' })
  commitHeaders()
}

/* ---------- Body 模式 + JSON ---------- */
const bodyText = ref(serializeBody(props.bodyOverride))
const bodyError = ref('')

watch(() => props.bodyOverride, (v) => {
  bodyText.value = serializeBody(v)
  bodyError.value = ''
})

/** 解析并提交 body 文本。返回值：null=空（无覆盖）/ 对象=已提交 / undefined=有错保留旧值 */
function commitBody() {
  if (props.bodyOverrideMode === 'off') return null
  const trimmed = bodyText.value.trim()
  if (trimmed === '') {
    emit('update:bodyOverride', null)
    bodyError.value = ''
    return null
  }
  try {
    const parsed = JSON.parse(trimmed)
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
      bodyError.value = '请求体必须是一个 JSON 对象（不能是数组或基本类型）'
      return undefined
    }
    emit('update:bodyOverride', parsed)
    bodyError.value = ''
    return parsed
  } catch (e) {
    bodyError.value = `JSON 解析失败：${e instanceof Error ? e.message : String(e)}`
    return undefined
  }
}

function formatBody() {
  const trimmed = bodyText.value.trim()
  if (!trimmed) return
  try {
    const parsed = JSON.parse(trimmed)
    bodyText.value = JSON.stringify(parsed, null, 2)
    bodyError.value = ''
    // 顺带把校验过的对象提交，避免格式化后焦点未移走时父组件读到旧值
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) emit('update:bodyOverride', parsed)
  } catch (e) {
    bodyError.value = `JSON 解析失败：${e instanceof Error ? e.message : String(e)}`
  }
}

function serializeBody(body) {
  if (!body || !Object.keys(body).length) return ''
  return JSON.stringify(body, null, 2)
}

function updateBodyMode(mode) {
  emit('update:bodyOverrideMode', mode)
  // 切回 off 时清掉 body（off 模式不携带覆盖体）
  if (mode === 'off') emit('update:bodyOverride', null)
}

const bodyModeHint = computed(() => bodyOverrideModeMeta(props.bodyOverrideMode).hint)

/* 占位示例按 provider / api_mode / 模式切换（对照旧 bodyPlaceholder 全表） */
const bodyPlaceholder = computed(() => {
  if (props.provider === 'openai' && props.apiMode === API_MODE_RESPONSES) {
    if (props.bodyOverrideMode === 'merge') return '{\n  "max_output_tokens": 20\n}'
    return '{\n  "model": "gpt-4o-mini",\n  "instructions": "You are a health check endpoint. Reply briefly.",\n  "input": "Reply with exactly: ok",\n  "max_output_tokens": 20,\n  "stream": false\n}'
  }
  if (props.provider === 'openai') {
    if (props.bodyOverrideMode === 'merge') return '{\n  "max_tokens": 20\n}'
    return '{\n  "model": "gpt-4o-mini",\n  "messages": [{"role":"user","content":"Reply with exactly: ok"}],\n  "max_tokens": 20,\n  "stream": false\n}'
  }
  if (props.bodyOverrideMode === 'merge') return '{\n  "system": "You are Claude Code..."\n}'
  return '{\n  "model": "claude-x",\n  "messages": [{"role":"user","content":"hi"}],\n  "max_tokens": 10\n}'
})

/** 保存前调用：提交未失焦的编辑，返回首个错误（空串 = 通过） */
function validate() {
  commitHeaders()
  if (headersError.value) return headersError.value
  const body = commitBody()
  if (bodyError.value) return bodyError.value
  // 后端 validateBodyModeParams：merge/replace 必须携带非空 body，这里提前拦
  if (props.bodyOverrideMode !== 'off' && body === null) return 'merge / 覆盖 模式需要填写请求体 JSON'
  return ''
}

defineExpose({ validate })
</script>

<template>
  <div class="adv-req">
    <!-- 自定义请求头：key-value 行编辑 -->
    <div class="blk">
      <div class="blk-t">自定义请求头</div>
      <div class="hdr-rows">
        <div v-for="(row, i) in headerRows" :key="i" class="hdr-row">
          <a-input
            v-model:value="row.name" placeholder="Header 名" class="hdr-name"
            spellcheck="false" @blur="commitHeaders"
          />
          <a-input v-model:value="row.value" placeholder="Value" class="hdr-value" spellcheck="false" @blur="commitHeaders" />
          <button type="button" class="hdr-del" title="删除" @click="removeRow(i)">
            <CloseOutlined />
          </button>
        </div>
        <button type="button" class="hdr-add" @click="addRow">
          <PlusOutlined />添加 Header
        </button>
      </div>
      <div v-if="headersError" class="warn-line">{{ headersError }}</div>
      <div v-else class="hint">与默认请求头合并，用户值优先；Host / Content-Length 等受保护头会被后端拒绝。</div>
    </div>

    <!-- 请求体处理三模式 -->
    <div class="blk">
      <div class="blk-t">请求体处理</div>
      <div class="mode-grid">
        <button
          v-for="opt in BODY_OVERRIDE_MODES" :key="opt.value" type="button"
          class="mode-btn" :class="{ on: bodyOverrideMode === opt.value }"
          @click="updateBodyMode(opt.value)"
        >{{ opt.label }}</button>
      </div>
      <div class="hint">{{ bodyModeHint }}</div>
    </div>

    <!-- Body JSON（仅 mode != off） -->
    <div v-if="bodyOverrideMode !== 'off'" class="blk">
      <div class="blk-head">
        <span class="blk-t">Body JSON</span>
        <a-button size="small" :disabled="!bodyText.trim()" @click="formatBody">格式化</a-button>
      </div>
      <a-textarea
        v-model:value="bodyText" :rows="8" :placeholder="bodyPlaceholder"
        class="body-json" spellcheck="false" @blur="commitBody"
      />
      <div v-if="bodyError" class="warn-line">{{ bodyError }}</div>
      <div v-else class="hint">失焦时自动解析校验；留空等价于没有覆盖。</div>
    </div>
  </div>
</template>

<style scoped>
.adv-req { display: flex; flex-direction: column; gap: 14px; }

.blk-t { font-size: 12.5px; font-weight: 500; margin-bottom: 6px; }
.blk-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 6px; }
.blk-head .blk-t { margin-bottom: 0; }
.hint { font-size: 12px; color: var(--text2); line-height: 1.6; margin-top: 4px; }
.warn-line { font-size: 12px; color: #c42b1c; line-height: 1.6; margin-top: 4px; }

/* Header 行：名字列定宽对齐，Value 弹性；行内等高 32px */
.hdr-rows { display: flex; flex-direction: column; gap: 6px; }
.hdr-row { display: flex; align-items: center; gap: 8px; }
.hdr-name { width: 200px; flex: none; font-family: Consolas, monospace; }
.hdr-value { flex: 1; min-width: 0; font-family: Consolas, monospace; }
.hdr-del {
  flex: none; width: 28px; height: 28px; border-radius: 6px; border: none;
  background: transparent; color: var(--text2); cursor: pointer;
  display: inline-flex; align-items: center; justify-content: center; font-size: 12px;
  transition: background 100ms, color 100ms;
}
.hdr-del:hover { background: rgba(196, 43, 28, 0.1); color: #c42b1c; }
.hdr-add {
  align-self: flex-start; display: inline-flex; align-items: center; gap: 6px;
  height: 28px; padding: 0 12px; border-radius: 8px; font-size: 12px; cursor: pointer;
  border: 1px dashed var(--hr); background: transparent; color: var(--text2);
  transition: border-color 100ms, color 100ms, background 100ms;
}
.hdr-add:hover { border-color: var(--theme-2); color: var(--theme-2); background: var(--hover-b); }

/* 三模式按钮：等宽三列，选中低饱和主题色描边（与平台按钮组同态） */
.mode-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.mode-btn {
  height: 32px; border-radius: 8px; border: 1px solid var(--hr);
  background: var(--card); color: var(--text2); font-size: 13px; cursor: pointer;
  transition: border-color 100ms, background 100ms, color 100ms;
}
.mode-btn:hover { background: var(--hover-b); color: inherit; }
.mode-btn.on {
  border-color: var(--theme-2); color: var(--theme-2);
  background: color-mix(in srgb, var(--theme-2) 12%, transparent);
}

/* Body JSON 文本域：等宽字体 + 横向滚动 */
.body-json { font-family: Consolas, monospace; font-size: 12px; white-space: pre; overflow-x: auto; }

@media (max-width: 640px) {
  .mode-grid { grid-template-columns: 1fr; }
  .hdr-name { width: 40%; }
}
</style>
