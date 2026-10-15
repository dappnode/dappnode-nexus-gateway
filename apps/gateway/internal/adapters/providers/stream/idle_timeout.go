// Package stream bounds provider silence while allowing active long streams.
package stream

import (
	"io"
	"sync"
	"time"
)

type idleTimeoutError struct{}

func (idleTimeoutError) Error() string   { return "provider stream idle timeout" }
func (idleTimeoutError) Timeout() bool   { return true }
func (idleTimeoutError) Temporary() bool { return true }

type idleBody struct {
	body     io.ReadCloser
	timeout  time.Duration
	timer    *time.Timer
	mu       sync.Mutex
	closed   bool
	expired  bool
	deadline time.Time
}

func WithIdleTimeout(body io.ReadCloser, timeout time.Duration) io.ReadCloser {
	if timeout <= 0 {
		return body
	}
	b := &idleBody{body: body, timeout: timeout, deadline: time.Now().Add(timeout)}
	b.timer = time.AfterFunc(timeout, func() {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		// A reset can race a callback already waiting for the lock. A stale
		// callback must not expire a stream that just delivered more bytes.
		if remaining := time.Until(b.deadline); remaining > 0 {
			b.timer.Reset(remaining)
			b.mu.Unlock()
			return
		}
		b.expired, b.closed = true, true
		b.mu.Unlock()
		_ = body.Close()
	})
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.expired {
		return n, idleTimeoutError{}
	}
	if n > 0 && !b.closed {
		b.deadline = time.Now().Add(b.timeout)
		b.timer.Reset(b.timeout)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.mu.Unlock()
	return b.body.Close()
}
