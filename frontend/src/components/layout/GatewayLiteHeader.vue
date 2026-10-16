<template>
  <header class="glass sticky top-0 z-30 border-b border-gray-200/50 dark:border-dark-700/50">
    <div class="flex h-16 items-center justify-between px-4 md:px-6">
      <div class="flex min-w-0 items-center gap-4">
        <button
          class="btn-ghost btn-icon lg:hidden"
          type="button"
          :aria-label="t('nav.expand')"
          @click="appStore.toggleMobileSidebar()"
        >
          <Icon name="menu" size="md" />
        </button>

        <div class="hidden min-w-0 lg:block">
          <h1 class="truncate text-lg font-semibold text-gray-900 dark:text-white">
            {{ pageTitle }}
          </h1>
          <p v-if="pageDescription" class="truncate text-xs text-gray-500 dark:text-dark-400">
            {{ pageDescription }}
          </p>
        </div>
      </div>

      <div class="flex items-center gap-3">
        <a
          v-if="docUrl"
          :href="docUrl"
          target="_blank"
          rel="noopener noreferrer"
          class="flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-sm font-medium text-gray-600 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
        >
          <Icon name="book" size="sm" />
          <span class="hidden sm:inline">{{ t('nav.docs') }}</span>
        </a>

        <LocaleSwitcher />

        <div v-if="user" ref="dropdownRef" class="relative">
          <button
            class="flex items-center gap-2 rounded-xl p-1.5 transition-colors hover:bg-gray-100 dark:hover:bg-dark-800"
            type="button"
            aria-label="User Menu"
            @click="dropdownOpen = !dropdownOpen"
          >
            <div class="flex h-8 w-8 items-center justify-center overflow-hidden rounded-xl bg-gradient-to-br from-primary-500 to-primary-600 text-sm font-medium text-white shadow-sm">
              <img
                v-if="avatarUrl"
                :src="avatarUrl"
                :alt="displayName"
                class="h-full w-full object-cover"
              >
              <span v-else>{{ userInitials }}</span>
            </div>
            <div class="hidden max-w-40 text-left md:block">
              <div class="truncate text-sm font-medium text-gray-900 dark:text-white">
                {{ displayName }}
              </div>
              <div class="truncate text-xs capitalize text-gray-500 dark:text-dark-400">
                {{ user.role }}
              </div>
            </div>
            <Icon name="chevronDown" size="sm" class="hidden text-gray-400 md:block" />
          </button>

          <transition name="dropdown">
            <div v-if="dropdownOpen" class="dropdown right-0 mt-2 w-56">
              <div class="border-b border-gray-100 px-4 py-3 dark:border-dark-700">
                <div class="truncate text-sm font-medium text-gray-900 dark:text-white">
                  {{ displayName }}
                </div>
                <div class="truncate text-xs text-gray-500 dark:text-dark-400">{{ user.email }}</div>
              </div>

              <div class="py-1">
                <a
                  href="https://github.com/Wei-Shaw/sub2api"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="dropdown-item"
                  @click="dropdownOpen = false"
                >
                  <Icon name="externalLink" size="sm" />
                  {{ t('nav.github') }}
                </a>
              </div>

              <div class="border-t border-gray-100 py-1 dark:border-dark-700">
                <button
                  class="dropdown-item w-full text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-900/20"
                  type="button"
                  @click="handleLogout"
                >
                  <Icon name="login" size="sm" />
                  {{ t('nav.logout') }}
                </button>
              </div>
            </div>
          </transition>
        </div>
      </div>
    </div>
  </header>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAdminSettingsStore } from '@/stores/adminSettings'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { sanitizeUrl } from '@/utils/url'
import { redirectToGatewayLiteLogin } from '@/utils/gatewayLiteLogin'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'

const route = useRoute()
const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const adminSettingsStore = useAdminSettingsStore()

const user = computed(() => authStore.user)
const docUrl = computed(() => sanitizeUrl(appStore.docUrl || '', { allowRelative: true }))
const avatarUrl = computed(() => sanitizeUrl(user.value?.avatar_url || '', {
  allowRelative: true,
  allowDataUrl: true,
}))
const dropdownOpen = ref(false)
const dropdownRef = ref<HTMLElement | null>(null)

const pageTitle = computed(() => {
  const titleKey = route.meta.titleKey as string
  return titleKey ? t(titleKey) : (route.meta.title as string) || ''
})

const pageDescription = computed(() => {
  if (route.path === '/admin/settings') {
    return t('admin.settings.gatewayLiteDescription')
  }
  const descKey = route.meta.descriptionKey as string
  return descKey ? t(descKey) : (route.meta.description as string) || ''
})

const displayName = computed(() => {
  if (!user.value) return ''
  return user.value.username || user.value.email?.split('@')[0] || ''
})

const userInitials = computed(() => {
  const name = displayName.value
  return name ? name.substring(0, 2).toUpperCase() : ''
})

function handleClickOutside(event: MouseEvent) {
  if (dropdownRef.value && !dropdownRef.value.contains(event.target as Node)) {
    dropdownOpen.value = false
  }
}

async function handleLogout() {
  dropdownOpen.value = false
  try {
    await authStore.logout()
  } catch (error) {
    console.error('Logout error:', error)
  }
  redirectToGatewayLiteLogin()
}

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  if (authStore.isAdmin) {
    adminSettingsStore.fetch()
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('click', handleClickOutside)
})
</script>

<style scoped>
.dropdown-enter-active,
.dropdown-leave-active {
  transition: all 0.2s ease;
}

.dropdown-enter-from,
.dropdown-leave-to {
  opacity: 0;
  transform: scale(0.95) translateY(-4px);
}
</style>
