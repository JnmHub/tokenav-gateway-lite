<script setup>
/* 模型白名单/映射面板（对照旧 ModelWhitelistSelector + EditAccountModal 模型区）
 * - 平台预设模型一键勾选（getModelsByPlatform，与 new-api 清单一致）
 * - 「获取上游模型」：编辑模式 POST /admin/accounts/:id/models/sync-upstream；
 *   新增模式 POST /admin/accounts/models/sync-upstream-preview（传连接凭据探测，
 *   无需账号 id）——旧版只覆盖 antigravity 编辑场景，这里两种流程都可用
 * - 模型映射（from→to）行编辑 + 平台预设映射快捷 chips（anthropic/openai）
 * 数据契约：白名单数组写入 credentials.model_mapping 身份映射；映射行写入
 * 非身份映射（编辑器 buildMappingCreds 统一拼装） */
import { computed, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ThunderboltOutlined, PlusOutlined, DeleteOutlined } from '@ant-design/icons-vue'
import { getModelsByPlatform, anthropicPresetMappings, openaiPresetMappings } from './modelPresets.js'
import { syncUpstreamModels, syncUpstreamModelsPreview } from './accountsApi.js'

const props = defineProps({
  platform: { type: String, default: '' },
  accountId: { type: Number, default: null }, // null = 新增流程（走 preview 接口）
  credentials: { type: Object, default: () => ({}) }, // { type, base_url, api_key }
})
const modelWhitelist = defineModel('modelWhitelist', { type: Array, default: () => [] })
const mappings = defineModel('mappings', { type: Array, default: () => [] }) // [{ from, to }]

/* 本地副本模式：defineModel 的 props 回传是异步的，连续快速点选 chips 会
 * 基于旧数组互相覆盖（实测丢更新）；所以所有编辑都落在本地副本，
 * 变更后再上报父组件，父组件回传时（外部 reset）才反向同步。 */
const localWl = ref([...(modelWhitelist.value || [])])
const localMap = ref([...(mappings.value || [])])
let reporting = 0
watch(localWl, (v) => { reporting++; modelWhitelist.value = [...v]; queueMicrotask(() => reporting--) }, { deep: true })
watch(localMap, (v) => { reporting++; mappings.value = [...v]; queueMicrotask(() => reporting--) }, { deep: true })
watch(modelWhitelist, (v) => {
  if (reporting > 0) return
  const next = JSON.stringify(v || [])
  if (next !== JSON.stringify(localWl.value)) localWl.value = [...(v || [])]
})
watch(mappings, (v) => {
  if (reporting > 0) return
  const next = JSON.stringify(v || [])
  if (next !== JSON.stringify(localMap.value)) localMap.value = [...(v || [])]
})

// 平台预设清单（chips 勾选；部分平台无预设清单则整块隐藏）
const presetModels = computed(() => getModelsByPlatform(props.platform) || [])
const showPresets = computed(() => presetModels.value.length > 0)
const hasModel = (m) => localWl.value.includes(m)
const toggleModel = (m) => {
  const i = localWl.value.indexOf(m)
  if (i >= 0) localWl.value.splice(i, 1)
  else localWl.value.push(m)
}

/* 获取上游模型：编辑走账号实例接口，新增走凭据探测接口；
 * 返回模型并入白名单（去重、保序），提示实际新增数量 */
const fetching = ref(false)
const fetchUpstream = async () => {
  fetching.value = true
  try {
    const result = props.accountId
      ? await syncUpstreamModels(props.accountId)
      : await syncUpstreamModelsPreview({
          platform: props.platform,
          type: props.credentials.type || 'apikey',
          base_url: props.credentials.base_url || undefined,
          api_key: props.credentials.api_key || '',
        })
    const list = (result?.models || []).map((m) => String(m).trim()).filter(Boolean)
    if (!list.length) { message.info('上游未返回任何模型'); return }
    const set = new Set(localWl.value)
    let added = 0
    for (const m of list) if (!set.has(m)) { set.add(m); added++ }
    localWl.value = [...set]
    if (added > 0) message.success(`已从上游获取 ${list.length} 个模型，新增 ${added} 个`)
    else message.info(`上游共 ${list.length} 个模型，均已在白名单中`)
  } catch (e) {
    message.error(`获取上游模型失败：${e.message}`)
  } finally {
    fetching.value = false
  }
}

