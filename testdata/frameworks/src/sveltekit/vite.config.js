import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';
import recordPackages from '../_shared/record-packages.mjs';

// Prerendered static site (adapter-static): server-rendered markup that the
// Svelte 5 runtime hydrates in place using its comment anchors.
//
// Built twice. SvelteKit's default polls /_app/version.json on an hourly
// self-rearming timer; FIXTURE_POLL=1 keeps that default (built/sveltekit-poll),
// the other build turns the poll off (built/sveltekit) so the hydration cases
// do not depend on how the engine treats a long timer.
const poll = process.env.FIXTURE_POLL === '1';
const out = poll ? '../../built/sveltekit-poll' : '../../built/sveltekit';

export default defineConfig({
  plugins: [
    sveltekit({
      adapter: adapter({ pages: out, assets: out }),
      version: { name: 'unblink-fixture', ...(poll ? {} : { pollInterval: 0 }) },
    }),
    recordPackages(import.meta.dirname),
  ],
});
