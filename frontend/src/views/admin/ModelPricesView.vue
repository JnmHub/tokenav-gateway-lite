<template>
  <AppLayout>
    <TablePageLayout>
      <template #actions>
        <section
          data-test="model-price-catalog-status"
          class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:p-5"
        >
          <div class="flex min-w-0 flex-col gap-4 xl:flex-row xl:items-center xl:justify-between">
            <div class="flex min-w-0 items-start gap-3">
              <div class="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg bg-emerald-50 text-emerald-600 dark:bg-emerald-900/20 dark:text-emerald-300">
                <Icon name="database" size="md" />
              </div>
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
                    {{ t('admin.modelPrices.catalogStatus.title') }}
                  </h2>
                  <span
                    class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium"
                    :class="catalogStatus?.enabled
                      ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
                      : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'"
                  >
                    <span class="h-1.5 w-1.5 rounded-full" :class="catalogStatus?.enabled ? 'bg-emerald-500' : 'bg-gray-400'"></span>
                    {{ catalogStatus?.enabled ? t('admin.modelPrices.catalogStatus.autoEnabled') : t('admin.modelPrices.catalogStatus.autoDisabled') }}
                  </span>
                </div>
                <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
                  {{ t('admin.modelPrices.catalogStatus.description') }}
                </p>
              </div>
            </div>

            <div class="grid min-w-0 grid-cols-2 gap-x-5 gap-y-3 sm:grid-cols-4 xl:flex xl:items-center xl:gap-6">
              <div class="min-w-0">
                <p class="text-[11px] font-medium text-gray-400 dark:text-gray-500">{{ t('admin.modelPrices.catalogStatus.source') }}</p>
                <p class="mt-0.5 truncate text-sm font-semibold text-gray-800 dark:text-gray-100" :title="catalogStatus?.source || '-'">
                  {{ catalogSourceLabel(catalogStatus?.source) }}
                </p>
              </div>
              <div class="min-w-0">
                <p class="text-[11px] font-medium text-gray-400 dark:text-gray-500">{{ t('admin.modelPrices.catalogStatus.modelCount') }}</p>
                <p class="mt-0.5 text-sm font-semibold tabular-nums text-gray-800 dark:text-gray-100">
                  {{ catalogStatusLoading && !catalogStatus ? '…' : formatCatalogCount(catalogStatus?.model_count) }}
                </p>
              </div>
              <div class="col-span-2 min-w-0 sm:col-span-1 xl:min-w-[154px]">
                <p class="text-[11px] font-medium text-gray-400 dark:text-gray-500">{{ t('admin.modelPrices.catalogStatus.lastSuccess') }}</p>
                <p class="mt-0.5 truncate text-sm font-semibold text-gray-800 dark:text-gray-100" :title="formatCatalogTime(catalogStatus?.last_success_at)">
                  {{ formatCatalogTime(catalogStatus?.last_success_at) }}
                </p>
              </div>
              <button
                type="button"
                data-test="sync-model-price-catalog"
                class="btn btn-secondary col-span-2 inline-flex min-h-9 items-center justify-center gap-2 whitespace-nowrap sm:col-span-1"
                :disabled="catalogSyncing || catalogStatus?.refreshing"
                @click="syncCatalog"
              >
                <Icon name="sync" size="sm" :class="catalogSyncing || catalogStatus?.refreshing ? 'animate-spin' : ''" />
                {{ catalogSyncing || catalogStatus?.refreshing ? t('admin.modelPrices.catalogStatus.syncing') : t('admin.modelPrices.catalogStatus.syncNow') }}
              </button>
            </div>
          </div>

          <div
            v-if="catalogStatus?.last_error"
            data-test="model-price-catalog-error"
            class="mt-4 flex min-w-0 items-start gap-2 rounded-lg bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-700 dark:bg-amber-900/15 dark:text-amber-300"
          >
            <Icon name="exclamationTriangle" size="sm" class="mt-0.5 flex-shrink-0" />
            <div class="min-w-0">
              <span class="font-medium">{{ t('admin.modelPrices.catalogStatus.lastError') }}</span>
              <span class="break-words">{{ catalogStatus.last_error }}</span>
              <span v-if="catalogStatus.last_error_at" class="ml-1 whitespace-nowrap text-amber-600/80 dark:text-amber-300/70">
                · {{ formatCatalogTime(catalogStatus.last_error_at) }}
              </span>
            </div>
          </div>
        </section>
      </template>

      <template #filters>
        <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div class="flex flex-1 flex-col gap-3 sm:flex-row">
            <div class="relative max-w-md flex-1">
              <Icon name="search" class="pointer-events-none absolute left-3 top-1/2 h-5 w-5 -translate-y-1/2 text-gray-400" />
              <input
                v-model="searchQuery"
                type="text"
                class="input pl-10"
                :placeholder="t('admin.modelPrices.searchPlaceholder', '搜索模型或供应商...')"
                @input="handleSearchInput"
              />
            </div>
            <select v-model="providerFilter" class="input max-w-[180px]" @change="reloadFirstPage">
              <option value="">{{ t('admin.modelPrices.allProviders', '全部供应商') }}</option>
              <option value="xiaomi">Xiaomi</option>
              <option value="openai">OpenAI</option>
              <option value="anthropic">Anthropic</option>
              <option value="google">Google</option>
              <option value="custom">Custom</option>
            </select>
            <select v-model="sourceFilter" class="input max-w-[180px]" @change="reloadFirstPage">
              <option value="">{{ t('admin.modelPrices.allSources', '全部来源') }}</option>
              <option value="catalog">{{ t('admin.modelPrices.source.catalog', '默认目录') }}</option>
              <option value="override">{{ t('admin.modelPrices.source.override', '覆盖价') }}</option>
              <option value="static_fallback">{{ t('admin.modelPrices.source.staticFallback', '内置兜底') }}</option>
            </select>
          </div>
          <div class="flex items-center gap-2">
            <button class="btn btn-secondary" type="button" :disabled="loading" @click="reload">
              <Icon name="refresh" size="sm" />
            </button>
            <button class="btn btn-primary" type="button" @click="openCreateDialog">
              <Icon name="plus" size="sm" />
              {{ t('admin.modelPrices.create', '新增价格') }}
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="prices" :loading="loading">
          <template #cell-model="{ row }">
            <div class="min-w-[220px]">
              <div class="font-medium text-gray-900 dark:text-white">{{ row.model }}</div>
              <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ row.mode || 'chat' }}</div>
              <div v-if="row.supports_service_tier || row.long_context_input_token_threshold" class="mt-2 flex flex-wrap gap-1.5">
                <span v-if="row.supports_service_tier" class="rounded-md bg-violet-100 px-2 py-0.5 text-[11px] font-medium text-violet-700 dark:bg-violet-500/15 dark:text-violet-300">
                  {{ localText('优先处理', 'Priority') }}
                </span>
                <span v-if="row.long_context_input_token_threshold" class="rounded-md bg-cyan-100 px-2 py-0.5 text-[11px] font-medium text-cyan-700 dark:bg-cyan-500/15 dark:text-cyan-300">
                  {{ longContextLabel(row) }}
                </span>
              </div>
            </div>
          </template>

          <template #cell-provider="{ row }">
            <span class="inline-flex rounded-md bg-gray-100 px-2 py-1 text-xs font-medium text-gray-700 dark:bg-dark-700 dark:text-dark-200">
              {{ row.provider || 'custom' }}
            </span>
          </template>

          <template #cell-prices="{ row }">
            <div class="min-w-[430px] space-y-3 py-1 text-xs">
              <div v-if="standardPriceRows(row).length">
                <div class="mb-1.5 font-semibold text-gray-700 dark:text-dark-200">{{ localText('标准处理', 'Standard') }}</div>
                <div class="grid grid-cols-2 gap-x-4 gap-y-1">
                  <template v-for="item in standardPriceRows(row)" :key="item.label">
                    <span class="text-gray-500 dark:text-dark-400">{{ item.label }}</span>
                    <span class="text-right font-medium text-gray-900 dark:text-white">{{ formatMoney(item.value) }}</span>
                  </template>
                </div>
              </div>
              <div v-if="priorityPriceRows(row).length" class="border-t border-gray-100 pt-2 dark:border-dark-700">
                <div class="mb-1.5 font-semibold text-violet-700 dark:text-violet-300">{{ localText('优先处理', 'Priority') }}</div>
                <div class="grid grid-cols-2 gap-x-4 gap-y-1">
                  <template v-for="item in priorityPriceRows(row)" :key="item.label">
                    <span class="text-gray-500 dark:text-dark-400">{{ item.label }}</span>
                    <span class="text-right font-medium text-gray-900 dark:text-white">{{ formatMoney(item.value) }}</span>
                  </template>
                </div>
              </div>
              <div v-if="longContextPriceRows(row).length" class="border-t border-gray-100 pt-2 dark:border-dark-700">
                <div class="mb-1.5 font-semibold text-cyan-700 dark:text-cyan-300">{{ longContextLabel(row) }}</div>
                <div class="grid grid-cols-2 gap-x-4 gap-y-1">
                  <template v-for="item in longContextPriceRows(row)" :key="item.label">
                    <span class="text-gray-500 dark:text-dark-400">{{ item.label }}</span>
                    <span class="text-right font-medium text-gray-900 dark:text-white">{{ formatMoney(item.value) }}</span>
                  </template>
                </div>
                <p v-if="row.supports_service_tier" class="mt-1.5 text-[11px] text-gray-400 dark:text-dark-500">
                  {{ localText('优先处理模式暂不支持该长上下文档位', 'Priority is not available for this long-context tier') }}
                </p>
              </div>
            </div>
          </template>

          <template #cell-media="{ row }">
            <div v-if="videoPriceRows(row).length" class="min-w-[150px] space-y-1 text-xs">
              <div v-for="item in videoPriceRows(row)" :key="item.label" class="flex items-center justify-between gap-3">
                <span class="text-gray-500 dark:text-dark-400">{{ item.label }}</span>
                <span class="font-medium text-gray-900 dark:text-white">{{ formatMoney(item.value, false) }} / s</span>
              </div>
            </div>
            <div v-else-if="imagePriceRows(row).length" class="min-w-[150px] space-y-1 text-xs">
              <div v-for="item in imagePriceRows(row)" :key="item.label" class="flex items-center justify-between gap-3">
                <span class="text-gray-500 dark:text-dark-400">{{ item.label }}</span>
                <span class="font-medium text-gray-900 dark:text-white">{{ item.value === 0 ? '$0' : formatMoney(item.value, false) }}</span>
              </div>
            </div>
            <span v-else class="text-sm text-gray-500 dark:text-dark-400">-</span>
          </template>

          <template #cell-source="{ row }">
            <span class="inline-flex rounded-md px-2 py-1 text-xs font-medium" :class="sourceClass(row.source)">
              {{ sourceLabel(row.source) }}
            </span>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center gap-2">
              <button class="btn btn-secondary btn-sm" type="button" @click="openEditDialog(row)">
                {{ t('common.edit', '编辑') }}
              </button>
              <button
                v-if="row.is_override"
                class="btn btn-secondary btn-sm text-red-600 hover:text-red-700 dark:text-red-400"
                type="button"
                @click="confirmReset(row)"
              >
                {{ t('admin.modelPrices.resetOverride', '取消覆盖') }}
              </button>
            </div>
          </template>

          <template #empty>
            <EmptyState
              :title="t('admin.modelPrices.emptyTitle', '暂无模型价格')"
              :description="t('admin.modelPrices.emptyDescription', '新增覆盖价或检查默认价格目录是否加载成功')"
              :action-text="t('admin.modelPrices.create', '新增价格')"
              @action="openCreateDialog"
            />
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="onPageChange"
          @update:pageSize="onPageSizeChange"
        />
      </template>
    </TablePageLayout>

    <BaseDialog
      :show="showDialog"
      :title="editing ? t('admin.modelPrices.editTitle', '编辑模型价格') : t('admin.modelPrices.createTitle', '新增模型价格')"
      width="wide"
      @close="closeDialog"
    >
      <form class="space-y-5" @submit.prevent="savePrice">
        <div class="grid gap-4 md:grid-cols-2">
          <label class="space-y-1">
            <span class="input-label">{{ t('admin.modelPrices.model', '模型') }}</span>
            <input v-model.trim="form.model" class="input" :disabled="Boolean(editing)" placeholder="mimo-v2.5-pro" required />
          </label>
          <label class="space-y-1">
            <span class="input-label">{{ t('admin.modelPrices.provider', '供应商') }}</span>
            <input v-model.trim="form.provider" class="input" placeholder="xiaomi" />
          </label>
          <label class="space-y-1">
            <span class="input-label">{{ t('admin.modelPrices.mode', '模式') }}</span>
            <input v-model.trim="form.mode" class="input" placeholder="chat" />
          </label>
          <label class="space-y-1">
            <span class="input-label">{{ t('admin.modelPrices.billingMode', '计费方式') }}</span>
            <select v-model="form.billing_mode" class="input">
              <option value="token">Token</option>
              <option value="image">{{ localText('按图片', 'Per image') }}</option>
              <option value="video">{{ localText('按视频秒数', 'Per video second') }}</option>
              <option value="per_request">{{ localText('按请求次数', 'Per request') }}</option>
            </select>
          </label>
          <div v-if="form.billing_mode === 'token'" class="flex flex-col gap-3 pt-7">
            <label class="flex items-center gap-2">
              <input v-model="form.supports_prompt_caching" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600" />
              <span class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.modelPrices.promptCaching', '支持缓存计费') }}</span>
            </label>
            <label class="flex items-center gap-2">
              <input v-model="form.supports_service_tier" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600" />
              <span class="text-sm text-gray-700 dark:text-dark-200">{{ localText('支持 Priority（优先处理）', 'Supports Priority') }}</span>
            </label>
          </div>
        </div>

        <div v-if="form.billing_mode === 'token'">
          <div class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">{{ localText('标准处理价格', 'Standard prices') }}</div>
          <div class="grid gap-4 md:grid-cols-2">
            <PriceInput v-model="form.input_cost_per_1m_tokens" :label="localText('输入 / 1M Token', 'Input / 1M tokens')" />
            <PriceInput v-model="form.output_cost_per_1m_tokens" :label="localText('输出 / 1M Token', 'Output / 1M tokens')" />
            <PriceInput v-model="form.cache_read_cost_per_1m_tokens" :label="localText('缓存读取 / 1M Token', 'Cache read / 1M tokens')" />
            <PriceInput v-model="form.cache_creation_cost_per_1m_tokens" :label="localText('缓存写入 / 1M Token', 'Cache write / 1M tokens')" />
          </div>
        </div>

        <div v-if="form.billing_mode === 'token' && form.supports_service_tier" class="border-t border-gray-100 pt-5 dark:border-dark-700">
          <div class="mb-3 text-sm font-semibold text-violet-700 dark:text-violet-300">{{ localText('Priority（优先处理）价格', 'Priority prices') }}</div>
          <div class="grid gap-4 md:grid-cols-2">
            <PriceInput v-model="form.input_cost_per_1m_tokens_priority" :label="localText('优先输入 / 1M Token', 'Priority input / 1M tokens')" />
            <PriceInput v-model="form.output_cost_per_1m_tokens_priority" :label="localText('优先输出 / 1M Token', 'Priority output / 1M tokens')" />
            <PriceInput v-model="form.cache_read_cost_per_1m_tokens_priority" :label="localText('优先缓存读取 / 1M Token', 'Priority cache read / 1M tokens')" />
            <PriceInput v-model="form.cache_creation_cost_per_1m_tokens_priority" :label="localText('优先缓存写入 / 1M Token', 'Priority cache write / 1M tokens')" />
          </div>
        </div>

        <div v-if="form.billing_mode === 'token'" class="border-t border-gray-100 pt-5 dark:border-dark-700">
          <label class="mb-4 block max-w-sm space-y-1">
            <span class="input-label">{{ localText('长上下文阈值（Token）', 'Long-context threshold (tokens)') }}</span>
            <input v-model.number="form.long_context_input_token_threshold" class="input" min="0" step="1" type="number" placeholder="272000" />
          </label>
          <div v-if="Number(form.long_context_input_token_threshold || 0) > 0">
            <div class="mb-3 text-sm font-semibold text-cyan-700 dark:text-cyan-300">{{ localText('超过阈值后的标准价格', 'Standard prices above the threshold') }}</div>
            <div class="grid gap-4 md:grid-cols-2">
              <PriceInput v-model="form.input_cost_per_1m_tokens_above_272k" :label="localText('长上下文输入 / 1M Token', 'Long-context input / 1M tokens')" />
              <PriceInput v-model="form.output_cost_per_1m_tokens_above_272k" :label="localText('长上下文输出 / 1M Token', 'Long-context output / 1M tokens')" />
              <PriceInput v-model="form.cache_read_cost_per_1m_tokens_above_272k" :label="localText('长上下文缓存读取 / 1M Token', 'Long-context cache read / 1M tokens')" />
              <PriceInput v-model="form.cache_creation_cost_per_1m_tokens_above_272k" :label="localText('长上下文缓存写入 / 1M Token', 'Long-context cache write / 1M tokens')" />
            </div>
            <p v-if="form.supports_service_tier" class="mt-3 text-xs text-gray-500 dark:text-dark-400">
              {{ localText('OpenAI Priority 当前不支持长上下文，因此这里不提供长上下文 Priority 价格。', 'OpenAI Priority does not currently support long context, so no long-context Priority prices are configured here.') }}
            </p>
          </div>
        </div>

        <div v-if="form.billing_mode !== 'token'" class="border-t border-gray-100 pt-5 dark:border-dark-700">
          <div class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">
            {{ billingSectionTitle }}
          </div>
          <div class="grid gap-4 md:grid-cols-2">
            <template v-if="form.billing_mode === 'image'">
              <PriceInput v-model="form.output_cost_per_image" :label="t('admin.modelPrices.imagePrice', '图片单价')" />
              <PriceInput v-model="form.input_cost_per_image_token" :label="localText('图片输入 Token 单价', 'Image input token price')" />
              <PriceInput v-model="form.output_cost_per_image_token" :label="localText('图片输出 Token 单价', 'Image output token price')" />
            </template>
            <template v-else-if="form.billing_mode === 'video'">
              <PriceInput v-model="form.video_price_per_second_480p" :label="localText('480p 视频单价 / 秒', '480p video price / second')" />
              <PriceInput v-model="form.video_price_per_second_720p" :label="localText('720p 视频单价 / 秒', '720p video price / second')" />
              <PriceInput v-model="form.video_price_per_second_1080p" :label="localText('1080p 视频单价 / 秒', '1080p video price / second')" />
            </template>
            <PriceInput
              v-else
              v-model="form.per_request_price"
              :label="localText('按次单价', 'Per-request price')"
            />
          </div>
        </div>
      </form>

      <template #footer>
        <button class="btn btn-secondary" type="button" @click="closeDialog">{{ t('common.cancel', '取消') }}</button>
        <button class="btn btn-primary" type="button" :disabled="saving" @click="savePrice">
          {{ saving ? t('common.saving', '保存中...') : t('common.save', '保存') }}
        </button>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="showResetDialog"
      :title="t('admin.modelPrices.resetOverride', '取消覆盖')"
      :message="resetMessage"
      :confirm-text="t('admin.modelPrices.resetOverride', '取消覆盖')"
      :cancel-text="t('common.cancel', '取消')"
      :danger="true"
      @confirm="resetOverride"
      @cancel="showResetDialog = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, reactive, ref, type PropType } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { ModelPrice, ModelPriceSyncStatus, UpsertModelPriceRequest } from '@/api/admin'
