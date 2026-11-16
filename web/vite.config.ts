import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// 构建产物必须落在 web/dist，由 Go 后端用 //go:embed 打进单二进制。
// base 用相对路径，这样 embed 后无论挂在 / 还是 /govdoc/ 前缀下都能加载。
export default defineConfig({
  base: './',
  plugins: [react(), tailwindcss()],
  build: {
    // 相对 web/（vite root）解析 → web/dist，即 ../web/dist
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    assetsDir: 'assets',
    target: 'es2020',
    // 单文件产物是这个项目的刻意选择：界面被 //go:embed 打进一个二进制，从
    // 本机 localhost 加载。再拆成十几个 chunk 只会多出十几个请求，而 gzip 后
    // 约 232 kB 的本地读取是瞬时的。因此这里放宽告警阈值而不是做代码分割。
    chunkSizeWarningLimit: 900,
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
