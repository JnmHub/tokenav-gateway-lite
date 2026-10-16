<template>
  <section class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-600 dark:bg-dark-800/60">
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-2">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-gray-100">
            {{ t('admin.groups.pricing.title') }}
          </h4>
          <span
            class="rounded-md px-2 py-0.5 text-[11px] font-medium"
            :class="modelValue.enabled
              ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
              : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'"
          >
            {{ modelValue.enabled
              ? t('admin.groups.pricing.customBadge')
              : t('admin.groups.pricing.inheritedBadge') }}
          </span>
        </div>
        <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
          {{ modelValue.enabled
            ? t('admin.groups.pricing.customHint')
            : t('admin.groups.pricing.inheritedHint') }}
        </p>
      </div>
      <Toggle
        :model-value="modelValue.enabled"
        :aria-label="t('admin.groups.pricing.customToggle')"
        @update:model-value="setEnabled"
      />
    </div>

    <div v-if="loading" class="mt-4 rounded-lg bg-gray-50 px-3 py-4 text-center text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
      {{ t('admin.groups.pricing.loading') }}
    </div>

    <template v-else-if="modelValue.enabled">
      <div v-if="shared" class="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-800 dark:border-amber-800/60 dark:bg-amber-950/20 dark:text-amber-300">
        {{ t('admin.groups.pricing.sharedHint') }}
      </div>

      <div class="mt-4">
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <p class="text-xs font-semibold text-gray-800 dark:text-gray-200">
              {{ t('admin.groups.pricing.rulesTitle') }}
            </p>
            <p class="mt-1 text-[11px] text-gray-500 dark:text-gray-400">
              {{ t('admin.groups.pricing.rulesHint') }}
            </p>
          </div>
          <div class="grid grid-cols-2 gap-1.5 sm:flex">
            <button
              v-for="mode in billingModes"
              :key="mode.value"
              type="button"
              class="inline-flex min-h-9 items-center justify-center rounded-lg border px-2.5 text-xs font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/30"
              :class="mode.className"
              @click="addPricing(mode.value)"
            >
              <Icon :name="mode.icon" size="xs" class="mr-1" />
              {{ mode.label }}
            </button>
          </div>
        </div>

        <div v-if="modelValue.model_pricing.length" class="mt-3 space-y-2">
          <PricingEntryCard
            v-for="(entry, index) in modelValue.model_pricing"
            :key="index"
            :entry="entry"
            :platform="platform"
            compact
            @update="updatePricing(index, $event)"
            @remove="removePricing(index)"
          />
        </div>
        <div v-else class="mt-3 rounded-lg border border-dashed border-gray-300 bg-gray-50/70 p-4 text-center dark:border-dark-600 dark:bg-dark-800">
          <p class="text-xs font-medium text-gray-600 dark:text-gray-300">
            {{ t('admin.groups.pricing.emptyTitle') }}
          </p>
          <p class="mt-1 text-[11px] text-gray-400">
            {{ t('admin.groups.pricing.emptyHint') }}
          </p>
        </div>
      </div>

      <div class="mt-4 border-t border-gray-100 pt-3 dark:border-dark-700">
        <button
          type="button"
          class="flex w-full items-center justify-between gap-3 rounded-lg px-1 py-1.5 text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/30"
          :aria-expanded="advancedOpen"
          @click="advancedOpen = !advancedOpen"
        >
          <span>
            <span class="block text-xs font-semibold text-gray-700 dark:text-gray-200">
              {{ t('admin.groups.pricing.advancedTitle') }}
            </span>
            <span class="mt-0.5 block text-[11px] text-gray-400">
              {{ t('admin.groups.pricing.advancedHint') }}
            </span>
          </span>
          <Icon :name="advancedOpen ? 'chevronUp' : 'chevronDown'" size="sm" class="shrink-0 text-gray-400" />
        </button>

        <div v-if="advancedOpen" class="mt-3 space-y-3">
          <PricingToggleRow
            :model-value="modelValue.restrict_models"
            :title="t('admin.groups.pricing.restrictModels')"
            :hint="t('admin.groups.pricing.restrictModelsHint')"
            @update:model-value="patch({ restrict_models: $event })"
          />
          <PricingToggleRow
            v-if="platform === 'anthropic'"
            :model-value="modelValue.web_search_emulation"
            :title="t('admin.channels.form.webSearchEmulation')"
            :hint="t('admin.channels.form.webSearchEmulationHint')"
            @update:model-value="patch({ web_search_emulation: $event })"
          />
          <PricingToggleRow
            v-if="platform === 'anthropic'"
            :model-value="modelValue.bedrock_cc_compat"
            :title="t('admin.channels.form.bedrockCCCompat')"
            :hint="t('admin.channels.form.bedrockCCCompatHint')"
            @update:model-value="patch({ bedrock_cc_compat: $event })"
          />
          <PricingToggleRow
            v-if="platform === 'openai'"
            :model-value="modelValue.codex_image_generation_bridge"
            :title="t('admin.channels.form.codexImageGenerationBridge')"
            :hint="t('admin.channels.form.codexImageGenerationBridgeHint')"
            @update:model-value="patch({ codex_image_generation_bridge: $event })"
          />
          <PricingToggleRow
            :model-value="modelValue.apply_pricing_to_account_stats"
            :title="t('admin.channels.form.applyPricingToAccountStats')"
            :hint="t('admin.channels.form.applyPricingToAccountStatsDesc')"
            @update:model-value="patch({ apply_pricing_to_account_stats: $event })"
          />

          <div
            v-if="modelValue.apply_pricing_to_account_stats"
            class="rounded-lg border border-gray-200 bg-gray-50 p-3 dark:border-dark-600 dark:bg-dark-800"
          >
            <div class="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
              <div>
                <p class="text-xs font-semibold text-gray-700 dark:text-gray-200">
                  {{ t('admin.groups.pricing.accountRulesTitle') }}
                </p>
                <p class="mt-1 text-[11px] leading-5 text-gray-500 dark:text-gray-400">
                  {{ t('admin.groups.pricing.accountRulesHint') }}
                </p>
              </div>
              <button type="button" class="btn btn-secondary min-h-9 text-xs" @click="addAccountRule">
                <Icon name="plus" size="xs" class="mr-1" />
                {{ t('admin.groups.pricing.addAccountRule') }}
              </button>
            </div>

            <div v-if="modelValue.account_stats_pricing_rules.length" class="mt-3 space-y-3">
              <div
                v-for="(rule, ruleIndex) in modelValue.account_stats_pricing_rules"
                :key="ruleIndex"
                class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-700"
              >
                <div class="flex items-center gap-2">
                  <input
                    :value="rule.name"
                    class="input min-w-0 flex-1 text-sm"
                    :placeholder="t('admin.groups.pricing.ruleNamePlaceholder')"
                    @input="patchAccountRule(ruleIndex, { name: ($event.target as HTMLInputElement).value })"
                  />
                  <button
                    type="button"
                    class="rounded-lg p-2 text-gray-400 hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-950/20"
                    :aria-label="t('common.delete')"
                    @click="removeAccountRule(ruleIndex)"
                  >
                    <Icon name="trash" size="sm" />
                  </button>
                </div>

                <div class="mt-3">
                  <p class="text-[11px] font-medium text-gray-500 dark:text-gray-400">
                    {{ t('admin.groups.pricing.ruleScope') }}
                  </p>
                  <div class="mt-2 grid grid-cols-2 gap-2">
                    <button
                      v-for="scope in accountRuleScopes"
                      :key="scope.value"
                      type="button"
                      class="min-h-9 rounded-lg border px-2 text-xs font-medium transition-colors"
                      :class="rule.scope === scope.value
                        ? 'border-primary-300 bg-primary-50 text-primary-700 dark:border-primary-700 dark:bg-primary-950/30 dark:text-primary-300'
                        : 'border-gray-200 bg-white text-gray-600 hover:border-gray-300 dark:border-dark-500 dark:bg-dark-700 dark:text-gray-300'"
                      @click="setRuleScope(ruleIndex, scope.value)"
                    >
                      {{ scope.label }}
                    </button>
                  </div>
                </div>

                <div v-if="rule.scope === 'selected_accounts'" class="mt-3">
                  <p class="text-[11px] font-medium text-gray-500 dark:text-gray-400">
                    {{ t('admin.groups.pricing.ruleAccounts') }}
                  </p>
                  <div v-if="rule.account_ids.length" class="mt-1.5 flex flex-wrap gap-1.5">
                    <span
                      v-for="accountID in rule.account_ids"
                      :key="accountID"
                      class="inline-flex items-center gap-1 rounded-md bg-primary-50 px-2 py-1 text-xs text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                    >
                      {{ accountLabel(accountID) }}
                      <button type="button" :aria-label="t('common.remove')" @click="removeRuleAccount(ruleIndex, accountID)">
                        <Icon name="x" size="xs" />
                      </button>
                    </span>
                  </div>
                  <div class="relative mt-2">
                    <input
                      v-model="accountQueries[ruleIndex]"
                      type="search"
                      class="input text-sm"
                      :placeholder="t('admin.groups.pricing.searchAccount')"
                      @focus="searchAccounts(ruleIndex)"
                      @input="searchAccounts(ruleIndex)"
                    />
                    <div
                      v-if="accountResults[ruleIndex]?.length"
                      class="absolute z-20 mt-1 max-h-44 w-full overflow-y-auto rounded-lg border border-gray-200 bg-white p-1 shadow-lg dark:border-dark-600 dark:bg-dark-700"
                    >
                      <button
                        v-for="account in accountResults[ruleIndex]"
                        :key="account.id"
                        type="button"
                        class="flex w-full items-center justify-between gap-2 rounded-md px-2 py-2 text-left text-xs hover:bg-gray-50 disabled:opacity-40 dark:hover:bg-dark-600"
                        :disabled="rule.account_ids.includes(account.id)"
                        @click="addRuleAccount(ruleIndex, account)"
                      >
                        <span class="truncate text-gray-700 dark:text-gray-200">{{ account.name }}</span>
                        <span class="shrink-0 text-gray-400">#{{ account.id }}</span>
                      </button>
                    </div>
                  </div>
                  <p class="mt-1 text-[11px] text-gray-400">
                    {{ t('admin.groups.pricing.selectedAccountsHint') }}
                  </p>
                </div>

                <p v-else class="mt-2 text-[11px] text-gray-400">
                  {{ t('admin.groups.pricing.allAccountsHint') }}
                </p>

                <div class="mt-3 flex flex-wrap gap-1.5">
                  <button
                    v-for="mode in billingModes"
                    :key="mode.value"
                    type="button"
                    class="rounded-lg border border-gray-200 px-2 py-1.5 text-[11px] font-medium text-gray-600 hover:border-primary-300 hover:text-primary-700 dark:border-dark-500 dark:text-gray-300 dark:hover:border-primary-700 dark:hover:text-primary-300"
                    @click="addRulePricing(ruleIndex, mode.value)"
                  >
                    + {{ mode.label }}
                  </button>
                </div>
                <div v-if="rule.pricing.length" class="mt-2 space-y-2">
                  <PricingEntryCard
                    v-for="(entry, pricingIndex) in rule.pricing"
                    :key="pricingIndex"
                    :entry="entry"
                    :platform="platform"
                    compact
                    @update="updateRulePricing(ruleIndex, pricingIndex, $event)"
                    @remove="removeRulePricing(ruleIndex, pricingIndex)"
                  />
                </div>
              </div>
            </div>
            <p v-else class="mt-3 text-center text-xs text-gray-400">
              {{ t('admin.groups.pricing.noAccountRules') }}
            </p>
          </div>
        </div>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account, GroupPlatform } from '@/types'
