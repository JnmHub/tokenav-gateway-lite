/* ============================================================
 * 桌面壁纸（纯 CSS，零图片依赖）
 * ------------------------------------------------------------
 * 设计原则：
 *   1. 每套主题配一套「同气质」壁纸 —— 五案各深浅两版，共 10 套，互不相同；
 *      切主题时桌面背景随之换，窗口/图标浮在其上才像一套完整系统。
 *   2. 保留原始饱和度（不加雾化/降饱和层），也不做「壁纸穿透应用窗口」——
 *      窗口材质仍由各主题包的 --bg50/--bg70/--unfoc 决定。
 *   3. 只用渐变/重复线性渐变（极细网格、斜纹、光晕），首屏零下载，
 *      且在任何 DPR 下都清晰。
 *
 * 壁纸只作用于桌面与锁屏层（.w12-desktop / .w12-lock）。
 * ============================================================ */

const WP = {}

/* —— A · Fluent 原生：冷灰 + 蓝光晕（Win11 设置应用气质）—— */
WP.fluent = {
  light: [
    'radial-gradient(70% 55% at 84% 4%, rgba(0, 103, 192, 0.16), rgba(0, 103, 192, 0) 68%)',
    'radial-gradient(50% 45% at 4% 98%, rgba(0, 103, 192, 0.10), rgba(0, 103, 192, 0) 70%)',
    '#f0f2f5',
  ].join(','),
  dark: [
    'radial-gradient(70% 55% at 84% 4%, rgba(78, 160, 255, 0.20), rgba(78, 160, 255, 0) 68%)',
    'radial-gradient(50% 45% at 4% 98%, rgba(78, 160, 255, 0.12), rgba(78, 160, 255, 0) 70%)',
    '#121417',
  ].join(','),
}

/* —— B · 靛夜云母：哑光靛紫极光（低对比雾面）—— */
WP.indigo = {
  light: [
    'linear-gradient(200deg, rgba(94, 106, 210, 0.16), rgba(94, 106, 210, 0) 42%)',
    'radial-gradient(60% 50% at 12% 8%, rgba(139, 147, 238, 0.18), rgba(139, 147, 238, 0) 70%)',
    '#eceefa',
  ].join(','),
  dark: [
    'linear-gradient(200deg, rgba(94, 106, 210, 0.34), rgba(10, 11, 16, 0) 55%)',
    'radial-gradient(65% 55% at 15% 5%, rgba(139, 147, 238, 0.22), rgba(139, 147, 238, 0) 72%)',
    '#0d0e14',
  ].join(','),
}

/* —— C · 翠语义：青绿斜纹 + 弱光晕 —— */
WP.verdant = {
  light: [
    'repeating-linear-gradient(135deg, rgba(26, 127, 55, 0.055) 0 12px, rgba(26, 127, 55, 0) 12px 26px)',
    'radial-gradient(60% 50% at 88% 8%, rgba(26, 127, 55, 0.16), rgba(26, 127, 55, 0) 70%)',
    '#eef7f1',
  ].join(','),
  dark: [
    'repeating-linear-gradient(135deg, rgba(63, 185, 80, 0.06) 0 12px, rgba(63, 185, 80, 0) 12px 26px)',
    'radial-gradient(60% 50% at 88% 8%, rgba(63, 185, 80, 0.18), rgba(63, 185, 80, 0) 72%)',
    '#0c1412',
  ].join(','),
}

/* —— D · 曜黑锐利：纯白/纯黑 + 极细网格（不要光晕，硬朗）—— */
WP.obsidian = {
  light: [
    'repeating-linear-gradient(0deg, rgba(0, 0, 0, 0.045) 0 1px, rgba(0, 0, 0, 0) 1px 28px)',
    'repeating-linear-gradient(90deg, rgba(0, 0, 0, 0.045) 0 1px, rgba(0, 0, 0, 0) 1px 28px)',
    '#ffffff',
  ].join(','),
  dark: [
    'repeating-linear-gradient(0deg, rgba(255, 255, 255, 0.06) 0 1px, rgba(255, 255, 255, 0) 1px 28px)',
    'repeating-linear-gradient(90deg, rgba(255, 255, 255, 0.06) 0 1px, rgba(255, 255, 255, 0) 1px 28px)',
    '#000000',
  ].join(','),
}

/* —— E · 深海商务：群青深海渐变 + 蓝光晕 —— */
WP.deepsea = {
  light: [
    'linear-gradient(155deg, rgba(111, 141, 255, 0.22), rgba(99, 91, 255, 0.04) 45%, rgba(255, 255, 255, 0) 70%)',
    'radial-gradient(70% 55% at 12% 92%, rgba(99, 91, 255, 0.14), rgba(99, 91, 255, 0) 70%)',
    '#e9f1fb',
  ].join(','),
  dark: [
    'linear-gradient(155deg, rgba(99, 91, 255, 0.34), rgba(6, 20, 40, 0.2) 50%)',
    'radial-gradient(75% 60% at 85% 6%, rgba(79, 172, 254, 0.20), rgba(79, 172, 254, 0) 72%)',
    '#081a30',
  ].join(','),
}

/* —— 经典两套：win12 毛玻璃（原版蓝紫双光斑升级版）/ flat 扁平化（近乎无装饰）—— */
WP.win12 = {
  light: [
    'radial-gradient(120% 90% at 78% -10%, rgba(59, 145, 216, 0.20), rgba(59, 145, 216, 0) 60%)',
    'radial-gradient(90% 70% at 8% 105%, rgba(173, 110, 202, 0.16), rgba(173, 110, 202, 0) 62%)',
    '#eef1f6',
  ].join(','),
  dark: [
    'radial-gradient(120% 90% at 78% -10%, rgba(90, 150, 220, 0.24), rgba(90, 150, 220, 0) 60%)',
    'radial-gradient(90% 70% at 8% 105%, rgba(150, 110, 200, 0.18), rgba(150, 110, 200, 0) 62%)',
    '#12151c',
  ].join(','),
}
WP.flat = {
  light: [
    'radial-gradient(50% 40% at 84% 6%, rgba(0, 0, 0, 0.03), rgba(0, 0, 0, 0) 70%)',
    '#f4f5f7',
  ].join(','),
  dark: [
    'radial-gradient(50% 40% at 84% 6%, rgba(255, 255, 255, 0.04), rgba(255, 255, 255, 0) 70%)',
    '#181a1d',
  ].join(','),
}

/** 取指定风格 + 明暗的壁纸 background 值（未注册的风格回退到经典毛玻璃那套） */
export function wallpaperBackground(style, dark) {
  const pack = WP[style] || WP.win12
  return dark ? pack.dark : pack.light
}

/** 全部已注册壁纸（自检/回归用：断言五案深浅共 10 套互不相同） */
export const WALLPAPER_PACKS = WP
