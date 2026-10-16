import { defineStore } from 'pinia'
import { request, setToken, getToken } from '../api/index.js'
import { getSessionEpoch, subscribeSessionReset, resetRequestSession } from '../api/session.js'
import { STYLES, DEFAULT_STYLE, styleOwnsAccent } from './themes.js'
import { constrainRect, initialRect, maximize, restore, snap, snapRect, withState, resume, orderLayers, topWindow } from './windowState.js'

export const THEME_PRESETS = [
  { name: 'Win12 经典', c1: '#ad6eca', c2: '#3b91d8', primary: '#0067c0' },
  { name: '海洋蓝', c1: '#2886d6', c2: '#54c7ec', primary: '#1677ff' },
  { name: '薄荷绿', c1: '#00b294', c2: '#6fd05f', primary: '#00a870' },
  { name: '落日橙', c1: '#f7630c', c2: '#ffb900', primary: '#fa8c16' },
]
function lsGet(key, fallback) {
  try { const value = localStorage.getItem(key); return value === null ? fallback : JSON.parse(value) }
  catch { return fallback }
}
export const useThemeStore = defineStore('theme', {
  state: () => ({
    dark: lsGet('w12-dark', false),
    style: (() => { const saved = lsGet('w12-style', DEFAULT_STYLE); return STYLES.some((s) => s.id === saved) ? saved : DEFAULT_STYLE })(),
    preset: lsGet('w12-preset', THEME_PRESETS[0]), wallpaper: lsGet('w12-wall', 'bg'), mica: lsGet('w12-mica', true), accent: '#0067c0',
  }),
  actions: {
    _apply() {
      const root = document.documentElement
      root.classList.toggle('dark', this.dark); root.classList.toggle('mica', this.mica); root.dataset.style = this.style
      const active = STYLES.find((s) => s.id === this.style)
      if (styleOwnsAccent(active)) {
        for (const key of ['--theme-1', '--theme-2', '--accent']) root.style.removeProperty(key)
      } else {
        const preset = this.preset || THEME_PRESETS[0]
        root.style.setProperty('--theme-1', preset.c1); root.style.setProperty('--theme-2', preset.c2)
        root.style.setProperty('--accent', preset.primary || preset.c2)
      }
      // CSS、组件库和图表读取同一强调色，不能各取预设的不同颜色。
      this.accent = getComputedStyle(root).getPropertyValue('--accent').trim() || '#0067c0'
      window.dispatchEvent(new CustomEvent('w12-theme-change', { detail: { dark: this.dark, accent: this.accent } }))
    },
    setStyle(style) { if (!STYLES.some((s) => s.id === style)) return; this.style = style; localStorage.setItem('w12-style', JSON.stringify(style)); this._apply() },
    setDark(dark) {
      this.dark = dark; this.wallpaper = dark ? 'bg-dark' : 'bg'
      localStorage.setItem('w12-dark', JSON.stringify(dark)); localStorage.setItem('w12-wall', JSON.stringify(this.wallpaper)); this._apply()
    },
    setPreset(preset) { this.preset = preset; localStorage.setItem('w12-preset', JSON.stringify(preset)); this._apply() },
    setWallpaper(wallpaper) { this.wallpaper = wallpaper; localStorage.setItem('w12-wall', JSON.stringify(wallpaper)); this._apply() },
    setMica(on) { this.mica = on; localStorage.setItem('w12-mica', JSON.stringify(on)); this._apply() },
  },
})

