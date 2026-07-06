# ADR 0017: rewrite `[\s-…]` regex classes goja rejects, before compile

Date: 2026-07-06
Status: accepted

## Context

unblink runs the page's untrusted JavaScript through goja. goja translates each JS
regex to a Go (`regexp`, RE2) pattern via `parser.TransformRegExp`, falling back to
its ECMAScript-accurate regexp2 engine only when RE2 can't represent the pattern —
but the fallback in `compileRegexp` (`builtin_regexp.go`) is taken **only** on
`ErrInvalidRepeatSize`. Any other RE2 error is returned as a `SyntaxError`.

That gap surfaces on a common, valid-ECMAScript idiom: a `-` immediately after a
`\s`/`\S`/`\d`/`\D`/`\w`/`\W` class shorthand — e.g. the ubiquitous camelCase/
slugify helper `/[\s-_]+/`. `TransformRegExp` expands `\s` to its literal whitespace
set (ending in `U+FEFF`) and leaves the following `-` as a range operator, producing
a descending, invalid range (`﻿-_`). Go's `regexp` rejects it with *"invalid
character class range"* — not a repeat-size error — so goja throws at **compile
time**, before the regex ever runs.

In ECMAScript that `-` is already a literal: a class shorthand cannot be a range
endpoint. V8/SpiderMonkey accept the regex. But because goja throws a `SyntaxError`,
the *entire enclosing script or module* fails to compile. A single such regex in a
bundle silently kills whole features. The field case was an Astro `client:only`
React island (poe.ninja) whose dynamically imported chunk carried
`/^([A-Z])|[\s-_]+(\w)/g`: the `import()` rejected, the island never hydrated, and
the page fell back to its static SSR shell. This idiom appears throughout real
bundles (lodash `camelCase`, kebab/slug utilities, class-name helpers), so the blast
radius is broad.

Fixing it "properly" means changing goja (fall back to regexp2 on more RE2 errors,
or escape the hyphen in `TransformRegExp`). goja is a deliberate pinned dependency
(ADR 0002); a bump/fork is its own reviewed change, and no upstream fix exists. We
wanted a self-contained fix that keeps goja pristine.

## Decision

Escape the offending hyphen in the JS **source**, before goja compiles it:
`/[\s-_]/` → `/[\s\-_]/`. Implemented in `fixClassRangeHyphen`
(`internal/js/regexfix.go`) and applied at the two compile chokepoints on the
cache-miss path — `compileCached` (modules + dynamic-import chunks) and
`classicProgram` (classic scripts).

Two properties make this safe without a JS tokenizer:

1. **`\-` ≡ `-` in every JS lexical context** — regex character class, regex body,
   string, template literal, comment. Inserting the backslash never changes a
   program's meaning *wherever it lands*, so we don't need to know we're inside a
   regex literal (the reason a general "rewrite regex literals" pass would need a
   full lexer to disambiguate `/` division from `/regex/`).

2. **A shorthand can never start a class range.** The only place `-` is special is a
   regex class range, and a range needs a single-character start; `\s` et al. can't
   be one. So a `-` right after a shorthand is always a literal, and escaping it is a
   no-op that merely sidesteps goja's mistranslation.

The one genuine-range case is `[\\s-z]` — a *literal backslash* followed by the
`s-z` range, where the `s` is not a shorthand. `shorthandEscapeBefore` excludes it by
requiring the run of backslashes before the shorthand letter to be **odd** (a real
escape); an even run means the backslash is itself escaped and the letter is literal.

The rewrite runs only when a shorthand-then-hyphen actually appears (a cheap scan
guards the allocation), so it is a no-op for the overwhelming majority of source.

## Consequences

- A regex goja would otherwise reject at compile time now compiles and behaves per
  ECMAScript, so the script/module that carries it runs instead of aborting. Astro/
  React/Vue bundles that use the idiom hydrate.
- The fix is textual and value-preserving in all contexts, so it needs no lexer and
  cannot alter program semantics — including inside strings and comments, where the
  inserted `\-` collapses back to `-`.
- It is a **targeted workaround for one goja bug**, not a general regex-compat layer.
  Other goja/RE2 divergences (e.g. certain lookbehind or Unicode-property cases)
  remain out of scope; if they surface, prefer the same "escape/rewrite only when
  provably semantics-preserving" discipline, or an ADR 0002 goja bump.
- If goja later fixes `TransformRegExp` (or broadens the regexp2 fallback), this
  rewrite becomes a harmless no-op and can be retired.
