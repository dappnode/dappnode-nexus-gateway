// Package presidio adapts Microsoft Presidio's analyzer HTTP API to the
// ports.PIIFilter interface. Only the analyzer is used — masking is performed
// in-process by domain.ApplyMask so that PII values never leave the gateway.
package presidio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Config controls adapter behavior.
type Config struct {
	// BaseURL is the analyzer root, e.g. "http://presidio-analyzer:3000".
	BaseURL string
	// DefaultLanguage is used when callers pass an empty language code.
	DefaultLanguage string
	// ScoreThreshold filters out detections below this confidence (0..1).
	ScoreThreshold float64
	// Timeout caps each Analyze call.
	Timeout time.Duration
	// Logger receives debug / warn events. Required.
	Logger ports.Logger
}

// Adapter implements ports.PIIFilter against Presidio's `/analyze` endpoint.
type Adapter struct {
	cfg    Config
	client *http.Client
}

// NewAdapter constructs an Adapter. Sensible defaults are filled in for any
// zero-valued config fields.
func NewAdapter(cfg Config) *Adapter {
	if cfg.DefaultLanguage == "" {
		cfg.DefaultLanguage = "en"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 1500 * time.Millisecond
	}
	if cfg.ScoreThreshold < 0 {
		cfg.ScoreThreshold = 0
	}
	return &Adapter{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// Enabled always returns true for the real adapter. The Noop adapter (see
// noop.go) is used to bypass detection.
func (a *Adapter) Enabled() bool { return true }

// analyzeRequest mirrors Presidio's POST /analyze body.
type analyzeRequest struct {
	Text           string   `json:"text"`
	Language       string   `json:"language"`
	ScoreThreshold float64  `json:"score_threshold,omitempty"`
	Entities       []string `json:"entities,omitempty"`
}

// analyzeResponseItem mirrors a single result from POST /analyze. Offsets are
// character offsets (Python `len()` on a unicode string).
type analyzeResponseItem struct {
	EntityType string  `json:"entity_type"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Score      float64 `json:"score"`
}

// Analyze sends `text` to Presidio and converts character offsets to byte
// offsets before returning.
func (a *Adapter) Analyze(ctx context.Context, text string, opts ports.PIIAnalyzeOptions) ([]domain.PIIEntity, error) {
	if text == "" {
		return nil, nil
	}
	language := opts.Language
	if language == "" {
		language = a.cfg.DefaultLanguage
	}

	body, err := json.Marshal(analyzeRequest{
		Text:           text,
		Language:       language,
		ScoreThreshold: a.cfg.ScoreThreshold,
		Entities:       presidioEntitiesForMode(opts.Mode),
	})
	if err != nil {
		return nil, fmt.Errorf("presidio: marshal request: %w", err)
	}

	url := a.cfg.BaseURL + "/analyze"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("presidio: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	mode := opts.Mode
	if mode == "" {
		mode = domain.APIKeyPIIModeHigh
	}
	start := time.Now()
	resp, err := a.client.Do(req)
	if err != nil {
		metrics.PIIAnalyzeCalls.WithLabelValues(mode, "transport_error").Inc()
		metrics.PIIAnalyzeDuration.WithLabelValues(mode).Observe(time.Since(start).Seconds())
		return nil, fmt.Errorf("presidio: call analyzer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		metrics.PIIAnalyzeCalls.WithLabelValues(mode, "http_error").Inc()
		metrics.PIIAnalyzeDuration.WithLabelValues(mode).Observe(time.Since(start).Seconds())
		// Drain a small prefix for diagnostics without leaking large bodies.
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("presidio: analyzer http %d: %s", resp.StatusCode, bytes.TrimSpace(preview))
	}

	var items []analyzeResponseItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		metrics.PIIAnalyzeCalls.WithLabelValues(mode, "decode_error").Inc()
		metrics.PIIAnalyzeDuration.WithLabelValues(mode).Observe(time.Since(start).Seconds())
		return nil, fmt.Errorf("presidio: decode response: %w", err)
	}

	metrics.PIIAnalyzeCalls.WithLabelValues(mode, "success").Inc()
	metrics.PIIAnalyzeDuration.WithLabelValues(mode).Observe(time.Since(start).Seconds())

	entities := convertEntities(text, items)
	for _, e := range entities {
		metrics.PIIDetections.WithLabelValues(e.Type).Inc()
	}
	return entities, nil
}

func presidioEntitiesForMode(mode string) []string {
	switch mode {
	case domain.APIKeyPIIModeLow:
		return cloneEntities(lowProfileEntities)
	case domain.APIKeyPIIModeBalanced:
		entities := cloneEntities(lowProfileEntities)
		entities = append(entities, "PERSON")
		return entities
	case domain.APIKeyPIIModeHigh, "":
		return nil
	default:
		return cloneEntities(lowProfileEntities)
	}
}

func cloneEntities(entities []string) []string {
	return append([]string(nil), entities...)
}

// lowProfileEntities is the Presidio-specific allowlist behind Nexus' low PII
// masking level. It is intentionally explicit: if Presidio adds a new broad
// semantic recognizer later, low mode should not start masking it by accident.
// Keep this profile limited to stable identifiers whose exact value
// is usually less important to prompt meaning than names, places, and dates.
// Every entry must also be supported by the pinned official Presidio analyzer
// image for English; unsupported names make Presidio log a warning per request.
var lowProfileEntities = flattenEntityGroups(
	contactAndNetworkEntities,
	paymentAndAccountEntities,
	genericCredentialEntities,
	usIdentifierEntities,
	ukIdentifierEntities,
)

var contactAndNetworkEntities = []string{
	"EMAIL_ADDRESS",
	"IP_ADDRESS",
	"MAC_ADDRESS",
	"PHONE_NUMBER",
}

var paymentAndAccountEntities = []string{
	"CREDIT_CARD",
	"CRYPTO",
	"IBAN_CODE",
}

var genericCredentialEntities = []string{
	"MEDICAL_LICENSE",
}

var usIdentifierEntities = []string{
	"US_BANK_NUMBER",
	"US_DRIVER_LICENSE",
	"US_ITIN",
	"US_PASSPORT",
	"US_SSN",
}

var ukIdentifierEntities = []string{
	"UK_NHS",
}

func flattenEntityGroups(groups ...[]string) []string {
	seen := make(map[string]struct{})
	entities := make([]string, 0)
	for _, group := range groups {
		for _, entity := range group {
			if _, ok := seen[entity]; ok {
				continue
			}
			seen[entity] = struct{}{}
			entities = append(entities, entity)
		}
	}
	sort.Strings(entities)
	return entities
}

// convertEntities turns Presidio's character offsets into byte offsets and
// drops any spans that fall outside `text`.
func convertEntities(text string, items []analyzeResponseItem) []domain.PIIEntity {
	if len(items) == 0 {
		return nil
	}

	// Build a runeIndex -> byteIndex lookup table. The final entry maps the
	// past-the-end rune index to len(text), so we can convert exclusive ends.
	runes := make([]int, 0, len(text)+1)
	for i := range text { // i is the byte index at each rune start
		runes = append(runes, i)
	}
	runes = append(runes, len(text))

	// totalRunes lets us bounds-check character offsets cheaply.
	totalRunes := utf8.RuneCountInString(text)

	out := make([]domain.PIIEntity, 0, len(items))
	for _, it := range items {
		if it.Start < 0 || it.End < it.Start || it.End > totalRunes {
			continue
		}
		out = append(out, domain.PIIEntity{
			Type:  it.EntityType,
			Start: runes[it.Start],
			End:   runes[it.End],
			Score: float32(it.Score),
		})
	}

	// Sort ascending by start to give callers a stable order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}