const sessionSubscriptions = new WeakMap()
let resettingSession = false
export const useAuthStore = defineStore('auth', {
  state: () => ({ user: lsGet('w12-user', null), sessionEpoch: getSessionEpoch() }),
  actions: {
    initSession() {
      if (sessionSubscriptions.has(this)) return
      const unsubscribe = subscribeSessionReset(({ epoch, reason }) => {
        this.sessionEpoch = epoch; this.user = null; useWindowsStore().reset()
        // storage 只清本页实例，不写共享凭据，否则旧标签页会注销刚登录的新标签页。
        if (reason !== 'storage-change') localStorage.removeItem('w12-user')
        // Shell 是唯一事件发送方；请求模块仅管理代际，避免互相 reset 循环。
        window.dispatchEvent(new CustomEvent('w12-session-reset', { detail: { epoch, reason } }))
      })
      sessionSubscriptions.set(this, unsubscribe)
    },
    disposeSession() { sessionSubscriptions.get(this)?.(); sessionSubscriptions.delete(this) },
    invalidateLocalSession() { this.initSession(); resetRequestSession('storage-change') },
    async login(username, password) {
      const auth = await request('/auth/login', { body: { email: username, password } })
      if (auth?.requires_2fa) {
        const error = new Error('requires_2fa')
        error.twoFactor = { tempToken: auth.temp_token, emailMasked: auth.user_email_masked || username }; throw error
      }
      return this.acceptLogin(auth)
    },
    async login2FA(tempToken, totpCode) { return this.acceptLogin(await request('/auth/login/2fa', { body: { temp_token: tempToken, totp_code: totpCode } })) },
    acceptLogin(auth) {
      this.initSession()
      const before = getSessionEpoch()
      setToken(auth.access_token)
      // 同 token 重登也清旧实例；token 改变时请求层已递增，只做一次。
      if (getSessionEpoch() === before) resetRequestSession('login')
      this.sessionEpoch = getSessionEpoch(); localStorage.setItem('w12-user', JSON.stringify(auth.user)); this.user = auth.user
      return auth.user
    },
    resetSession(reason = 'logout') {
      if (resettingSession) return
      this.initSession()
      const hadToken = !!getToken()
      if (!hadToken && !this.user && !useWindowsStore().windows.length) return
      resettingSession = true
      try {
        setToken(null)
        if (!hadToken) resetRequestSession(reason)
        this.sessionEpoch = getSessionEpoch(); this.user = null; localStorage.removeItem('w12-user'); useWindowsStore().reset()
      } finally { resettingSession = false }
    },
    logout() { this.resetSession('logout') },
  },
})