// 映射行编辑 + 预设映射快捷 chips（anthropic/openai 有内置清单）
const presetMappings = computed(() => {
  if (props.platform === 'anthropic') return anthropicPresetMappings
  if (props.platform === 'openai') return openaiPresetMappings
  return []
})
const addMappingRow = () => localMap.value = [...localMap.value, { from: '', to: '' }]
const rmMappingRow = (i) => localMap.value = localMap.value.filter((_, idx) => idx !== i)
const applyPresetMapping = (p) => {
  // 同 from 的旧规则先移除，再追加预设（与旧版「点 chip 加一条映射」语义一致）
  const rest = localMap.value.filter((m) => m.from !== p.from)
  localMap.value = [...rest, { from: p.from, to: p.to }]
}
const hasMapping = (p) => localMap.value.some((m) => m.from === p.from && m.to === p.to)
</script>

<template>
  <div class="mwp">
    <a-select :value="localWl" mode="tags" :token-separators="[',']" :max-tag-count="8"
      placeholder="输入模型名回车；留空 = 不限制"
      @change="(v) => (localWl = [...(v || [])])" />
    <div class="mwp-toolbar">
      <a-button size="small" :loading="fetching" @click="fetchUpstream">
        <ThunderboltOutlined /> 获取上游模型
      </a-button>
      <span class="hint">{{ accountId ? '从该账号的上游拉取模型清单' : '用上方连接凭据探测上游模型清单' }}；已选 {{ localWl.length }} 个</span>
    </div>

    <div v-if="showPresets" class="mwp-presets">
      <span v-for="m in presetModels" :key="m" class="chip" :class="{ on: hasModel(m) }"
        :title="m" @click="toggleModel(m)">{{ m }}</span>
    </div>

    <div class="mwp-mapping">
      <div class="mwp-map-head">
        <span class="hint">模型映射（请求模型 → 上游模型；留空不配置）</span>
        <a-button size="small" @click="addMappingRow"><PlusOutlined /> 添加映射</a-button>
      </div>
      <div v-for="(row, i) in localMap" :key="i" class="mwp-map-row">
        <a-input v-model:value="row.from" size="small" placeholder="请求模型（源）" />
        <span class="arrow">→</span>
        <a-input v-model:value="row.to" size="small" placeholder="上游模型（目标）" />
        <a-button size="small" type="text" danger @click="rmMappingRow(i)"><DeleteOutlined /></a-button>
      </div>
      <div v-if="presetMappings.length" class="mwp-presets" style="margin-top: 4px">
        <span v-for="p in presetMappings" :key="p.label" class="chip map" :class="{ on: hasMapping(p) }"
          :title="`${p.from} → ${p.to}`" @click="applyPresetMapping(p)">{{ p.label }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mwp { display: flex; flex-direction: column; gap: 8px; }
.mwp-toolbar { display: flex; align-items: center; gap: 10px; }
.mwp-presets { display: flex; flex-wrap: wrap; gap: 4px; }
.chip { font-size: 12px; line-height: 22px; padding: 0 8px; border-radius: 4px; border: 1px solid var(--stroke-card); background: var(--card); color: var(--txt2); cursor: pointer; max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; transition: background 0.12s, color 0.12s; }
.chip:hover { background: var(--fill-subtle); }
.chip.on { background: var(--accent-soft, #3b91d826); border-color: var(--accent, #3b91d8); color: var(--txt1); }
.chip.map.on { background: #6b6b6b26; }
.mwp-map-head { display: flex; align-items: center; justify-content: space-between; }
.mwp-map-row { display: flex; align-items: center; gap: 6px; margin-top: 6px; }
.mwp-map-row .arrow { color: var(--txt3); flex: none; }
</style>
