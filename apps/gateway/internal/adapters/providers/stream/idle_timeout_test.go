package stream

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestActivityRefreshesIdleTimeout(t *testing.T) {
	r, w := io.Pipe()
	body := WithIdleTimeout(r, 150*time.Millisecond)
	defer body.Close()
	go func() {
		defer w.Close()
		for i := 0; i < 7; i++ {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
	}()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "xxxxxxx" {
		t.Fatalf("active stream timed out: %v %q", err, got)
	}
}

func TestProviderSilenceClosesBlockedRead(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	body := WithIdleTimeout(r, 20*time.Millisecond)
	defer body.Close()
	_, err := body.Read(make([]byte, 1))
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("idle provider did not time out: %v", err)
	}
}
