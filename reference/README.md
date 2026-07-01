# reference/

Curated, high-signal reference material that informs unblink's semantic
reduction and (later) its DOM bridge.

**We do not vendor full Chromium or Firefox source.** That is gigabytes of
mostly-C++ that is irrelevant to a no-render tool. Instead we internalize
*heuristics and contracts* — the knowledge worth keeping — not source trees.

## Layout

| Path                          | What it is                                                                 |
| ----------------------------- | ------------------------------------------------------------------------- |
| `PINNED.md`                   | Source URLs + commit hashes + retrieval dates, so this folder is reproducible. |
| `readability/`                | Mozilla `Readability.js` (MIT) — the scoring algorithm our reducer echoes, plus a few input/expected test cases for parity. |
| `distiller/NOTES.md`          | Chromium DOM Distiller content-classification heuristics, summarized (links to components by commit). |
| `dom-spec/node-interfaces.md` | The **minimal** `Node`/`Element`/`Document`/`Window` IDL surface we will implement in the JS phase — the contract that prevents "implement all of DOM" scope creep. |
| `anti-patterns.md`            | Living catalog of visual-junk selectors (cookie banners, nav, share bars, ad slots) that reduction should drop. Our accumulated domain knowledge. |
| `corpus/`                     | Curated real-world page snapshots used as the reduction-quality eval set.  |

## Rule

Every artifact gets a `PINNED.md` entry with its source, version/commit, and the
date it was retrieved. Prefer a summarized `NOTES.md` over vendored source
wherever a summary suffices. Keep this folder a curated library, not a junk
drawer.