import type { BillingMode } from '@/api/admin/channels'
import { adminAPI } from '@/api/admin'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import PricingEntryCard from '@/components/admin/channel/PricingEntryCard.vue'
import type { PricingFormEntry } from '@/components/admin/channel/types'
import {
  createPricingEntry,
  type GroupAccountPricingRuleForm,
  type GroupPricingFormState,
} from './groupPricing'

const props = defineProps<{
  modelValue: GroupPricingFormState
  platform: GroupPlatform
  groupId?: number
  loading?: boolean
  shared?: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [state: GroupPricingFormState]
}>()

const { t } = useI18n()
const advancedOpen = ref(false)
const accountQueries = reactive<Record<number, string>>({})
const accountResults = reactive<Record<number, Account[]>>({})
const knownAccounts = reactive<Record<number, Account>>({})
const searchTimers = new Map<number, ReturnType<typeof setTimeout>>()

const PricingToggleRow = defineComponent({
  props: {
    modelValue: { type: Boolean, required: true },
    title: { type: String, required: true },
    hint: { type: String, required: true },
  },
  emits: ['update:modelValue'],
  setup(rowProps, { emit: rowEmit }) {
    return () => h('div', { class: 'flex items-start justify-between gap-4 rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-700' }, [
      h('div', { class: 'min-w-0' }, [
        h('p', { class: 'text-xs font-medium text-gray-700 dark:text-gray-200' }, rowProps.title),
        h('p', { class: 'mt-1 text-[11px] leading-5 text-gray-500 dark:text-gray-400' }, rowProps.hint),
      ]),
      h(Toggle, {
        modelValue: rowProps.modelValue,
        'onUpdate:modelValue': (value: boolean) => rowEmit('update:modelValue', value),
      }),
    ])
  },
})

