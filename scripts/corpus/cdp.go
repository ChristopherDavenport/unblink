package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// cdp is a minimal Chrome DevTools Protocol client over --remote-debugging-pipe:
// Chromium reads NUL-delimited JSON commands on fd 3 and writes replies and
// events on fd 4. Messages are handled untyped on purpose — a typed binding
// (cdproto) lags the browser and drops events carrying an enum it does not know,
// which for a recorder means silently missing responses.
type cdp struct {
	cmd     *exec.Cmd
	w       io.WriteCloser
	profile string

	wmu sync.Mutex // serialises pipe writes; never held with mu

	mu      sync.Mutex
	nextID  int
	pending map[int]chan reply
	subs    map[string]chan event // by sessionId
	closed  chan struct{}
}

type reply struct {
	result json.RawMessage
	err    error
}

// event is one protocol event delivered to a session's subscriber.
type event struct {
	Method string
	Params json.RawMessage
}

type wireMsg struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// launch starts a headless Chromium with a throwaway profile at the 1280x720
// viewport unblink's engine reports (so responsive code takes the same branch).
func launch(execPath string) (*cdp, error) {
	profile, err := os.MkdirTemp("", "unblink-corpus-profile-")
	if err != nil {
		return nil, err
	}
	toChromeR, toChromeW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	fromChromeR, fromChromeW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(execPath,
		"--headless=new",
		"--remote-debugging-pipe",
		"--no-sandbox", // WSL2 / CI, as scripts/membench
		"--disable-gpu",
		"--window-size=1280,720",
		"--user-data-dir="+profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--disable-extensions",
		"--mute-audio",
		"--hide-scrollbars",
		"about:blank",
	)
	cmd.ExtraFiles = []*os.File{toChromeR, fromChromeW} // fd 3 (in), fd 4 (out)
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = toChromeR.Close()
	_ = fromChromeW.Close()

	c := &cdp{
		cmd: cmd, w: toChromeW, profile: profile,
		pending: map[int]chan reply{},
		subs:    map[string]chan event{},
		closed:  make(chan struct{}),
	}
	go c.readLoop(fromChromeR)
	return c, nil
}

func (c *cdp) readLoop(r io.Reader) {
	defer close(c.closed)
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		raw, err := br.ReadBytes(0)
		if err != nil {
			c.mu.Lock()
			for id, ch := range c.pending {
				ch <- reply{err: errors.New("cdp: browser closed the pipe")}
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var m wireMsg
		if json.Unmarshal(raw[:len(raw)-1], &m) != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch == nil {
				continue
			}
			if m.Error != nil {
				ch <- reply{err: fmt.Errorf("cdp: %s", m.Error.Message)}
			} else {
				ch <- reply{result: m.Result}
			}
			continue
		}
		c.mu.Lock()
		ch := c.subs[m.SessionID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- event{Method: m.Method, Params: m.Params}:
			default: // subscriber stalled; never block the reader
			}
		}
	}
}

// subscribe returns the event stream for one target session.
func (c *cdp) subscribe(sessionID string) chan event {
	ch := make(chan event, 1<<16)
	c.mu.Lock()
	c.subs[sessionID] = ch
	c.mu.Unlock()
	return ch
}

func (c *cdp) unsubscribe(sessionID string) {
	c.mu.Lock()
	delete(c.subs, sessionID)
	c.mu.Unlock()
}

// call sends one command and decodes its result into out (which may be nil).
func (c *cdp) call(ctx context.Context, sessionID, method string, params, out any) error {
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		rawParams = b
	}
	ch := make(chan reply, 1)
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()
	b, err := json.Marshal(wireMsg{ID: id, Method: method, Params: rawParams, SessionID: sessionID})
	if err == nil {
		// The write can block until Chromium drains its input, and Chromium may be
		// blocked writing an event the read loop needs mu to dispatch — so the
		// write must not hold mu.
		c.wmu.Lock()
		_, err = c.w.Write(append(b, 0))
		c.wmu.Unlock()
	}
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("%s: %w", method, r.err)
		}
		if out != nil {
			return json.Unmarshal(r.result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

func (c *cdp) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = c.call(ctx, "", "Browser.close", nil, nil)
	cancel()
	_ = c.w.Close()
	select {
	case <-c.closed:
	case <-time.After(3 * time.Second):
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
	_ = os.RemoveAll(c.profile)
}
