<template>
  <div class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-600">
    <div>
      <label :for="`${idPrefix}-max-effort`" class="input-label">
        {{ t("admin.groups.form.maxReasoningEffort") }}
      </label>
      <Select
        :id="`${idPrefix}-max-effort`"
        :model-value="maxEffort"
        :options="reasoningEffortOptions"
        :placeholder="t('admin.groups.form.maxReasoningEffortUnlimited')"
        :aria-label="t('admin.groups.form.maxReasoningEffort')"
        :searchable="false"
        clearable
        @update:model-value="updateMaxEffort"
      />
      <p class="input-hint">{{ t("admin.groups.form.maxReasoningEffortHint") }}</p>
    </div>

    <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
      <div class="mb-3 flex items-center justify-between gap-3">
        <label class="input-label mb-0">{{ t("admin.groups.form.reasoningEffortMappings") }}</label>
        <button
          type="button"
          class="inline-flex min-h-11 items-center gap-1.5 rounded-lg px-2.5 text-sm font-medium text-primary-600 transition-colors hover:bg-primary-50 focus:outline-none focus:ring-2 focus:ring-primary-500/30 dark:text-primary-400 dark:hover:bg-primary-900/20"
          @click="addMapping"
        >
          <Icon name="plus" size="sm" />
          {{ t("admin.groups.form.addReasoningEffortMapping") }}
        </button>
      </div>

      <div v-if="mappings.length" class="space-y-2">
        <div
          v-for="row in mappings"
          :key="row.id"
          class="grid gap-3 rounded-lg bg-gray-50 p-3 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto] sm:items-start dark:bg-dark-800/50"
        >
          <div>
            <label class="input-label">{{ t("admin.groups.form.reasoningEffortFrom") }}</label>
            <Select
              :model-value="row.from"
              :options="reasoningEffortOptions"
              :error="showValidation && !!validationErrors[row.id]?.from"
              :searchable="false"
              clearable
              @update:model-value="updateMapping(row.id, 'from', $event)"
            />
            <p v-if="showValidation && validationErrors[row.id]?.from" class="mt-1 text-xs text-red-600" role="alert">
              {{ mappingErrorText(validationErrors[row.id]?.from) }}
            </p>
          </div>
          <div class="hidden pt-8 text-gray-400 sm:block">→</div>
          <div>
            <label class="input-label">{{ t("admin.groups.form.reasoningEffortTo") }}</label>
            <Select
              :model-value="row.to"
              :options="reasoningEffortOptions"
              :error="showValidation && !!validationErrors[row.id]?.to"
              :searchable="false"
              clearable
              @update:model-value="updateMapping(row.id, 'to', $event)"
            />
            <p v-if="showValidation && validationErrors[row.id]?.to" class="mt-1 text-xs text-red-600" role="alert">
              {{ mappingErrorText(validationErrors[row.id]?.to) }}
            </p>
          </div>
          <button
            type="button"
            class="flex h-11 w-11 items-center justify-center rounded-lg text-gray-400 hover:bg-red-50 hover:text-red-500 sm:mt-6 dark:hover:bg-red-900/20"
            :aria-label="t('admin.groups.form.removeReasoningEffortMapping')"
            @click="removeMapping(row.id)"
          >
            <Icon name="trash" size="sm" />
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import type { GroupPlatform } from "@/types";
import Icon from "@/components/icons/Icon.vue";
import Select from "@/components/common/Select.vue";
import {
  createReasoningEffortMappingRow,
  reasoningEffortOptionsForPlatform,
  validateReasoningEffortMappings,
  type ReasoningEffortMappingErrorCode,
  type ReasoningEffortMappingRow,
} from "@/views/admin/groupsReasoningEffort";

const props = defineProps<{
  idPrefix: string;
  platform: GroupPlatform;
  maxEffort: string;
  mappings: ReasoningEffortMappingRow[];
}>();
const emit = defineEmits<{
  (event: "update:maxEffort", value: string): void;
  (event: "update:mappings", value: ReasoningEffortMappingRow[]): void;
}>();

const { t } = useI18n();
const showValidation = ref(false);
const reasoningEffortOptions = computed(() => reasoningEffortOptionsForPlatform(props.platform));
const validationErrors = computed(() => validateReasoningEffortMappings(props.mappings, props.platform));
const asString = (value: string | number | boolean | null): string => value == null ? "" : String(value);
const updateMaxEffort = (value: string | number | boolean | null) => emit("update:maxEffort", asString(value));
const updateMapping = (id: string, field: "from" | "to", value: string | number | boolean | null) => emit(
  "update:mappings",
  props.mappings.map((row) => row.id === id ? { ...row, [field]: asString(value) } : row),
);
const addMapping = () => emit("update:mappings", [...props.mappings, createReasoningEffortMappingRow()]);
const removeMapping = (id: string) => emit("update:mappings", props.mappings.filter((row) => row.id !== id));
const mappingErrorText = (code: ReasoningEffortMappingErrorCode | undefined) => code ? t(`admin.groups.form.${code}`) : "";
const validate = () => {
  showValidation.value = true;
  return Object.keys(validationErrors.value).length === 0;
};
const resetValidation = () => {
  showValidation.value = false;
};
defineExpose({ validate, resetValidation });
</script>
