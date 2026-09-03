// Package metrics centralizes Prometheus metric definitions and the HTTP
// handler that exposes them. Metrics are declared once and recorded from the
// HTTP layer, application services, and adapter clients.
package metrics

import (
	"net/http"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Outcome label values.
const (
	OutcomeSuccess = "success"
	OutcomeError   = "error"
)

// LLMBuckets extends the default Prometheus buckets past the 10 s ceiling,
// which is unsuitable for LLM traffic: streaming and non-streaming generation
// can last up to the server's 300 s write timeout, and p95/p99 above 10 s is
// otherwise lost.
var LLMBuckets = prometheus.ExponentialBuckets(0.25, 2, 12)

// HTTP request metrics, recorded by the HTTP middleware. This is the
// authoritative count of incoming requests.
var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_http_requests_total",
			Help: "Total HTTP requests handled by the gateway, by route, method and status class.",
		},
		[]string{"route", "method", "status"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nexus_gateway_http_request_duration_seconds",
			Help:    "End-to-end HTTP request duration in seconds, by route and method.",
			Buckets: LLMBuckets,
		},
		[]string{"route", "method"},
	)

	HTTPInflight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "nexus_gateway_http_inflight_requests",
			Help: "Number of HTTP requests currently being handled.",
		},
	)
)

// Generation metrics, recorded by the generation service. GenerationsTotal is
// labeled by stream so the duration histogram can be sliced correctly, since
// streaming and non-streaming requests have radically different distributions.
var (
	GenerationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_generations_total",
			Help: "Total terminal generation outcomes, by endpoint, model, provider, stream and outcome.",
		},
		[]string{"endpoint", "model", "provider", "stream", "outcome"},
	)

	GenerationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nexus_gateway_generation_duration_seconds",
			Help:    "Total generation duration in seconds, by endpoint, model, provider and stream.",
			Buckets: LLMBuckets,
		},
		[]string{"endpoint", "model", "provider", "stream"},
	)

	UpstreamLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nexus_gateway_upstream_latency_seconds",
			Help:    "Upstream provider call latency in seconds, by provider, model and outcome. Streaming latency is time to stream start.",
			Buckets: LLMBuckets,
		},
		[]string{"provider", "model", "outcome"},
	)

	TokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_tokens_total",
			Help: "Total tokens consumed, by direction (input|output), model and provider.",
		},
		[]string{"direction", "model", "provider"},
	)

	CacheTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_cache_tokens_total",
			Help: "Total cache tokens, by direction (read|write), model and provider.",
		},
		[]string{"direction", "model", "provider"},
	)
)

// Downstream dependency metrics.
var (
	MeteringRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_metering_requests_total",
			Help: "Total calls to the internal metering service, by operation and status.",
		},
		[]string{"operation", "status"},
	)

	MeteringDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nexus_gateway_metering_duration_seconds",
			Help:    "Metering service call duration in seconds, by operation.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"operation"},
	)

	RouterRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_router_requests_total",
			Help: "Total calls to the router service, by status.",
		},
		[]string{"status"},
	)

	RouterDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "nexus_gateway_router_duration_seconds",
			Help:    "Router service call duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)

	PIIAnalyzeCalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_pii_analyze_calls_total",
			Help: "Total PII analyzer (Presidio) calls, by mode and status.",
		},
		[]string{"mode", "status"},
	)

	PIIAnalyzeDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nexus_gateway_pii_analyze_duration_seconds",
			Help:    "PII analyzer call duration in seconds, by mode.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"mode"},
	)

	PIIDetections = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_pii_detections_total",
			Help: "Total PII entities detected, by entity type.",
		},
		[]string{"entity_type"},
	)

	// Attestation metrics describe the attestation operation itself, not the
	// enclosing HTTP request: they distinguish rate limiting from attester
	// failure even though both return HTTP 503. Invalid requests (bad nonce,
	// malformed body) are intentionally excluded.
	AttestationOperationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_attestation_operations_total",
			Help: "Total Nitro attestation operations, by outcome.",
		},
		[]string{"outcome"},
	)

	AttestationDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "nexus_gateway_attestation_duration_seconds",
			Help:    "Nitro attestation duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)

	ProviderRetries = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nexus_gateway_provider_retries_total",
			Help: "Total upstream provider retries, by provider and reason.",
		},
		[]string{"provider", "reason"},
	)
)

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		HTTPInflight,
		GenerationsTotal,
		GenerationDuration,
		UpstreamLatency,
		TokensTotal,
		CacheTokensTotal,
		MeteringRequests,
		MeteringDuration,
		RouterRequests,
		RouterDuration,
		PIIAnalyzeCalls,
		PIIAnalyzeDuration,
		PIIDetections,
		AttestationOperationsTotal,
		AttestationDuration,
		ProviderRetries,
	)
}

// Handler returns the Prometheus metrics HTTP handler.
func Handler() http.Handler {
	return promhttp.Handler()
}

// RecordUsage records token usage for a model and provider. Prompt tokens are
// recorded as input, completion tokens as output, and cache tokens are recorded
// separately to avoid double counting. A nil usage is a no-op.
func RecordUsage(u *domain.Usage, model, provider string) {
	if u == nil {
		return
	}
	if v := u.PromptTokens; v > 0 {
		TokensTotal.WithLabelValues("input", model, provider).Add(float64(v))
	}
	if v := u.CompletionTokens; v > 0 {
		TokensTotal.WithLabelValues("output", model, provider).Add(float64(v))
	}
	if v := u.CacheReadTokens; v > 0 {
		CacheTokensTotal.WithLabelValues("read", model, provider).Add(float64(v))
	}
	if v := u.CacheCreationTokens; v > 0 {
		CacheTokensTotal.WithLabelValues("write", model, provider).Add(float64(v))
	}
}
