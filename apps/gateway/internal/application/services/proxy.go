package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/observability/logfields"
	"github.com/google/uuid"
)

// maxSSELine bounds one SSE line (a whole tool call can arrive in one).
const maxSSELine = 16 << 20

// ProxyCall is a proxied request: the provider's response goes to the client
// as it came, except the model field (the public model) and, for keys that
// mask PII, the restored text.
type ProxyCall struct {
	Model domain.PublicModel
	// Stream is set for streaming requests.
	Stream *ProxyStream
	// Body is the response of a non-streaming request.
	Body []byte
}

// Proxy runs a chat-completions request. The gateway authenticates, routes,
// validates, masks PII for keys that ask for it, reserves credit, and meters
// the usage it reads from the response; it never rebuilds the request or the
// response.
func (s *GenerateService) Proxy(ctx context.Context, endpoint string, raw []byte, req domain.GenerateRequest, bearerToken string) (*ProxyCall, error) {
	start := time.Now()
	requestID := middleware.GetRequestID(ctx)

	authCtx, err := s.auth.AuthenticateAPIKey(ctx, bearerToken)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, req, nil, time.Since(start).Milliseconds())
		return nil, err
	}
	model, execReq, err := s.resolveModel(ctx, req)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, req, nil, time.Since(start).Milliseconds())
		return nil, err
	}
	if err := s.validateRequest(endpoint, execReq, model); err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, execReq, &model, time.Since(start).Milliseconds())
		s.recordFailure(ctx, nil, &authCtx, endpoint, &execReq, &model, err, nil, time.Since(start).Milliseconds())
		return nil, err
	}
	masked, mapping, err := s.maskBody(ctx, raw, authCtx.APIKey.PIIMode)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, execReq, &model, time.Since(start).Milliseconds())
		s.recordFailure(ctx, nil, &authCtx, endpoint, &execReq, &model, err, nil, time.Since(start).Milliseconds())
		return nil, err
	}
	reservationID, err := s.metering.Reserve(ctx, authCtx, endpoint, execReq, model, uuid.NewString())
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, execReq, &model, time.Since(start).Milliseconds())
		return nil, err
	}
	finish := func(target domain.PublicModel, o outcome) {
		o.err = sanitizeErrorWithPIIMapping(o.err, mapping)
		s.finishProxy(ctx, authCtx, endpoint, execReq, target, requestID, reservationID, start, o)
	}
	unmask := func(target domain.PublicModel) *unmasker {
		return newUnmasker(mapping, s.logger, "request_id", requestID, "endpoint", endpoint, "model", execReq.PublicModelID,
			"provider", target.ProviderConfig.ProviderName, "pii_mode", authCtx.APIKey.PIIMode)
	}

	attempt := func(target domain.PublicModel) (*ProxyCall, error) {
		provider, err := s.registry.GetProvider(target.ProviderConfig.ProviderName)
		if err != nil {
			return nil, domain.ErrProviderUnavailable(target.ProviderConfig.ProviderName)
		}
		upstreamStart := time.Now()
		if !execReq.Stream {
			body, proof, err := provider.Complete(ctx, masked, target)
			recordUpstreamLatency(execReq.PublicModelID, target.ProviderConfig.ProviderName, upstreamStart, err)
			if err != nil {
				return nil, err
			}
			rewritten, o, err := observeJSON(body, target.PublicModelID, unmask(target))
			if err != nil {
				return nil, err
			}
			o.proof = proof
			finish(target, o)
			return &ProxyCall{Model: target, Body: rewritten}, nil
		}
		resp, err := provider.Stream(ctx, masked, target)
		recordUpstreamLatency(execReq.PublicModelID, target.ProviderConfig.ProviderName, upstreamStart, err)
		if err != nil {
			return nil, err
		}
		stream := newProxyStream(resp.Body, target.PublicModelID)
		stream.proof = resp.Proof
		stream.hideUsage = !wantsUsage(raw)
		stream.unmask = unmask(target)
		// Nothing has reached the client yet: an upstream that fails before its
		// first output can still fall back.
		if err := stream.prime(); err != nil {
			stream.body.Close()
			return nil, err
		}
		stream.onEnd = func(o outcome) { finish(target, o) }
		return &ProxyCall{Model: target, Stream: stream}, nil
	}

	call, err := attempt(model)
	executed := model
	if err != nil && shouldTryFallback(ctx, err, model.Fallback) {
		executed = withProviderTarget(model, *model.Fallback)
		s.logFallback(requestID, model, executed, sanitizeErrorWithPIIMapping(err, mapping))
		call, err = attempt(executed)
	}
	if err != nil {
		err = sanitizeErrorWithPIIMapping(err, mapping)
		fields := s.buildErrorLogFields(ctx, requestID, &authCtx, endpoint, execReq.PublicModelID, executed, err, time.Since(start).Milliseconds())
		s.logger.Error("provider request failed", fields...)
		s.recordGeneration(metrics.OutcomeError, endpoint, execReq, executed, time.Since(start).Milliseconds())
		s.recordFailure(ctx, &reservationID, &authCtx, endpoint, &execReq, &executed, err, nil, time.Since(start).Milliseconds())
		return nil, err
	}
	return call, nil
}

