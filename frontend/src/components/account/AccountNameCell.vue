<template>
  <div class="flex min-w-0 flex-col">
    <a
      v-if="upstreamOrigin"
      :href="upstreamOrigin"
      :title="upstreamOrigin"
      target="_blank"
      rel="noopener noreferrer"
      class="w-fit max-w-full truncate font-medium text-gray-900 underline decoration-gray-300 underline-offset-2 transition-colors hover:text-primary-600 hover:decoration-primary-500 dark:text-white dark:decoration-gray-600 dark:hover:text-primary-400"
    >
      {{ account.name }}
    </a>
    <span v-else class="truncate font-medium text-gray-900 dark:text-white">{{ account.name }}</span>
    <span
      v-if="email"
      class="max-w-[200px] truncate text-xs text-gray-500 dark:text-gray-400"
      :title="email"
    >
      {{ email }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Account } from '@/types'
import { accountUpstreamOrigin } from '@/utils/accountUpstreamOrigin'

const props = defineProps<{ account: Account }>()

const upstreamOrigin = computed(() => accountUpstreamOrigin(props.account))
const email = computed(() => {
  const value = props.account.extra?.email_address
    ?? props.account.extra?.email
    ?? props.account.credentials?.email
  return typeof value === 'string' && value.trim() !== '' ? value : null
})
</script>
