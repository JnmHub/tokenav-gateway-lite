<template>
  <div class="rounded-lg border border-gray-200 bg-gray-50 p-3 dark:border-dark-600 dark:bg-dark-800">
    <!-- Collapsed summary header -->
    <div class="flex items-center gap-2">
      <button
        type="button"
        class="flex min-w-0 flex-1 cursor-pointer select-none items-center gap-2 text-left"
        :aria-expanded="!collapsed"
        :aria-controls="collapsibleContentId"
        @click="collapsed = !collapsed"
      >
        <Icon
          :name="collapsed ? 'chevronRight' : 'chevronDown'"
          size="sm"
          :stroke-width="2"
          class="flex-shrink-0 text-gray-400 transition-transform duration-200"
        />

        <!-- Summary: model tags + billing badge -->
        <div v-if="collapsed" class="flex min-w-0 flex-1 items-center gap-2 overflow-hidden">
          <!-- Compact model tags (show first 3) -->
          <div class="flex min-w-0 flex-1 flex-wrap items-center gap-1">
            <span
              v-for="(m, i) in entry.models.slice(0, 3)"
              :key="i"
              class="inline-flex shrink-0 rounded px-1.5 py-0.5 text-xs"
              :class="getPlatformTagClass(effectivePlatform)"
            >
              {{ m }}
            </span>
            <span
              v-if="entry.models.length > 3"
              class="whitespace-nowrap text-xs text-gray-400"
            >
              +{{ entry.models.length - 3 }}
            </span>
            <span
              v-if="entry.models.length === 0"
              class="text-xs italic text-gray-400"
            >
              {{ t('admin.channels.form.noModels', '未添加模型') }}
            </span>
          </div>

          <!-- Billing mode badge -->
          <span
            class="flex-shrink-0 rounded-full bg-primary-100 px-2 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
          >
            {{ billingModeLabel }}
          </span>
          <span
            v-if="isComposite && entry.platform"
            class="flex-shrink-0 rounded-md px-2 py-0.5 text-xs font-medium"
            :class="getPlatformTagClass(entry.platform)"
          >
            {{ platformLabel }}
          </span>
        </div>

        <!-- Expanded: show the label "Pricing Entry" or similar -->
        <span v-else class="flex-1 text-xs font-medium text-gray-500 dark:text-gray-400">
          {{ t('admin.channels.form.pricingEntry', '定价配置') }}
        </span>
      </button>

      <!-- Remove button remains separate so the disclosure control has valid button semantics. -->
      <button
        type="button"
        class="flex-shrink-0 rounded p-1 text-gray-400 hover:text-red-500"
        @click="emit('remove')"
      >
        <Icon name="trash" size="sm" />
      </button>
    </div>

    <!-- Expandable content with transition -->
    <div
      :id="collapsibleContentId"
      class="collapsible-content"
      :class="{ 'collapsible-content--collapsed': collapsed }"
    >
      <div class="collapsible-inner">
        <!-- Header: Models + Billing Mode -->
        <div class="mt-3 flex flex-col items-stretch gap-2 sm:flex-row sm:items-start">
          <div class="flex-1">
            <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
              {{ t('admin.channels.form.models', '模型列表') }} <span class="text-red-500">*</span>
            </label>
            <ModelTagInput
              :models="entry.models"
              :platform="effectivePlatform"
              @update:models="onModelsUpdate($event)"
              :placeholder="t('admin.channels.form.modelsPlaceholder', '输入模型名后按回车添加，支持通配符 *')"
              class="mt-1"
            />
          </div>
          <div
            class="grid w-full grid-cols-1 gap-2"
            :class="isComposite ? 'sm:w-[22rem] sm:grid-cols-2' : 'sm:w-40'"
          >
            <div v-if="isComposite">
              <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.channels.form.pricingPlatform', '实际平台') }}
                <span class="text-red-500">*</span>
              </label>
              <Select
                :model-value="entry.platform || null"
                :options="platformOptions"
                :placeholder="t('admin.channels.form.pricingPlatformPlaceholder', '选择真实目标平台')"
                :aria-label="t('admin.channels.form.pricingPlatform', '实际平台')"
                data-testid="pricing-platform-select"
                class="mt-1"
                @update:model-value="updatePlatform"
              />
            </div>
            <div>
              <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.channels.form.billingMode', '计费模式') }}
              </label>
              <Select
                :model-value="entry.billing_mode"
                :options="billingModeOptions"
                class="mt-1"
                @update:model-value="updateBillingMode($event as BillingMode)"
              />
            </div>
          </div>
        </div>

        <p
          v-if="isComposite"
          class="mt-1.5 text-[11px] leading-5"
          :class="entry.platform ? 'text-gray-400' : 'text-amber-600 dark:text-amber-400'"
        >
          {{ t('admin.channels.form.pricingPlatformHint', '聚合分组必须为每条价格选择真实平台，避免同名模型跨平台串价。') }}
        </p>

        <!-- Token mode -->
        <div v-if="entry.billing_mode === 'token'">
          <!-- Default prices (fallback when no interval matches) -->
          <label class="mt-3 block text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.channels.form.defaultPrices', '默认价格（未命中区间时使用）') }}
            <span class="ml-1 font-normal text-gray-400">$/MTok</span>
          </label>
          <div class="mt-1 grid grid-cols-2 gap-2" :class="compact ? '' : 'sm:grid-cols-6'">
            <div>
              <label class="text-xs text-gray-400">{{ t('admin.channels.form.inputPrice', '输入') }}</label>
              <input :value="entry.input_price" @input="emitField('input_price', ($event.target as HTMLInputElement).value)"
                type="number" step="any" min="0" class="input mt-0.5 text-sm" :placeholder="t('admin.channels.form.pricePlaceholder', '默认')" />
            </div>
            <div>
              <label class="text-xs text-gray-400">{{ t('admin.channels.form.outputPrice', '输出') }}</label>
              <input :value="entry.output_price" @input="emitField('output_price', ($event.target as HTMLInputElement).value)"
                type="number" step="any" min="0" class="input mt-0.5 text-sm" :placeholder="t('admin.channels.form.pricePlaceholder', '默认')" />
            </div>
            <div v-if="!compact || advancedOpen">
              <label class="text-xs text-gray-400">{{ t('admin.channels.form.cacheWritePrice', '缓存写入') }}</label>
              <input :value="entry.cache_write_price" @input="emitField('cache_write_price', ($event.target as HTMLInputElement).value)"
                type="number" step="any" min="0" class="input mt-0.5 text-sm" :placeholder="t('admin.channels.form.pricePlaceholder', '默认')" />
            </div>
            <div v-if="!compact || advancedOpen">
              <label class="text-xs text-gray-400">{{ t('admin.channels.form.cacheReadPrice', '缓存读取') }}</label>
              <input :value="entry.cache_read_price" @input="emitField('cache_read_price', ($event.target as HTMLInputElement).value)"
                type="number" step="any" min="0" class="input mt-0.5 text-sm" :placeholder="t('admin.channels.form.pricePlaceholder', '默认')" />
            </div>
            <div v-if="!compact || advancedOpen">
              <label class="text-xs text-gray-400">{{ t('admin.channels.form.imageInputPrice', '图片输入') }}</label>
              <input :value="entry.image_input_price" @input="emitField('image_input_price', ($event.target as HTMLInputElement).value)"
                type="number" step="any" min="0" class="input mt-0.5 text-sm" :placeholder="t('admin.channels.form.pricePlaceholder', '默认')" />
            </div>
            <div v-if="!compact || advancedOpen">
              <label class="text-xs text-gray-400">{{ t('admin.channels.form.imageTokenPrice', '图片输出') }}</label>
              <input :value="entry.image_output_price" @input="emitField('image_output_price', ($event.target as HTMLInputElement).value)"
                type="number" step="any" min="0" class="input mt-0.5 text-sm" :placeholder="t('admin.channels.form.pricePlaceholder', '默认')" />
            </div>
          </div>

			<div v-if="!compact || advancedOpen" class="mt-3 rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-700">
				<div class="flex flex-wrap items-start justify-between gap-2">
					<div>
						<p class="text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.channels.form.serviceTierMultipliers', '服务档位倍率') }}</p>
						<p class="mt-1 text-[11px] text-gray-400">{{ t('admin.channels.form.serviceTierMultipliersHint', '留空沿用模型官方价格；快捷值仅用于减少输入。') }}</p>
					</div>
					<div class="flex gap-1.5">
						<button type="button" class="rounded-md border border-gray-200 px-2 py-1 text-[11px] text-gray-500 hover:border-primary-300 dark:border-dark-500 dark:text-gray-300" @click="setTierDefaults">2× / 0.5×</button>
						<button type="button" class="rounded-md px-2 py-1 text-[11px] text-gray-400 hover:text-red-500" @click="clearTierMultipliers">{{ t('common.clear', '清空') }}</button>
					</div>
				</div>
				<div class="mt-2 grid grid-cols-2 gap-2 sm:max-w-md">
					<label class="block">
						<span class="text-[11px] text-gray-400">{{ t('admin.channels.form.fastMultiplier', 'Fast 快速档') }}</span>
						<input :value="entry.fast_multiplier" type="number" step="any" min="0.000001" class="input mt-0.5 text-sm" placeholder="2" @input="emitField('fast_multiplier', ($event.target as HTMLInputElement).value)" />
					</label>
					<label class="block">
						<span class="text-[11px] text-gray-400">{{ t('admin.channels.form.flexMultiplier', 'Flex 弹性档') }}</span>
						<input :value="entry.flex_multiplier" type="number" step="any" min="0.000001" class="input mt-0.5 text-sm" placeholder="0.5" @input="emitField('flex_multiplier', ($event.target as HTMLInputElement).value)" />
					</label>
				</div>
			</div>

          <!-- Token intervals -->
          <div v-if="!compact || advancedOpen" class="mt-3">
            <div class="flex items-center justify-between">
              <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.channels.form.intervals', '上下文区间定价（可选）') }}
                <span class="ml-1 font-normal text-gray-400">(min, max]</span>
              </label>
              <button type="button" @click="addInterval" class="text-xs text-primary-600 hover:text-primary-700">
                + {{ t('admin.channels.form.addInterval', '添加区间') }}
              </button>
            </div>
            <div v-if="entry.intervals && entry.intervals.length > 0" class="mt-2 space-y-2">
              <IntervalRow
                v-for="(iv, idx) in entry.intervals"
                :key="idx"
                :interval="iv"
                :mode="entry.billing_mode"
				:show-multipliers="true"
                @update="updateInterval(idx, $event)"
                @remove="removeInterval(idx)"
              />
            </div>
          </div>
        </div>

        <!-- Per-request mode -->
        <div v-else-if="entry.billing_mode === 'per_request'">
          <!-- Default per-request price -->
          <label class="mt-3 block text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.channels.form.defaultPerRequestPrice', '默认单次价格（未命中层级时使用）') }}
            <span class="ml-1 font-normal text-gray-400">$</span>
          </label>
          <div class="mt-1 w-48">
            <input :value="entry.per_request_price" @input="emitField('per_request_price', ($event.target as HTMLInputElement).value)"
              type="number" step="any" min="0" class="input text-sm" :placeholder="t('admin.channels.form.pricePlaceholder', '默认')" />
          </div>

          <!-- Tiers -->
          <div v-if="!compact || advancedOpen" class="mt-3 flex items-center justify-between">
            <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
              {{ t('admin.channels.form.requestTiers', '按次计费层级') }}
            </label>
            <button type="button" @click="addInterval" class="text-xs text-primary-600 hover:text-primary-700">
              + {{ t('admin.channels.form.addTier', '添加层级') }}
            </button>
          </div>
          <div v-if="(!compact || advancedOpen) && entry.intervals && entry.intervals.length > 0" class="mt-2 space-y-2">
            <IntervalRow
              v-for="(iv, idx) in entry.intervals"
              :key="idx"
              :interval="iv"
              :mode="entry.billing_mode"
              @update="updateInterval(idx, $event)"
              @remove="removeInterval(idx)"
            />
          </div>
          <div v-else-if="!compact || advancedOpen" class="mt-2 rounded border border-dashed border-gray-300 p-3 text-center text-xs text-gray-400 dark:border-dark-500">
            {{ t('admin.channels.form.noTiersYet', '暂无层级，点击添加配置按次计费价格') }}
          </div>
        </div>

        <!-- Image mode -->
        <div v-else-if="entry.billing_mode === 'image'">
          <!-- Default image price (per-request, same as per_request mode) -->
          <label class="mt-3 block text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.channels.form.defaultImagePrice', '默认图片价格（未命中层级时使用）') }}
            <span class="ml-1 font-normal text-gray-400">$</span>
          </label>
          <div class="mt-1 w-48">
            <input :value="entry.per_request_price" @input="emitField('per_request_price', ($event.target as HTMLInputElement).value)"
              type="number" step="any" min="0" class="input text-sm" :placeholder="t('admin.channels.form.pricePlaceholder', '默认')" />
          </div>

          <!-- Image tiers -->
          <div v-if="!compact || advancedOpen" class="mt-3 flex items-center justify-between">
            <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
              {{ t('admin.channels.form.imageTiers', '图片计费层级（按次）') }}
            </label>
            <button type="button" @click="addImageTier" class="text-xs text-primary-600 hover:text-primary-700">
              + {{ t('admin.channels.form.addTier', '添加层级') }}
            </button>
          </div>
          <div v-if="(!compact || advancedOpen) && entry.intervals && entry.intervals.length > 0" class="mt-2 space-y-2">
            <IntervalRow
              v-for="(iv, idx) in entry.intervals"
              :key="idx"
              :interval="iv"
              :mode="entry.billing_mode"
              @update="updateInterval(idx, $event)"
              @remove="removeInterval(idx)"
            />
          </div>
        </div>

        <!-- Video mode: the price is charged for every generated second. -->
        <div v-else-if="entry.billing_mode === 'video'">
          <label class="mt-3 block text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.channels.form.defaultVideoPrice', '默认视频价格') }}
            <span class="ml-1 font-normal text-gray-400">$ / {{ t('admin.channels.form.secondUnit', '秒') }}</span>
          </label>
          <div class="mt-1 w-full sm:w-56">
            <input
              :value="entry.per_request_price"
              type="number"
              step="any"
              min="0"
              class="input text-sm"
              :placeholder="t('admin.channels.form.videoDefaultPlaceholder', '未配置分辨率时使用')"
              @input="emitField('per_request_price', ($event.target as HTMLInputElement).value)"
            />
          </div>

          <div class="mt-3 rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-700">
            <div>
              <p class="text-xs font-medium text-gray-600 dark:text-gray-300">
                {{ t('admin.channels.form.videoResolutionPrices', '分辨率每秒价格') }}
              </p>
              <p class="mt-1 text-[11px] leading-5 text-gray-400">
                {{ t('admin.channels.form.videoResolutionPricesHint', '按视频最终分辨率计费；留空时使用上方默认价格。单位：美元 / 生成秒。') }}
              </p>
            </div>
            <div class="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-3">
              <label
                v-for="tier in videoTiers"
                :key="tier.value"
                class="block rounded-lg border border-gray-200 bg-gray-50 p-2.5 dark:border-dark-500 dark:bg-dark-800"
              >
                <span class="flex items-center justify-between gap-2 text-xs font-medium text-gray-600 dark:text-gray-300">
                  <span>{{ tier.label }}</span>
                  <span class="font-normal text-gray-400">$ / {{ t('admin.channels.form.secondUnit', '秒') }}</span>
                </span>
                <input
                  :value="videoTierPrice(tier.value)"
                  type="number"
                  step="any"
                  min="0"
                  :data-testid="`video-price-${tier.value}`"
                  class="input mt-1.5 text-sm"
                  :placeholder="t('admin.channels.form.pricePlaceholder', '默认')"
                  @input="updateVideoTier(tier.value, ($event.target as HTMLInputElement).value)"
                />
              </label>
            </div>
          </div>
        </div>

        <button
          v-if="compact && entry.billing_mode !== 'video'"
          type="button"
          class="mt-3 inline-flex items-center gap-1 text-xs font-medium text-gray-500 transition-colors hover:text-primary-600 dark:text-gray-400 dark:hover:text-primary-400"
          :aria-expanded="advancedOpen"
          @click="advancedOpen = !advancedOpen"
        >
          <Icon :name="advancedOpen ? 'chevronUp' : 'chevronDown'" size="xs" />
          {{ advancedOpen
            ? t('admin.groups.pricing.hideEntryAdvanced', '收起高级价格')
            : t('admin.groups.pricing.showEntryAdvanced', '缓存、图片 Token 与分层价格') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, getCurrentInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import IntervalRow from './IntervalRow.vue'
import ModelTagInput from './ModelTagInput.vue'
import type { PricingFormEntry, IntervalFormEntry } from './types'
import { perTokenToMTok, getPlatformTagClass } from './types'
import type { BillingMode } from '@/api/admin/channels'
import channelsAPI from '@/api/admin/channels'

const { t } = useI18n()

const props = defineProps<{
  entry: PricingFormEntry
  platform?: string
  compact?: boolean
}>()

const emit = defineEmits<{
  update: [entry: PricingFormEntry]
  remove: []
}>()

// Collapse state: entries with existing models default to collapsed
const collapsed = ref(props.entry.models.length > 0)
const advancedOpen = ref(false)
const collapsibleContentId = `pricing-entry-content-${getCurrentInstance()?.uid ?? 'unknown'}`

const isComposite = computed(() => props.platform === 'composite')
const effectivePlatform = computed(() => {
  if (isComposite.value) return String(props.entry.platform || '')
  return String(props.platform || props.entry.platform || '')
})

const platformOptions = computed(() => [...CONCRETE_PLATFORM_OPTIONS])

const platformLabel = computed(() =>
  platformOptions.value.find(option => option.value === props.entry.platform)?.label || props.entry.platform || '',
)

const billingModeOptions = computed(() => [
  { value: 'token', label: 'Token' },
  { value: 'per_request', label: t('admin.channels.billingMode.perRequest', '按次') },
  { value: 'image', label: t('admin.channels.billingMode.image', '图片（按次）') },
  { value: 'video', label: t('admin.channels.billingMode.video', '视频（按秒）') },
])

const videoTiers = [
  { value: '480p', label: '480p' },
  { value: '720p', label: '720p' },
  { value: '1080p', label: '1080p' },
] as const

const billingModeLabel = computed(() => {
  const opt = billingModeOptions.value.find(o => o.value === props.entry.billing_mode)
  return opt ? opt.label : props.entry.billing_mode
})

function emitField(field: keyof PricingFormEntry, value: string) {
  emit('update', { ...props.entry, [field]: value === '' ? null : value })
}

function updatePlatform(value: string | number | boolean | null) {
  const platform = typeof value === 'string' && platformOptions.value.some(option => option.value === value)
    ? value
    : undefined
  emit('update', { ...props.entry, platform })
}

function updateBillingMode(mode: BillingMode) {
  emit('update', { ...props.entry, billing_mode: mode, intervals: [] })
}

function videoTierPrice(tierLabel: string): number | string {
  const interval = (props.entry.intervals || []).find(
    item => item.tier_label.trim().toLowerCase() === tierLabel.toLowerCase(),
  )
  return interval?.per_request_price ?? ''
}

function updateVideoTier(tierLabel: string, value: string) {
  const intervals = [...(props.entry.intervals || [])]
  const index = intervals.findIndex(
    item => item.tier_label.trim().toLowerCase() === tierLabel.toLowerCase(),
  )
  const perRequestPrice = value === '' ? null : value

  if (index >= 0) {
    intervals[index] = { ...intervals[index], tier_label: tierLabel, per_request_price: perRequestPrice }
  } else {
    intervals.push({
      min_tokens: 0,
      max_tokens: null,
      tier_label: tierLabel,
      input_price: null,
      output_price: null,
      cache_write_price: null,
      cache_read_price: null,
      input_multiplier: null,
      output_multiplier: null,
      cache_write_multiplier: null,
      cache_read_multiplier: null,
      per_request_price: perRequestPrice,
      sort_order: videoTiers.findIndex(tier => tier.value === tierLabel),
    })
  }

  emit('update', { ...props.entry, intervals })
}

function addInterval() {
  const intervals = [...(props.entry.intervals || [])]
  intervals.push({
    min_tokens: 0, max_tokens: null, tier_label: '',
    input_price: null, output_price: null, cache_write_price: null,
    cache_read_price: null, input_multiplier: null, output_multiplier: null,
    cache_write_multiplier: null, cache_read_multiplier: null, per_request_price: null,
    sort_order: intervals.length
  })
  emit('update', { ...props.entry, intervals })
}

function addImageTier() {
  const intervals = [...(props.entry.intervals || [])]
  const labels = ['1K', '2K', '4K', 'HD']
  intervals.push({
    min_tokens: 0, max_tokens: null, tier_label: labels[intervals.length] || '',
    input_price: null, output_price: null, cache_write_price: null,
    cache_read_price: null, input_multiplier: null, output_multiplier: null,
    cache_write_multiplier: null, cache_read_multiplier: null, per_request_price: null,
    sort_order: intervals.length
  })
  emit('update', { ...props.entry, intervals })
}

function setTierDefaults() {
  emit('update', { ...props.entry, fast_multiplier: 2, flex_multiplier: 0.5 })
}

function clearTierMultipliers() {
  emit('update', { ...props.entry, fast_multiplier: null, flex_multiplier: null })
}

function updateInterval(idx: number, updated: IntervalFormEntry) {
  const intervals = [...(props.entry.intervals || [])]
  intervals[idx] = updated
  emit('update', { ...props.entry, intervals })
}

function removeInterval(idx: number) {
  const intervals = [...(props.entry.intervals || [])]
  intervals.splice(idx, 1)
  emit('update', { ...props.entry, intervals })
}

async function onModelsUpdate(newModels: string[]) {
  const oldModels = props.entry.models
  emit('update', { ...props.entry, models: newModels })

  // 只在新增模型且当前无价格时自动填充
  const addedModels = newModels.filter(m => !oldModels.includes(m))
  if (addedModels.length === 0) return

  // 检查是否所有价格字段都为空
  const e = props.entry
  const hasPrice = e.input_price != null || e.output_price != null ||
                   e.cache_write_price != null || e.cache_read_price != null ||
                   e.image_input_price != null || e.image_output_price != null
  if (hasPrice) return

  // 查询第一个新增模型的默认价格
  try {
    const result = await channelsAPI.getModelDefaultPricing(addedModels[0])
    if (result.found) {
      emit('update', {
        ...props.entry,
        models: newModels,
        input_price: perTokenToMTok(result.input_price ?? null),
        output_price: perTokenToMTok(result.output_price ?? null),
        cache_write_price: perTokenToMTok(result.cache_write_price ?? null),
        cache_read_price: perTokenToMTok(result.cache_read_price ?? null),
        image_input_price: perTokenToMTok(result.image_input_price ?? null),
        image_output_price: perTokenToMTok(result.image_output_price ?? null),
      })
    }
  } catch {
    // 查询失败不影响用户操作
  }
}
</script>

<style scoped>
.collapsible-content {
  display: grid;
  grid-template-rows: 1fr;
  transition: grid-template-rows 0.25s ease;
}

.collapsible-content--collapsed {
  grid-template-rows: 0fr;
}

.collapsible-inner {
  overflow: hidden;
}
</style>
