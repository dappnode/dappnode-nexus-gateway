package handlers

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/mapper"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/sse"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Bound the wait for usage after generation has finished, without extending
// the lifetime of requests canceled before completion.
const streamUsageDrainTimeout = 5 * time.Second

// ChatCompletionsHandler handles POST /v1/chat/completions.
type ChatCompletionsHandler struct {
	service *services.ChatCompletionsService
	logger  ports.Logger
}

func NewChatCompletionsHandler(service *services.ChatCompletionsService, logger ports.Logger) *ChatCompletionsHandler {
	return &ChatCompletionsHandler{service: service, logger: logger}
}

func (h *ChatCompletionsHandler) Handle(w http.ResponseWriter, r *http.Request) {
	token, err := ExtractBearerToken(r)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, domain.ErrInvalidField("failed to read request body"))
		return
	}

	genReq, err := mapper.ChatCompletionRequestToDomain(body)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}

	// The gateway is a proxy: the request goes upstream as the client sent it
	// and the provider's response comes back as it came.
	upstream, cancelUpstream := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancelUpstream()
	var finished atomic.Bool
	stopWatch := context.AfterFunc(r.Context(), func() {
		// A client gone after the finish reason still gets its usage metered:
		// the upstream is drained briefly instead of cut off.
		if finished.Load() {
			time.AfterFunc(streamUsageDrainTimeout, cancelUpstream)
			return
		}
		cancelUpstream()
	})
	defer stopWatch()
	call, err := h.service.Proxy(upstream, body, genReq, token)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}
	if call.Stream != nil {
		h.relay(w, r, call.Stream, &finished)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(call.Body)
}

// relay sends a provider's stream to the client line by line, as it came.
// Keep-alive comments fill long silences (slow reasoning), and a stream the
// provider finished without [DONE] gets one, so every client sees an end.
func (h *ChatCompletionsHandler) relay(w http.ResponseWriter, r *http.Request, stream *services.ProxyStream, finished *atomic.Bool) {
	defer stream.Close()
	sw, err := sse.NewWriter(w)
	if err != nil {
		WriteError(w, domain.ErrInternal("an internal error occurred"))
		return
	}
	var writeMu sync.Mutex
	lastWrite := time.Now()
	write := func(line []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()
		lastWrite = time.Now()
		// A client that left keeps the loop draining the upstream for usage.
		_ = sw.WriteLine(line)
	}
	stopKeepAlive := make(chan struct{})
	defer close(stopKeepAlive)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopKeepAlive:
				return
			case <-ticker.C:
				writeMu.Lock()
				if time.Since(lastWrite) >= 15*time.Second {
					lastWrite = time.Now()
					_ = sw.WriteComment("keepalive")
				}
				writeMu.Unlock()
			}
		}
	}()

	var readErr error
	for {
		line, err := stream.Next()
		if err != nil {
			readErr = err
			break
		}
		if stream.Finished() {
			finished.Store(true)
		}
		write(line)
	}
	switch {
	case stream.Complete() && !stream.SawDone():
		write([]byte("data: [DONE]"))
		write(nil)
	case readErr != io.EOF && r.Context().Err() == nil:
		// The upstream broke mid-response: say so rather than end quietly.
		h.logger.Warn("proxied stream interrupted", "request_id", middleware.GetRequestID(r.Context()))
		write([]byte(`data: {"error":{"type":"provider_error","code":"stream_interrupted","message":"The provider stream ended unexpectedly."}}`))
		write(nil)
	}
}
