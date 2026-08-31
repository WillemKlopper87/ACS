import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    // Playwright specs run via `npm run e2e`, not vitest.
    exclude: ['node_modules/**', 'e2e/**'],
  },
})
