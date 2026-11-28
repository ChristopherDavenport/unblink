import recordPackages from '../_shared/record-packages.mjs';

// Static generation: prerendered HTML, the Nuxt payload, and client hydration.
export default defineNuxtConfig({
  compatibilityDate: '2026-11-01',
  telemetry: false,
  // A fixed build id: the default is a random UUID, which would change the
  // payload URL and three HTML files on every rebuild.
  buildId: 'unblink-fixture',
  devtools: { enabled: false },
  ssr: true,
  sourcemap: false,
  experimental: { appManifest: false },
  vite: { plugins: [recordPackages(import.meta.dirname)] },
  nitro: { preset: 'static', prerender: { routes: ['/'], crawlLinks: false } },
});
