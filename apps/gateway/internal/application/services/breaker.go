package services

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// SelfHealing configures how the gateway reacts to a provider that keeps
// failing: a circuit breaker per provider and upstream model, and one quick
// retry for a failure that happened before any output.
type SelfHealing struct {
	// BreakerFailures is how many provider failures in a row open the
	// circuit; 0 turns the breaker off.
	BreakerFailures int
	// BreakerOpenFor is how long a newly opened circuit stays open. A probe
	// that fails doubles it, up to BreakerMaxOpen.
	BreakerOpenFor time.Duration
	BreakerMaxOpen time.Duration
	// RetryFastFailures retries once a connection error or a 502/503 that the
	// provider returned within RetryWithin, before any output.
	RetryFastFailures bool
	RetryWithin       time.Duration
	RetryDelay        time.Duration
}

// DefaultSelfHealing is what the gateway runs with unless configured.
var DefaultSelfHealing = SelfHealing{
	BreakerFailures:   5,
	BreakerOpenFor:    30 * time.Second,
	BreakerMaxOpen:    5 * time.Minute,
	RetryFastFailures: true,
	RetryWithin:       5 * time.Second,
	RetryDelay:        250 * time.Millisecond,
}

// Circuit states, for logs and metrics.
const (
	circuitClosed   = "closed"
	circuitOpen     = "open"
	circuitHalfOpen = "half_open"
)

// breaker tracks provider failures per target. While a target's circuit is
// open, requests skip it: they go to the model's fallback, or fail at once
// with Retry-After instead of each paying for a provider that is down. When
// the open period ends, one request probes the target; its result closes or
// reopens the circuit.
type breaker struct {
	mu      sync.Mutex
	cfg     SelfHealing
	now     func() time.Time
	targets map[string]*circuit
}

type circuit struct {
	failures  int
	openUntil time.Time
	openFor   time.Duration
	probing   bool
}

// transition reports a circuit changing state, for logs and metrics.
type transition struct {
	to       string
	failures int
	openFor  time.Duration
}

func newBreaker(cfg SelfHealing) *breaker {
	return &breaker{cfg: cfg, now: time.Now, targets: map[string]*circuit{}}
}

func (b *breaker) enabled() bool { return b != nil && b.cfg.BreakerFailures > 0 }

