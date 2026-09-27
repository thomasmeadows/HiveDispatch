import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The build lands in internal/web/dist, which is committed and embedded
// into the Go binary. There is no Node server: the Go binary serves the
// UI in development too (`hivedispatch website -assets internal/web/dist`
// while `npm run dev` rebuilds on every change).
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
})
