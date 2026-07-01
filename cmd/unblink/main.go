// Command unblink is a pure-Go "browser for AI": it fetches web pages, reduces
// them to their meaningful content, and exposes them to an AI model over MCP.
//
// By default it serves the MCP protocol over stdio, which is the native fit for
// a local agent that spawns unblink as a subprocess.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/mcpserver"
	"github.com/christopherdavenport/unblink/internal/search"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	logLevel := flag.String("log-level", "warn", "log level: debug|info|warn|error (logs go to stderr)")
	rateLimit := flag.Float64("rate-limit", browser.DefaultRateRPS, "per-host requests/sec (0 disables)")
	rateBurst := flag.Int("rate-burst", browser.DefaultRateBurst, "per-host request burst")
	retries := flag.Int("retries", browser.DefaultRetries, "retries for transient fetch failures")
	tlsMimic := flag.Bool("tls-mimic", false, "present a Chrome fingerprint (utls ClientHello + tuned h2 SETTINGS/headers) to evade anti-bot blocks")
	allowPrivate := flag.Bool("allow-private", false, "permit page fetches to private/loopback/metadata IPs (off by default; needed for localhost/internal targets)")
	noSiteHints := flag.Bool("no-site-hints", false, "omit robots.txt/llms.txt presence hints from browse output")
	noSafeOutput := flag.Bool("no-safe-output", false, "disable untrusted-content safety on returned content: keep raw output (no provenance fence, no hidden-text stripping, no image-beacon defanging)")
	enableJS := flag.Bool("js", false, "enable opt-in JavaScript rendering (the render tool arg)")
	jsTimeout := flag.Duration("js-timeout", browser.DefaultJSTimeout, "per-render wall-clock budget for JavaScript")
	jsNoNetwork := flag.Bool("js-no-network", false, "disable page-JS network requests (DOM-only render)")
	jsAllowPrivate := flag.Bool("js-allow-private", false, "permit page-JS requests to private/loopback IPs (internal/dev use)")
	jsMaxRequests := flag.Int("js-max-requests", browser.DefaultJSMaxRequests, "max page-JS network requests per render")
	jsPrewarm := flag.Int("js-prewarm", browser.DefaultJSPrewarm, "number of pre-warmed JS runtimes kept ready (0 disables)")
	searchProvider := flag.String("search-provider", "", "web search provider for the search tool: searxng|brave (empty disables it)")
	searchEndpoint := flag.String("search-endpoint", "", "search endpoint URL (SearXNG base URL; optional Brave override). The API key comes from UNBLINK_SEARCH_API_KEY")
	flag.Parse()

	if *showVersion {
		fmt.Println("unblink", mcpserver.Version(), buildMeta())
		return
	}

	// Logs go to stderr; stdout is reserved for MCP JSON-RPC.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(*logLevel)})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	opts := []browser.Option{
		browser.WithRateLimit(*rateLimit, *rateBurst),
		browser.WithRetries(*retries),
		browser.WithTLSMimic(*tlsMimic),
		browser.WithAllowPrivate(*allowPrivate),
		browser.WithSiteHints(!*noSiteHints),
		browser.WithSafeOutput(!*noSafeOutput),
	}
	if *enableJS {
		opts = append(opts,
			browser.WithJS(*jsTimeout),
			browser.WithJSNetwork(!*jsNoNetwork),
			browser.WithJSAllowPrivate(*jsAllowPrivate),
			browser.WithJSMaxRequests(*jsMaxRequests),
			browser.WithJSPrewarm(*jsPrewarm),
		)
	}
	if *searchProvider != "" {
		// The API key is read from the environment, never a flag, so it stays out
		// of the process argv.
		p, err := search.New(search.Config{
			Provider: *searchProvider,
			Endpoint: *searchEndpoint,
			APIKey:   os.Getenv("UNBLINK_SEARCH_API_KEY"),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "unblink:", err)
			os.Exit(1)
		}
		opts = append(opts, browser.WithSearchProvider(p))
	}
	b, err := browser.New(opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "unblink:", err)
		os.Exit(1)
	}
	defer b.Close()

	if err := mcpserver.New(b, !*noSafeOutput).Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "unblink:", err)
		os.Exit(1)
	}
}

// buildMeta reports the VCS revision and commit time embedded by the Go
// toolchain, so --version identifies the exact build even without a release tag.
func buildMeta() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, at string
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			at = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	if at != "" {
		return fmt.Sprintf("(%s %s)", rev, at)
	}
	return "(" + rev + ")"
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}
