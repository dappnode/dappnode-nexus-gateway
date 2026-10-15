package stream

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// DoWithHeaderTimeout bounds the wait for headers without imposing a deadline
// on the response body or changing the caller's verified HTTP transport.
func DoWithHeaderTimeout(client *http.Client, request *http.Request, timeout time.Duration) (*http.Response, error) {
	if timeout <= 0 {
		return client.Do(request)
	}
	parent := request.Context()
	ctx, cancel := context.WithCancel(parent)
	var mu sync.Mutex
	waiting, expired := true, false
	timer := time.AfterFunc(timeout, func() {
		mu.Lock()
		defer mu.Unlock()
		if waiting {
			expired = true
			cancel()
		}
	})
	response, err := client.Do(request.WithContext(ctx))
	mu.Lock()
	waiting = false
	timer.Stop()
	timedOut := expired
	mu.Unlock()
	if timedOut && parent.Err() == nil {
		cancel()
		if response != nil {
			_ = response.Body.Close()
		}
		return nil, context.DeadlineExceeded
	}
	if err != nil {
		cancel()
		return response, err
	}
	response.Body = &cancelBody{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error { b.cancel(); return b.ReadCloser.Close() }
