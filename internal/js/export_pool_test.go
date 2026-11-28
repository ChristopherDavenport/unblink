package js

// PoolFull reports whether the pre-warm pool holds its full complement of
// prepared runtimes. Test-only: lets a test (or benchmark) wait until a render is
// guaranteed to take a prepared runtime rather than build one cold.
func (e *Engine) PoolFull() bool { return e.pool != nil && len(e.pool) == cap(e.pool) }
