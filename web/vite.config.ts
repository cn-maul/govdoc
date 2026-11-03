import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物必须落在 web/dist，由 Go 后端用 //go:embed 打进单二进制。
// base 用相对路径，这样 embed 后无论挂在 / 还是 /govdoc/ 前缀下都能加载。
export default defineConfig({
  base: './',
  plugins: [vue()],
  build: {
    // 相对 web/（vite root）解析 → web/dist，即 ../web/dist
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    assetsDir: 'assets',
    target: 'es2020',
  },
  server: {
    // 仅在本地开发时使用：把 /api 转发到 Go 服务，避免跨域。
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
})
