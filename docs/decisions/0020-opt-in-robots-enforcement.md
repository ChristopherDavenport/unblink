# ADR 0020: Opt-in robots.txt enforcement (`--respect-robots`)

Date: 2026-11-27
Status: accepted

## Context

unblink has parsed robots.txt since Phase 6, but only to report it: the `site`
tool and the `browse` hint show a host's rules and whether the requested path is
`allowed_for_us`, and nothing ever blocked a fetch. That is the right default.
unblink normally acts as a browser for a person (an agent fetching the page its
user asked about), it sends a standard browser User-Agent, and the Robots
Exclusion Protocol (RFC 9309) addresses automated crawlers, not user-directed
browsing.

Some operators do run it as a crawler: a scheduled agent that walks sites with
`map` and reads what it finds. They had no way to make it comply. The agent
could read `allowed_for_us` and decline, but a policy that depends on the model
choosing to follow it is not something an operator can promise a site owner.

## Decision

A server flag, `--respect-robots` (`browser.WithRespectRobots`), off by default.
With it off nothing changes. With it on, every **agent-directed navigation** is
checked against the target origin's robots.txt before the request is sent, and
refused if disallowed.

**What is gated.** The gate sits where a navigation leaves `internal/browser`,
not in each tool:

- `fetchPage`: the document fetch of every page tool (`read`, `browse`, `links`,
  `forms`, `find`, `controls`, `data`, `extract`, `requests`, `console`), with or
  without a session, with or without one-shot credentials; `click`; the `map`
  seed and every page its crawl fetches; and the page `site` fetches to resolve
  an origin.
- `fetchStateless`: the URL cache. A cache hit is checked (request URL and the
  URL the cached response finally came from) and so is a revalidation, so a
  cached copy of a now-disallowed page is refused too.
- `Submit`: the form action, as it will be requested (for a GET form the values
  are the query string).
- `harvestSitemaps`: each declared sitemap file `map` fetches (a robots.txt
  `Sitemap:` entry or a sitemap-index child).
- **Redirects**, per hop. `internal/fetch` gained `WithRedirectCheck(ctx, fn)`:
  a hook carried on the request context that the client's existing
  `checkRedirect` calls before following a hop. The browser attaches it to
  navigation requests only, so a redirect to a disallowed URL is stopped before
  that URL is requested; nothing is fetched, returned, or cached. `fetch` stays
  ignorant of robots.txt: it sees a function that can refuse a URL.

`interact` never navigates on its own (a handler's `location.href` comes back as
`pending_navigation` for the agent to follow with a page tool, which is gated),
and `session` back/forward replay stored pages without refetching, so neither
needs a gate.

**What is not gated, on purpose.**

- A page's own subresources and page-JS subrequests (scripts, module imports,
  `fetch`/XHR). RFC 9309 governs what a crawler requests; these are the fetched
  document rendering itself, as in any browser, and many sites disallow asset
  or API paths for crawlers while expecting browsers to load them.
- The origin-root discovery probes: `/robots.txt`, `/llms.txt` and
  `/llms-full.txt`. robots.txt has to be fetched to be obeyed, and RFC 9309
  makes it implicitly allowed (an agent may also `read` it); the llms files are
  single well-known metadata files a site publishes for exactly this kind of
  client. Sitemap files *are* checked, like any other URL `map` fetches: those
  declared in robots.txt, sitemap-index children, and `map`'s guess at the
  `/sitemap.xml` convention. A site that disallows `/sitemap.xml` has said not
  to fetch it.
- The `search` tool. It calls the configured provider; it does not fetch result
  pages. Reading a result is a navigation and is gated.
- **Crawl-delay.** It is not part of RFC 9309. `--rate-limit` is the pacing
  control; `map` keeps honoring Crawl-delay between its own crawl fetches as it
  did before, and it is still reported.

