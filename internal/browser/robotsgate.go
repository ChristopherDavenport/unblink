package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/christopherdavenport/unblink/internal/fetch"
	"github.com/christopherdavenport/unblink/internal/robots"
)

// Opt-in robots.txt enforcement (--respect-robots, ADR 0020). Off by default:
// unblink then only reports a site's rules. When on, every agent-directed
// navigation is checked against the target origin's robots.txt before the
// request is sent. The gate sits at the few places a navigation leaves the
// browser — fetchPage, fetchStateless (cache hits), Submit, and the map tool's
// sitemap fetches — plus a per-hop redirect check carried on the request
// context. A page's own subresources, page-JS subrequests, and the
// robots.txt/llms.txt/sitemap-discovery probes never pass through it.

// robotsUnreachableTTL is how long an unreachable robots.txt (5xx or a network
// failure) stays cached while enforcement is on. RFC 9309 section 2.3.1.4 makes
// that outcome a complete disallow, so it must expire far sooner than the
// 10-minute site-cache TTL or one transient 503 would block a host for ten
// minutes. Thirty seconds still absorbs a burst of navigations to a failing
// host (each probe already retries with backoff) without re-probing per call.
const robotsUnreachableTTL = 30 * time.Second

// ErrRobotsDisallowed is the sentinel every robots.txt refusal matches with
// errors.Is. The concrete error is a *RobotsError carrying the detail.
var ErrRobotsDisallowed = errors.New("disallowed by robots.txt")

// RobotsError is the refusal of one navigation under --respect-robots: either
// a Disallow rule matched the URL, or the origin's robots.txt was unreachable
// and so must be read as disallowing everything.
type RobotsError struct {
	URL         string // the URL that was refused (a redirect target, when a hop was refused)
	Rule        string // the matching Disallow pattern; empty when Unreachable
	Unreachable bool   // robots.txt answered 5xx or could not be fetched
	Status      int    // HTTP status of /robots.txt (0 = fetch error)
}

func (e *RobotsError) Error() string {
	switch {
	case !e.Unreachable:
		return fmt.Sprintf("%s is disallowed by the site's robots.txt (rule \"Disallow: %s\")", e.URL, e.Rule)
	case e.Status != 0:
		return fmt.Sprintf("%s is disallowed because the site's robots.txt is unreachable (HTTP %d), which must be treated as disallowing everything (RFC 9309)", e.URL, e.Status)
	default:
		return fmt.Sprintf("%s is disallowed because the site's robots.txt is unreachable (the fetch failed), which must be treated as disallowing everything (RFC 9309)", e.URL)
	}
}

// Is makes errors.Is(err, ErrRobotsDisallowed) hold for any wrapped RobotsError.
func (e *RobotsError) Is(target error) bool { return target == ErrRobotsDisallowed }

// agentMessage is the tool-error text: what was refused, who decided, and that
// repeating the call is pointless.
func (e *RobotsError) agentMessage() string {
	msg := e.Error() + "; the operator enabled --respect-robots, so unblink will not fetch it"
	if e.Unreachable {
		return msg + fmt.Sprintf(". Retrying immediately will not help: robots.txt is re-checked at most every %s", robotsUnreachableTTL)
	}
	return msg + " and retrying will not help"
}

// robotsVerdict decides path against info's "*" group. With enforcement off it
// is exactly the informational allowed_for_us (an unusable or unreachable
// robots.txt allows everything). With enforcement on it is the decision the gate
// makes: an unreachable robots.txt disallows everything, and matching normalizes
// percent-encoding so an equivalent spelling of a path cannot slip past a rule.
func (b *Browser) robotsVerdict(info *siteInfo, path string) (rule *robots.Rule, allowed bool) {
	g := info.policy.StarGroup()
	if !b.respectRobots {
		return g.Match(path)
	}
	if isRobotsTxt(path) {
		return nil, true // RFC 9309 section 2.2.2: /robots.txt is implicitly allowed
	}
	if info.robotsUnreachable {
		return nil, false
	}
	return g.MatchNormalized(path)
}

func isRobotsTxt(path string) bool {
	return path == "/robots.txt"
}

// checkRobots returns a *RobotsError when enforcement is on and u is disallowed
// for the "*" group of its own origin's robots.txt (the origin of the URL being
// fetched, not of the page that linked to it). It returns nil when enforcement
// is off and for non-http(s) URLs, which have no robots.txt. The robots.txt
// fetch goes through the anonymous default client, so it carries no session
// cookies or injected credentials and is subject to the SSRF guard.
func (b *Browser) checkRobots(ctx context.Context, u *url.URL) error {
	if !b.respectRobots || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	path := requestPath(u)
	if isRobotsTxt(path) {
		return nil
	}
	info := b.gather(ctx, &url.URL{Scheme: u.Scheme, Host: u.Host}, false)
	rule, allowed := b.robotsVerdict(info, path)
	if allowed {
		return nil
	}
	if info.robotsUnreachable {
		// A cancelled call or an SSRF-blocked origin is not a robots verdict;
		// report the real cause.
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(info.robotsErr, errBlockedAddr) {
			return info.robotsErr
		}
		return &RobotsError{URL: u.String(), Unreachable: true, Status: info.robotsStatus}
	}
	re := &RobotsError{URL: u.String(), Status: info.robotsStatus}
	if rule != nil {
		re.Rule = rule.Pattern
	}
	return re
}

// gateURL is checkRobots for a raw URL. An unparseable URL is left for the
// fetch to reject with its own error.
func (b *Browser) gateURL(ctx context.Context, rawURL string) error {
	if !b.respectRobots {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return b.checkRobots(ctx, u)
}

// navContext returns the context a navigation's request is sent under: with
// enforcement on, every redirect hop is checked against its own origin's
// robots.txt before it is requested, so a redirect cannot carry a navigation to
// a disallowed URL. The probe a hop triggers runs under the plain ctx, so it is
// not itself gated.
func (b *Browser) navContext(ctx context.Context) context.Context {
	if !b.respectRobots {
		return ctx
	}
	return fetch.WithRedirectCheck(ctx, func(u *url.URL) error { return b.checkRobots(ctx, u) })
}
