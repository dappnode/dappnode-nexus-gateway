// Package fx provides cached exchange-rate clients.
package fx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const frankfurterURL = "https://api.frankfurter.app/latest"

// CacheTTL bounds how stale a cached rate may be. ECB publishes once per
// business day, so an hour is comfortably fresh while sparing the API.
const CacheTTL = time.Hour

// Frankfurter fetches and caches exchange rates from the Frankfurter API.
type Frankfurter struct {
	httpClient *http.Client

	mu     sync.Mutex
	cached map[rateKey]cachedRate
}

type rateKey struct {
	from string
	to   string
}

type cachedRate struct {
	rate      float64
	fetchedAt time.Time
}

// NewFrankfurter builds an FX rate provider with the given request timeout.
func NewFrankfurter(timeout time.Duration) *Frankfurter {
	return &Frankfurter{
		httpClient: &http.Client{Timeout: timeout},
		cached:     make(map[rateKey]cachedRate),
	}
}

// USDToEUR returns how many euros one US dollar is worth.
func (f *Frankfurter) USDToEUR(ctx context.Context) (float64, error) {
	return f.Rate(ctx, "USD", "EUR")
}

// EURToUSD returns how many US dollars one euro is worth.
func (f *Frankfurter) EURToUSD(ctx context.Context) (float64, error) {
	return f.Rate(ctx, "EUR", "USD")
}

// Rate returns the exchange rate from one currency to another.
func (f *Frankfurter) Rate(ctx context.Context, from, to string) (float64, error) {
	key := rateKey{from: from, to: to}

	f.mu.Lock()
	defer f.mu.Unlock()

	if cached, ok := f.cached[key]; ok && cached.rate > 0 && time.Since(cached.fetchedAt) < CacheTTL {
		return cached.rate, nil
	}

	rate, err := f.fetch(ctx, from, to)
	if err != nil {
		// Fall back to a stale rate rather than failing callers that can
		// tolerate a slightly old conversion.
		if cached, ok := f.cached[key]; ok && cached.rate > 0 {
			return cached.rate, nil
		}
		return 0, err
	}

	f.cached[key] = cachedRate{rate: rate, fetchedAt: time.Now()}
	return rate, nil
}

func (f *Frankfurter) fetch(ctx context.Context, from, to string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, frankfurterURL, nil)
	if err != nil {
		return 0, fmt.Errorf("fx: build request: %w", err)
	}
	q := req.URL.Query()
	q.Set("from", from)
	q.Set("to", to)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fx: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return 0, fmt.Errorf("fx: read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("fx: unexpected status %d", resp.StatusCode)
	}

	var parsed struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("fx: decode response: %w", err)
	}
	rate := parsed.Rates[to]
	if rate <= 0 {
		return 0, fmt.Errorf("fx: missing or invalid %s rate", to)
	}
	return rate, nil
}
