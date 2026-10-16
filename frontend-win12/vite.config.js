import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发代理与旧前端 frontend/vite.config.ts 等价；生产走同源 embed。
const backendUrl = process.env.VITE_DEV_PROXY_TARGET || 'http://localhost:18080'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5190,
    host: true,
    proxy: {
      // ws: true —— 运维中心「实时推送 · WebSocket」面板（/admin/ops/ws/qps）走 WebSocket，
      // 开发期必须让 vite 代理转发 upgrade 请求；生产走同源 embed，不经过该代理。
      '/api': { target: backendUrl, changeOrigin: true, ws: true },
      '/v1': { target: backendUrl, changeOrigin: true },
      '/setup': { target: backendUrl, changeOrigin: true },
    },
  },
  build: {
    // embed 切换：与旧 frontend 相同的产物路径，Go 侧 //go:embed all:dist 无需改动
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1600, // antd 全量+图标 vendor 属长缓存合理大块
    rollupOptions: {
      output: {
        // 框架与 UI 库拆为独立 chunk：业务代码更新时浏览器可继续命中 vendor 缓存
        manualChunks: {
          vue: ['vue', 'pinia'],
          antd: ['ant-design-vue', '@ant-design/icons-vue'],
        },
      },
    },
  },
})

