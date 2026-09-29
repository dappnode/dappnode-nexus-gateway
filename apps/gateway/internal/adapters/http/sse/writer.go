package sse

import (
	"fmt"
	"net/http"
)

// Writer writes Server-Sent Events to an HTTP response.
type Writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewWriter creates a new SSE writer and sets appropriate headers.
func NewWriter(w http.ResponseWriter) (*Writer, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return &Writer{w: w, flusher: flusher}, nil
}

// WriteComment writes an SSE comment (ignored by clients). Useful as a keep-alive.
func (sw *Writer) WriteComment(text string) error {
	if _, err := fmt.Fprintf(sw.w, ": %s\n\n", text); err != nil {
		return err
	}
	sw.flusher.Flush()
	return nil
}

// WriteLine relays one line of an upstream SSE stream as it came.
func (sw *Writer) WriteLine(line []byte) error {
	if _, err := sw.w.Write(append(line, '\n')); err != nil {
		return err
	}
	sw.flusher.Flush()
	return nil
}
