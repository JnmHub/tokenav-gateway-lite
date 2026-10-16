<template>
  <aside
    class="sidebar"
    :class="[
      sidebarCollapsed ? 'w-[72px]' : 'w-64',
      { '-translate-x-full lg:translate-x-0': !mobileOpen }
    ]"
  >
    <div class="sidebar-header" :class="{ 'gateway-sidebar-header-collapsed': sidebarCollapsed }">
      <router-link
        to="/admin/dashboard"
        class="gateway-sidebar-logo flex h-9 w-9 items-center justify-center overflow-hidden rounded-xl shadow-glow transition-opacity hover:opacity-80"
        :aria-label="t('nav.dashboard')"
        @click="handleMenuClick"
      >
        <img v-if="settingsLoaded" :src="siteLogo || '/logo.png'" alt="Logo" class="h-full w-full object-contain">
      </router-link>
      <div class="gateway-sidebar-brand" :class="{ 'gateway-sidebar-brand-collapsed': sidebarCollapsed }" :aria-hidden="sidebarCollapsed ? 'true' : 'false'">
        <router-link
          to="/admin/dashboard"
          class="gateway-sidebar-brand-title text-lg font-bold text-gray-900 transition-colors hover:text-primary-600 dark:text-white dark:hover:text-primary-400"
          @click="handleMenuClick"
        >
          {{ siteName }}
        </router-link>
        <VersionBadge :version="siteVersion" />
      </div>
    </div>

    <nav ref="sidebarNavRef" class="sidebar-nav scrollbar-hide">
      <div class="sidebar-section">
        <template v-for="item in navItems" :key="item.path">
          <button
            v-if="item.children?.length"
            class="sidebar-link mb-1 w-full"
            :class="{
              'sidebar-link-active': isGroupActive(item) && !isGroupExpanded(item),
              'gateway-sidebar-link-collapsed': sidebarCollapsed
            }"
            type="button"
            :title="sidebarCollapsed ? item.label : undefined"
            @click="toggleGroup(item.path)"
          >
            <Icon :name="item.icon" class="h-5 w-5 flex-shrink-0" />
            <span
              class="gateway-sidebar-label gateway-sidebar-label-flex"
              :class="{ 'gateway-sidebar-label-collapsed': sidebarCollapsed }"
              :aria-hidden="sidebarCollapsed ? 'true' : 'false'"
            >
              <span class="min-w-0 truncate">{{ item.label }}</span>
              <Icon
                name="chevronDown"
                size="sm"
                class="flex-shrink-0 transition-transform duration-200"
                :class="isGroupExpanded(item) ? 'rotate-180' : ''"
              />
            </span>
          </button>

          <div v-if="item.children?.length && !sidebarCollapsed && isGroupExpanded(item)" class="mb-1 ml-4 border-l border-gray-200 pl-2 dark:border-dark-600">
            <router-link
              v-for="child in item.children"
              :key="child.path"
              :to="child.path"
              class="sidebar-link mb-0.5 py-1.5 text-sm"
              :class="{ 'sidebar-link-active': route.path === child.path }"
              @click="handleMenuClick"
            >
              <Icon :name="child.icon" class="h-4 w-4 flex-shrink-0" />
              <span>{{ child.label }}</span>
            </router-link>
          </div>

          <router-link
            v-else
            :to="item.path"
            class="sidebar-link mb-1"
            :class="{ 'sidebar-link-active': isActive(item.path), 'gateway-sidebar-link-collapsed': sidebarCollapsed }"
            :title="sidebarCollapsed ? item.label : undefined"
            @click="handleMenuClick"
          >
            <Icon :name="item.icon" class="h-5 w-5 flex-shrink-0" />
            <span
              class="gateway-sidebar-label"
              :class="{ 'gateway-sidebar-label-collapsed': sidebarCollapsed }"
              :aria-hidden="sidebarCollapsed ? 'true' : 'false'"
            >
              {{ item.label }}
            </span>
          </router-link>
        </template>
      </div>
    </nav>

    <div class="mt-auto border-t border-gray-100 p-3 dark:border-dark-800">
      <button
        class="sidebar-link mb-2 w-full"
        :class="{ 'gateway-sidebar-link-collapsed': sidebarCollapsed }"
        type="button"
        :title="sidebarCollapsed ? themeTitle : undefined"
        @click="toggleTheme"
      >
        <Icon v-if="isDark" name="sun" class="h-5 w-5 flex-shrink-0 text-amber-500" />
        <Icon v-else name="moon" class="h-5 w-5 flex-shrink-0" />
        <span
          class="gateway-sidebar-label"
          :class="{ 'gateway-sidebar-label-collapsed': sidebarCollapsed }"
          :aria-hidden="sidebarCollapsed ? 'true' : 'false'"
        >
          {{ themeTitle }}
        </span>
      </button>

      <button
        class="sidebar-link w-full"
        :class="{ 'gateway-sidebar-link-collapsed': sidebarCollapsed }"
        type="button"
        :title="sidebarCollapsed ? t('nav.expand') : t('nav.collapse')"
        @click="appStore.toggleSidebar()"
      >
        <Icon :name="sidebarCollapsed ? 'chevronRight' : 'chevronLeft'" class="h-5 w-5 flex-shrink-0" />
        <span
          class="gateway-sidebar-label"
          :class="{ 'gateway-sidebar-label-collapsed': sidebarCollapsed }"
          :aria-hidden="sidebarCollapsed ? 'true' : 'false'"
        >
          {{ t('nav.collapse') }}
        </span>
      </button>
    </div>
  </aside>

  <transition name="fade">
    <div
      v-if="mobileOpen"
      class="fixed inset-0 z-30 bg-black/50 lg:hidden"
      @click="appStore.setMobileOpen(false)"
    ></div>
  </transition>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { sanitizeUrl } from '@/utils/url'