// outcome is what the gateway read from a proxied response.
type outcome struct {
	providerID string
	finish     *string
	usage      *domain.Usage
	proof      *domain.TinfoilTransportProof
	err        error  // Set when the response failed.
	end        string // How a stream ended, for logs.
	// Stream diagnostics: data events read, and ones that were not JSON.
	chunks, malformed int
}

// finishProxy meters and logs a proxied response.
func (s *GenerateService) finishProxy(ctx context.Context, authCtx domain.AuthContext, endpoint string, req domain.GenerateRequest, model domain.PublicModel, requestID, reservationID string, start time.Time, o outcome) {
	ctx = context.WithoutCancel(ctx) // Accounting outlives the client.
	latencyMs := time.Since(start).Milliseconds()
	fields := []any{
		"request_id", requestID, "reservation_id", reservationID, "provider_request_id", o.providerID,
		"account_id", authCtx.Account.ID, "endpoint", endpoint, "model", req.PublicModelID,
		"provider", model.ProviderConfig.ProviderName, "provider_model", model.UpstreamModelName,
		"stream", req.Stream, "stream_end", o.end, "finish_reason", logfields.FinishReason(o.finish),
		"usage_received", o.usage != nil, "usage", o.usage, "latency_ms", latencyMs,
		"chunks_received", o.chunks, "malformed_chunks", o.malformed,
	}
	if o.err != nil {
		s.logger.Error("generation failed", append(fields, "error_type", fmt.Sprintf("%T", o.err))...)
		s.recordGeneration(metrics.OutcomeError, endpoint, req, model, latencyMs)
		s.recordFailure(ctx, &reservationID, &authCtx, endpoint, &req, &model, o.err, o.usage, latencyMs)
		return
	}
	result := domain.GenerateResult{
		ID: o.providerID, PublicModelID: model.PublicModelID, ProviderName: model.ProviderConfig.ProviderName,
		ProviderModelID: model.ProviderModelID, FinishReason: o.finish, Usage: o.usage, TinfoilProof: o.proof,
	}
	if result.TinfoilProof != nil && result.TinfoilProof.ProviderResponseID == "" {
		result.TinfoilProof.ProviderResponseID = o.providerID
	}
	s.storeTinfoilProof(ctx, authCtx, model, result)
	metrics.RecordUsage(o.usage, req.PublicModelID, model.ProviderConfig.ProviderName)
	if o.usage == nil {
		s.logger.Warn("generation completed without usage", fields...)
	}
	s.recordGeneration(metrics.OutcomeSuccess, endpoint, req, model, latencyMs)
	s.logger.Info("generation completed", append(fields, "gateway_status", 200, "upstream_status", 200)...)
	if err := s.metering.RecordSuccess(ctx, reservationID, authCtx, endpoint, req, result, model, latencyMs); err != nil {
		s.logger.Error("failed to record usage", "request_id", requestID, "reservation_id", reservationID, "error_type", fmt.Sprintf("%T", err))
	}
}

// chunk is what the gateway reads from a provider's JSON: never content.
type chunk struct {
	ID      string  `json:"id"`
	Model   *string `json:"model"`
	Choices []struct {
		Delta *struct {
			Content          *string           `json:"content"`
			ReasoningContent *string           `json:"reasoning_content"`
			Reasoning        *string           `json:"reasoning"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage    *usageJSON      `json:"usage"`
	Error    json.RawMessage `json:"error"`
	BaseResp *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

type usageJSON struct {
	PromptTokens         int64 `json:"prompt_tokens"`
	CompletionTokens     int64 `json:"completion_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
	PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u *usageJSON) domain() *domain.Usage {
	if u == nil {
		return nil
	}
	usage := &domain.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens, CacheReadTokens: u.PromptCacheHitTokens}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		usage.CacheReadTokens = u.PromptTokensDetails.CachedTokens
	}
	return usage
}

