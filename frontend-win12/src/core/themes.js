/* ============================================================
 * 主题风格注册表（模块化主题体系）
 * ------------------------------------------------------------
 * 设计：风格（style）与明暗（dark）正交。
 *   - dark：既有布尔开关，html.dark 类（所有页面已适配）
 *   - style：主题风格包，html[data-style] 属性驱动
 *
 * 两代风格并存：
 *   - 经典两套（win12 毛玻璃 / flat 扁平化）：不带 accent，强调色由「主题色」预设控制；
 *   - 五案（fluent / indigo / verdant / obsidian / deepsea）：每案自带 accent，
 *     强调色不再由预设覆盖（setStyle 时会清掉行内预设），保证「一套主题一个气质」。
 *
 * 新增一种风格只需三步：
 *   1. 新建 src/styles/themes/<id>.css —— 用
 *      `:root[data-style="<id>"]` / `:root[data-style="<id>"].dark`
 *      覆写 global.css（外壳层）与 fluent.css（应用层）的设计令牌；
 *   2. 在 STYLES 里注册（自带配色的再填 accent/tone）并到 main.js import 对应 css；
 *   3. 壁纸在 core/wallpaper.js 的 WALLPAPERS 里按 <id> 增加深浅两套。
 * 业务组件禁止写死颜色/圆角/模糊——一律引用令牌变量，
 * 保证任何风格切换无需改业务代码。
 * ============================================================ */

export const DEFAULT_STYLE = 'win12'

/** 五案主题（每案自带强调色，见 styles/themes/*.css 的注释说明区分轴） */
export const CASE_STYLES = [
  {
    id: 'fluent',
    name: 'Fluent 原生',
    desc: '中性冷灰画布、纯白卡片靠 1px 描边分层、几乎不用投影、标准 Fluent 蓝',
    tag: 'A',
    accent: { c1: '#0067c0', c2: '#2b88d8', primary: '#0067c0' },
    tone: '#0067c0',
  },
  {
    id: 'indigo',
    name: '靛夜云母',
    desc: '淡靛紫画布、哑光弱玻璃、紧凑圆角，靛色细描边代替投影',
    tag: 'B',
    accent: { c1: '#5e6ad2', c2: '#7b85e8', primary: '#5e6ad2' },
    tone: '#5e6ad2',
  },
  {
    id: 'verdant',
    name: '翠语义',
    desc: '薄荷绿画布、白卡 + 绿描边、绿调柔影，强调色＝成功色',
    tag: 'C',
    accent: { c1: '#1a7f37', c2: '#34a853', primary: '#1a7f37' },
    tone: '#1a7f37',
  },
  {
    id: 'obsidian',
    name: '曜黑锐利',
    desc: '纯黑白画布、4px 锐角、高对比硬描边、最弱玻璃',
    tag: 'D',
    accent: { c1: '#0072f5', c2: '#3b9dff', primary: '#0072f5' },
    tone: '#0072f5',
  },
  {
    id: 'deepsea',
    name: '深海商务',
    desc: '淡蓝画布、最厚玻璃、最大圆角，卡片靠深投影浮起',
    tag: 'E',
    accent: { c1: '#635bff', c2: '#6f8dff', primary: '#635bff' },
    tone: '#635bff',
  },
]

/** 经典两套风格（不自带强调色，由「主题色」预设驱动），保留原有 CSS 与观感 */
export const LEGACY_STYLES = [
  {
    id: 'win12',
    name: 'Win12 毛玻璃',
    desc: 'Fluent 亚克力 / Mica 材质，半透明分层，当前基准风格',
  },
  {
    id: 'flat',
    name: '扁平化',
    desc: '实底卡片、小圆角、无毛玻璃、极弱阴影',
  },
]

export const STYLES = [...LEGACY_STYLES, ...CASE_STYLES]

export const styleById = (id) => STYLES.find((s) => s.id === id) || STYLES[0]

/** 该风格是否自带强调色（自带时不被「主题色」预设覆盖） */
export const styleOwnsAccent = (style) => Boolean(style && style.accent)
