import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'

// Multi-page Vite app: three independent HTML entries that the Go backend
// embeds from internal/app/web/dist and serves at / (admin), /u (user portal)
// and as the inline access-gate page (gate.html, pre-read at startup).
export default defineConfig({
  plugins: [vue()],
  base: '/',
  build: {
    outDir: resolve(__dirname, '../internal/app/web/dist'),
    emptyOutDir: true,
    rollupOptions: {
      input: {
        index: resolve(__dirname, 'index.html'),
        user: resolve(__dirname, 'user.html'),
        gate: resolve(__dirname, 'gate.html'),
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:51873',
      '/proxy': 'http://localhost:51873',
    },
  },
})
