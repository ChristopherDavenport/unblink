import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import recordPackages from '../_shared/record-packages.mjs';

// Absolute base: history-mode deep links (/tides/harbor) must find /assets/*.
export default defineConfig({
  base: '/',
  plugins: [vue(), recordPackages(import.meta.dirname)],
  build: { outDir: '../../built/vite-vue', emptyOutDir: true, modulePreload: { polyfill: false } },
});