// failure reports an error a provider sent in a 200 body or mid-stream.
func (c *chunk) failure() error {
	if c.BaseResp != nil && c.BaseResp.StatusCode != 0 {
		return domain.ErrProviderError(http.StatusBadGateway, "provider returned an unsuccessful response").WithMeta("upstream_code", c.BaseResp.StatusCode)
	}
	if len(c.Error) > 0 && string(c.Error) != "null" {
		return domain.ErrProviderError(http.StatusBadGateway, "provider stream failed")
	}
	return nil
}

// commits reports whether a chunk carries output, after which the response
// belongs to the client and can no longer fall back.
func (c *chunk) commits() bool {
	for _, choice := range c.Choices {
		if choice.FinishReason != nil {
			return true
		}
		if d := choice.Delta; d != nil && (len(d.ToolCalls) > 0 ||
			(d.Content != nil && *d.Content != "") ||
			(d.ReasoningContent != nil && *d.ReasoningContent != "") ||
			(d.Reasoning != nil && *d.Reasoning != "")) {
			return true
		}
	}
	return false
}

func (c *chunk) finishReason() *string {
	for _, choice := range c.Choices {
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			return choice.FinishReason
		}
	}
	return nil
}

// rewriteModel names the public model in a JSON object. Only the value of
// the top-level "model" field changes; every other byte stays as sent.
func rewriteModel(raw []byte, c *chunk, public string) []byte {
	if c.Model == nil || *c.Model == public {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return raw
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return raw
		}
		afterKey := int(dec.InputOffset())
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return raw
		}
		if key != "model" {
			continue
		}
		end := int(dec.InputOffset())
		start := afterKey
		for start < end && (raw[start] == ':' || raw[start] == ' ' || raw[start] == '\t' || raw[start] == '\n' || raw[start] == '\r') {
			start++
		}
		name, _ := json.Marshal(public)
		return bytes.Join([][]byte{raw[:start], name, raw[end:]}, nil)
	}
	return raw
}

// observeJSON reads a non-streaming response.
func observeJSON(body []byte, public string, unmask *unmasker) ([]byte, outcome, error) {
	var c chunk
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, outcome{}, domain.ErrProviderError(http.StatusBadGateway, "provider returned an invalid response")
	}
	if err := c.failure(); err != nil {
		return nil, outcome{}, err
	}
	if unmask != nil {
		body = unmask.body(body)
		unmask.report(false)
	}
	return rewriteModel(body, &c, public), outcome{providerID: c.ID, finish: c.finishReason(), usage: c.Usage.domain(), end: "complete"}, nil
}

// ProxyStream relays a provider's SSE stream line by line, reading usage and
// the finish reason as they pass. Lines are forwarded exactly, except the
// model field of JSON events.
type ProxyStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	public  string
	pending [][]byte
	proof   *domain.TinfoilTransportProof
	onEnd   func(outcome)

	// hideUsage drops usage-only events the client didn't ask for (the
	// gateway requests them to meter); usage is still read from them.
	hideUsage bool
	skipBlank bool
	// unmask restores PII placeholders, for keys that mask.
	unmask *unmasker

	o        outcome
	sawDone  bool
	finished bool // Metered.
	chunks   int
}

// wantsUsage reports whether the client asked for usage in its stream.
func wantsUsage(raw []byte) bool {
	var req struct {
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	return json.Unmarshal(raw, &req) == nil && req.StreamOptions != nil && req.StreamOptions.IncludeUsage
}

// NewProxyStream relays body, naming the public model. The service creates
// streams; it is exported for handler tests.
func NewProxyStream(body io.ReadCloser, public string) *ProxyStream {
	return newProxyStream(body, public)
}

func newProxyStream(body io.ReadCloser, public string) *ProxyStream {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	return &ProxyStream{body: body, scanner: scanner, public: public}
}

// prime reads until the first output, so an upstream that fails first can
// fall back without the client seeing anything.
func (p *ProxyStream) prime() error {
	for p.scanner.Scan() {
		lines, c := p.observe(p.scanner.Bytes())
		for _, line := range lines {
			if p.keep(line, c) {
				p.pending = append(p.pending, line)
			}
		}
		if p.o.err != nil {
			return p.o.err
		}
		if p.sawDone || (c != nil && c.commits()) {
			return nil
		}
	}
	if err := p.scanner.Err(); err != nil {
		return domain.ErrProviderError(http.StatusBadGateway, "provider stream failed before output")
	}
	if p.chunks == 0 {
		return domain.ErrProviderError(http.StatusBadGateway, "provider returned an empty stream")
	}
	return nil
}

// observe reads one line; data events are parsed for metering, get the
// public model name, and have PII restored for keys that mask. It returns
// the lines to send: usually the one it read.
func (p *ProxyStream) observe(raw []byte) ([][]byte, *chunk) {
	line := append([]byte(nil), raw...)
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return [][]byte{line}, nil
	}
	data = bytes.TrimPrefix(data, []byte(" "))
	if string(bytes.TrimSpace(data)) == "[DONE]" {
		p.sawDone = true
		if tail := p.heldTail(); tail != nil {
			return [][]byte{tail, {}, line}, nil
		}
		return [][]byte{line}, nil
	}
	var c chunk
	if json.Unmarshal(data, &c) != nil {
		p.o.malformed++
		return [][]byte{line}, nil
	}
	p.chunks++
	p.o.chunks = p.chunks
	if c.ID != "" {
		p.o.providerID = c.ID
	}
	if u := c.Usage.domain(); u != nil {
		p.o.usage = u
	}
	if f := c.finishReason(); f != nil {
		p.o.finish = f
	}
	if err := c.failure(); err != nil && p.o.err == nil {
		p.o.err = err
	}
	prefix := line[:len(line)-len(data)]
	if p.unmask != nil && len(c.Choices) > 0 {
		data = p.unmask.event(data)
	}
	return [][]byte{append(append([]byte(nil), prefix...), rewriteModel(data, &c, p.public)...)}, &c
}

