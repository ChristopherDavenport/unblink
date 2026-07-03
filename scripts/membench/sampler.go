package main

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sampler polls a process tree's RSS on an interval and records the peak, so a
// measurement window captures the true high-water mark rather than a single
// instantaneous read. 50ms is fine-grained enough for render spikes.
type sampler struct {
	pid  int
	stop chan struct{}
	wg   sync.WaitGroup
	mu   sync.Mutex
	peak uint64 // KiB
}

func newSampler(pid int) *sampler {
	s := &sampler{pid: pid, stop: make(chan struct{})}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		s.record()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.record()
			}
		}
	}()
	return s
}

func (s *sampler) record() {
	rss := treeRSSKiB(s.pid)
	s.mu.Lock()
	if rss > s.peak {
		s.peak = rss
	}
	s.mu.Unlock()
}

// peakKiB stops the sampler and returns the observed peak tree RSS in KiB.
func (s *sampler) peakKiB() uint64 {
	close(s.stop)
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

// findChildByComm returns the pid of a direct child of parent whose comm
// contains sub (e.g. "chrome"), or 0. chromedp launches chrome as our child.
func findChildByComm(parent int, sub string) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if procPPID(pid) != parent {
			continue
		}
		if strings.Contains(strings.ToLower(procComm(pid)), sub) {
			return pid
		}
	}
	return 0
}

// findChromeRoot locates the chrome process chromedp spawned as our child.
func findChromeRoot() int {
	return findChildByComm(os.Getpid(), "chrome")
}

// procComm reads /proc/<pid>/comm (the executable name).
func procComm(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