import { extractApiErrorMessage } from '@/utils/apiError'
import { normalizeModelPricePayloadForBillingMode } from '@/utils/modelPriceBilling'
import { useAppStore } from '@/stores/app'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/GatewayLiteLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { formatDateTime } from '@/utils/format'

type PriceForm = {
  model: string
  provider: string
  mode: string
  billing_mode: 'token' | 'per_request' | 'image' | 'video'
  input_cost_per_1m_tokens: number | null
  input_cost_per_1m_tokens_priority: number | null
  output_cost_per_1m_tokens: number | null
  output_cost_per_1m_tokens_priority: number | null
  cache_creation_cost_per_1m_tokens: number | null
  cache_creation_cost_per_1m_tokens_priority: number | null
  cache_read_cost_per_1m_tokens: number | null
  cache_read_cost_per_1m_tokens_priority: number | null
  input_cost_per_1m_tokens_above_272k: number | null
  output_cost_per_1m_tokens_above_272k: number | null
  cache_creation_cost_per_1m_tokens_above_272k: number | null
  cache_read_cost_per_1m_tokens_above_272k: number | null
  long_context_input_token_threshold: number | null
  output_cost_per_image: number | null
  output_cost_per_image_token: number | null
  input_cost_per_image_token: number | null
  per_request_price: number | null
  video_price_per_second_480p: number | null
  video_price_per_second_720p: number | null
  video_price_per_second_1080p: number | null
  supports_prompt_caching: boolean
  supports_service_tier: boolean
}

