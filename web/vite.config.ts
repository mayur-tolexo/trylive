import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// Dev proxy forwards the API and badge routes to the Go backend so the SPA
// can be served by Vite while /v1 (including the pty websocket) hits :8080.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/v1': { target: 'http://localhost:8080', changeOrigin: true, ws: true },
      '/badge': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
  build: { outDir: 'dist', emptyOutDir: true },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
  },
})
