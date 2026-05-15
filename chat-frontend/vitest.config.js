import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

export default defineConfig({
  plugins: [react()],
  // Keep the alias in lock-step with vite.config.js so test imports
  // resolve identically to runtime imports.
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./test/setup.js'],
    // Pin the JUnit reporter's output to an ABSOLUTE path anchored to
    // this config file. Without the pin, vitest resolves outputFile
    // against process.cwd() — which means the report lands at
    // /workspace/junit.xml when CI runs vitest from the repo root
    // (with `-c chat-frontend/vitest.config.js`) instead of at
    // chat-frontend/junit.xml where GitLab's `artifacts.reports.junit`
    // glob expects it. Anchoring via __dirname makes it CWD-independent.
    //
    // Only takes effect when the JUnit reporter is active (see
    // `npm run test:ci`); plain `vitest run` doesn't write a file.
    outputFile: {
      junit: path.resolve(__dirname, 'junit.xml'),
    },
  },
})