const { t, locale } = useI18n()
const appStore = useAppStore()

const PriceInput = defineComponent({
  props: {
    modelValue: { type: Number as PropType<number | null>, default: null },
    label: { type: String, required: true }
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () => h('label', { class: 'space-y-1' }, [
      h('span', { class: 'input-label' }, props.label),
      h('input', {
        class: 'input',
        type: 'number',
        min: '0',
        step: '0.000001',
        value: props.modelValue ?? '',
        placeholder: '0',
        onInput: (event: Event) => {
          const value = (event.target as HTMLInputElement).value
          emit('update:modelValue', value === '' ? null : Number(value))
        }
      })
    ])
  }
})

const prices = ref<ModelPrice[]>([])
const loading = ref(false)
const catalogStatus = ref<ModelPriceSyncStatus | null>(null)
const catalogStatusLoading = ref(false)
const catalogSyncing = ref(false)
const saving = ref(false)
const searchQuery = ref('')
const providerFilter = ref('')
const sourceFilter = ref('')
const pagination = reactive({ page: 1, page_size: getPersistedPageSize(), total: 0 })
const showDialog = ref(false)
const editing = ref<ModelPrice | null>(null)
const showResetDialog = ref(false)
const resetting = ref<ModelPrice | null>(null)

const billingSectionTitle = computed(() => {
  if (form.billing_mode === 'image') return localText('图片价格', 'Image prices')
  if (form.billing_mode === 'video') return localText('视频价格', 'Video prices')
  return localText('按次价格', 'Per-request price')
})
let searchTimer: ReturnType<typeof setTimeout> | null = null
let abortController: AbortController | null = null

