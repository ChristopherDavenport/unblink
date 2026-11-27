// unblink patch: cooperative cancellation. This file is not part of upstream
// dslipak/pdf; see unblink docs/decisions/0001 (Amendment 3).
//
// Upstream has no way to stop a parse: its loops are pure CPU over in-memory
// data and check nothing, so a caller that gives up on a hostile document
// leaves the parsing goroutine running. NewReaderContext ties a Reader to a
// context, and the library then stops at three kinds of checkpoint:
//
//  1. every read of the underlying file fails once the context is done
//     (cancelReaderAt), which starves the lexer and every stream filter;
//  2. Reader.resolve — the single path behind Value.Key and Value.Index, so
//     every walk over the object graph — panics (hunk in read.go);
//  3. cmap.Decode panics once per decoded character code (hunk in page.go),
//     because that loop is quadratic over in-memory data and resolves nothing.
//
// A checkpoint panics because that is how this library already reports
// malformed input; the caller must recover, as it must for any other input.
// Cancellation is cooperative: code that reaches none of these checkpoints is
// not interrupted.

package pdf

import (
	"context"
	"fmt"
	"io"
)

// NewReaderContext is NewReader bound to ctx: once ctx is done, parsing —
// both inside this call and in every later use of the returned Reader and its
// Values — stops at the next checkpoint (see the file comment) by returning an
// error or panicking. A context that is never done never cancels.
func NewReaderContext(ctx context.Context, f io.ReaderAt, size int64) (*Reader, error) {
	return NewReaderEncrypted(&cancelReaderAt{ctx: ctx, f: f}, size, nil)
}

// cancelReaderAt fails every read once its context is done. It also carries
// the context into NewReaderEncrypted, which copies it onto the Reader.
type cancelReaderAt struct {
	ctx context.Context
	f   io.ReaderAt
}

func (c *cancelReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.f.ReadAt(p, off)
}

// checkCanceled panics if the Reader's context is done. It is safe on a nil
// Reader (Values built by Interpret carry none) and on a Reader made without a
// context, and costs one non-blocking channel receive.
func (r *Reader) checkCanceled() {
	if r == nil || r.ctx == nil {
		return
	}
	select {
	case <-r.ctx.Done():
		panic(fmt.Errorf("pdf: canceled: %w", context.Cause(r.ctx)))
	default:
	}
}
