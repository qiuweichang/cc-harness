import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

/** Vite development configuration proxies API calls to the local Go control plane. */
export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 5172,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
    },
  },
})