const billingModes = computed(() => [
  { value: 'token' as BillingMode, label: 'Token', icon: 'cpu' as const, className: 'border-primary-200 bg-primary-50 text-primary-700 hover:bg-primary-100 dark:border-primary-800 dark:bg-primary-950/30 dark:text-primary-300' },
  { value: 'per_request' as BillingMode, label: t('admin.channels.billingMode.perRequest'), icon: 'dollar' as const, className: 'border-gray-200 bg-white text-gray-700 hover:border-primary-300 dark:border-dark-500 dark:bg-dark-700 dark:text-gray-200' },
  { value: 'image' as BillingMode, label: t('admin.channels.billingMode.image'), icon: 'sparkles' as const, className: 'border-gray-200 bg-white text-gray-700 hover:border-primary-300 dark:border-dark-500 dark:bg-dark-700 dark:text-gray-200' },
  { value: 'video' as BillingMode, label: t('admin.channels.billingMode.video'), icon: 'play' as const, className: 'border-gray-200 bg-white text-gray-700 hover:border-primary-300 dark:border-dark-500 dark:bg-dark-700 dark:text-gray-200' },
])

const accountRuleScopes = computed(() => [
  { value: 'whole_group' as const, label: t('admin.groups.pricing.wholeGroup') },
  { value: 'selected_accounts' as const, label: t('admin.groups.pricing.selectedAccounts') },
])

