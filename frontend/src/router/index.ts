/**
 * Vue Router configuration for JnmGatewayApi frontend
 * Defines all application routes with lazy loading and navigation guards
 */

import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { useNavigationLoadingState } from '@/composables/useNavigationLoading'
import { useRoutePrefetch } from '@/composables/useRoutePrefetch'
import { getSetupStatus } from '@/api/setup'
import { resolveCompletedSetupRedirectPath } from './setupRedirect'
import { resolveDocumentTitle } from './title'
import {
  getGatewayLiteLoginPath,
  isGatewayLitePrivateLoginEntry,
} from '@/utils/gatewayLiteLogin'

/**
 * gateway-lite（轻量网关）后台只声明实际会打开的页面。
 * 这里不再注册客户侧、支付、订阅、返利和完整主站后台路由，避免这些页面继续参与打包。
 */
const routes: RouteRecordRaw[] = [
  {
    path: '/setup',
    name: 'Setup',
    component: () => import('@/views/setup/SetupWizardView.vue'),
    meta: {
      requiresAuth: false,
      title: 'Setup'
    }
  },
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/auth/GatewayLiteLoginEntryView.vue'),
    meta: {
      requiresAuth: false,
      title: 'Login',
      titleKey: 'home.login'
    }
  },
  {
    path: '/',
    redirect: '/admin/dashboard'
  },
  {
    path: '/admin',
    redirect: '/admin/dashboard'
  },
  {
    path: '/admin/dashboard',
    name: 'AdminDashboard',
    component: () => import('@/views/admin/DashboardView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Admin Dashboard',
      titleKey: 'admin.dashboard.title',
      descriptionKey: 'admin.dashboard.description'
    }
  },
  {
    path: '/admin/ops',
    name: 'AdminOps',
    component: () => import('@/views/admin/ops/OpsDashboard.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Ops Monitoring',
      titleKey: 'admin.ops.title',
      descriptionKey: 'admin.ops.description'
    }
  },
  {
    path: '/admin/ops/codex-tail-burst',
    name: 'AdminCodexTailBurstStats',
    component: () => import('@/views/admin/ops/CodexTailBurstStatsView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Codex Tail-Burst Statistics',
      titleKey: 'admin.ops.tailBurstStats.title',
      descriptionKey: 'admin.ops.tailBurstStats.description'
    }
  },
  {
    path: '/admin/ops/request-schema',
    name: 'AdminRequestSchemaAnalyzer',
    component: () => import('@/views/admin/ops/RequestSchemaAnalyzerView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true, title: '字段结构分析' }
  },
  {
    path: '/admin/audit-logs',
    name: 'AdminAuditLogs',
    component: () => import('@/views/admin/AuditLogView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Operation Audit Logs'
    }
  },
  {
    path: '/admin/groups',
    name: 'AdminGroups',
    component: () => import('@/views/admin/GroupsView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Group Management',
      titleKey: 'admin.groups.title',
      descriptionKey: 'admin.groups.description'
    }
  },
  {
    path: '/admin/channels',
    redirect: '/admin/groups'
  },
  {
    path: '/admin/channels/pricing',
    redirect: '/admin/groups'
  },
  {
    path: '/admin/model-prices',
    name: 'AdminModelPrices',
    component: () => import('@/views/admin/ModelPricesView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Model Prices',
      titleKey: 'admin.modelPrices.title',
      descriptionKey: 'admin.modelPrices.description'
    }
  },
  {
    path: '/admin/channels/monitor',
    name: 'AdminChannelMonitor',
    component: () => import('@/views/admin/ChannelMonitorView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Channel Monitor',
      titleKey: 'admin.channelMonitor.title',
      descriptionKey: 'admin.channelMonitor.description'
    }
  },
  {
    path: '/admin/accounts',
    name: 'AdminAccounts',
    component: () => import('@/views/admin/AccountsView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Account Management',
      titleKey: 'admin.accounts.title',
      descriptionKey: 'admin.accounts.description'
    }
  },
  {
    path: '/admin/local-api-keys',
    name: 'AdminGatewayLiteLocalKeys',
    component: () => import('@/views/admin/GatewayLiteLocalKeysView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Local API Keys',
      titleKey: 'admin.localApiKeys.title',
      descriptionKey: 'admin.localApiKeys.description'
    }
  },
  {
    path: '/admin/proxies',
    name: 'AdminProxies',
    component: () => import('@/views/admin/ProxiesView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Proxy Management',
      titleKey: 'admin.proxies.title',
      descriptionKey: 'admin.proxies.description'
    }
  },
  {
    path: '/admin/settings',
    name: 'AdminSettings',
    component: () => import('@/views/admin/SettingsView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'System Settings',
      titleKey: 'admin.settings.title',
      descriptionKey: 'admin.settings.description'
    }
  },
  {
    path: '/admin/runtime',
    name: 'AdminRuntimeSummary',
    component: () => import('@/views/admin/RuntimeSummaryView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Runtime Configuration',
      titleKey: 'admin.runtimeSummary.title',
      descriptionKey: 'admin.runtimeSummary.description'
    }
  },
  {
    path: '/admin/image-tasks',
    name: 'AdminImageTasks',
    component: () => import('@/views/admin/ImageTasksView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Asynchronous Image Tasks'
    }
  },
  {
    path: '/admin/risk-control',
    name: 'AdminRiskControl',
    component: () => import('@/views/admin/RiskControlView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Risk Control',
      titleKey: 'admin.riskControl.title',
      descriptionKey: 'admin.riskControl.description',
      requiresRiskControl: true
    }
  },
  {
    path: '/admin/usage',
    name: 'AdminUsage',
    component: () => import('@/views/admin/UsageView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Usage Records',
      titleKey: 'admin.usage.title',
      descriptionKey: 'admin.usage.description'
    }
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'GatewayLiteLoginOrNotFound',
    component: () => import('@/views/auth/GatewayLiteLoginEntryView.vue'),
    meta: {
      requiresAuth: false,
      title: '404 Not Found'
    }
  }
]