const form = reactive<PriceForm>(emptyForm())

const columns = computed<Column[]>(() => [
  { key: 'model', label: t('admin.modelPrices.columns.model', '模型'), sortable: false },
  { key: 'provider', label: t('admin.modelPrices.columns.provider', '供应商'), sortable: false },
  { key: 'prices', label: t('admin.modelPrices.columns.prices', 'Token 价格（USD / 1M）'), sortable: false },
  { key: 'media', label: t('admin.modelPrices.columns.media', '媒体价格'), sortable: false },
  { key: 'source', label: t('admin.modelPrices.columns.source', '来源'), sortable: false },
  { key: 'actions', label: t('admin.modelPrices.columns.actions', '操作'), sortable: false },
])

const resetMessage = computed(() => {
  const model = resetting.value?.model || ''
  return t('admin.modelPrices.resetConfirm', { model }, `确定取消 ${model} 的覆盖价吗？取消后会回退到默认价格。`)
})

function emptyForm(): PriceForm {
  return {
    model: '',
    provider: '',
    mode: 'chat',
    billing_mode: 'token',
    input_cost_per_1m_tokens: null,
    input_cost_per_1m_tokens_priority: null,
    output_cost_per_1m_tokens: null,
    output_cost_per_1m_tokens_priority: null,
    cache_creation_cost_per_1m_tokens: null,
    cache_creation_cost_per_1m_tokens_priority: null,
    cache_read_cost_per_1m_tokens: null,
    cache_read_cost_per_1m_tokens_priority: null,
    input_cost_per_1m_tokens_above_272k: null,
    output_cost_per_1m_tokens_above_272k: null,
    cache_creation_cost_per_1m_tokens_above_272k: null,
    cache_read_cost_per_1m_tokens_above_272k: null,
    long_context_input_token_threshold: null,
    output_cost_per_image: null,
    output_cost_per_image_token: null,
    input_cost_per_image_token: null,
    per_request_price: null,
    video_price_per_second_480p: null,
    video_price_per_second_720p: null,
    video_price_per_second_1080p: null,
    supports_prompt_caching: false,
    supports_service_tier: false
  }
}