function patch(values: Partial<GroupPricingFormState>) {
  emit('update:modelValue', { ...props.modelValue, ...values })
}

function setEnabled(enabled: boolean) {
  patch({ enabled })
}

function addPricing(mode: BillingMode) {
  const entryPlatform = props.platform === 'composite' ? undefined : props.platform
  patch({ model_pricing: [...props.modelValue.model_pricing, createPricingEntry(mode, entryPlatform)] })
}

function updatePricing(index: number, entry: PricingFormEntry) {
  const entries = [...props.modelValue.model_pricing]
  entries[index] = entry
  patch({ model_pricing: entries })
}

function removePricing(index: number) {
  const entries = [...props.modelValue.model_pricing]
  entries.splice(index, 1)
  patch({ model_pricing: entries })
}

function updateAccountRules(rules: GroupAccountPricingRuleForm[]) {
  patch({ account_stats_pricing_rules: rules })
}

function addAccountRule() {
  updateAccountRules([
    ...props.modelValue.account_stats_pricing_rules,
    { name: '', scope: 'whole_group', account_ids: [], pricing: [] },
  ])
}

function setRuleScope(index: number, scope: GroupAccountPricingRuleForm['scope']) {
  patchAccountRule(index, {
    scope,
    account_ids: scope === 'whole_group'
      ? []
      : props.modelValue.account_stats_pricing_rules[index].account_ids,
  })
}

