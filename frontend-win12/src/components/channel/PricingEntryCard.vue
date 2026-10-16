<script setup>
/* 定价条目卡（对照旧 components/admin/channel/PricingEntryCard.vue 移植）
 * 4 种计费模式：
 * - token：6 默认价字段（$/MTok 显示，提交由父级 mTokToPerToken 换算）+ 服务档位倍率 + 上下文区间
 * - per_request：默认单次价 + tier_label 层级
 * - image：默认图片价 + 1K/2K/4K/HD 层级
 * - video：默认每秒价 + 480p/720p/1080p 三档（固定 intervals，tier_label 匹配）
 * 新增模型且条目无价格时自动查询官方默认价（GET /admin/channels/model-pricing?model=） */
import { ref, computed, getCurrentInstance } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'
import ModelTagInput from './ModelTagInput.vue'
import IntervalRow from './IntervalRow.vue'
import TimePricingSection from './TimePricingSection.vue'
import {
  emptyInterval, perTokenToMTok, platformTagStyle, platformLabel,
  BILLING_MODES, billingModeLabel,
} from './pricing'

const props = defineProps({
  entry: { type: Object, required: true },
  platform: { type: String, default: '' },
  /* 规则定价复用：每条目自带 platform，平台可选（候选由 platform-options 传入） */
  platformEditable: { type: Boolean, default: false },
  platformOptions: { type: Array, default: () => [] },
  /* 规则定价不入倍率（后端 allowMultipliers=false）：false 时隐藏 Fast/Flex 与区间倍率，
   * 避免用户填写后被后端静默丢弃 */
  showMultipliers: { type: Boolean, default: true },
})
const emit = defineEmits(['update', 'remove'])

// 实际平台：条目自带 platform（规则定价）优先，否则父级固定平台
const effPlatform = computed(() => props.entry.platform || props.platform)

// 已有模型的条目默认折叠（对照旧行为）
const collapsed = ref(props.entry.models.length > 0)
const contentId = `pe-${getCurrentInstance()?.uid ?? Math.random().toString(36).slice(2)}`

// 视频固定三档分辨率（对照旧 videoTiers）
const videoTiers = [
  { value: '480p', label: '480p' },
  { value: '720p', label: '720p' },
  { value: '1080p', label: '1080p' },
]

const billingMode = computed(() => billingModeLabel(props.entry.billing_mode))

/** 单字段更新：空串归一 null */
function setField(field, e) {
  const v = e.target.value
  emit('update', { ...props.entry, [field]: v === '' ? null : v })
}

/** 切换计费模式：清空区间（不同模式区间语义不同，对照旧 updateBillingMode） */
function changeBillingMode(mode) {
  emit('update', { ...props.entry, billing_mode: mode, intervals: [] })
}

/* ── 区间操作 ── */
function addInterval() {
  const intervals = [...(props.entry.intervals || [])]
  intervals.push(emptyInterval(intervals.length))
  emit('update', { ...props.entry, intervals })
}

/** 图片层级添加：自动预填 1K/2K/4K/HD（对照旧 addImageTier） */
function addImageTier() {
  const intervals = [...(props.entry.intervals || [])]
  const labels = ['1K', '2K', '4K', 'HD']
  intervals.push({ ...emptyInterval(intervals.length), tier_label: labels[intervals.length] || '' })
  emit('update', { ...props.entry, intervals })
}

function updateInterval(idx, updated) {
  const intervals = [...(props.entry.intervals || [])]
  intervals[idx] = updated
  emit('update', { ...props.entry, intervals })
}

function removeInterval(idx) {
  const intervals = [...(props.entry.intervals || [])]
  intervals.splice(idx, 1)
  emit('update', { ...props.entry, intervals })
}

/* ── 视频三档：intervals 中按 tier_label 匹配（对照旧 videoTierPrice/updateVideoTier） ── */
function videoTierPrice(tierLabel) {
  const iv = (props.entry.intervals || []).find(
    (i) => String(i.tier_label || '').trim().toLowerCase() === tierLabel.toLowerCase(),
  )
  return iv?.per_request_price ?? ''
}

