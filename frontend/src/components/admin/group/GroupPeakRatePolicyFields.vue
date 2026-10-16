<template>
  <section
    class="rounded-lg border border-gray-200 p-4 dark:border-dark-600"
    data-testid="group-peak-rate-policy"
  >
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0 flex-1">
        <p class="text-sm font-medium text-gray-900 dark:text-gray-100">
          {{ t("admin.groups.peakRate.title") }}
        </p>
        <p class="mt-1 text-xs leading-relaxed text-gray-500 dark:text-gray-400">
          {{ t("admin.groups.peakRate.description") }}
        </p>
      </div>
      <button
        type="button"
        role="switch"
        :aria-label="t('admin.groups.peakRate.title')"
        :aria-checked="enabled"
        :class="[
          'relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
          enabled ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600',
        ]"
        data-testid="peak-rate-toggle"
        @click="emit('update:enabled', !enabled)"
      >
        <span
          :class="[
            'inline-block h-4 w-4 rounded-full bg-white shadow transition-transform',
            enabled ? 'translate-x-6' : 'translate-x-1',
          ]"
        />
      </button>
    </div>

    <div v-if="enabled" class="mt-4 space-y-4" data-testid="peak-rate-fields">
      <div>
        <label class="input-label">{{ t("admin.groups.peakRate.timeWindow") }}</label>
        <div class="grid grid-cols-[1fr_auto_1fr] items-center gap-2">
          <input
            :value="start"
            type="time"
            step="60"
            required
            class="input min-w-0"
            :aria-label="t('admin.groups.peakRate.start')"
            data-testid="peak-rate-start"
            @input="emitInputValue('update:start', $event)"
          />
          <span class="text-sm text-gray-400" aria-hidden="true">→</span>
          <input
            :value="end"
            type="time"
            step="60"
            required
            class="input min-w-0"
            :aria-label="t('admin.groups.peakRate.end')"
            data-testid="peak-rate-end"
            @input="emitInputValue('update:end', $event)"
          />
        </div>
        <p
          v-if="windowState === 'cross-midnight'"
          class="mt-1.5 rounded-md bg-amber-50 px-2.5 py-2 text-xs leading-relaxed text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
          data-testid="peak-rate-cross-midnight-hint"
        >
          {{ t("admin.groups.peakRate.crossMidnightHint") }}
        </p>
        <p
          v-else-if="windowState === 'same-time'"
          class="mt-1.5 text-xs text-red-600 dark:text-red-400"
          data-testid="peak-rate-same-time-error"
        >
          {{ t("admin.groups.peakRate.sameTimeError") }}
        </p>
      </div>

      <div>
        <label class="input-label">{{ t("admin.groups.peakRate.timezone") }}</label>
        <div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <button
            v-for="option in timezoneOptions"
            :key="option.value"
            type="button"
            :title="option.value"
            :class="[
              'rounded-md border px-2 py-2 text-xs font-medium transition-colors',
              timezone === option.value
                ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
                : 'border-gray-200 text-gray-600 hover:border-primary-300 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-700',
            ]"
            :data-testid="`peak-timezone-${option.value}`"
            @click="emit('update:timezone', option.value)"
          >
            {{ t(option.labelKey) }}
          </button>
        </div>
        <p class="input-hint">{{ t("admin.groups.peakRate.timezoneHint") }}</p>
      </div>

      <div>
        <label class="input-label">{{ t("admin.groups.peakRate.multiplier") }}</label>
        <div class="grid grid-cols-4 gap-2">
          <button
            v-for="preset in multiplierPresets"
            :key="preset"
            type="button"
            :class="[
              'rounded-md border px-2 py-2 text-xs font-medium transition-colors',
              multiplier === preset
                ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
                : 'border-gray-200 text-gray-600 hover:border-primary-300 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-700',
            ]"
            :data-testid="`peak-multiplier-${preset}`"
            @click="emit('update:multiplier', preset)"
          >
            {{ preset }}x
          </button>
        </div>
        <input
          :value="multiplier"
          type="number"
          min="0.001"
          max="100"
          step="0.05"
          required
          class="input mt-2"
          :aria-label="t('admin.groups.peakRate.multiplier')"
          data-testid="peak-rate-multiplier"
          @input="emitNumberValue($event)"
        />
        <p class="input-hint">{{ t("admin.groups.peakRate.multiplierHint") }}</p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";

const props = defineProps<{
  enabled: boolean;
  start: string;
  end: string;
  multiplier: number;
  timezone: string;
}>();

const emit = defineEmits<{
  "update:enabled": [value: boolean];
  "update:start": [value: string];
  "update:end": [value: string];
  "update:multiplier": [value: number];
  "update:timezone": [value: string];
}>();

const { t } = useI18n();

const multiplierPresets = [1, 1.25, 1.5, 2] as const;
const timezoneOptions = [
  { value: "Asia/Shanghai", labelKey: "admin.groups.peakRate.timezones.shanghai" },
  { value: "America/Los_Angeles", labelKey: "admin.groups.peakRate.timezones.losAngeles" },
  { value: "Asia/Tokyo", labelKey: "admin.groups.peakRate.timezones.tokyo" },
  { value: "UTC", labelKey: "admin.groups.peakRate.timezones.utc" },
] as const;

const windowState = computed<"normal" | "cross-midnight" | "same-time">(() => {
  if (!props.start || !props.end) return "normal";
  if (props.start === props.end) return "same-time";
  return props.start > props.end ? "cross-midnight" : "normal";
});

type TimeUpdateEvent = "update:start" | "update:end";

const emitInputValue = (eventName: TimeUpdateEvent, event: Event) => {
  const value = (event.target as HTMLInputElement).value;
  if (eventName === "update:start") {
    emit("update:start", value);
    return;
  }
  emit("update:end", value);
};

const emitNumberValue = (event: Event) => {
  const value = (event.target as HTMLInputElement).valueAsNumber;
  if (Number.isFinite(value)) {
    emit("update:multiplier", value);
  }
};
</script>
