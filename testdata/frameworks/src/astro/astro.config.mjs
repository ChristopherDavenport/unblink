import { defineConfig } from 'astro/config';
import preact from '@astrojs/preact';
import recordPackages from '../_shared/record-packages.mjs';

// Static build: server-rendered HTML with two Preact islands hydrated by the
// <astro-island> runtime (one on load, one when it becomes visible).
export default defineConfig({
  integrations: [preact()],
  outDir: '../../built/astro',
  build: { format: 'directory' },
  devToolbar: { enabled: false },
  vite: { plugins: [recordPackages(import.meta.dirname)] },
});
