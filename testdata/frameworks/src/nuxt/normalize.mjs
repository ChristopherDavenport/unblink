// Nuxt stamps the prerender time into the payload and every generated page, so
// two builds of the same sources differ. Replace that one value with a fixed
// instant so a rebuild is byte-identical and the diff shows only real changes.
import { readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const out = '../../built/nuxt';
const fixed = '1795824000000'; // 2026-11-28T00:00:00Z

const payload = JSON.parse(readFileSync(join(out, '_payload.json'), 'utf8'));
const stamp = String(payload[payload[0].prerenderedAt]);
if (!/^\d{13}$/.test(stamp)) throw new Error(`unexpected prerenderedAt value ${stamp}`);

// Each generated file carries its own stamp, taken moments apart: replace any
// millisecond timestamp within ten minutes of the payload's.
const near = (n) => Math.abs(Number(n) - Number(stamp)) < 10 * 60 * 1000;
for (const name of readdirSync(out)) {
  if (!/\.(html|json)$/.test(name)) continue;
  const file = join(out, name);
  const text = readFileSync(file, 'utf8');
  const next = text.replace(/\b\d{13}\b/g, (n) => (near(n) ? fixed : n));
  if (next !== text) writeFileSync(file, next);
}
