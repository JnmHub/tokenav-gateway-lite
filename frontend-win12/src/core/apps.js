/* 应用组件全部懒加载：首屏只加载桌面外壳，各模块在真正打开时按需拉取 chunk。
 * 这对后台很关键——19 个管理模块（含 ECharts 图表页）静态引入会让首屏包体膨胀数倍。 */
import { markRaw } from 'vue'
import { message } from 'ant-design-vue'
import { useWindowsStore } from './stores'
import { lazyApp as lazy } from './lazyApp'
import { parseAppLink } from './appLinks'

const Dashboard = lazy(() => import('../apps/Dashboard.vue'))
const Settings = lazy(() => import('../apps/Settings.vue'))
const Accounts = lazy(() => import('../apps/Accounts.vue'))
const Proxies = lazy(() => import('../apps/Proxies.vue'))
const Groups = lazy(() => import('../apps/Groups.vue'))
const Channels = lazy(() => import('../apps/Channels.vue'))
const Usage = lazy(() => import('../apps/Usage.vue'))
const ModelPrices = lazy(() => import('../apps/ModelPrices.vue'))
const LocalKeys = lazy(() => import('../apps/LocalKeys.vue'))
const RiskControl = lazy(() => import('../apps/RiskControl.vue'))
const ChannelMonitor = lazy(() => import('../apps/ChannelMonitor.vue'))
const AuditLogs = lazy(() => import('../apps/AuditLogs.vue'))
const OpsCenter = lazy(() => import('../apps/OpsCenter.vue'))
const RuntimeSummary = lazy(() => import('../apps/RuntimeSummary.vue'))
const ImageTasks = lazy(() => import('../apps/ImageTasks.vue'))
const SystemSettings = lazy(() => import('../apps/SystemSettings.vue'))
const ControlPlane = lazy(() => import('../apps/ControlPlane.vue'))
const GatewayBinding = lazy(() => import('../apps/GatewayBinding.vue'))
const Plugins = lazy(() => import('../apps/Plugins.vue'))

/* ============================================================
 * 应用注册表：网关后台的每个管理模块 = 一个 Win12「应用」。
 * 新增模块只需加一条
 * { id, name, glyph, size, onDesktop, pinned, padded, component }
 * glyph: 应用标识；AppIcon.vue 统一映射 Ant Design SVG（矢量）系统图标。
 *        **必填**：桌面图标与 Dock 都只渲染这个字形，缺失会落到通用兜底方块。
 * component: Vue 组件需 markRaw 避免被响应式代理
 *
 * 视觉约定（扁平化桌面）：所有应用共用同一个图标颜色（global.css 的
 * --dicon-icon，随明暗反转），不使用彩色底块——18 种亮色方块会让桌面显脏，
 * 辨识度由「图标字形 + 下方文字」承担。
 * ============================================================ */

export const APPS = [
  {
    id: 'dashboard',
    name: '仪表盘',
    glyph: 'dashboard',
    size: { w: 960, h: 640 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(Dashboard),
  },
  {
    id: 'settings',
    name: '网关设置',
    glyph: 'settings',
    size: { w: 960, h: 640 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(Settings),
  },
  {
    id: 'accounts',
    name: '账号管理',
    glyph: 'accounts',
    size: { w: 1120, h: 680 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(Accounts),
  },
  {
    id: 'proxies',
    name: '代理管理',
    glyph: 'proxies',
    size: { w: 1000, h: 640 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(Proxies),
  },
  {
    id: 'groups',
    name: '分组管理',
    glyph: 'groups',
    size: { w: 1000, h: 640 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(Groups),
  },
  {
    id: 'channels',
    name: '渠道管理',
    glyph: 'channels',
    size: { w: 1040, h: 640 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(Channels),
  },
  {
    id: 'usage',
    name: '用量查询',
    glyph: 'usage',
    size: { w: 1080, h: 660 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(Usage),
  },
  {
    id: 'modelPrices',
    name: '模型价格',
    glyph: 'modelPrices',
    size: { w: 1040, h: 640 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(ModelPrices),
  },
  {
    id: 'localKeys',
    name: '本地密钥',
    glyph: 'localKeys',
    size: { w: 1000, h: 620 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(LocalKeys),
  },
  {
    id: 'riskControl',
    name: '风控',
    glyph: 'riskControl',
    size: { w: 1080, h: 680 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(RiskControl),
  },
  {
    id: 'channelMonitor',
    name: '渠道监控',
    glyph: 'channelMonitor',
    size: { w: 1080, h: 640 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(ChannelMonitor),
  },
  {
    id: 'auditLogs',
    name: '审计日志',
    glyph: 'auditLogs',
    size: { w: 1120, h: 640 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(AuditLogs),
  },
  {
    id: 'opsCenter',
    name: '运维中心',
    glyph: 'opsCenter',
    size: { w: 1120, h: 680 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(OpsCenter),
  },
  {
    id: 'runtimeSummary',
    name: '运行摘要',
    glyph: 'runtimeSummary',
    size: { w: 1000, h: 620 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(RuntimeSummary),
  },
  {
    id: 'imageTasks',
    name: '镜像任务',
    glyph: 'imageTasks',
    size: { w: 1040, h: 620 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(ImageTasks),
  },
  {
    id: 'systemSettings',
    name: '系统配置',
    glyph: 'systemSettings',
    size: { w: 960, h: 640 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(SystemSettings),
  },
  {
    id: 'gatewayBinding', name: '主站绑定', glyph: 'controlPlane',
    size: { w: 760, h: 620 }, onDesktop: true, pinned: true, padded: false,
    component: markRaw(GatewayBinding),
  },
  {
    id: 'controlPlane',
    name: '控制面对接',
    glyph: 'controlPlane',
    size: { w: 1080, h: 660 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(ControlPlane),
  },
  {
    id: 'plugins',
    name: '插件管理',
    glyph: 'plugins',
    size: { w: 1120, h: 660 },
    onDesktop: true,
    pinned: true,
    padded: true,
    component: markRaw(Plugins),
  },
]

export function openApp(id, target = null) {
  const app = getApp(id)
  if (!app) { message.warning('该应用尚未提供，请从应用列表选择。'); return null }
  return useWindowsStore().open(app, target)
}

export function openLocationApp() {
  const link = parseAppLink(window.location.href, APPS.map((app) => app.id))
  if (link.error) { message.warning(link.error); return }
  if (link.appId) openApp(link.appId, link.target)
}

// 按 appId 查应用定义（Dock / WindowFrame 使用）
export function getApp(id) {
  return APPS.find((a) => a.id === id)
}

















