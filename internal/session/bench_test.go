package session_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/fetch"
	"github.com/christopherdavenport/unblink/internal/session"
)

// BenchmarkManagerGetOrCreate measures the per-lookup cost with a well-populated
// manager — previously every lookup swept the whole session map under the
// global mutex.
func BenchmarkManagerGetOrCreate(b *testing.B) {
	m := session.NewManager(time.Hour, 4096, func(session.Config) (*fetch.Client, error) {
		return fetch.New()
	}, nil)
	const n = 1000
	for i := 0; i < n; i++ {
		if _, err := m.New(fmt.Sprintf("s%d", i)); err != nil {
			b.Fatalf("seed session: %v", err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.GetOrCreate(fmt.Sprintf("s%d", i%n)); err != nil {
			b.Fatalf("get: %v", err)
		}
	}
}
