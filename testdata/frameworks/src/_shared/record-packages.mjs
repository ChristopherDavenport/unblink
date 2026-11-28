// A Vite/Rollup plugin that records which npm packages contributed modules to a
// build, so licenses.mjs can ship exactly those packages' licence texts beside
// the built output. It over-reports rather than under-reports: every build of a
// fixture (client and server) adds to the same list.
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

export default function recordPackages(fixtureDir) {
  const file = join(fixtureDir, '.bundled-packages.json');
  const seen = new Set(existsSync(file) ? JSON.parse(readFileSync(file, 'utf8')) : []);
  return {
    name: 'unblink-record-packages',
    enforce: 'post',
    generateBundle() {
      for (const id of this.getModuleIds()) {
        const clean = id.replace(/\\/g, '/').replace(/^\0+/, '').split('?')[0];
        const at = clean.lastIndexOf('/node_modules/');
        if (at < 0) continue;
        const parts = clean.slice(at + '/node_modules/'.length).split('/');
        const name = parts[0].startsWith('@') ? parts.slice(0, 2).join('/') : parts[0];
        if (name && !name.startsWith('.')) seen.add(name);
      }
    },
    closeBundle() {
      writeFileSync(file, JSON.stringify([...seen].sort(), null, 2) + '\n');
    },
  };
}
