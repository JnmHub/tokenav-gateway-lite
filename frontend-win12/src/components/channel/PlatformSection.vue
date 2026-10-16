<script setup>
/* 平台配置折叠区（对照旧 ChannelsView.vue 平台 Tab 内容 274-454 行）
 * 每启用平台一个实例：分组多选 chips + 平台特性开关 + 模型映射 + 定价条目列表
 * section 为父级 reactive 数组元素，就地变更（与旧版 form.platforms[i] 直改语义一致）
 * 特性开关显隐（对照旧版 328-370 行 v-if）：
 * - web_search_emulation：仅 anthropic，且全局搜索仿真开启（父级查 GET /admin/settings/web-search-emulation）
 * - codex_image_generation_bridge：仅 openai
 * - bedrock_cc_compat：仅 anthropic */
import { ref, computed } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'
import ModelMappingEditor from './ModelMappingEditor.vue'
import PricingEntryCard from './PricingEntryCard.vue'
import { emptyPricingEntry, platformLabel, platformTagStyle } from './pricing'

const props = defineProps({
  section: { type: Object, required: true },
  /* 该平台的可选分组（父级已按 platform 过滤） */
  groups: { type: Array, default: () => [] },
  groupsLoading: { type: Boolean, default: false },
  /* 跨渠道占用：gid → 占用该分组的其他渠道名；空串 = 未被占用（编辑时排除自身） */
  groupChannelMap: { type: Object, default: () => ({}) },
  /* 全局搜索仿真开关（anthropic web_search_emulation 显隐） */
  webSearchGlobalEnabled: { type: Boolean, default: false },
})

const syncing = ref(false)

const groups = computed(() => props.groups)

function groupName(g) {
  return g.name || `分组 ${g.id}`
}

/** 分组是否被其他渠道占用（提交侧后端也会校验，这里前置拦截） */
function occupiedName(g) {
  return props.groupChannelMap[g.id] || ''
}

/** 勾选/取消一个分组 */
function toggleGroup(g) {
  const ids = props.section.group_ids
  const idx = ids.indexOf(g.id)
  if (idx >= 0) ids.splice(idx, 1)
  else ids.push(g.id)
}

/* ── 定价条目 ── */
function addPricingEntry() {
  props.section.model_pricing.push(emptyPricingEntry())
}
function updatePricingEntry(idx, updated) {
  props.section.model_pricing.splice(idx, 1, updated)
}
function removePricingEntry(idx) {
  props.section.model_pricing.splice(idx, 1)
}

/* ── 同步最新模型（对照旧 syncLatestModels，接口 GET /admin/channels/pricing/sync-models） ── */
async function syncLatestModels() {
  if (syncing.value) return
  syncing.value = true
  try {
    const result = await request('/admin/channels/pricing/sync-models', {
      method: 'GET',
      query: { platform: props.section.platform },
    })
    // 收集本平台已有模型，过滤出目录里新增的
    const existing = new Set()
    for (const entry of props.section.model_pricing) {
      for (const m of entry.models) existing.add(m)
    }
    const newModels = (result?.models || []).filter((m) => !existing.has(m))
    if (newModels.length === 0) {
      message.info('模型目录已是最新的，没有需要补充的模型')
      return
    }
    // 新模型合并为一个条目，价格由用户补填
    props.section.model_pricing.push({ ...emptyPricingEntry(), models: newModels })
    message.success(`已从定价目录同步 ${newModels.length} 个新模型`)
  } catch (e) {
    message.error(`同步模型失败：${e.message}`)
  } finally {
    syncing.value = false
  }
}
</script>

