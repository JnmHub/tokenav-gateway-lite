<template>
  <div class="min-h-screen bg-gray-50 dark:bg-dark-950">
    <div class="pointer-events-none fixed inset-0 bg-mesh-gradient"></div>

    <GatewayLiteSidebar />

    <div
      class="relative min-h-screen transition-all duration-300"
      :class="[sidebarCollapsed ? 'lg:ml-[72px]' : 'lg:ml-64']"
    >
      <GatewayLiteHeader />
      <main class="p-4 md:p-6 lg:p-8">
        <slot />
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useAppStore } from '@/stores/app'
import GatewayLiteHeader from './GatewayLiteHeader.vue'
import GatewayLiteSidebar from './GatewayLiteSidebar.vue'

const appStore = useAppStore()
const sidebarCollapsed = computed(() => appStore.sidebarCollapsed)
</script>
