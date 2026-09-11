import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

// Vite dev server proxies /api/* to the Go backend on :8080.
// WebSocket upgrade is forwarded automatically (ws: true).
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 3000,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
  worker: {
    // JASSUB ships its libass worker as an ES module. Vite's default IIFE
    // worker output cannot split that worker's own imports.
    format: 'es',
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    rollupOptions: {
      output: {
        // 只拆哈希长期稳定的核心库：业务代码发版后用户只需重新下载变小的
        // 业务 chunk，核心 vendor 走浏览器长缓存。其余依赖保持 Vite 默认
        // 分块，避免把 hls.js 等懒加载库拖进首屏。
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined
          if (/[\\/]node_modules[\\/](react|react-dom|react-router|react-router-dom|scheduler)[\\/]/.test(id)) {
            return 'vendor-react'
          }
          if (/[\\/]node_modules[\\/](axios|zustand)[\\/]/.test(id)) {
            return 'vendor-data'
          }
          if (/[\\/]node_modules[\\/]framer-motion[\\/]/.test(id)) {
            return 'vendor-motion'
          }
          return undefined
        },
      },
    },
  },
})
