<script setup>
/* ============================================================
 * AppGlyph —— 手绘 SVG 图标体系（桌面 / 开始菜单 / Dock / 窗口标题栏共用）
 * ------------------------------------------------------------
 * 设计契约（19 枚应用字形，全部 24 视口手绘，无第三方图标库）：
 *   1. 画布：viewBox 0 0 24 24；安全区 3~21，笔画半宽 0.8，任何形状不越界。
 *   2. 主笔画：stroke="currentColor"、stroke-width=1.6（24 视口下，不细于 1.4），
 *      round 线帽 / 圆角连接；只用线与圆，不堆细碎细节（16px 仍要能认）。
 *   3. 强调色：每枚**最多一处** var(--accent) 实心点缀（fill / stroke="none"），
 *      位置即语义（指针轴心、密钥孔、齿轮轮毂…），随主题强调色变化；
 *      主笔画绝不用强调色，保证暗色/桌面/开始菜单三处颜色自动一致。
 *   4. 辨识度：19 个应用语义互不重叠的 19 套构图，禁止复用同一套通用图形。
 *   5. 尺寸：由 size 控制宽高，viewBox 固定，故 16 / 20 / 26 / 32 / 44px
 *      共用同一份几何，无需多套素材。
 *   6. 间距：相邻笔画中心线的净距 ≥ 2.2（24 视口），避免 16px 下糊成一团。
 * ============================================================ */
defineProps({
  // 应用 id（= APPS[].glyph），见 src/core/apps.js
  name: { type: String, default: '' },
  // 渲染尺寸（px），默认 24
  size: { type: Number, default: 24 },
})
</script>