/**
 * Create router instance
 */
const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
  scrollBehavior(_to, _from, savedPosition) {
    // Scroll to saved position when using browser back/forward
    if (savedPosition) {
      return savedPosition
    }
    // Scroll to top for new routes
    return { top: 0 }
  }
})

/**
 * Navigation guard: Authentication check
 */
let authInitialized = false

// 初始化导航加载状态和预加载
const navigationLoading = useNavigationLoadingState()
// 延迟初始化预加载，传入 router 实例
let routePrefetch: ReturnType<typeof useRoutePrefetch> | null = null
const BACKEND_MODE_ALLOWED_PATHS = ['/login', '/setup']
const GATEWAY_LITE_PUBLIC_ALLOWED_PATHS = ['/login', '/setup']
const GATEWAY_LITE_ADMIN_ALLOWED_PATHS = [
  '/admin',
  '/admin/dashboard',
  '/admin/ops',
  '/admin/ops/codex-tail-burst',
  '/admin/ops/request-schema',
  '/admin/audit-logs',
  '/admin/groups',
  '/admin/channels',
  '/admin/channels/pricing',
  '/admin/model-prices',
  '/admin/channels/monitor',
  '/admin/accounts',
  '/admin/local-api-keys',
  '/admin/proxies',
  '/admin/risk-control',
  '/admin/usage',
  '/admin/settings',
  '/admin/runtime',
  '/admin/image-tasks',
]

function isBackendModePublicRouteAllowed(path: string): boolean {
  return BACKEND_MODE_ALLOWED_PATHS.some((allowedPath) => path === allowedPath || path.startsWith(allowedPath))
}

function matchesAllowedPath(path: string, allowedPaths: string[]): boolean {
  return allowedPaths.some((allowedPath) => path === allowedPath || path.startsWith(`${allowedPath}/`))
}

function isGatewayLiteAdminRouteAllowed(path: string): boolean {
  return (
    isGatewayLitePrivateLoginEntry(path) ||
    matchesAllowedPath(path, GATEWAY_LITE_PUBLIC_ALLOWED_PATHS) ||
    matchesAllowedPath(path, GATEWAY_LITE_ADMIN_ALLOWED_PATHS)
  )
}

