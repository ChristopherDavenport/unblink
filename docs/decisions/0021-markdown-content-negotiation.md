# ADR 0021: Markdown content negotiation (`prefer_markdown`, `--prefer-markdown`)

Date: 2026-11-27
Status: accepted; the operator default is provisional (see "The default")

## Context

unblink's job is to turn a page into token-budgeted Markdown. A growing number
of origins will do that themselves: sent `Accept: text/markdown`, they answer
with a Markdown representation of the page (Cloudflare's "Markdown for Agents"
does it at the edge for any zone that enables it; docs platforms and static
sites do it at the origin). When one is on offer it is usually better than what
unblink can derive: the site chose what the content is, there is no JavaScript to
run, and the response is a fraction of the bytes.

unblink could not use it. Its `Accept` was the browser one, and a `text/markdown`
body that did arrive (a `.md` URL, say) was classified as plain text and wrapped
in a code fence, which hides its headings and links from the reader.

Asking is not free of risk. The Markdown variant is a different response from the
one a browser gets, produced by a path that knows the client is an agent. A
site can serve agents something its human visitors never see: abridged, stale,
or deliberately different (cloaking). unblink's default is to act as a browser
for a user and report what that user would see.

## Decision

Three parts: receiving Markdown, asking for it per call, and what a call that
does not say gets.

**Receiving is always on.** `classify` maps `text/markdown` and
`text/x-markdown` to a new `page.KindMarkdown`. `content.Render` passes such a
body through as the Markdown output. It is not parsed, reduced or fenced. The
only edits are normalisation (a leading BOM dropped, CRLF/CR to LF, a final
newline) so pagination and the cursor fingerprint do not depend on the origin's
platform. The title comes from the first ATX `# ` heading outside a code fence,
else a leading YAML front-matter `title:`, else the URL filename as for other
non-HTML kinds. This half changes behaviour for no origin that was not already
sending Markdown, and it applies however the Markdown arrived.

A Markdown page is a non-HTML page like JSON or a feed: there is no DOM, so no
JavaScript render, and the tools that need one behave as they already do for
those kinds (`data`/`extract`/a `raw_html` selector fail with "requires an HTML
page (got markdown)"; `links`/`forms`/`controls`/`find` return nothing;
`format=text` and selector-less `raw_html` return the source). `read` reports
`kind: "markdown"` and `mode: "markdown"`.

Safe output still applies: the body is origin-controlled text, so it is inside
the untrusted-content fence and its image beacons are defanged like any emitted
Markdown.

