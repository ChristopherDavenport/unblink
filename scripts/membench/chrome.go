package main

import (
	"context"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// chromeRunner launches one headless Chromium via chromedp and measures the RSS
// of its whole process tree while it renders pages. Chromium is the multi-process
// baseline unblink is compared against.
type chromeRunner struct {
	allocCtx context.Context
	cancelA  context.CancelFunc
	browser  context.Context
	cancelB  context.CancelFunc
	pid      int
}

// chromeOpts matches unblink's constant viewport (1280x720) and runs headless
// with the settle window comparable to unblink's quiet period.
func chromeOpts(execPath string) []chromedp.ExecAllocatorOption {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.Headless,
		chromedp.NoSandbox, // WSL2 / CI
		chromedp.DisableGPU,
		chromedp.WindowSize(1280, 720),
	)
	if execPath != "" {
		opts = append(opts, chromedp.ExecPath(execPath))
	}
	return opts
}

// startChrome launches the browser and returns the runner plus cold-start time
// (launch → first blank target ready).
func startChrome(execPath string) (*chromeRunner, time.Duration, error) {
	t0 := time.Now()
	allocCtx, cancelA := chromedp.NewExecAllocator(context.Background(), chromeOpts(execPath)...)
	// Silence chromedp's cdproto event-unmarshal warnings — cdproto lags newer
	// Chrome protocol enums (harmless for a footprint measurement).
	browser, cancelB := chromedp.NewContext(allocCtx, chromedp.WithErrorf(func(string, ...any) {}))
	// Force the browser to actually start by running a trivial task.
	if err := chromedp.Run(browser); err != nil {
		cancelB()
		cancelA()
		return nil, 0, err
	}
	ready := time.Since(t0)
	r := &chromeRunner{allocCtx: allocCtx, cancelA: cancelA, browser: browser, cancelB: cancelB}
	r.pid = browserPID(browser)
	return r, ready, nil
}

// browserPID returns the root chrome process pid, so the tree walker can sum the
// whole multi-process footprint.
func browserPID(ctx context.Context) int {
	if c := chromedp.FromContext(ctx); c != nil && c.Browser != nil {
		// chromedp doesn't expose the pid directly; the exec allocator owns it.
	}
	return findChromeRoot()
}

// render navigates a fresh tab to url, waits for the load event plus a short
// settle, and returns after the DOM is ready — comparable to unblink's render.
func (r *chromeRunner) render(url string) error {
	tab, cancel := chromedp.NewContext(r.browser)
	defer cancel()
	return chromedp.Run(tab,
		chromedp.Navigate(url),
		chromedp.WaitReady("body"),
		chromedp.Sleep(150*time.Millisecond),
	)
}

// renderConcurrent opens n tabs against the given urls (cycled) at once, holding
// them open so the peak-RSS sampler sees the concurrent footprint.
func (r *chromeRunner) renderConcurrent(urls []string, n int) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			tab, cancel := chromedp.NewContext(r.browser)
			defer cancel()
			_ = chromedp.Run(tab,
				chromedp.Navigate(u),
				chromedp.WaitReady("body"),
				chromedp.Sleep(400*time.Millisecond),
			)
		}(urls[i%len(urls)])
	}
	wg.Wait()
}

func (r *chromeRunner) Close() {
	r.cancelB()
	r.cancelA()
}