function updateVideoTier(tierLabel, value) {
  const intervals = [...(props.entry.intervals || [])]
  const idx = intervals.findIndex(
    (i) => String(i.tier_label || '').trim().toLowerCase() === tierLabel.toLowerCase(),
  )
  const price = value === '' ? null : value
  if (idx >= 0) {
    intervals[idx] = { ...intervals[idx], tier_label: tierLabel, per_request_price: price }
  } else {
    intervals.push({
      ...emptyInterval(videoTiers.findIndex((t) => t.value === tierLabel)),
      tier_label: tierLabel,
      per_request_price: price,
    })
  }
  emit('update', { ...props.entry, intervals })
}

/* ── 服务档位倍率快捷键（对照旧 setTierDefaults/clearTierMultipliers） ── */
function setTierDefaults() {
  emit('update', { ...props.entry, fast_multiplier: 2, flex_multiplier: 0.5 })
}
function clearTierMultipliers() {
  emit('update', { ...props.entry, fast_multiplier: null, flex_multiplier: null })
}

/* ── 模型列表更新：新增模型且无价格时自动查询官方默认价 ── */
async function onModelsUpdate(newModels) {
  emit('update', { ...props.entry, models: newModels })
  const added = newModels.filter((m) => !props.entry.models.includes(m))
  if (added.length === 0) return
  const e = props.entry
  const hasPrice =
    e.input_price != null || e.output_price != null ||
    e.cache_write_price != null || e.cache_write_1h_price != null ||
    e.cache_read_price != null ||
    e.image_input_price != null || e.image_output_price != null
  if (hasPrice) return
  try {
    const result = await request('/admin/channels/model-pricing', { method: 'GET', query: { model: added[0] } })
    if (result?.found) {
      emit('update', {
        ...props.entry,
        models: newModels,
        input_price: perTokenToMTok(result.input_price ?? null),
        output_price: perTokenToMTok(result.output_price ?? null),
        cache_write_price: perTokenToMTok(result.cache_write_price ?? null),
        // v0.2.5：官方默认价带 1h 缓存写价时一并回填
        cache_write_1h_price: perTokenToMTok(result.cache_write_1h_price ?? null),
        cache_read_price: perTokenToMTok(result.cache_read_price ?? null),
        image_input_price: perTokenToMTok(result.image_input_price ?? null),
        image_output_price: perTokenToMTok(result.image_output_price ?? null),
      })
    }
  } catch {
    // 默认价查询失败不阻塞用户操作
  }
}
</script>

