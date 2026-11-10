import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'
import { readFileSync } from 'node:fs'

const https = process.env.VITE_HTTPS_CERT_FILE && process.env.VITE_HTTPS_KEY_FILE
  ? {
      cert: readFileSync(process.env.VITE_HTTPS_CERT_FILE),
      key: readFileSync(process.env.VITE_HTTPS_KEY_FILE),
    }
  : undefined

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    https,
    watch: {
      ignored: ['**/playwright-report/**', '**/test-results/**'],
    },
    proxy: {
      '/api': {
        target: process.env.VITE_BACKEND_URL ?? 'http://127.0.0.1:8080',
        changeOrigin: false,
        secure: process.env.VITE_BACKEND_INSECURE !== 'true',
        ws: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    css: true,
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