import VersionBadge from '@/components/common/VersionBadge.vue'
import Icon from '@/components/icons/Icon.vue'

type IconName = InstanceType<typeof Icon>['$props']['name']

interface GatewayNavItem {
  path: string
  label: string
  icon: IconName
  children?: GatewayNavItem[]
  enabled?: boolean
}

const route = useRoute()
const { t } = useI18n()
const appStore = useAppStore()
const expandedGroups = ref<Set<string>>(new Set(['/admin/channels']))
const sidebarNavRef = ref<HTMLElement | null>(null)
const isDark = ref(document.documentElement.classList.contains('dark'))

const sidebarCollapsed = computed(() => appStore.sidebarCollapsed)
const mobileOpen = computed(() => appStore.mobileOpen)
const settingsLoaded = computed(() => appStore.publicSettingsLoaded)
const siteName = computed(() => appStore.siteName)
const siteLogo = computed(() => sanitizeUrl(appStore.siteLogo || '', {
  allowRelative: true,
  allowDataUrl: true,
}))
const siteVersion = computed(() => appStore.siteVersion)
const channelMonitorEnabled = computed(() => appStore.cachedPublicSettings?.channel_monitor_enabled !== false)
const themeTitle = computed(() => isDark.value ? t('nav.lightMode') : t('nav.darkMode'))

