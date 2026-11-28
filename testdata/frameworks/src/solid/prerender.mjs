// Renders the app with the SSR build and writes the markup into the client
// build's index.html, then removes the SSR build.
import { readFileSync, rmSync, writeFileSync } from 'node:fs';
import { render } from './.ssr/entry-server.js';

const out = '../../built/solid/index.html';
const { html, script } = render();
const page = readFileSync(out, 'utf8')
  .replace('<!--hydration-script-->', script)
  .replace('<!--app-html-->', html);
writeFileSync(out, page);
rmSync('.ssr', { recursive: true, force: true });
