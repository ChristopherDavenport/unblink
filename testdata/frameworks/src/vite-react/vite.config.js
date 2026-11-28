import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import recordPackages from '../_shared/record-packages.mjs';

// Relative base so the build is served from any path prefix by the eval host.
export default defineConfig({
  base: './',
  plugins: [react(), recordPackages(import.meta.dirname)],
  build: { outDir: '../../built/vite-react', emptyOutDir: true, modulePreload: { polyfill: false } },
});