**Asking is the agent's choice, per call.** `read` takes a tri-state
`prefer_markdown` argument: `true` asks the origin for Markdown on this
navigation, `false` does not, and absent follows the operator default below. It
needs no operator flag, so an agent can always use the feature and can always
re-read the same URL as HTML when it wants the DOM-derived view. A negotiating
request sends

    Accept: text/markdown, text/html;q=0.9, application/xhtml+xml;q=0.9, */*;q=0.8

so an origin with no Markdown representation answers with the HTML it would have
sent anyway, and the read proceeds as before (parse, render, reduce).

**The default.** What an absent `prefer_markdown` means is one constant,
`browser.DefaultPreferMarkdown`, which the `--prefer-markdown` flag
(`browser.WithPreferMarkdown`) overrides in either direction
(`--prefer-markdown`, `--prefer-markdown=false`). The constant is currently
`false`. That value is **provisional**: the choice is open, and it is built so
that changing it is a one-line edit (the CLI flag default, `browser.New` and the
tests all read the constant; the tests assert "default", "explicit on" and
"explicit off" separately).

For defaulting **on**:

- unblink is an agent's browser, and the origin published this variant for
  agents. Taking it is using the site as its owner intended.
- It is far fewer tokens than a reduced HTML page, and it is the site's own
  judgement of what the content is, not unblink's heuristic.
- No JavaScript runs: no render cost, no hydration failures, no settle wait.
- An origin without the variant is unaffected, so the cost of asking is small.

For defaulting **off**:

- The variant can differ from what a browser user sees (abridged, stale, or
  cloaked). unblink's stated job is to report the page a user would get.
- A Markdown page has no DOM: `links`, `forms`, `click`, `data` and `extract`
  see nothing on it, so an agent that reads and then acts must fetch again.
- It changes the request fingerprint. The `Accept` header announces an agent,
  which some origins block or treat differently, and under `--tls-mimic` it
  breaks the Chrome persona (see Consequences).
- A negotiating `read` and the DOM tools (`browse`, `links`, …) no longer share
  one cached fetch of a URL: the two representations are separate cache entries,
  so `browse` then `read` costs two requests where it cost one.
- It changes existing behaviour for every deployment on upgrade.

**What negotiates.** Only a navigation whose result the caller consumes as
Markdown text (`Browser.negotiatesMarkdown`):

- `read`, navigating to a URL, with `format=markdown` (the default).
- With an explicit `prefer_markdown`, in or out of a session. The operator
  default reaches stateless reads only: a session's current page is shared with
  `links`, `click`, `forms` and `interact`, which need the DOM a Markdown body
  does not have, so a session navigation negotiates only when the call says so.
- Every redirect hop of that request (same context, same header).
- Credentialed one-shot reads follow the same rule as other stateless reads.

**What does not negotiate, on purpose.**

- `read` with `format=raw_html` or `format=text` (they exist to reach the HTML
  source), or with `wait_for`/`wait_text` (they ask for a rendered DOM).
- `read` of a session's current page (`use_current`): it does not navigate, so
  it returns the page in the representation it was fetched in. An explicit
  `prefer_markdown` that names the other representation is refused with an error
  giving the URL to re-read; it is never answered with the wrong one.
- Every other tool: `browse`, `links`, `forms`, `find`, `controls`, `data`,
  `extract`, `requests`, `console`, `click`, `submit_form`, `map`, `site`. Their
  results are computed from the DOM.
- A page's subresources and page-JS subrequests, the robots.txt/llms.txt
  probes, sitemap fetches, and `search` provider calls.

**Mechanism.** `internal/fetch` gained `WithAccept(ctx, accept)`, a per-request
override carried on the context like `WithRedirectCheck`. `setHeaders` applies it
before the plain and `--tls-mimic` personas, both of which only fill `Accept`
when it is unset. The browser attaches it in `docContext`, used for the document
request (and its conditional revalidation) only; a render's subrequests run
under the caller's plain context and never inherit it. `fetch` stays ignorant of
Markdown.

**Cache.** The stateless URL cache keys a negotiated navigation separately
(`cacheKeyFor`: a `markdown` prefix on top of the existing `render` one), and the
Markdown memo rides the same key. A cached HTML page is never returned to a
negotiating read or the reverse, whether or not the origin sent `Vary: Accept`:
`prefer_markdown=false` after a Markdown read fetches the HTML, and `true` after
an HTML read fetches the Markdown. A session navigation always refetches.
Revalidation of an expired entry sends the validators under the same `Accept`
that produced it, so an origin that reuses one ETag for both variants cannot
have a 304 confirm the wrong one.

**Not consulted.** The `x-markdown-tokens` response header (an origin's own
token count) is ignored; unblink's estimate is the one pagination uses. No
`Content-Signal`-style usage header is interpreted.

## Consequences

- A negotiated read of a supporting origin costs one small request and no
  render.
- While the default is off, a call without `prefer_markdown` sends an `Accept`
  byte-identical to before.
- A negotiated read returns what the origin chose to give an agent, which may
  differ from the page a browser shows. That is why the result says
  `kind: "markdown"` and why the tool schema describes the trade; `prefer_markdown=false` (or any DOM tool) gets
  the browser-facing page for comparison.
- The origin's Markdown is not reduced: `mode=article`/`full` do not apply, and
  navigation or boilerplate the origin left in stays in. Pagination bounds it.
- Under `--tls-mimic` a negotiating request keeps the uTLS ClientHello, the h2
  SETTINGS, the User-Agent, `sec-ch-ua` and `Sec-Fetch-*`, but its `Accept` is
  the Markdown one, not Chrome's. The header set is then not a coherent Chrome
  persona, and an origin that fingerprints headers can tell. Negotiation is not
  suppressed under `--tls-mimic`: an explicit `prefer_markdown=true` is the
  caller's decision. An operator who needs the fingerprint should keep the
  default off and have agents not pass `true`; this is one reason the default is
  not simply on.
- After a session read with `prefer_markdown=true`, that session's current page
  has no links or forms until it navigates again.
- `fetch.WithAccept` is a small generic addition; other representations can be
  negotiated the same way.
