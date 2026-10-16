<script setup>
import { computed, onBeforeUnmount, ref } from 'vue'
import { Calendar, Popover } from 'ant-design-vue'
import { AppstoreOutlined, BellOutlined, BgColorsOutlined } from '@ant-design/icons-vue'
import { APPS, openApp, getApp } from '../core/apps'
import { useWindowsStore, useThemeStore } from '../core/stores'
import AppIcon from './AppIcon.vue'
import { request } from '../api'
import { isCancellation } from '../api/session'

defineProps({ startOpen: { type: Boolean, default: false } })
const emit = defineEmits(['toggle-start'])
const store = useWindowsStore(), theme = useThemeStore()
const now = ref(new Date()), notices = ref([]), noticeState = ref('idle'), noticeError = ref('')
const timer = setInterval(() => { now.value = new Date() }, 30_000)
const controller = new AbortController()
onBeforeUnmount(() => { clearInterval(timer); controller.abort() })
const coreIds = ['dashboard', 'accounts', 'groups', 'settings']
const pinnedIds = APPS.filter((app) => coreIds.includes(app.id)).map((app) => app.id)
const dockAppIds = computed(() => store.mobile ? coreIds : [...pinnedIds,
  ...store.windows.filter((w) => !w.closing).map((w) => w.appId).filter((id) => !pinnedIds.includes(id))])
const winOf = (appId) => store.windows.find((win) => win.appId === appId && !win.closing)
const pad = (n) => String(n).padStart(2, '0')
const launch = (id) => { store.startOpen = false; const win = winOf(id); win ? store.toggleFromTaskbar(win.id) : openApp(id) }
const loadNotices = async (open) => {
  if (!open || ['loading', 'ready', 'unsupported'].includes(noticeState.value)) return
  noticeState.value = 'loading'
  try {
    const data = await request('/announcements', { method: 'GET', signal: controller.signal })
    notices.value = Array.isArray(data) ? data : []; noticeState.value = 'ready'
  } catch (error) {
    if (isCancellation(error)) return
    noticeError.value = error.message
    // 轻量模式不注册公告路由，明确说明能力，而不是把 404 伪装为“暂无通知”。
    noticeState.value = error.status === 404 || error.message.includes('404') ? 'unsupported' : 'error'
  }
}
</script>
<template>
  <nav class="w12-dock-box" :class="{ 'mobile-nav': store.mobile }" aria-label="任务栏">
    <div class="w12-dock">
      <button v-if="!store.mobile" data-start-button class="w12-dock-btn" :class="{ foc: startOpen }" title="开始" aria-label="开始"
        :aria-expanded="startOpen" @click="emit('toggle-start')"><AppstoreOutlined /></button>
      <button v-for="appId in dockAppIds" :key="appId" :data-task-app="appId" class="w12-dock-btn"
        :class="{ opened: !!winOf(appId), foc: winOf(appId)?.id === store.focusedId && !store.appsOpen }"
        :title="getApp(appId).name" :aria-label="getApp(appId).name" @click="launch(appId)">
        <AppIcon :app="getApp(appId)" :size="store.mobile ? 22 : 24" /><span v-if="store.mobile" class="dock-label">{{ getApp(appId).name.replace('管理', '').replace('网关', '') }}</span><span class="ind" />
      </button>
      <button v-if="store.mobile" class="w12-dock-btn" :class="{ foc: store.appsOpen || !store.focusedId }" aria-label="全部应用" @click="store.showApps()">
        <AppstoreOutlined /><span class="dock-label">应用</span>
      </button>
    </div>
    <div v-if="!store.mobile" class="w12-dock w12-dock-tray"><div class="w12-tray">
      <button class="tray-ico" :title="theme.dark ? '浅色模式' : '深色模式'" :aria-label="theme.dark ? '浅色模式' : '深色模式'" @click="theme.setDark(!theme.dark)"><BgColorsOutlined /></button>
      <Popover placement="topRight" trigger="click" @open-change="loadNotices"><template #content>
        <div class="w12-notices">
          <p v-if="noticeState === 'loading'" role="status">正在读取通知…</p>
          <p v-else-if="noticeState === 'unsupported'">此轻量网关未启用用户公告。</p>
          <div v-else-if="noticeState === 'error'"><p>{{ noticeError }}</p><button @click="loadNotices(true)">重试</button></div>
          <p v-else-if="!notices.length">暂无通知</p>
          <article v-for="notice in notices" :key="notice.id"><span>{{ notice.title }}</span><small>{{ new Date(notice.created_at).toLocaleString() }}</small></article>
        </div>
      </template><button class="tray-ico" title="通知" aria-label="通知"><BellOutlined /></button></Popover>
      <Popover placement="topRight" trigger="click"><template #content><Calendar :fullscreen="false" style="width: 300px" /></template>
        <button class="w12-clock" title="日期和时间" aria-label="日期和时间"><span>{{ pad(now.getHours()) }}:{{ pad(now.getMinutes()) }}</span><span>{{ now.toLocaleDateString() }}</span></button>
      </Popover>
    </div></div>
  </nav>
</template>