<template>
  <div class="psec">
    <!-- 分组多选（必填）：显示倍率/账号数，被其他渠道占用的分组禁用 -->
    <div class="blk">
      <div class="blk-head">
        <span class="blk-t">关联分组 <i class="req">*</i></span>
        <span v-if="section.group_ids.length" class="blk-sub">已选 {{ section.group_ids.length }} 个</span>
      </div>
      <div class="grp-box">
        <div v-if="groupsLoading" class="empty-tip">分组加载中…</div>
        <div v-else-if="groups.length === 0" class="empty-tip">该平台暂无可用分组</div>
        <template v-else>
          <label
            v-for="g in groups" :key="g.id"
            class="grp-chip"
            :class="{ on: section.group_ids.includes(g.id), off: !!occupiedName(g) && !section.group_ids.includes(g.id) }"
            :title="occupiedName(g) ? `已被渠道「${occupiedName(g)}」占用` : ''"
          >
            <input
              type="checkbox"
              :checked="section.group_ids.includes(g.id)"
              :disabled="!!occupiedName(g) && !section.group_ids.includes(g.id)"
              @change="toggleGroup(g)"
            />
            <span class="grp-name">{{ groupName(g) }}</span>
            <span class="grp-meta">{{ g.rate_multiplier }}x</span>
            <span class="grp-meta">{{ g.account_count || 0 }} 账号</span>
            <span v-if="occupiedName(g)" class="grp-meta occ">{{ occupiedName(g) }}</span>
          </label>
        </template>
      </div>
    </div>

    <!-- 平台特性开关：web_search_emulation（anthropic + 全局开启） -->
    <div v-if="section.platform === 'anthropic' && webSearchGlobalEnabled" class="frow">
      <div class="frow-main">
        <div class="frow-t">Web Search 模拟</div>
        <div class="frow-d">为该渠道的 Anthropic 请求启用搜索工具模拟（消耗搜索服务商配额）。</div>
      </div>
      <a-switch v-model:checked="section.web_search_emulation" size="small" />
    </div>
    <!-- codex_image_generation_bridge（openai） -->
    <div v-if="section.platform === 'openai'" class="frow">
      <div class="frow-main">
        <div class="frow-t">Codex 图片生成桥接</div>
        <div class="frow-d">为 Codex /v1/responses 请求自动注入 image_generation 工具与桥接指令。</div>
      </div>
      <a-switch v-model:checked="section.codex_image_generation_bridge" size="small" />
    </div>
    <!-- bedrock_cc_compat（anthropic）：后端为渠道级布尔，网关对所有请求应用 CC 兼容转换 -->
    <div v-if="section.platform === 'anthropic'" class="frow">
      <div class="frow-main">
        <div class="frow-t">Bedrock CC 兼容</div>
        <div class="frow-d">开启后该渠道请求应用 Claude Code 兼容转换（system 重写 / 工具名规范化）。</div>
      </div>
      <a-switch v-model:checked="section.bedrock_cc_compat" size="small" />
    </div>

    <!-- 模型映射 -->
    <div class="blk">
      <div class="blk-head">
        <span class="blk-t">模型映射</span>
        <span class="blk-sub">把请求模型改写为上游实际模型</span>
        <button type="button" class="mini-btn" @click="$refs.mapping.addEntry()">+ 添加</button>
      </div>
      <ModelMappingEditor ref="mapping" :mapping="section.model_mapping" />
    </div>

    <!-- 模型定价 -->
    <div class="blk">
      <div class="blk-head">
        <span class="blk-t">模型定价</span>
        <span class="blk-sub">{{ platformLabel(section.platform) }} 平台，{{ section.model_pricing.length }} 个条目</span>
        <div class="blk-btns">
          <button type="button" class="mini-btn" :disabled="syncing" @click="syncLatestModels">
            {{ syncing ? '同步中…' : '⟳ 同步最新模型' }}
          </button>
          <button type="button" class="mini-btn accent" @click="addPricingEntry">+ 添加定价</button>
        </div>
      </div>
      <div v-if="section.model_pricing.length === 0" class="empty-tip">暂无定价条目，点击「添加定价」或「同步最新模型」创建</div>
      <div v-else class="price-list">
        <PricingEntryCard
          v-for="(entry, idx) in section.model_pricing"
          :key="idx"
          :entry="entry"
          :platform="section.platform"
          @update="updatePricingEntry(idx, $event)"
          @remove="removePricingEntry(idx)"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.psec { display: grid; gap: 14px; }
.blk-head { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; flex-wrap: wrap; }
.blk-t { font-size: 12.5px; font-weight: 500; }
.blk-sub { font-size: 11.5px; color: var(--text2); }
.blk-btns { margin-left: auto; display: flex; gap: 6px; }
.req { color: #d13438; font-style: normal; }
.grp-box {
  display: flex; flex-wrap: wrap; gap: 6px; max-height: 168px; overflow: auto;
  padding: 8px; border: 1px solid var(--hr); border-radius: 8px; background: var(--card);
}
.grp-chip {
  display: inline-flex; align-items: center; gap: 5px; padding: 3px 8px;
  border: 1px solid var(--hr); border-radius: 6px; font-size: 12px; cursor: pointer;
  transition: background 100ms;
}
.grp-chip:hover { background: var(--hover-b); }
.grp-chip.on { border-color: var(--theme-2); background: color-mix(in srgb, var(--theme-2) 12%, transparent); }
.grp-chip.off { opacity: 0.45; cursor: not-allowed; }
.grp-chip input { margin: 0; }
.grp-name { font-weight: 500; }
.grp-meta { font-size: 10.5px; color: var(--text2); }
.grp-meta.occ { color: #b8860b; }
.frow {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 8px 10px; border-radius: 8px; min-height: 40px; background: var(--card);
}
.frow:hover { filter: brightness(1.03); }
.frow-main { min-width: 0; }
.frow-t { font-size: 12.5px; }
.frow-d { font-size: 11.5px; color: var(--text2); margin-top: 1px; }
.mini-btn {
  height: 26px; padding: 0 10px; border: 1px solid var(--hr); border-radius: 6px;
  background: transparent; color: var(--text2); font-size: 11.5px; cursor: pointer; font-family: inherit;
}
.mini-btn:hover { border-color: var(--theme-2); color: var(--theme-2); }
.mini-btn:disabled { opacity: 0.5; cursor: default; }
.mini-btn.accent { border-color: var(--theme-2); color: var(--theme-2); }
.price-list { display: grid; gap: 8px; }
.empty-tip {
  padding: 12px; text-align: center; font-size: 11.5px; color: var(--text2);
  border: 1px dashed var(--hr); border-radius: 8px;
}
</style>
