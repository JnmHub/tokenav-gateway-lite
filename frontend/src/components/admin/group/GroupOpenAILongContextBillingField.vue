<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-1.5">
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.openaiLongContextBilling.title") }}
          </label>
          <div class="group relative inline-flex">
            <Icon
              name="questionCircle"
              size="sm"
              :stroke-width="2"
              class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
            />
            <div
              class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-80 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
            >
              <div class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800">
                <p class="text-xs leading-relaxed text-gray-300">
                  {{ t("admin.groups.openaiLongContextBilling.tooltip") }}
                </p>
                <div
                  class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                ></div>
              </div>
            </div>
          </div>
        </div>
        <p class="mt-1 text-xs leading-relaxed text-gray-500 dark:text-gray-400">
          {{ t("admin.groups.openaiLongContextBilling.description") }}
        </p>
      </div>

      <button
        type="button"
        role="switch"
        :aria-checked="modelValue"
        :aria-label="t('admin.groups.openaiLongContextBilling.title')"
        :disabled="!canEnable"
        :class="[
          'relative inline-flex h-6 w-11 flex-shrink-0 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50',
          modelValue ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600',
        ]"
        data-testid="group-openai-long-context-billing-toggle"
        @click="toggle"
      >
        <span
          :class="[
            'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
            modelValue ? 'translate-x-6' : 'translate-x-1',
          ]"
        />
      </button>
    </div>

    <div class="mt-3 flex flex-wrap gap-2">
      <span
        class="inline-flex items-center rounded-md bg-amber-50 px-2 py-1 text-xs font-medium text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
      >
        {{
          t("admin.groups.openaiLongContextBilling.longContextAccounts", {
            count: longContextAccountCount,
          })
        }}
      </span>
      <span
        class="inline-flex items-center rounded-md bg-emerald-50 px-2 py-1 text-xs font-medium text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300"
      >
        {{
          t("admin.groups.openaiLongContextBilling.standardAccounts", {
            count: standardAccountCount,
          })
        }}
      </span>
    </div>

    <p
      :class="[
        'mt-2 text-xs leading-relaxed',
        canEnable
          ? 'text-gray-500 dark:text-gray-400'
          : 'text-amber-700 dark:text-amber-300',
      ]"
    >
      {{ stateHint }}
    </p>
    <p
      v-if="usesCopiedGroups"
      class="mt-1 text-xs leading-relaxed text-gray-500 dark:text-gray-400"
    >
      {{ t("admin.groups.openaiLongContextBilling.copyEstimateHint") }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";

import Icon from "@/components/icons/Icon.vue";

const props = defineProps<{
  modelValue: boolean;
  longContextAccountCount: number;
  standardAccountCount: number;
  canEnable: boolean;
  usesCopiedGroups: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
}>();

const { t } = useI18n();

const stateHint = computed(() => {
  if (!props.canEnable) {
    return t("admin.groups.openaiLongContextBilling.unavailableHint");
  }
  if (props.modelValue) {
    return t("admin.groups.openaiLongContextBilling.enabledHint");
  }
  if (props.standardAccountCount > 0) {
    return t("admin.groups.openaiLongContextBilling.standardOnlyHint");
  }
  return t("admin.groups.openaiLongContextBilling.noStandardAccountHint");
});

const toggle = () => {
  if (!props.canEnable) return;
  emit("update:modelValue", !props.modelValue);
};
</script>