function fillForm(price?: ModelPrice | null) {
  const next = price
    ? {
        model: price.model,
        provider: price.provider || 'custom',
        mode: price.mode || 'chat',
        billing_mode: normalizeBillingMode(price.billing_mode),
        input_cost_per_1m_tokens: price.input_cost_per_1m_tokens || null,
        input_cost_per_1m_tokens_priority: price.input_cost_per_1m_tokens_priority || null,
        output_cost_per_1m_tokens: price.output_cost_per_1m_tokens || null,
        output_cost_per_1m_tokens_priority: price.output_cost_per_1m_tokens_priority || null,
        cache_creation_cost_per_1m_tokens: price.cache_creation_cost_per_1m_tokens || null,
        cache_creation_cost_per_1m_tokens_priority: price.cache_creation_cost_per_1m_tokens_priority || null,
        cache_read_cost_per_1m_tokens: price.cache_read_cost_per_1m_tokens || null,
        cache_read_cost_per_1m_tokens_priority: price.cache_read_cost_per_1m_tokens_priority || null,
        input_cost_per_1m_tokens_above_272k: price.input_cost_per_1m_tokens_above_272k || null,
        output_cost_per_1m_tokens_above_272k: price.output_cost_per_1m_tokens_above_272k || null,
        cache_creation_cost_per_1m_tokens_above_272k: price.cache_creation_cost_per_1m_tokens_above_272k || null,
        cache_read_cost_per_1m_tokens_above_272k: price.cache_read_cost_per_1m_tokens_above_272k || null,
        long_context_input_token_threshold: price.long_context_input_token_threshold || null,
        output_cost_per_image: price.output_cost_per_image || null,
        output_cost_per_image_token: price.output_cost_per_image_token || null,
        input_cost_per_image_token: price.input_cost_per_image_token ?? null,
        per_request_price: price.per_request_price || null,
        video_price_per_second_480p: price.video_price_per_second_480p || null,
        video_price_per_second_720p: price.video_price_per_second_720p || null,
        video_price_per_second_1080p: price.video_price_per_second_1080p || null,
        supports_prompt_caching: price.supports_prompt_caching,
        supports_service_tier: price.supports_service_tier
      }
    : emptyForm()
  Object.assign(form, next)
}

