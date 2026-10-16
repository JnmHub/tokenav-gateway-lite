<script setup>
import { computed, ref, watch, nextTick } from 'vue'
import { PoweroffOutlined, SearchOutlined, UserOutlined, FullscreenOutlined } from '@ant-design/icons-vue'
import { APPS, openApp, getApp } from '../core/apps'
import { useAuthStore, useWindowsStore } from '../core/stores'
import AppIcon from './AppIcon.vue'
const props = defineProps({ open: { type: Boolean, default: false } })
const emit = defineEmits(['close'])
const auth = useAuthStore(), store = useWindowsStore()
const keyword = ref(''), showAll = ref(false), search = ref(null), menu = ref(null)
let previousFocus
const coreIds = ['dashboard', 'accounts', 'groups', 'settings', 'usage', 'opsCenter']
const listed = computed(() => (showAll.value || keyword.value.trim() ? APPS : APPS.filter(app => coreIds.includes(app.id)))
  .filter(app => !keyword.value.trim() || app.name.toLowerCase().includes(keyword.value.trim().toLowerCase())))
const recent = computed(() => store.recentIds.map(getApp).filter(Boolean))
watch(() => props.open, async (open) => {
  if (open) {
    previousFocus = document.activeElement; keyword.value = ''; await nextTick()
    if (props.open) search.value?.focus()
  } else if (previousFocus?.isConnected && !previousFocus.closest?.('[inert], [aria-hidden="true"]')) previousFocus.focus({ preventScroll: true })
})
const onTab = event => {
  const nodes = [...(menu.value?.querySelectorAll('button:not(:disabled), input:not(:disabled)') || [])]
  if (!nodes.length) return
  const index = nodes.indexOf(document.activeElement)
  if (event.shiftKey && index <= 0) { event.preventDefault(); nodes.at(-1).focus() }
  else if (!event.shiftKey && (index < 0 || index === nodes.length - 1)) { event.preventDefault(); nodes[0].focus() }
}
const launch = (id) => { emit('close'); openApp(id) }
const toggleFull = () => { const action = document.fullscreenElement ? document.exitFullscreen() : document.documentElement.requestFullscreen?.(); action?.catch(() => {}) }
</script>
<template>
  <section v-if="open" ref="menu" class="w12-start show" role="dialog" aria-modal="true" aria-label="开始菜单" @keydown.tab="onTab" @keydown.esc.stop="emit('close')">
    <div class="w12-start-body">
      <aside class="w12-start-l">
        <div class="w12-start-user"><UserOutlined /><span>{{ auth.user?.nickname || auth.user?.username || auth.user?.email || '未登录' }}</span></div>
        <label class="w12-start-search"><SearchOutlined /><input ref="search" v-model="keyword" aria-label="搜索应用" placeholder="搜索应用" /></label>
        <div class="w12-start-quick"><button @click="launch('settings')">网关设置</button><button @click="launch('opsCenter')">运维中心</button></div>
        <footer class="w12-start-tools"><button aria-label="全屏" title="全屏" @click="toggleFull"><FullscreenOutlined /></button><button @click="emit('close'); auth.logout()"><PoweroffOutlined />退出登录</button></footer>
      </aside>
      <div class="w12-start-r">
        <section class="w12-start-card"><div class="w12-start-cardtit"><span>{{ showAll || keyword ? '所有应用' : '已固定' }}</span><button @click="showAll = !showAll">{{ showAll ? '返回固定' : '所有应用' }}</button></div>
          <div class="w12-start-grid"><button v-for="app in listed" :key="app.id" class="w12-start-app" @click="launch(app.id)"><AppIcon :app="app" :size="30" /><span>{{ app.name }}</span></button></div>
          <p v-if="!listed.length" class="w12-start-empty">没有匹配的应用</p>
        </section>
        <section class="w12-start-card grow"><div class="w12-start-cardtit"><span>最近使用</span></div>
          <div class="w12-start-reco"><button v-for="app in recent" :key="app.id" class="w12-reco" @click="launch(app.id)"><AppIcon :app="app" :size="24" /><span>{{ app.name }}</span></button></div>
          <p v-if="!recent.length" class="w12-start-empty">打开应用后会显示在这里</p>
        </section>
      </div>
    </div>
  </section>
</template>
