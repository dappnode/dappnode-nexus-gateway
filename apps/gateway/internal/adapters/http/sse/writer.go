package sse

import (
	"encoding/json"
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

// WriteEvent writes a named SSE event with JSON data.
func (sw *Writer) WriteEvent(eventType string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal SSE data: %w", err)
	}

	if eventType != "" {
		if _, err := fmt.Fprintf(sw.w, "event: %s\n", eventType); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(sw.w, "data: %s\n\n", jsonData); err != nil {
		return err
	}
	sw.flusher.Flush()
	return nil
}

// WriteData writes a data-only SSE event with JSON content.
func (sw *Writer) WriteData(data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal SSE data: %w", err)
	}

	if _, err := fmt.Fprintf(sw.w, "data: %s\n\n", jsonData); err != nil {
		return err
	}
	sw.flusher.Flush()
	return nil
}

// WriteDone writes the [DONE] terminal marker.
func (sw *Writer) WriteDone() error {
	if _, err := fmt.Fprint(sw.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	sw.flusher.Flush()
	return nil
}

// WriteComment writes an SSE comment (ignored by clients). Useful as a keep-alive.
func (sw *Writer) WriteComment(text string) error {
	if _, err := fmt.Fprintf(sw.w, ": %s\n\n", text); err != nil {
		return err
	}
	sw.flusher.Flush()
	return nil
}