async function loadPrices() {
  abortController?.abort()
  abortController = new AbortController()
  loading.value = true
  try {
    const res = await adminAPI.modelPrices.list({
      page: pagination.page,
      page_size: pagination.page_size,
      search: searchQuery.value || undefined,
      provider: providerFilter.value || undefined,
      source: sourceFilter.value || undefined
    }, { signal: abortController.signal })
    prices.value = res.items || []
    pagination.total = res.total || 0
  } catch (error: any) {
    if (error?.code !== 'ERR_CANCELED') {
      appStore.showError(extractApiErrorMessage(error, t('admin.modelPrices.loadError', '加载模型价格失败')))
    }
  } finally {
    loading.value = false
  }
}

function reload() {
  void loadPrices()
}

async function loadCatalogStatus(showError = false) {
  catalogStatusLoading.value = true
  try {
    catalogStatus.value = await adminAPI.modelPrices.getSyncStatus()
  } catch (error) {
    if (showError) {
      appStore.showError(extractApiErrorMessage(error, t('admin.modelPrices.catalogStatus.loadError')))
    }
  } finally {
    catalogStatusLoading.value = false
  }
}

async function syncCatalog() {
  catalogSyncing.value = true
  try {
    catalogStatus.value = await adminAPI.modelPrices.sync()
    appStore.showSuccess(t('admin.modelPrices.catalogStatus.syncSuccess'))
    await loadPrices()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.modelPrices.catalogStatus.syncError')))
    await loadCatalogStatus(false)
  } finally {
    catalogSyncing.value = false
  }
}

