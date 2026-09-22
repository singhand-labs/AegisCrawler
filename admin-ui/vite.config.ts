import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  base: '/admin/',
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/admin/rules': 'http://localhost:8080',
      '/admin/tasks': 'http://localhost:8080',
      '/admin/audit_logs': 'http://localhost:8080',
      '/tasks': 'http://localhost:8080',
      '/health': 'http://localhost:8080',
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
    rollupOptions: {
      output: {
        manualChunks: {
          g6: ['@antv/g6'],
        },
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test-setup.ts',
    testTimeout: 10_000,
  },
});
