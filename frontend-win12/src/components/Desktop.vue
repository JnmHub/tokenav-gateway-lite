<script setup>
import { computed, onMounted, onBeforeUnmount, ref, nextTick } from 'vue'
import { BgColorsOutlined, FolderOpenOutlined } from '@ant-design/icons-vue'
import { APPS, openApp, openLocationApp } from '../core/apps'
import { useThemeStore, useWindowsStore } from '../core/stores'
import { workArea } from '../core/windowState'
import AppIcon from './AppIcon.vue'
import WindowFrame from './WindowFrame.vue'
import ContextMenu from './ContextMenu.vue'
import Dock from './Dock.vue'
import StartMenu from './StartMenu.vue'
import { wallpaperBackground } from '../core/wallpaper'

const store = useWindowsStore(), theme = useThemeStore()
const selIcon = ref(null), ctx = ref(null), desktop = ref(null)
const bgStyle = computed(() => ({ background: wallpaperBackground(theme.style, theme.dark) }))
const coarse = window.matchMedia('(pointer: coarse)').matches
const desktopApps = APPS.filter((app) => app.onDesktop)
let dockObserver, areaFrame = 0
const syncWorkspace = () => {
  cancelAnimationFrame(areaFrame)
  areaFrame = requestAnimationFrame(() => {
    const visual = window.visualViewport
    const width = window.innerWidth, mobile = width < 768
    const keyboardOpen = mobile && visual && window.innerHeight - visual.height > 120
    desktop.value?.style.setProperty('--visual-height', `${visual?.height || window.innerHeight}px`)
    const dock = document.querySelector('.w12-dock-box')?.getBoundingClientRect()
    const safeTop = parseFloat(getComputedStyle(desktop.value).paddingTop) || 0
    const height = keyboardOpen ? visual.height : window.innerHeight
    const area = workArea({ width, height, y: mobile ? safeTop : 0,
      dockTop: keyboardOpen ? height : dock?.top, bottom: mobile ? 0 : 8 })
    store.updateWorkspace(area, width, !!keyboardOpen)
  })
}
const outside = (event) => {
  // 按钮的 pointerdown 不先关闭菜单，click（点击）才进行唯一的开/关动作。
  if (!event.target.closest('.w12-start, [data-start-button]')) store.startOpen = false
  if (!event.target.closest('.w12-dicon')) selIcon.value = null
}
const keydown = (event) => {
  if (event.key === 'Escape') { store.startOpen = false; ctx.value = null }
}
const navigate = () => openLocationApp()
onMounted(async () => {
  syncWorkspace(); await nextTick()
  const dock = document.querySelector('.w12-dock-box')
  if (dock) { dockObserver = new ResizeObserver(syncWorkspace); dockObserver.observe(dock) }
  store.reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  window.addEventListener('resize', syncWorkspace); window.visualViewport?.addEventListener('resize', syncWorkspace)
  window.visualViewport?.addEventListener('scroll', syncWorkspace)
  window.addEventListener('keydown', keydown); window.addEventListener('hashchange', navigate); window.addEventListener('popstate', navigate)
  navigate()
})
onBeforeUnmount(() => {
  cancelAnimationFrame(areaFrame); dockObserver?.disconnect()
  window.removeEventListener('resize', syncWorkspace); window.visualViewport?.removeEventListener('resize', syncWorkspace)
  window.visualViewport?.removeEventListener('scroll', syncWorkspace); window.removeEventListener('keydown', keydown)
  window.removeEventListener('hashchange', navigate); window.removeEventListener('popstate', navigate)
})
const desktopCtxItems = computed(() => [
  { key: 'dark', icon: BgColorsOutlined, label: theme.dark ? '切换到浅色模式' : '切换到深色模式', onClick: () => theme.setDark(!theme.dark) },
  { key: 'personal', icon: BgColorsOutlined, label: '外观设置', onClick: () => openApp('settings', { tab: 'general' }) },
])
const onDesktopCtx = (event) => {
  if (event.target.closest('.w12-win, .w12-dicon, .w12-dock-box')) return
  event.preventDefault(); store.startOpen = false; ctx.value = { x: event.clientX, y: event.clientY, items: desktopCtxItems.value }
}
const onIconCtx = (app, event) => {
  event.preventDefault(); event.stopPropagation()
  ctx.value = { x: event.clientX, y: event.clientY, items: [{ key: 'open', icon: FolderOpenOutlined, label: `打开 ${app.name}`, onClick: () => openApp(app.id) }] }
}
</script>
<template>
  <div ref="desktop" class="w12-desktop" :class="{ 'mobile-mode': store.mobile, 'keyboard-open': store.keyboardOpen }"
    :style="bgStyle" @contextmenu="onDesktopCtx" @pointerdown="outside">
    <div class="w12-wall-logo" aria-hidden="true"><img src="/logo-mark.png" alt="" draggable="false" /></div>
    <div v-if="!store.mobile" class="w12-dicons" aria-label="桌面应用">
      <button v-for="app in desktopApps" :key="app.id" class="w12-dicon" :class="{ sel: selIcon === app.id }"
        @pointerdown="selIcon = app.id" @click="coarse && openApp(app.id)" @dblclick="!coarse && openApp(app.id)"
        @keydown.enter="openApp(app.id)" @contextmenu="onIconCtx(app, $event)">
        <AppIcon :app="app" :size="40" /><span class="name">{{ app.name }}</span>
      </button>
    </div>
    <WindowFrame v-for="win in store.windows" :key="win.id" :win="win" />
    <div v-if="store.snapPreview" class="w12-snap-preview" :style="{ left: store.snapPreview.x + 'px', top: store.snapPreview.y + 'px', width: store.snapPreview.w + 'px', height: store.snapPreview.h + 'px' }" aria-hidden="true" />
    <main v-if="store.mobile && (store.appsOpen || !store.focusedId)" class="w12-app-switcher">
      <h1>应用</h1>
      <section v-if="store.windows.length" class="w12-running-apps"><h2>正在运行</h2>
        <button v-for="win in store.windows.filter(w => !w.closing)" :key="win.id" @click="store.focus(win.id)">
          <AppIcon :app="APPS.find(a => a.id === win.appId)" :size="24" />{{ APPS.find(a => a.id === win.appId)?.name }}<span>{{ win.min ? '已最小化' : '打开' }}</span>
        </button>
      </section>
      <div class="w12-app-grid"><button v-for="app in APPS" :key="app.id" @click="openApp(app.id)"><AppIcon :app="app" :size="30" /><span>{{ app.name }}</span></button></div>
    </main>
    <StartMenu :open="store.startOpen" @close="store.startOpen = false" />
    <Dock :start-open="store.startOpen" @toggle-start="store.startOpen = !store.startOpen" />
    <ContextMenu v-if="ctx" :pos="ctx" :items="ctx.items" @close="ctx = null" />
  </div>
</template>