function reloadFirstPage() {
  pagination.page = 1
  reload()
}

function handleSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(reloadFirstPage, 300)
}

function onPageChange(page: number) {
  pagination.page = page
  reload()
}

function onPageSizeChange(pageSize: number) {
  pagination.page_size = pageSize
  pagination.page = 1
  reload()
}

function openCreateDialog() {
  editing.value = null
  fillForm(null)
  showDialog.value = true
}

function openEditDialog(price: ModelPrice) {
  editing.value = price
  fillForm(price)
  showDialog.value = true
}

function closeDialog() {
  showDialog.value = false
  editing.value = null
}

async function savePrice() {
  // 计费方式切换后不能把旧模式的隐藏字段继续提交，否则覆盖价格会混入无关计费数据。
  const payload: UpsertModelPriceRequest = normalizeModelPricePayloadForBillingMode(form)
  if (!payload.supports_service_tier) {
    payload.input_cost_per_1m_tokens_priority = null
    payload.output_cost_per_1m_tokens_priority = null
    payload.cache_creation_cost_per_1m_tokens_priority = null
    payload.cache_read_cost_per_1m_tokens_priority = null
  }
  if (!Number(payload.long_context_input_token_threshold || 0)) {
    payload.long_context_input_token_threshold = 0
    payload.input_cost_per_1m_tokens_above_272k = null
    payload.output_cost_per_1m_tokens_above_272k = null
    payload.cache_creation_cost_per_1m_tokens_above_272k = null
    payload.cache_read_cost_per_1m_tokens_above_272k = null
  }
  if (!payload.model) {
    appStore.showError(t('admin.modelPrices.modelRequired', '请填写模型名称'))
    return
  }
  if (
    payload.input_cost_per_1m_tokens === null &&
    payload.input_cost_per_1m_tokens_priority === null &&
    payload.output_cost_per_1m_tokens === null &&
    payload.output_cost_per_1m_tokens_priority === null &&
    payload.cache_creation_cost_per_1m_tokens === null &&
    payload.cache_creation_cost_per_1m_tokens_priority === null &&
    payload.cache_read_cost_per_1m_tokens === null &&
    payload.cache_read_cost_per_1m_tokens_priority === null &&
    payload.input_cost_per_1m_tokens_above_272k === null &&
    payload.output_cost_per_1m_tokens_above_272k === null &&
    payload.cache_creation_cost_per_1m_tokens_above_272k === null &&
    payload.cache_read_cost_per_1m_tokens_above_272k === null &&
    payload.output_cost_per_image === null &&
    payload.output_cost_per_image_token === null &&
    payload.input_cost_per_image_token === null &&
    payload.per_request_price === null &&
    payload.video_price_per_second_480p === null &&
    payload.video_price_per_second_720p === null &&
    payload.video_price_per_second_1080p === null
  ) {
    appStore.showError(t('admin.modelPrices.priceRequired', '请至少填写一个价格字段'))
    return
  }
  saving.value = true
  try {
    await adminAPI.modelPrices.upsert(payload)
    appStore.showSuccess(t('admin.modelPrices.saveSuccess', '模型价格已保存'))
    closeDialog()
    reload()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.modelPrices.saveError', '保存模型价格失败')))
  } finally {
    saving.value = false
  }
}

function confirmReset(price: ModelPrice) {
  resetting.value = price
  showResetDialog.value = true
}

async function resetOverride() {
  if (!resetting.value) return
  try {
    await adminAPI.modelPrices.removeOverride(resetting.value.model)
    appStore.showSuccess(t('admin.modelPrices.resetSuccess', '覆盖价已取消'))
    showResetDialog.value = false
    resetting.value = null
    reload()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.modelPrices.resetError', '取消覆盖失败')))
  }
}

function formatMoney(value: number | null | undefined, perMillion = true) {
  if (!value || value <= 0) return '-'
  const fixed = value >= 1 ? value.toFixed(4) : value.toFixed(6)
  return `$${trimZeros(fixed)}${perMillion ? '' : ''}`
}

function localText(zh: string, en: string) {
  return locale.value === 'en' ? en : zh
}