function patchAccountRule(index: number, values: Partial<GroupAccountPricingRuleForm>) {
  const rules = props.modelValue.account_stats_pricing_rules.map((rule, ruleIndex) =>
    ruleIndex === index ? { ...rule, ...values } : rule,
  )
  updateAccountRules(rules)
}

function removeAccountRule(index: number) {
  const rules = [...props.modelValue.account_stats_pricing_rules]
  rules.splice(index, 1)
  updateAccountRules(rules)
}

function addRulePricing(ruleIndex: number, mode: BillingMode) {
  const rule = props.modelValue.account_stats_pricing_rules[ruleIndex]
  const entryPlatform = props.platform === 'composite' ? undefined : props.platform
  patchAccountRule(ruleIndex, { pricing: [...rule.pricing, createPricingEntry(mode, entryPlatform)] })
}

function updateRulePricing(ruleIndex: number, pricingIndex: number, entry: PricingFormEntry) {
  const pricing = [...props.modelValue.account_stats_pricing_rules[ruleIndex].pricing]
  pricing[pricingIndex] = entry
  patchAccountRule(ruleIndex, { pricing })
}

function removeRulePricing(ruleIndex: number, pricingIndex: number) {
  const pricing = [...props.modelValue.account_stats_pricing_rules[ruleIndex].pricing]
  pricing.splice(pricingIndex, 1)
  patchAccountRule(ruleIndex, { pricing })
}

function accountLabel(accountID: number) {
  return knownAccounts[accountID]?.name || `#${accountID}`
}

function searchAccounts(ruleIndex: number) {
  const existingTimer = searchTimers.get(ruleIndex)
  if (existingTimer) clearTimeout(existingTimer)
  searchTimers.set(ruleIndex, setTimeout(async () => {
    try {
      const result = await adminAPI.accounts.list(1, 20, {
        platform: props.platform === 'composite' ? undefined : props.platform,
        group: props.groupId ? String(props.groupId) : undefined,
        search: accountQueries[ruleIndex]?.trim() || undefined,
        lite: 'true',
      })
      accountResults[ruleIndex] = result.items || []
      for (const account of result.items || []) knownAccounts[account.id] = account
    } catch {
      accountResults[ruleIndex] = []
    }
  }, 180))
}

function addRuleAccount(ruleIndex: number, account: Account) {
  knownAccounts[account.id] = account
  const rule = props.modelValue.account_stats_pricing_rules[ruleIndex]
  if (!rule.account_ids.includes(account.id)) {
    patchAccountRule(ruleIndex, { account_ids: [...rule.account_ids, account.id] })
  }
  accountQueries[ruleIndex] = ''
  accountResults[ruleIndex] = []
}

function removeRuleAccount(ruleIndex: number, accountID: number) {
  const rule = props.modelValue.account_stats_pricing_rules[ruleIndex]
  patchAccountRule(ruleIndex, { account_ids: rule.account_ids.filter(id => id !== accountID) })
}
</script>
