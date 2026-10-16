import { theme as antdTheme } from 'ant-design-vue'

const FONT = "'Segoe UI', 'Segoe UI Variable Text', 'Microsoft YaHei', system-ui, sans-serif"
const alpha = (hex, opacity) => {
  const value = hex.replace('#', '')
  const full = value.length === 3 ? [...value].map((c) => c + c).join('') : value
  return `rgba(${parseInt(full.slice(0, 2), 16)}, ${parseInt(full.slice(2, 4), 16)}, ${parseInt(full.slice(4, 6), 16)}, ${opacity})`
}

// 所有组件使用 CSS 实际强调色；主题自己提供颜色时不能重新取旧预设的蓝色。
export function buildAntdTheme({ dark, accent = '#0067c0' }) {
  const primary = /^#[\da-f]{3}([\da-f]{3})?$/i.test(accent) ? accent : '#0067c0'
  const border = dark ? 'rgba(255,255,255,.14)' : 'rgba(0,0,0,.12)'
  const hover = dark ? 'rgba(255,255,255,.06)' : 'rgba(0,0,0,.04)'
  const control = dark ? 'rgba(255,255,255,.06)' : 'rgba(255,255,255,.62)'
  return {
    algorithm: dark ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
    token: {
      colorPrimary: primary, colorInfo: primary, colorLink: primary,
      fontFamily: FONT, fontSize: 14, borderRadius: 8, borderRadiusSM: 4, borderRadiusLG: 8,
      controlHeight: 32, controlHeightSM: 28,
      colorText: dark ? '#eeeeee' : '#1c1c1c', colorTextSecondary: dark ? '#b5b5b5' : '#5c5c5c',
      colorBorder: border, colorBorderSecondary: dark ? 'rgba(255,255,255,.07)' : 'rgba(0,0,0,.07)',
      colorBgContainer: dark ? '#2b2b2b' : '#fafafa',
      colorBgElevated: dark ? 'rgba(43,43,43,.98)' : 'rgba(251,251,252,.98)',
      colorBgSpotlight: '#3d3d3d',
      boxShadow: 'none', boxShadowSecondary: '0 2px 8px rgba(0,0,0,.10)',
    },
    components: {
      Button: {
        primaryShadow: 'none', defaultShadow: 'none', fontWeight: 400,
        defaultBg: 'transparent', defaultBorderColor: border,
        defaultHoverBg: hover, defaultHoverBorderColor: border,
        defaultActiveBg: hover, paddingContentHorizontal: 12,
      },
      Table: {
        headerBg: 'transparent', headerColor: dark ? '#b5b5b5' : '#5c5c5c', headerSplitColor: 'transparent',
        rowHoverBg: hover, rowSelectedBg: alpha(primary, .08), rowSelectedHoverBg: alpha(primary, .12),
        borderColor: dark ? 'rgba(255,255,255,.07)' : 'rgba(0,0,0,.07)',
        cellPaddingBlock: 8, cellPaddingBlockSM: 8, fontSize: 13,
      },
      Input: { activeShadow: 'none', colorBgContainer: control, colorBorder: border, hoverBorderColor: border, activeBorderColor: primary },
      InputNumber: { activeShadow: 'none', colorBgContainer: control, activeBorderColor: primary },
      Select: { optionSelectedBg: hover, activeBorderColor: primary, activeOutlineColor: 'transparent' },
      Checkbox: { borderRadiusXS: 4 },
      Switch: { trackMinWidth: 40, trackHeight: 20, trackPadding: 3, handleSize: 14 },
      Segmented: { trackBg: hover, itemSelectedBg: control, trackPadding: 2 },
      Pagination: { itemActiveBg: 'transparent' }, Progress: { remainingColor: hover },
      Dropdown: { fontSize: 13 },
    },
  }
}