// allow reports whether a request may go to the target, and if not, how long
// until it may try again. After the open period one request is let through
// as a probe; others keep waiting until the probe answers.
func (b *breaker) allow(key string) (bool, time.Duration) {
	if !b.enabled() {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.targets[key]
	if c == nil || c.openUntil.IsZero() {
		return true, 0
	}
	now := b.now()
	if now.Before(c.openUntil) {
		return false, c.openUntil.Sub(now)
	}
	if c.probing {
		return false, time.Second
	}
	c.probing = true
	return true, 0
}

// success closes the target's circuit. It returns a transition when the
// circuit was open.
func (b *breaker) success(key string) *transition {
	if !b.enabled() {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.targets[key]
	if c == nil {
		return nil
	}
	wasOpen := !c.openUntil.IsZero()
	delete(b.targets, key)
	if wasOpen {
		return &transition{to: circuitClosed}
	}
	return nil
}

// failure counts a provider failure. It returns a transition when the
// circuit opens, or reopens after a failed probe.
func (b *breaker) failure(key string) *transition {
	if !b.enabled() {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.targets[key]
	if c == nil {
		c = &circuit{}
		b.targets[key] = c
	}
	c.failures++
	switch {
	case c.probing:
		c.probing = false
		c.openFor = minDuration(c.openFor*2, b.cfg.BreakerMaxOpen)
	case c.openUntil.IsZero() && c.failures >= b.cfg.BreakerFailures:
		c.openFor = b.cfg.BreakerOpenFor
	default:
		return nil
	}
	c.openUntil = b.now().Add(c.openFor)
	return &transition{to: circuitOpen, failures: c.failures, openFor: c.openFor}
}

// release frees a probe slot when the probe ended without telling us anything
// about the provider (the client left, or the request was rejected).
func (b *breaker) release(key string) {
	if !b.enabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if c := b.targets[key]; c != nil {
		c.probing = false
	}
}

// state names the target's circuit state, for metrics.
func (b *breaker) state(key string) string {
	if !b.enabled() {
		return circuitClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.targets[key]
	switch {
	case c == nil || c.openUntil.IsZero():
		return circuitClosed
	case b.now().Before(c.openUntil):
		return circuitOpen
	default:
		return circuitHalfOpen
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func breakerKey(target domain.PublicModel) string {
	return target.ProviderConfig.ProviderName + "/" + target.UpstreamModelName
}

// providerOutage reports whether an error says the provider itself is failing
// (down, overloaded, timing out, or refusing our account), as opposed to the
// caller's mistake, a rate limit, a rejected request or a client that left.
func providerOutage(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) {
		return true // A transport error with no classification.
	}
	switch gwErr.Code {
	case domain.ErrCodeProviderConfiguration:
		return true // Our key or account at the provider: every request fails until it is fixed.
	case domain.ErrCodeProviderUnavailable, domain.ErrCodeProviderTimeout, domain.ErrCodeProviderError:
		if status := domain.UpstreamStatus(err); status >= 400 && status < 500 {
			return false // The provider answered about this request (rejected it, rate limited it).
		}
		return gwErr.Metadata["cause"] != domain.CauseCircuitOpen && gwErr.Metadata["cause"] != domain.CauseProviderNotRegistered
	}
	return false
}

// fastTransient reports whether a failure is worth one quick retry: the
// connection failed, or the provider answered 502 or 503, quickly and before
// any output. A slow failure is not retried; it would only double the wait.
func fastTransient(ctx context.Context, err error, took, within time.Duration) bool {
	if !providerOutage(ctx, err) || took > within {
		return false
	}
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) {
		return true
	}
	if gwErr.Code == domain.ErrCodeProviderConfiguration {
		return false // Our key or account at the provider: a retry fails the same way.
	}
	switch domain.UpstreamStatus(err) {
	case 0, 502, 503:
		return true
	}
	return false
}

// circuitOpenError is what a request gets when its provider's circuit is
// open and the model has no fallback.
func circuitOpenError(provider string, wait time.Duration) *domain.GatewayError {
	err := domain.ErrProviderUnavailable(provider).WithMeta("cause", domain.CauseCircuitOpen)
	err.RetryAfter = strconv.Itoa(int(math.Ceil(wait.Seconds())))
	return err
}

// tryTarget sends a request to one provider target through its circuit
// breaker. When mayRetry is set (the model has no fallback to go to), a quick
// transient failure before any output is retried once.
func (s *GenerateService) tryTarget(ctx context.Context, requestID string, target domain.PublicModel,
	attempt func(domain.PublicModel) (*ProxyCall, error), mayRetry bool) (*ProxyCall, error) {
	key := breakerKey(target)
	provider := target.ProviderConfig.ProviderName
	if ok, wait := s.breaker.allow(key); !ok {
		return nil, circuitOpenError(provider, wait)
	}
	for try := 1; ; try++ {
		began := time.Now()
		call, err := attempt(target)
		if err == nil {
			s.noteCircuit(requestID, target, s.breaker.success(key))
			return call, nil
		}
		if !providerOutage(ctx, err) {
			s.breaker.release(key)
			return nil, err
		}
		if t := s.breaker.failure(key); t != nil {
			s.noteCircuit(requestID, target, t)
			return nil, err // The circuit just opened: stop sending to this provider.
		}
		if try >= 2 || !mayRetry || !s.heal.RetryFastFailures || !fastTransient(ctx, err, time.Since(began), s.heal.RetryWithin) {
			return nil, err
		}
		metrics.ProviderRetries.WithLabelValues(provider, "transient_retry").Inc()
		if s.logger != nil {
			s.logger.Info("retrying provider after a quick failure", append([]any{
				"request_id", requestID, "model", target.PublicModelID, "provider", provider,
				"provider_model", target.UpstreamModelName}, domain.DiagnosticFields(err)...)...)
		}
		if !sleepContext(ctx, s.heal.RetryDelay) {
			return nil, err
		}
		if ok, _ := s.breaker.allow(key); !ok {
			return nil, err
		}
	}
}

// noteCircuit records a circuit state change in the metrics and the log.
func (s *GenerateService) noteCircuit(requestID string, target domain.PublicModel, t *transition) {
	if t == nil {
		return
	}
	provider, upstream := target.ProviderConfig.ProviderName, target.UpstreamModelName
	metrics.CircuitTransitions.WithLabelValues(provider, upstream, t.to).Inc()
	fields := []any{"request_id", requestID, "model", target.PublicModelID, "provider", provider, "provider_model", upstream}
	if t.to == circuitOpen {
		metrics.CircuitOpen.WithLabelValues(provider, upstream).Set(1)
		if s.logger != nil {
			s.logger.Warn("provider circuit opened", append(fields, "failures", t.failures, "open_seconds", int(t.openFor.Seconds()))...)
		}
		return
	}
	metrics.CircuitOpen.WithLabelValues(provider, upstream).Set(0)
	if s.logger != nil {
		s.logger.Info("provider circuit closed", fields...)
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