function compactThreshold(value: number | null | undefined) {
  const amount = Number(value || 0)
  if (amount >= 1000 && amount % 1000 === 0) return `${amount / 1000}K`
  return String(amount)
}

function longContextLabel(price: ModelPrice) {
  const threshold = compactThreshold(price.long_context_input_token_threshold)
  return localText(`>${threshold} 长上下文`, `>${threshold} long context`)
}

function positivePriceRows(rows: Array<{ label: string; value: number }>) {
  return rows.filter((item) => Number(item.value || 0) > 0)
}

function normalizeBillingMode(value: string): PriceForm['billing_mode'] {
  if (value === 'image' || value === 'video' || value === 'per_request') return value
  return 'token'
}

function videoPriceRows(price: ModelPrice) {
  return positivePriceRows([
    { label: '480p', value: price.video_price_per_second_480p },
    { label: '720p', value: price.video_price_per_second_720p },
    { label: '1080p', value: price.video_price_per_second_1080p }
  ])
}

function imagePriceRows(price: ModelPrice) {
  return [
    { label: localText('每张', 'Per image'), value: price.output_cost_per_image },
    { label: localText('输入 Token', 'Input token'), value: price.input_cost_per_image_token },
    { label: localText('输出 Token', 'Output token'), value: price.output_cost_per_image_token }
  ].filter((item): item is { label: string; value: number } => item.value != null && (item.value > 0 || item.label === localText('输入 Token', 'Input token')))
}

function standardPriceRows(price: ModelPrice) {
  return positivePriceRows([
    { label: localText('输入', 'Input'), value: price.input_cost_per_1m_tokens },
    { label: localText('输出', 'Output'), value: price.output_cost_per_1m_tokens },
    { label: localText('缓存读取', 'Cache read'), value: price.cache_read_cost_per_1m_tokens },
    { label: localText('缓存写入', 'Cache write'), value: price.cache_creation_cost_per_1m_tokens }
  ])
}

function priorityPriceRows(price: ModelPrice) {
  if (!price.supports_service_tier) return []
  return positivePriceRows([
    { label: localText('输入', 'Input'), value: price.input_cost_per_1m_tokens_priority },
    { label: localText('输出', 'Output'), value: price.output_cost_per_1m_tokens_priority },
    { label: localText('缓存读取', 'Cache read'), value: price.cache_read_cost_per_1m_tokens_priority },
    { label: localText('缓存写入', 'Cache write'), value: price.cache_creation_cost_per_1m_tokens_priority }
  ])
}

function longContextPriceRows(price: ModelPrice) {
  if (!Number(price.long_context_input_token_threshold || 0)) return []
  return positivePriceRows([
    { label: localText('输入', 'Input'), value: price.input_cost_per_1m_tokens_above_272k },
    { label: localText('输出', 'Output'), value: price.output_cost_per_1m_tokens_above_272k },
    { label: localText('缓存读取', 'Cache read'), value: price.cache_read_cost_per_1m_tokens_above_272k },
    { label: localText('缓存写入', 'Cache write'), value: price.cache_creation_cost_per_1m_tokens_above_272k }
  ])
}

function trimZeros(value: string) {
  return value.replace(/\.0+$/, '').replace(/(\.\d*?)0+$/, '$1')
}

function sourceLabel(source: string) {
  if (source === 'override') return t('admin.modelPrices.source.override', '覆盖价')
  if (source === 'static_fallback') return t('admin.modelPrices.source.staticFallback', '内置兜底')
  return t('admin.modelPrices.source.catalog', '默认目录')
}

function sourceClass(source: string) {
  if (source === 'override') return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300'
  if (source === 'static_fallback') return 'bg-cyan-100 text-cyan-700 dark:bg-cyan-500/15 dark:text-cyan-300'
  return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
}

function catalogSourceLabel(source?: string) {
  const normalized = String(source || '').trim().toLowerCase()
  if (!normalized) return '-'
  if (normalized === 'remote') return t('admin.modelPrices.catalogStatus.sourceRemote')
  if (normalized === 'cache' || normalized === 'local_cache') return t('admin.modelPrices.catalogStatus.sourceCache')
  if (normalized === 'embedded' || normalized === 'static_fallback') return t('admin.modelPrices.catalogStatus.sourceEmbedded')
  return source || '-'
}

function formatCatalogCount(value?: number) {
  if (typeof value !== 'number') return '-'
  return new Intl.NumberFormat(locale.value === 'en' ? 'en-US' : 'zh-CN').format(value)
}

function formatCatalogTime(value?: string | null) {
  return formatDateTime(value) || t('admin.modelPrices.catalogStatus.never')
}

onMounted(() => {
  void loadPrices()
  void loadCatalogStatus(false)
})
</script>