// heldTail returns an event with PII-restored text still held back, if any.
func (p *ProxyStream) heldTail() []byte {
	if p.unmask == nil {
		return nil
	}
	if tail := p.unmask.tail(p.o.providerID, p.public); tail != nil {
		return append([]byte("data: "), tail...)
	}
	return nil
}

// keep reports whether a line goes to the client: everything does, except a
// usage-only event the client didn't ask for, with its blank separator.
func (p *ProxyStream) keep(line []byte, c *chunk) bool {
	if p.skipBlank && len(line) == 0 {
		p.skipBlank = false
		return false
	}
	p.skipBlank = false
	if p.hideUsage && c != nil && len(c.Choices) == 0 && c.Usage != nil && len(c.Error) == 0 {
		p.skipBlank = true
		return false
	}
	return true
}

// Next returns the next line to send, without its newline. At the end it
// returns io.EOF (clean) or the read error.
func (p *ProxyStream) Next() ([]byte, error) {
	if len(p.pending) > 0 {
		line := p.pending[0]
		p.pending = p.pending[1:]
		return line, nil
	}
	for p.scanner.Scan() {
		lines, c := p.observe(p.scanner.Bytes())
		for _, line := range lines {
			if p.keep(line, c) {
				p.pending = append(p.pending, line)
			}
		}
		if len(p.pending) > 0 {
			return p.Next()
		}
	}
	if tail := p.heldTail(); tail != nil {
		p.pending = append(p.pending, tail, []byte{})
		return p.Next()
	}
	err := p.scanner.Err()
	switch {
	case p.o.err != nil:
		p.end("provider_error")
	case err == nil && (p.sawDone || p.o.finish != nil):
		p.end("complete")
	case err == nil:
		// The provider closed without a finish reason or [DONE]: the output
		// may be cut short; bill what it reported, and log it.
		p.end("eof_without_finish")
	case p.o.finish != nil:
		p.end("read_error_after_finish") // Complete; only trailing bytes were lost.
	default:
		p.o.err = domain.ErrProviderError(http.StatusBadGateway, "provider stream failed")
		p.end("read_error")
	}
	if err == nil {
		err = io.EOF
	}
	return nil, err
}

// Complete reports whether the provider finished the response cleanly.
func (p *ProxyStream) Complete() bool { return p.sawDone || p.o.finish != nil }

// Finished reports whether a finish reason has passed.
func (p *ProxyStream) Finished() bool { return p.o.finish != nil }

// SawDone reports whether the provider sent [DONE].
func (p *ProxyStream) SawDone() bool { return p.sawDone }

// Close releases the upstream. Closing before the end is a cancellation,
// unless the response had already finished.
func (p *ProxyStream) Close() error {
	if !p.finished {
		if p.o.finish == nil && p.o.err == nil {
			p.o.err = domain.ErrClientCanceled()
		}
		p.end("closed_before_eof")
	}
	return p.body.Close()
}

func (p *ProxyStream) end(how string) {
	if p.finished {
		return
	}
	p.finished = true
	p.o.end = how
	p.o.proof = p.proof
	if p.unmask != nil {
		p.unmask.report(true)
	}
	if p.onEnd != nil {
		p.onEnd(p.o)
	}
}
