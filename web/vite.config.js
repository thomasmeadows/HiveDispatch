import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The build lands in internal/web/dist, which is committed and embedded
// into the Go binary. `npm run dev` proxies the API to a running
// `hivedispatch website` on the default address.
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:7878', changeOrigin: true },
    },
  },
})