**Which rules apply.** The `*` group, exactly as `allowed_for_us` was already
computed: unblink sends a browser User-Agent and has no product token, so no
named group is consulted. Matching is on path plus query, longest pattern wins,
Allow wins a tie. The check is per origin of the URL being fetched (scheme, host
and port), never of the page that linked to it. Non-http(s) URLs are not gated.
It reuses `internal/robots` and the per-origin site cache; there is no second
parser or cache.

Enforcement compares in the normalized form RFC 9309 section 2.2.2 requires
(`Group.MatchNormalized`: unreserved characters percent-decoded, other escapes
upper-cased, non-ASCII percent-encoded, on both the path and the pattern).
Without it `/%70rivate` would pass a `Disallow: /private` rule while the server
treats the two as the same resource. With the flag off, `allowed_for_us` keeps
the as-written comparison it always used; with it on, `allowed_for_us` is the
enforcement verdict, so the two cannot disagree.

**Unavailable robots.txt** follows RFC 9309 section 2.3.1: a 4xx answer means no
restrictions; a 5xx answer or a failed fetch (network error, timeout, too many
redirects) means everything on that origin is disallowed. A 200 that is not a
robots.txt (an HTML soft-404) has no rules and allows. A robots.txt that
redirects is followed by the fetch client as before.

Complete disallow is a harsh verdict for a transient fault, so it is cached for
30 seconds (`robotsUnreachableTTL`) instead of the site cache's 10 minutes.
Thirty seconds is long enough that a burst of navigations to a failing host
costs one probe rather than one per call (each probe already retries 5xx with
backoff), and short enough that the host comes back within half a minute of its
robots.txt recovering. A probe that failed because the caller's context was
cancelled is not cached at all.

**The refusal** is a typed error, `*browser.RobotsError`, matching
`browser.ErrRobotsDisallowed` with `errors.Is`. It carries the refused URL, the
matching Disallow pattern, or that robots.txt was unreachable and with what
status. `Classify` maps it to the code `robots_disallowed`, not retryable, with a
message saying the URL is disallowed by the site's robots.txt, that the operator
enabled `--respect-robots`, and that retrying will not help. A refusal is a
policy decision: the fetch layer does not retry it and logs it at debug.

**Visibility.** `site`'s `robots` object and `browse`'s `robots` hint gain
`enforced` (omitted when false), so an agent can tell "`allowed_for_us: false`
will be refused" from "informational". `site` on a disallowed URL reports that
origin's rules without fetching the page. `map` reports `skipped_by_robots`: a
disallowed seed fails the call, any other disallowed URL is skipped and counted.
A skipped URL can still be listed when a fetched page links to it, since listing
is not fetching.

**Probe hygiene.** Enforcement fetches robots.txt for every new origin an agent
navigates to. That fetch uses the anonymous default client, never a session's:
no session cookies or injected credentials are sent, and the SSRF guard and
redirect rules apply as they do to the existing probe. An origin whose
robots.txt is blocked by the SSRF guard reports the SSRF error, not a robots
one. The gate asks for the robots half of the site-cache entry only, so it
costs one request per origin (not three), and works with `--no-site-hints`,
which only controls what `browse` prints.

## Consequences

- An operator can state that the deployment obeys robots.txt, and that holds
  regardless of what the agent decides.
- The default is unchanged and stays off: user-directed browsing is not what
  robots.txt addresses, and turning it on by default would break reads that
  work today (many sites disallow `*` broadly while serving browsers).
- A site can deny its own origin, and only that. A hostile robots.txt has no
  other lever; the parser already bounds its size.
- The first navigation to each origin costs one extra request, cached for 10
  minutes.
- A host whose robots.txt is failing is unreadable while it fails. That is the
  RFC's rule; the 30-second TTL bounds how long it outlasts the fault.
- Page-JS subrequests can still reach disallowed paths. An operator who needs
  no request at all to such paths should also run with `--disable-js`.
- Matching the `*` group means a site that singles out a named crawler is not
  honored in that group's terms; unblink has no product token to match.
- The redirect hook is a small generic addition to `internal/fetch`; other
  per-hop policies can use it.