<template>
  <div class="pec">
    <!-- 折叠摘要头：模型 tag + 计费模式徽章 + 删除 -->
    <div class="pec-head">
      <button type="button" class="pec-toggle" :aria-expanded="!collapsed" :aria-controls="contentId" @click="collapsed = !collapsed">
        <span class="chev" :class="{ open: !collapsed }">›</span>
        <template v-if="collapsed">
          <span v-for="(m, i) in entry.models.slice(0, 3)" :key="i" class="mtag" :style="platformTagStyle(effPlatform)">{{ m }}</span>
          <span v-if="entry.models.length > 3" class="more">+{{ entry.models.length - 3 }}</span>
          <span v-if="entry.models.length === 0" class="more">未添加模型</span>
          <span class="badge">{{ billingMode }}</span>
        </template>
        <span v-else class="pec-title">定价配置</span>
      </button>
      <button type="button" class="del" title="删除条目" @click="emit('remove')">✕</button>
    </div>

    <!-- 展开区 -->
    <div v-show="!collapsed" :id="contentId" class="pec-body">
      <div class="row-models">
        <div class="grow">
          <span class="lb">模型列表 <i class="req">*</i></span>
          <ModelTagInput :models="entry.models" :platform="effPlatform" @update:models="onModelsUpdate" />
        </div>
        <!-- 规则定价：平台每条目自选（isPlatformMatch 空平台=不限，这里显式必选） -->
        <div v-if="platformEditable" class="w140">
          <span class="lb">平台</span>
          <a-select
            :value="entry.platform || undefined" :options="platformOptions" size="small"
            style="width: 100%" placeholder="平台"
            @change="(v) => emit('update', { ...props.entry, platform: v })"
          />
        </div>
        <div class="w180">
          <span class="lb">计费模式</span>
          <a-select :value="entry.billing_mode" :options="BILLING_MODES" size="small" style="width: 100%" @change="changeBillingMode" />
        </div>
      </div>
      <p class="hint">{{ platformLabel(effPlatform) }} 平台 · 价格按 $/MTok（每百万 token 美元）填写，提交时自动换算为 per-token</p>

      <!-- token 模式 -->
      <template v-if="entry.billing_mode === 'token'">
        <div class="sub-lb">默认价格（未命中区间时使用）<em>$/MTok</em></div>
        <div class="grid7">
          <label class="fld"><span class="lb">输入</span><input :value="entry.input_price ?? ''" type="number" step="any" min="0" placeholder="默认" @input="setField('input_price', $event)" /></label>
          <label class="fld"><span class="lb">输出</span><input :value="entry.output_price ?? ''" type="number" step="any" min="0" placeholder="默认" @input="setField('output_price', $event)" /></label>
          <label class="fld"><span class="lb">缓存写入</span><input :value="entry.cache_write_price ?? ''" type="number" step="any" min="0" placeholder="默认" @input="setField('cache_write_price', $event)" /></label>
          <!-- v0.2.5：1h 缓存写入价（与 5m 写价并排） -->
          <label class="fld"><span class="lb">缓存写入 1h</span><input :value="entry.cache_write_1h_price ?? ''" type="number" step="any" min="0" placeholder="默认" @input="setField('cache_write_1h_price', $event)" /></label>
          <label class="fld"><span class="lb">缓存读取</span><input :value="entry.cache_read_price ?? ''" type="number" step="any" min="0" placeholder="默认" @input="setField('cache_read_price', $event)" /></label>
          <label class="fld"><span class="lb">图片输入</span><input :value="entry.image_input_price ?? ''" type="number" step="any" min="0" placeholder="默认" @input="setField('image_input_price', $event)" /></label>
          <label class="fld"><span class="lb">图片输出</span><input :value="entry.image_output_price ?? ''" type="number" step="any" min="0" placeholder="默认" @input="setField('image_output_price', $event)" /></label>
        </div>

        <!-- 服务档位倍率：留空沿用官方价格；规则定价不支持倍率（后端丢弃），隐藏 -->
        <div v-if="showMultipliers" class="tier-box">
          <div class="tier-head">
            <div>
              <div class="lb strong">服务档位倍率</div>
              <div class="hint">留空沿用模型官方价格；快捷值仅用于减少输入。</div>
            </div>
            <div class="tier-btns">
              <button type="button" class="mini-btn" @click="setTierDefaults">2× / 0.5×</button>
              <button type="button" class="mini-btn ghost" @click="clearTierMultipliers">清空</button>
            </div>
          </div>
          <div class="grid3">
            <label class="fld"><span class="lb">Fast 快速档</span><input :value="entry.fast_multiplier ?? ''" type="number" step="any" min="0.000001" placeholder="2" @input="setField('fast_multiplier', $event)" /></label>
            <label class="fld"><span class="lb">Flex 弹性档</span><input :value="entry.flex_multiplier ?? ''" type="number" step="any" min="0.000001" placeholder="0.5" @input="setField('flex_multiplier', $event)" /></label>
            <!-- v0.2.5：max 推理力度计费倍率（nil 时后端沿用模型默认，如 claude fable 5.1 = 3.0） -->
            <label class="fld"><span class="lb">Max 推理力度倍率</span><input :value="entry.max_reasoning_effort_multiplier ?? ''" type="number" step="any" min="0.000001" placeholder="默认" @input="setField('max_reasoning_effort_multiplier', $event)" /></label>
          </div>
        </div>

        <!-- v0.2.5：分时计价折叠区（时段 + 倍率 + 时区） -->
        <TimePricingSection
          :model-value="entry.time_pricing || { timezone: 'Asia/Shanghai', weekdays_only: false, periods: [] }"
          @update="(tp) => emit('update', { ...props.entry, time_pricing: tp })"
        />

        <!-- 上下文区间定价 -->
        <div class="iv-head">
          <span class="sub-lb">上下文区间定价（可选）<em>(min, max]</em></span>
          <button type="button" class="mini-btn" @click="addInterval">+ 添加区间</button>
        </div>
        <div v-if="entry.intervals && entry.intervals.length" class="iv-list">
          <IntervalRow
            v-for="(iv, idx) in entry.intervals" :key="idx"
            :interval="iv" mode="token" :show-multipliers="showMultipliers"
            @update="updateInterval(idx, $event)" @remove="removeInterval(idx)"
          />
        </div>
      </template>

      <!-- per_request 模式 -->
      <template v-else-if="entry.billing_mode === 'per_request'">
        <div class="sub-lb">默认单次价格（未命中层级时使用）<em>$</em></div>
        <label class="fld w180"><input :value="entry.per_request_price ?? ''" type="number" step="any" min="0" placeholder="默认" @input="setField('per_request_price', $event)" /></label>
        <div class="iv-head">
          <span class="sub-lb">按次计费层级</span>
          <button type="button" class="mini-btn" @click="addInterval">+ 添加层级</button>
        </div>
        <div v-if="entry.intervals && entry.intervals.length" class="iv-list">
          <IntervalRow
            v-for="(iv, idx) in entry.intervals" :key="idx"
            :interval="iv" :mode="entry.billing_mode"
            @update="updateInterval(idx, $event)" @remove="removeInterval(idx)"
          />
        </div>
        <div v-else class="empty-tip">暂无层级，点击添加配置按次计费价格</div>
      </template>

      <!-- image 模式 -->
      <template v-else-if="entry.billing_mode === 'image'">
        <div class="sub-lb">默认图片价格（未命中层级时使用）<em>$</em></div>
        <label class="fld w180"><input :value="entry.per_request_price ?? ''" type="number" step="any" min="0" placeholder="默认" @input="setField('per_request_price', $event)" /></label>
        <div class="iv-head">
          <span class="sub-lb">图片计费层级（按次）</span>
          <button type="button" class="mini-btn" @click="addImageTier">+ 添加层级</button>
        </div>
        <div v-if="entry.intervals && entry.intervals.length" class="iv-list">
          <IntervalRow
            v-for="(iv, idx) in entry.intervals" :key="idx"
            :interval="iv" :mode="entry.billing_mode"
            @update="updateInterval(idx, $event)" @remove="removeInterval(idx)"
          />
        </div>
      </template>

      <!-- video 模式：按生成秒计费，三档分辨率 -->
      <template v-else-if="entry.billing_mode === 'video'">
        <div class="sub-lb">默认视频价格<em>$ / 秒</em></div>
        <label class="fld" style="max-width: 220px">
          <input :value="entry.per_request_price ?? ''" type="number" step="any" min="0" placeholder="未配置分辨率时使用" @input="setField('per_request_price', $event)" />
        </label>
        <div class="tier-box">
          <div class="lb strong">分辨率每秒价格</div>
          <div class="hint">按视频最终分辨率计费；留空时使用上方默认价格。单位：美元 / 生成秒。</div>
          <div class="grid3">
            <label v-for="tier in videoTiers" :key="tier.value" class="v-tier">
              <span class="lb">{{ tier.label }} <em class="unit">$ / 秒</em></span>
              <input
                :value="videoTierPrice(tier.value)" type="number" step="any" min="0" placeholder="默认"
                @input="updateVideoTier(tier.value, $event.target.value)"
              />
            </label>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.pec { border: 1px solid var(--hr); border-radius: 8px; background: var(--card); }
