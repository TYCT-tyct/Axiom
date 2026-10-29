package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"axiom/internal/suppliers"
)

// streaming is an upstream that answers with the prefix events, one every wait, and then goes on producing text(each)
// every wait, whether or not the gateway reads it: a real upstream's socket buffers take what it writes until the
// attempt is cancelled, and what it wrote is what its supplier bills. produced counts the text bytes it wrote.
func streaming(prefix []string, each int, wait time.Duration, produced *atomic.Int64) scripted {
	return func(ctx context.Context) (*http.Response, *suppliers.Failure) {
		b := &producing{more: make(chan struct{}, 1), done: ctx.Done()}
		go func() {
			for _, s := range prefix {
				if !pause(ctx, wait) {
					return
				}
				b.write(s)
			}
			for pause(ctx, wait) {
				b.write(text(each))
				produced.Add(int64(each))
			}
		}()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: b}, nil
	}
}

// producing is a body the upstream writes to on its own clock: a read takes what is there, or waits for the next write,
// and the end of the attempt ends both.
type producing struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	more chan struct{}
	done <-chan struct{}
}

func (b *producing) write(s string) {
	b.mu.Lock()
	b.buf.WriteString(s)
	b.mu.Unlock()
	select {
	case b.more <- struct{}{}:
	default: // a signal already pending covers this write
	}
}

func (b *producing) Read(p []byte) (int, error) {
	for {
		b.mu.Lock()
		if b.buf.Len() > 0 {
			n, err := b.buf.Read(p)
			b.mu.Unlock()
			return n, err
		}
		b.mu.Unlock()
		select {
		case <-b.more:
		case <-b.done:
			return 0, io.EOF
		}
	}
}

func (*producing) Close() error { return nil }

// pause waits for d and reports whether the attempt is still on.
func pause(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}