let sequence = 0
let cascade = 0
const closeTasks = new Map()
const pendingClosures = new Map()
const closeGuards = new Map()
const cancelClose = (id) => { clearTimeout(closeTasks.get(id)); closeTasks.delete(id); pendingClosures.delete(id) }
export const useWindowsStore = defineStore('windows', {
  state: () => ({
    windows: [], focusedId: null, recentIds: [], mobile: false, appsOpen: false, startOpen: false,
    snapPreview: null, keyboardOpen: false, area: { x: 0, y: 0, w: 1280, h: 740 }, reducedMotion: false,
  }),
  actions: {
    open(app, target = null) {
      if (!app) return null
      this.startOpen = false; this.appsOpen = false
      this.recentIds = [app.id, ...this.recentIds.filter((id) => id !== app.id)].slice(0, 8)
      const existing = this.windows.find((w) => w.appId === app.id)
      if (existing) {
        cancelClose(existing.id)
        if (existing.state === 'closing') this.windows = this.windows.map((w) => w.id === existing.id ? resume(w) : w)
        if (target) this.windows = this.windows.map((w) => w.id === existing.id ? { ...w, target, targetVersion: w.targetVersion + 1 } : w)
        this.focus(existing.id); return existing.id
      }
      const rect = initialRect(app.size, this.area, cascade++)
      const id = `${app.id}-${++sequence}`
      this.windows = orderLayers([...this.windows, withState({ id, appId: app.id, ...rect, restoreRect: rect,
        z: 100, target, targetVersion: 1, minimum: app.minSize || { w: 360, h: 240 } }, 'normal')], id)
      this.focusedId = id; return id
    },
    registerCloseGuard(id, guard) { closeGuards.set(id, guard); return () => { if (closeGuards.get(id) === guard) closeGuards.delete(id) } },
    async close(id) {
      const win = this.windows.find((w) => w.id === id)
      if (!win || win.state === 'closing' || pendingClosures.has(id)) return
      const attempt = Symbol(id)
      pendingClosures.set(id, attempt)
      const guard = closeGuards.get(id)
      try {
        if (guard && !await guard()) return
        // 等待确认期间若已重新打开/注销，旧确认不得再关闭新的活动窗口。
        if (pendingClosures.get(id) !== attempt || !this.windows.some((w) => w.id === id)) return
        const previousState = win.state === 'minimized' ? win.previousState : win.state
        this.windows = this.windows.map((w) => w.id === id ? withState({ ...w, previousState }, 'closing') : w)
        this.focusedId = topWindow(this.windows)?.id || null
        cancelClose(id)
        closeTasks.set(id, setTimeout(() => {
          if (this.windows.find((w) => w.id === id)?.state === 'closing') this.remove(id)
        }, this.reducedMotion ? 0 : 180))
      } finally { if (pendingClosures.get(id) === attempt) pendingClosures.delete(id) }
    },
    remove(id) {
      cancelClose(id); closeGuards.delete(id); this.windows = orderLayers(this.windows.filter((w) => w.id !== id))
      if (this.focusedId === id) this.focusedId = topWindow(this.windows)?.id || null
    },
    focus(id) {
      const win = this.windows.find((w) => w.id === id)
      if (!win || win.state === 'closing') return
      this.appsOpen = false
      this.windows = orderLayers(this.windows.map((w) => w.id === id && w.state === 'minimized' ? resume(w) : w), id)
      this.focusedId = id
    },
    minimize(id) {
      const target = this.windows.find((w) => w.id === id)
      if (!target || ['minimized', 'closing'].includes(target.state)) return
      this.windows = this.windows.map((w) => w.id === id ? withState({ ...w, previousState: w.state }, 'minimized') : w)
      this.focusedId = topWindow(this.windows)?.id || null
      if (this.mobile && !this.focusedId) this.appsOpen = true
    },
    toggleFromTaskbar(id) {
      const win = this.windows.find((w) => w.id === id)
      if (!win) return
      if (this.mobile || win.min || this.focusedId !== id) this.focus(id); else this.minimize(id)
    },
    toggleMax(id) {
      if (this.mobile) return
      this.windows = this.windows.map((w) => w.id === id
        ? (w.state === 'maximized' || w.state === 'snapped' ? restore(w, this.area, w.minimum) : maximize(w, this.area)) : w)
      this.focus(id)
    },
    setRect(id, rect) {
      this.windows = this.windows.map((w) => {
        if (w.id !== id) return w
        const bounded = constrainRect(rect, this.area, w.minimum)
        return withState({ ...w, ...bounded, restoreRect: bounded, snapSide: null }, 'normal')
      })
    },
    snap(id, side) { this.windows = this.windows.map((w) => w.id === id ? snap(w, side, this.area) : w); this.focus(id); this.snapPreview = null },
    updateWorkspace(area, width, keyboardOpen = false) {
      const wasMobile = this.mobile
      this.mobile = width < 768; this.keyboardOpen = keyboardOpen; this.area = area
      // 手机仅改变呈现方式，返回桌面再限制普通窗口，最大化与吸附按实际工作区重算。
      if (!this.mobile) this.windows = this.windows.map((w) => {
        const state = w.min || w.closing ? w.previousState : w.state
        const adjusted = state === 'maximized' ? { ...area } : state === 'snapped' ? snapRect(w.snapSide, area) : constrainRect(w, area, w.minimum)
        const restoreRect = constrainRect(state === 'normal' ? adjusted : w.restoreRect || w, area, w.minimum)
        return { ...w, ...adjusted, restoreRect }
      })
      if (wasMobile !== this.mobile) { this.snapPreview = null; this.startOpen = false; this.appsOpen = this.mobile && !this.focusedId }
    },
    showApps() { this.startOpen = false; this.appsOpen = true },
    reset() {
      for (const id of closeTasks.keys()) cancelClose(id)
      pendingClosures.clear(); closeGuards.clear(); this.windows = []; this.focusedId = null; this.recentIds = []
      this.startOpen = false; this.appsOpen = false; this.snapPreview = null
    },
  },
})
