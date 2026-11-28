import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';
import recordPackages from '../_shared/record-packages.mjs';

// Solid with SSR + hydration and no meta-framework: the client build is
// hydratable, an SSR build renders the same component to a string, and
// prerender.mjs writes that markup into the built index.html.
export default defineConfig({
  base: './',
  plugins: [solid({ ssr: true }), recordPackages(import.meta.dirname)],
  build: { outDir: '../../built/solid', emptyOutDir: true, modulePreload: { polyfill: false } },
});