<template>
  <svg
    class="app-glyph"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.6"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <!-- 仪表盘：3/4 表盘 + 指针 + 强调色轴心 -->
    <g v-if="name === 'dashboard'">
      <path d="M6.06 17.94A8.4 8.4 0 1 1 17.94 17.94" />
      <path d="M12 12 16.5 7.5" />
      <circle cx="12" cy="12" r="1.8" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 网关设置：齿轮（8 齿 + 轮毂） -->
    <g v-else-if="name === 'settings'">
      <circle cx="12" cy="12" r="4.6" />
      <path d="M18 12h2.2" />
      <path d="M16.2 7.8 17.8 6.2" />
      <path d="M12 6V3.8" />
      <path d="M7.8 7.8 6.2 6.2" />
      <path d="M6 12H3.8" />
      <path d="M7.8 16.2 6.2 17.8" />
      <path d="M12 18v2.2" />
      <path d="M16.2 16.2 17.8 17.8" />
      <circle cx="12" cy="12" r="1.6" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 账号管理：两个人（右侧账号用强调色实心） -->
    <g v-else-if="name === 'accounts'">
      <circle cx="7.9" cy="8" r="2.9" />
      <path d="M3.8 19.6a4.1 4.1 0 0 1 8.2 0" />
      <circle cx="17.4" cy="9.6" r="2.3" fill="var(--accent)" stroke="none" />
      <path d="M14.2 19.6a3.2 3.2 0 0 1 6.4 0" />
    </g>

    <!-- 代理管理：双向转发箭头（上右 / 下左）+ 强调色中转条 -->
    <g v-else-if="name === 'proxies'">
      <path d="M4.6 7h10.3" />
      <path d="M12.4 4.4 14.9 7 12.4 9.6" />
      <path d="M19.4 17H9.1" />
      <path d="M11.6 14.4 9.1 17 11.6 19.6" />
      <rect x="10.4" y="11.2" width="3.2" height="1.6" rx=".8" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 分组管理：三层堆叠 + 顶层强调色菱形 -->
    <g v-else-if="name === 'groups'">
      <path d="M12 3.2 20.6 7.8 12 12.4 3.4 7.8z" />
      <path d="M3.4 12 12 16.6l8.6-4.6" />
      <path d="M3.4 16.2 12 20.8l8.6-4.6" />
      <path d="M12 6.1 15.2 7.8 12 9.5 8.8 7.8z" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 渠道管理：数据舱（圆柱 + 中缝）+ 顶盖强调色指示点 -->
    <g v-else-if="name === 'channels'">
      <ellipse cx="12" cy="6.6" rx="7.4" ry="2.9" />
      <path d="M4.6 6.6v10.8" />
      <path d="M19.4 6.6v10.8" />
      <path d="M4.6 12.2a7.4 2.9 0 0 0 14.8 0" />
      <path d="M4.6 17.4a7.4 2.9 0 0 0 14.8 0" />
      <circle cx="12" cy="6.6" r="1.5" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 用量查询：坐标基线 + 递增柱，最高柱用强调色 -->
    <g v-else-if="name === 'usage'">
      <path d="M4.6 20.6h14.8" />
      <path d="M7 20.6v-5.4" />
      <path d="M12 20.6v-9" />
      <rect x="16.1" y="6.8" width="1.8" height="13.8" rx=".9" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 模型价格：标签牌 + 强调色吊孔 -->
    <g v-else-if="name === 'modelPrices'">
      <path d="M4.6 3.4h8.2l6.8 6.8a2.1 2.1 0 0 1 0 3l-6.7 6.7a2.1 2.1 0 0 1-3 0L3.4 12.8V4.6a1.2 1.2 0 0 1 1.2-1.2z" />
      <circle cx="7.1" cy="7.1" r="1.6" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 本地密钥：钥匙（环 + 杆 + 两齿）+ 强调色锁孔 -->
    <g v-else-if="name === 'localKeys'">
      <circle cx="7.8" cy="16.2" r="4.2" />
      <path d="M10.9 13.1 20.4 3.6" />
      <path d="M15.6 8.4 17.4 10.2" />
      <path d="M18.2 5.8 19.8 7.4" />
      <circle cx="7.8" cy="16.2" r="1.6" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 风控：盾牌 + 警示叹号（强调色为叹号圆点） -->
    <g v-else-if="name === 'riskControl'">
      <path d="M12 3.6 5.2 6.4v5.5c0 4.1 2.8 7.6 6.8 8.9 4-1.3 6.8-4.8 6.8-8.9V6.4z" />
      <path d="M12 8.4v4.2" />
      <circle cx="12" cy="16" r="1.6" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 渠道监控：监控屏 + 心跳波形，底座用强调色短条 -->
    <g v-else-if="name === 'channelMonitor'">
      <rect x="3.4" y="4.2" width="17.2" height="12.4" rx="2.4" />
      <path d="M6.6 10.4h2.1l1.6-3.2 2 6.4 1.6-3.2h2.1" />
      <path d="M12 16.6v2.4" />
      <rect x="9.4" y="19" width="5.2" height="1.8" rx=".9" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 审计日志：折角文书 + 两条记录行 + 强调色重点条 -->
    <g v-else-if="name === 'auditLogs'">
      <path d="M6.9 3.2h7.4l4.6 4.6v11.2a1.8 1.8 0 0 1-1.8 1.8H6.9a1.8 1.8 0 0 1-1.8-1.8V5a1.8 1.8 0 0 1 1.8-1.8z" />
      <path d="M14.3 3.2v4.6h4.6" />
      <path d="M8.1 11.4h7" />
      <path d="M8.1 15h7" />
      <rect x="8.1" y="17.5" width="4.6" height="1.8" rx=".9" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 运维中心：工具箱（箱体 + 提手 + 盖缝 + 强调色卡扣） -->
    <g v-else-if="name === 'opsCenter'">
      <rect x="3.4" y="7.4" width="17.2" height="12.4" rx="2.2" />
      <path d="M8.8 7.4V6a1.6 1.6 0 0 1 1.6-1.6h3.2A1.6 1.6 0 0 1 15.2 6v1.4" />
      <path d="M3.4 12.2h17.2" />
      <rect x="10.4" y="11.4" width="3.2" height="1.6" rx=".8" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 运行摘要：秒表（计时盘 + 指针 + 强调色表冠） -->
    <g v-else-if="name === 'runtimeSummary'">
      <circle cx="12" cy="13.2" r="7.2" />
      <path d="M12 4.2v1.8" />
      <path d="M12 13.2V8.8" />
      <path d="M12 13.2 15.4 15.2" />
      <rect x="10.2" y="2.2" width="3.6" height="1.8" rx=".9" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 镜像任务：画框 + 山形，右上角强调色太阳 -->
    <g v-else-if="name === 'imageTasks'">
      <rect x="3.4" y="4.4" width="17.2" height="15.2" rx="2.6" />
      <path d="M5.4 18.2l3.8-4.6 2.8 3.1 2.4-2.5 3.8 4" />
      <circle cx="15.6" cy="8.6" r="1.6" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 初始化向导：魔杖 + 强调色星芒 + 小十字 -->
    <g v-else-if="name === 'setupWizard'">
      <path d="M4.8 19.2 14.2 9.8" />
      <path d="M19.6 14.2v2.6" />
      <path d="M18.3 15.5h2.6" />
      <path d="M17.2 3.6Q18.1 6.5 20.8 7.2 18.1 7.9 17.2 10.8 16.3 7.9 13.6 7.2 16.3 6.5 17.2 3.6z" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 系统配置：两组调节滑块，上行滑块用强调色 -->
    <g v-else-if="name === 'systemSettings'">
      <path d="M3.6 7h5.3" />
      <path d="M10.9 7h9.5" />
      <rect x="8.9" y="4.6" width="2" height="4.8" rx="1" fill="var(--accent)" stroke="none" />
      <path d="M3.6 17h8.6" />
      <path d="M14.2 17h6.2" />
      <rect x="12.2" y="14.6" width="2" height="4.8" rx="1" fill="currentColor" stroke="none" />
    </g>

    <!-- 控制面对接：控制面节点（强调色）+ 两枚本地节点 -->
    <g v-else-if="name === 'controlPlane'">
      <rect x="9.2" y="4.6" width="5.6" height="3.6" rx="1.4" fill="var(--accent)" stroke="none" />
      <path d="M12 8.2v3" />
      <path d="M6.4 11.2h11.2" />
      <path d="M6.4 11.2v3.2" />
      <path d="M17.6 11.2v3.2" />
      <rect x="3.6" y="14.4" width="5.6" height="4.4" rx="1.4" />
      <rect x="14.8" y="14.4" width="5.6" height="4.4" rx="1.4" />
    </g>

    <!-- 插件管理：拼图块 + 强调色芯点 -->
    <g v-else-if="name === 'plugins'">
      <path d="M7.6 7h.6a2.4 2.4 0 0 1 4.8 0h1.6a2 2 0 0 1 2 2v1.6a2.4 2.4 0 0 1 0 4.8v1.6a2 2 0 0 1-2 2h-7a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2z" />
      <circle cx="11" cy="12.6" r="1.6" fill="var(--accent)" stroke="none" />
    </g>

    <!-- 兜底：注册表里出现未登记 glyph 时不消失，画一个通用应用方块 -->
    <g v-else>
      <rect x="4" y="4" width="16" height="16" rx="3.2" />
      <circle cx="12" cy="12" r="1.6" fill="var(--accent)" stroke="none" />
    </g>
  </svg>
</template>

<style scoped>
/* 只做布局：颜色一律由外层（AppIcon.vue 的 --dicon-icon）通过 currentColor 控制，
 * 这样桌面、开始菜单、Dock、窗口标题栏四处自动一致，并随明暗主题反转。 */
.app-glyph {
  display: block;
  flex: none;
}
</style>