const navItems = computed(() => {
  const items: GatewayNavItem[] = [
    { path: '/admin/dashboard', label: t('nav.dashboard'), icon: 'grid' },
    {
      path: '/admin/ops',
      label: t('nav.ops'),
      icon: 'chart',
      children: [
        { path: '/admin/ops', label: t('nav.opsOverview'), icon: 'chart' },
        { path: '/admin/ops/codex-tail-burst', label: t('nav.codexTailBurstStats'), icon: 'fire' },
        { path: '/admin/ops/request-schema', label: '字段结构分析', icon: 'search' },
      ]
    },
    { path: '/admin/audit-logs', label: t('nav.auditLogs'), icon: 'shield' },
    { path: '/admin/groups', label: t('nav.groups'), icon: 'database' },
    {
      path: '/admin/channels',
      label: t('nav.channelManagement'),
      icon: 'cube',
      children: [
        { path: '/admin/model-prices', label: t('nav.modelPrices'), icon: 'calculator' },
        { path: '/admin/channels/monitor', label: t('nav.channelMonitor'), icon: 'chartBar', enabled: channelMonitorEnabled.value },
      ],
    },
    { path: '/admin/accounts', label: t('nav.accounts'), icon: 'globe' },
    { path: '/admin/local-api-keys', label: t('nav.localApiKeys'), icon: 'key' },
    { path: '/admin/proxies', label: t('nav.proxies'), icon: 'server' },
    { path: '/admin/risk-control', label: t('nav.riskControl'), icon: 'shield' },
    { path: '/admin/usage', label: t('nav.usage'), icon: 'chartBar' },
    { path: '/admin/image-tasks', label: t('nav.imageTasks'), icon: 'sparkles' },
    { path: '/admin/runtime', label: t('nav.runtimeSummary'), icon: 'cpu' },
    { path: '/admin/settings', label: t('nav.settings'), icon: 'cog' },
  ]

  return items
    .filter(item => item.enabled !== false)
    .map(item => item.children
      ? { ...item, children: item.children.filter(child => child.enabled !== false) }
      : item)
})

function isActive(path: string): boolean {
  return route.path === path || route.path.startsWith(`${path}/`)
}

function isGroupActive(item: GatewayNavItem): boolean {
  return item.children?.some(child => isActive(child.path)) ?? false
}

function isGroupExpanded(item: GatewayNavItem): boolean {
  return expandedGroups.value.has(item.path) || isGroupActive(item)
}

function toggleGroup(path: string) {
  if (sidebarCollapsed.value) return
  if (expandedGroups.value.has(path)) {
    expandedGroups.value.delete(path)
  } else {
    expandedGroups.value.add(path)
  }
}

function handleMenuClick() {
  if (mobileOpen.value) {
    setTimeout(() => appStore.setMobileOpen(false), 150)
  }
}

function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

onMounted(() => {
  const savedTheme = localStorage.getItem('theme')
  if (
    savedTheme === 'dark' ||
    (!savedTheme && window.matchMedia('(prefers-color-scheme: dark)').matches)
  ) {
    isDark.value = true
    document.documentElement.classList.add('dark')
  }

  if (appStore.sidebarScrollTop > 0 && sidebarNavRef.value) {
    void nextTick(() => {
      if (sidebarNavRef.value) {
        sidebarNavRef.value.scrollTop = appStore.sidebarScrollTop
      }
    })
  }
})

onBeforeUnmount(() => {
  if (sidebarNavRef.value) {
    appStore.sidebarScrollTop = sidebarNavRef.value.scrollTop
  }
})
</script>

<style scoped>
.gateway-sidebar-logo {
  flex: 0 0 2.25rem;
  min-width: 2.25rem;
}

.gateway-sidebar-header-collapsed {
  gap: 0;
  padding-left: 1.125rem;
  padding-right: 1.125rem;
}

.gateway-sidebar-brand {
  min-width: 0;
  flex: 1 1 auto;
  white-space: nowrap;
  transition:
    max-width 0.22s ease,
    opacity 0.14s ease,
    transform 0.14s ease;
  max-width: 12rem;
}

.gateway-sidebar-brand-collapsed {
  max-width: 0;
  overflow: hidden;
  opacity: 0;
  transform: translateX(-4px);
  pointer-events: none;
}

.gateway-sidebar-brand-title {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gateway-sidebar-link-collapsed {
  gap: 0;
  padding-left: 0.875rem;
  padding-right: 0.875rem;
}

.gateway-sidebar-label {
  display: block;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  transition:
    max-width 0.2s ease,
    opacity 0.12s ease,
    transform 0.12s ease;
  max-width: 12rem;
}

.gateway-sidebar-label-flex {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}

.gateway-sidebar-label-collapsed {
  max-width: 0;
  opacity: 0;
  transform: translateX(-4px);
  pointer-events: none;
}
</style>
