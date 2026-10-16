<template>
  <LoginView v-if="showLogin" />
  <NotFoundView v-else />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import LoginView from './LoginView.vue'
import NotFoundView from '@/views/NotFoundView.vue'
import {
  isGatewayLiteLoginProtectionEnabled,
  isGatewayLitePrivateLoginEntry,
} from '@/utils/gatewayLiteLogin'

const route = useRoute()

const showLogin = computed(() => {
  if (!isGatewayLiteLoginProtectionEnabled()) {
    return route.path === '/login'
  }
  return isGatewayLitePrivateLoginEntry(route.path)
})
</script>