router.beforeEach(async (to, _from, next) => {
  // 开始导航加载状态
  navigationLoading.startNavigation()

  const authStore = useAuthStore()

  // Restore auth state from localStorage on first navigation (page refresh)
  if (!authInitialized) {
    authStore.checkAuth()
    authInitialized = true
  }

  // Set page title
  const appStore = useAppStore()
  if (!appStore.publicSettingsLoaded) {
    await appStore.fetchPublicSettings()
  }
  document.title = resolveDocumentTitle(to.meta.title, appStore.siteName, to.meta.titleKey as string)

  // Check if route requires authentication
  const requiresAuth = to.meta.requiresAuth !== false // Default to true
  const requiresAdmin = to.meta.requiresAdmin === true

  if (to.path === '/setup') {
    try {
      const status = await getSetupStatus()
      if (!status.needs_setup) {
        next(resolveCompletedSetupRedirectPath(authStore.isAuthenticated, authStore.isAdmin))
        return
      }
    } catch {
      // If setup status cannot be determined, keep the setup page reachable.
    }
  }

  if (appStore.isGatewayLiteAdmin && !isGatewayLiteAdminRouteAllowed(to.path)) {
    if (authStore.isAuthenticated && authStore.isAdmin) {
      next('/admin/dashboard')
      return
    }
    // Unknown paths stay on the neutral 404 component instead of revealing an
    // administrator login route through a redirect.
    next()
    return
  }

  // If route doesn't require auth, allow access
  if (!requiresAuth) {
    // If already authenticated and trying to access login, redirect to the gateway admin dashboard.
    if (authStore.isAuthenticated && (to.path === '/login' || isGatewayLitePrivateLoginEntry(to.path))) {
      // In backend mode, non-admin users should NOT be redirected away from login
      // (they are blocked from all protected routes, so redirecting would cause a loop)
      if (appStore.backendModeEnabled && !authStore.isAdmin) {
        next()
        return
      }
      if (!authStore.isAdmin) {
        next()
        return
      }
      next('/admin/dashboard')
      return
    }
    // Backend mode: block public pages for unauthenticated users (except login, key-usage, setup)
    if (appStore.backendModeEnabled && !authStore.isAuthenticated) {
      const isAllowed =
        isBackendModePublicRouteAllowed(to.path) ||
        (appStore.isGatewayLiteAdmin && isGatewayLitePrivateLoginEntry(to.path))
      if (!isAllowed) {
        next('/login')
        return
      }
    }
    next()
    return
  }

  // Route requires authentication
  if (!authStore.isAuthenticated) {
    // Not authenticated, redirect to login
    if (appStore.isGatewayLiteAdmin) {
      window.location.replace(getGatewayLiteLoginPath())
      return
    }
    next({
      path: '/login',
      query: { redirect: to.fullPath } // Save intended destination
    })
    return
  }

  // Check admin requirement
  if (requiresAdmin && !authStore.isAdmin) {
    // gateway-lite（轻量网关）不保留客户控制台，非管理员统一回登录页。
    next('/login')
    return
  }

  if (to.meta.requiresRiskControl && !appStore.isGatewayLiteAdmin) {
    const riskControlEnabled = appStore.cachedPublicSettings?.risk_control_enabled === true
    if (!riskControlEnabled) {
      next(authStore.isAdmin ? '/admin/settings' : '/login')
      return
    }
  }

  // 简易模式下限制访问某些页面
  if (authStore.isSimpleMode) {
    const restrictedPaths = ['/admin/groups']

    if (restrictedPaths.some((path) => to.path.startsWith(path))) {
      // 简易模式下访问受限页面,重定向到仪表板
      next(authStore.isAdmin ? '/admin/dashboard' : '/login')
      return
    }
  }

  // Backend mode: admin gets full access, non-admin blocked
  if (appStore.backendModeEnabled) {
    if (authStore.isAuthenticated && authStore.isAdmin) {
      next()
      return
    }
    const isAllowed = isBackendModePublicRouteAllowed(to.path)
    if (!isAllowed) {
      next('/login')
      return
    }
  }

  // All checks passed, allow navigation
  next()
})

/**
 * Navigation guard: End loading and trigger prefetch
 */
router.afterEach((to) => {
  // 结束导航加载状态
  navigationLoading.endNavigation()

  // 懒初始化预加载（首次导航时创建，传入 router 实例）
  if (!routePrefetch) {
    routePrefetch = useRoutePrefetch(router)
  }
  // 触发路由预加载（在浏览器空闲时执行）
  routePrefetch.triggerPrefetch(to)
})

/**
 * Navigation guard: Error handling
 * Handles dynamic import failures caused by deployment updates
 */
router.onError((error) => {
  console.error('Router error:', error)

  // Check if this is a dynamic import failure (chunk loading error)
  const isChunkLoadError =
    error.message?.includes('Failed to fetch dynamically imported module') ||
    error.message?.includes('Loading chunk') ||
    error.message?.includes('Loading CSS chunk') ||
    error.name === 'ChunkLoadError'

  if (isChunkLoadError) {
    // Avoid infinite reload loop by checking sessionStorage
    const reloadKey = 'chunk_reload_attempted'
    const lastReload = sessionStorage.getItem(reloadKey)
    const now = Date.now()

    // Allow reload if never attempted or more than 10 seconds ago
    if (!lastReload || now - parseInt(lastReload) > 10000) {
      sessionStorage.setItem(reloadKey, now.toString())
      console.warn('Chunk load error detected, reloading page to fetch latest version...')
      window.location.reload()
    } else {
      console.error('Chunk load error persists after reload. Please clear browser cache.')
    }
  }
})

export default router