.pec-head { display: flex; align-items: center; gap: 4px; padding: 4px 6px 4px 2px; }
.pec-toggle {
  flex: 1; min-width: 0; display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
  border: 0; background: transparent; color: inherit; cursor: pointer;
  padding: 6px 4px; text-align: left; font-family: inherit;
}
.chev { display: inline-block; transition: transform 150ms; color: var(--text2); font-size: 14px; }
.chev.open { transform: rotate(90deg); }
.pec-title { font-size: 12px; color: var(--text2); }
.mtag {
  display: inline-flex; align-items: center; height: 20px; padding: 0 7px;
  border-radius: 4px; font-size: 11.5px; max-width: 200px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.more { font-size: 11.5px; color: var(--text2); }
.badge {
  margin-left: auto; height: 20px; line-height: 20px; padding: 0 8px;
  border-radius: 10px; font-size: 11px;
  color: var(--theme-2); background: color-mix(in srgb, var(--theme-2) 14%, transparent);
}
.del { border: 0; background: transparent; color: var(--text2); cursor: pointer; padding: 4px 8px; font-size: 12px; }
.del:hover { color: #d13438; }
.pec-body { padding: 4px 10px 12px; border-top: 1px solid var(--hr); }
.row-models { display: flex; gap: 12px; margin-top: 8px; }
.grow { flex: 1; min-width: 0; }
.w140 { width: 140px; flex: none; }
.w180 { width: 180px; flex: none; }
.lb { font-size: 11.5px; color: var(--text2); display: block; margin-bottom: 3px; }
.lb.strong { font-size: 12px; color: var(--text); font-weight: 500; }
.lb .unit { font-style: normal; font-weight: 400; color: var(--text2); }
.req { color: #d13438; font-style: normal; }
.hint { font-size: 11.5px; color: var(--text2); margin: 4px 0 0; line-height: 1.5; }
.sub-lb { font-size: 12px; font-weight: 500; margin: 12px 0 6px; }
.sub-lb em { font-style: normal; font-weight: 400; font-size: 11px; color: var(--text2); margin-left: 6px; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.grid3 { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.grid6 { display: grid; grid-template-columns: repeat(6, 1fr); gap: 8px; }
.grid7 { display: grid; grid-template-columns: repeat(7, 1fr); gap: 8px; }
.fld input, .v-tier input {
  height: 30px; width: 100%; padding: 0 8px;
  border: 1px solid var(--stroke-ctl); border-radius: var(--r-ctl);
  background: var(--surface-card); color: inherit;
  font-size: 12px; font-family: inherit;
}
:root.dark .fld input, :root.dark .v-tier input { background: rgba(48, 48, 48, 0.6); }
.fld input:hover, .v-tier input:hover { border-color: var(--accent); }
/* 聚焦统一用主题强调色（原 --theme-2 在五案主题下与 --accent 不同色） */
.fld input:focus, .v-tier input:focus { outline: none; border-color: var(--accent); }
.tier-box {
  margin-top: 12px; padding: 10px 12px; border: 1px solid var(--hr);
  border-radius: 8px; background: var(--surface);
}
.tier-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.tier-btns { display: flex; gap: 6px; }
.mini-btn {
  height: 26px; padding: 0 10px; border: 1px solid var(--hr); border-radius: 6px;
  background: transparent; color: var(--text2); font-size: 11.5px; cursor: pointer; font-family: inherit;
}
.mini-btn:hover { border-color: var(--theme-2); color: var(--theme-2); }
.mini-btn.ghost { border-color: transparent; }
.iv-head { display: flex; align-items: center; justify-content: space-between; margin-top: 12px; }
.iv-head .sub-lb { margin: 0; }
.iv-list { display: grid; gap: 8px; margin-top: 8px; }
.empty-tip {
  margin-top: 8px; padding: 12px; text-align: center; font-size: 11.5px; color: var(--text2);
  border: 1px dashed var(--hr); border-radius: 8px;
}
.v-tier { display: block; padding: 8px 10px; border: 1px solid var(--hr); border-radius: 8px; background: var(--card); }
@media (max-width: 760px) {
  .grid6 { grid-template-columns: repeat(3, 1fr); }
  .grid7 { grid-template-columns: repeat(3, 1fr); }
  .row-models { flex-direction: column; }
  .w140, .w180 { width: 100%; }
  .multis3 { grid-template-columns: 1fr; }
}
</style>
