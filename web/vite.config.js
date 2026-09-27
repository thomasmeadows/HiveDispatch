import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The build lands in internal/web/dist, which is committed and embedded
// into the Go binary. In development, `hivedispatch website -dev` runs
// Vite's dev server itself on a private port and proxies to it, so the
// browser only ever talks to the Go server.
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
})
