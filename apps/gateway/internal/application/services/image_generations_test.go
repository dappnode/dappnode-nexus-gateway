package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/shopspring/decimal"
)

const twoImages = `{"created":1,"data":[{"b64_json":"aGVsbG8="},{"b64_json":"d29ybGQ="}],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"cost":0.03}}`

// imageProvider is an OpenAI-compatible provider that also generates images.
type imageProvider struct {
	stubProvider
	mu        sync.Mutex
	body      string
	imageErr  error
	imageRuns int
	lastImage string
	block     chan struct{} // When set, a generation waits for it.
}

func (p *imageProvider) GenerateImages(_ context.Context, raw []byte, _ domain.PublicModel) ([]byte, error) {
	p.mu.Lock()
	p.imageRuns++
	p.lastImage = string(raw)
	block := p.block
	p.mu.Unlock()
	if block != nil {
		<-block
	}
	if p.imageErr != nil {
		return nil, p.imageErr
	}
	return []byte(p.body), nil
}

// imageMeter records what each image generation was charged for.
type imageMeter struct {
	stubUsageMeter
	mu      sync.Mutex
	charged []domain.Usage
}

func (m *imageMeter) RecordSuccess(ctx context.Context, reservationID string, auth domain.AuthContext, endpoint string, req domain.GenerateRequest, result domain.GenerateResult, model domain.PublicModel, latencyMs int64) error {
	m.mu.Lock()
	if result.Usage != nil {
		m.charged = append(m.charged, *result.Usage)
	}
	m.mu.Unlock()
	return m.stubUsageMeter.RecordSuccess(ctx, reservationID, auth, endpoint, req, result, model, latencyMs)
}

func imageModel() domain.PublicModel {
	price := decimal.RequireFromString("0.02")
	return domain.PublicModel{
		PublicModelID:            "flux",
		ProviderModelID:          "phala-flux",
		UpstreamModelName:        "black-forest-labs/flux",
		ProviderConfig:           domain.ProviderConfig{ProviderName: "phala"},
		SupportsImageGenerations: true,
		PricePerImage:            &price,
	}
}

func newImageService(meter ports.UsageMeter, provider ports.Provider, guest bool) *GenerateService {
	return NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{
			Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive, IsGuest: guest},
			APIKey:  domain.APIKey{ID: "key1", Active: true},
		}},
		&stubModelCatalog{model: imageModel()},
		nil,
		&stubProviderRegistry{providers: map[string]ports.Provider{"phala": provider}},
		meter,
		nil,
		stubLogger{},
	)
}

func imageRequest(n int) domain.GenerateRequest {
	return domain.GenerateRequest{PublicModelID: "flux", RequestedModelID: "flux", Images: &n}
}

const imageBody = `{"model":"flux","prompt":"a fox","n":2}`

func TestGenerateImagesChargesReturnedImages(t *testing.T) {
	meter := &imageMeter{}
	provider := &imageProvider{body: twoImages}
	svc := newImageService(meter, provider, false)

	call, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk-test", "")
	if err != nil {
		t.Fatalf("GenerateImages: %v", err)
	}
	if meter.reserveCalls != 1 || *meter.lastReserveReq.Images != 2 {
		t.Fatalf("reserve calls = %d with images %v, want one hold for 2", meter.reserveCalls, meter.lastReserveReq.Images)
	}
	if len(meter.charged) != 1 || meter.charged[0].Images != 2 {
		t.Fatalf("charged %+v, want 2 images once", meter.charged)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(call.Body, &out); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if _, ok := out["usage"]; ok {
		t.Fatal("the provider's usage and cost must not reach the client")
	}
	if !strings.Contains(string(call.Body), "aGVsbG8=") {
		t.Fatal("images missing from the response")
	}
	if !strings.Contains(provider.lastImage, `"prompt":"a fox"`) {
		t.Fatalf("provider got %s", provider.lastImage)
	}
}

func TestGenerateImagesRefusedPromptCostsNothing(t *testing.T) {
	meter := &imageMeter{}
	refusal := domain.ErrProviderError(400, "provider rejected request").WithMeta(
		"upstream_status", 400, "upstream_code", "moderation_blocked")
	svc := newImageService(meter, &imageProvider{imageErr: refusal}, false)

	_, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(1), "sk-test", "")
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.HTTPStatus != 422 || gwErr.Code != domain.ErrCodeContentPolicy {
		t.Fatalf("err = %v, want a 422 content_policy_violation", err)
	}
	if len(meter.charged) != 0 || meter.failureCalls != 1 {
		t.Fatalf("charged %+v with %d failures, want nothing charged and the hold released", meter.charged, meter.failureCalls)
	}
}

func TestGenerateImagesRejectsGuests(t *testing.T) {
	meter := &imageMeter{}
	provider := &imageProvider{body: twoImages}
	svc := newImageService(meter, provider, true)

	_, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(1), "sk-guest", "")
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.HTTPStatus != 403 {
		t.Fatalf("err = %v, want 403 for a guest", err)
	}
	if meter.reserveCalls != 0 || provider.imageRuns != 0 {
		t.Fatal("a guest's image request must not reach metering or the provider")
	}
}

func TestGenerateImagesRejectsChatModel(t *testing.T) {
	meter := &imageMeter{}
	svc := newDirectModelGenerateService(&meter.stubUsageMeter, &stubProvider{})
	_, err := svc.GenerateImages(context.Background(), []byte(`{"model":"model","prompt":"x"}`), domain.GenerateRequest{PublicModelID: "model"}, "sk", "")
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeUnsupportedEndpoint {
		t.Fatalf("err = %v, want unsupported_endpoint for a chat model", err)
	}
}

func TestGenerateImagesIdempotencyKeyReplaysWithoutCharging(t *testing.T) {
	meter := &imageMeter{}
	provider := &imageProvider{body: twoImages}
	svc := newImageService(meter, provider, false)

	first, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || string(second.Body) != string(first.Body) {
		t.Fatal("a retry with the same key must replay the first result")
	}
	if provider.imageRuns != 1 || len(meter.charged) != 1 {
		t.Fatalf("provider ran %d times and charged %d times, want once each", provider.imageRuns, len(meter.charged))
	}

	_, err = svc.GenerateImages(context.Background(), []byte(`{"model":"flux","prompt":"a cat"}`), imageRequest(1), "sk", "key-1")
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeIdempotencyKeyReused {
		t.Fatalf("err = %v, want idempotency_key_reused for a different body", err)
	}
}

func TestGenerateImagesIdempotencyKeyInProgress(t *testing.T) {
	meter := &imageMeter{}
	provider := &imageProvider{body: twoImages, block: make(chan struct{})}
	svc := newImageService(meter, provider, false)

	done := make(chan error, 1)
	go func() {
		_, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk", "key-2")
		done <- err
	}()
	waitFor(t, func() bool { provider.mu.Lock(); defer provider.mu.Unlock(); return provider.imageRuns == 1 })

	_, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk", "key-2")
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeIdempotencyInProgress || gwErr.HTTPStatus != 409 {
		t.Fatalf("err = %v, want 409 idempotency_request_in_progress", err)
	}
	close(provider.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGenerateImagesFailedKeyCanBeRetried(t *testing.T) {
	meter := &imageMeter{}
	provider := &imageProvider{imageErr: domain.ErrProviderUnavailable("phala")}
	svc := newImageService(meter, provider, false)

	if _, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk", "key-3"); err == nil {
		t.Fatal("want the provider's error")
	}
	provider.imageErr = nil
	provider.body = twoImages
	if _, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk", "key-3"); err != nil {
		t.Fatalf("a key whose request failed (and cost nothing) must be retryable: %v", err)
	}
	if len(meter.charged) != 1 {
		t.Fatalf("charged %d times, want once", len(meter.charged))
	}
}

func TestGenerateImagesLimitsConcurrentJobsPerAccount(t *testing.T) {
	meter := &imageMeter{}
	provider := &imageProvider{body: twoImages, block: make(chan struct{})}
	svc := newImageService(meter, provider, false)
	svc.SetImageOptions(0, 1)

	done := make(chan error, 1)
	go func() {
		_, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk", "")
		done <- err
	}()
	waitFor(t, func() bool { provider.mu.Lock(); defer provider.mu.Unlock(); return provider.imageRuns == 1 })

	_, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk", "")
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.HTTPStatus != 429 || gwErr.Code != domain.ErrCodeTooManyConcurrent {
		t.Fatalf("err = %v, want 429 too_many_concurrent_requests", err)
	}
	if meter.reserveCalls != 1 {
		t.Fatalf("reserve calls = %d, want the refused request never to hold credit", meter.reserveCalls)
	}
	close(provider.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGenerateImagesTimesOut(t *testing.T) {
	meter := &imageMeter{}
	provider := &imageProvider{body: twoImages, block: make(chan struct{})}
	defer close(provider.block)
	svc := newImageService(meter, &timeoutAwareImageProvider{provider}, false)
	svc.SetImageOptions(50*time.Millisecond, 0)

	_, err := svc.GenerateImages(context.Background(), []byte(imageBody), imageRequest(2), "sk", "")
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeProviderTimeout {
		t.Fatalf("err = %v, want provider_timeout", err)
	}
	if len(meter.charged) != 0 || meter.failureCalls != 1 {
		t.Fatal("a timed-out generation must release its hold uncharged")
	}
}

// timeoutAwareImageProvider returns when the context ends, like a real HTTP
// call would.
type timeoutAwareImageProvider struct{ *imageProvider }

func (p *timeoutAwareImageProvider) GenerateImages(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestReadImageResponseRejectsURLsAndExtras(t *testing.T) {
	one := 1
	for name, body := range map[string]string{
		"url only":   `{"data":[{"url":"https://cdn.example/x.png"}]}`,
		"no images":  `{"data":[]}`,
		"not json":   `nope`,
		"too many":   `{"data":[{"b64_json":"a"},{"b64_json":"b"}]}`,
		"empty item": `{"data":[{"b64_json":""}]}`,
	} {
		if _, _, err := readImageResponse([]byte(body), &one); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestImageProviderErrorKeepsOtherRejections(t *testing.T) {
	plain := domain.ErrProviderError(400, "bad size").WithMeta("upstream_status", 400, "upstream_code", "invalid_parameter")
	var gwErr *domain.GatewayError
	if !errors.As(imageProviderError(plain), &gwErr) || gwErr.Code == domain.ErrCodeContentPolicy {
		t.Fatal("an ordinary 400 is not a content-policy refusal")
	}
	server := domain.ErrProviderError(500, "safety system down").WithMeta("upstream_status", 500, "upstream_error", "safety system down")
	if errors.As(imageProviderError(server), &gwErr) && gwErr.Code == domain.ErrCodeContentPolicy {
		t.Fatal("a provider outage is not a content-policy refusal")
	}
}

func TestIdempotencyStoreForgetsReplayAfterTTL(t *testing.T) {
	now := time.Unix(0, 0)
	store := newIdempotencyStore()
	store.now = func() time.Time { return now }
	entry, _, err := store.begin("acc", "k", []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	store.finish(entry, []byte("result"))
	if _, replay, _ := store.begin("acc", "k", []byte("body")); string(replay) != "result" {
		t.Fatal("want the stored result")
	}
	if _, _, err := store.begin("other", "k", []byte("body")); err != nil {
		t.Fatalf("keys are per account: %v", err)
	}
	now = now.Add(idempotencyReplayTTL + time.Second)
	_, replay, err := store.begin("acc", "k", []byte("body"))
	var gwErr *domain.GatewayError
	if replay != nil || !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeIdempotencyKeyReused {
		t.Fatalf("after the replay TTL: replay=%q err=%v, want a refusal, never a second run", replay, err)
	}
	now = now.Add(idempotencyKeyTTL)
	if _, _, err := store.begin("acc", "k", []byte("body")); err != nil {
		t.Fatalf("an expired key is free again: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Another client library serializes the same request differently; it is still
// the same request.
func TestIdempotencyKeyMatchesTheSameRequestFormattedDifferently(t *testing.T) {
	store := newIdempotencyStore()
	entry, _, err := store.begin("acc", "k", []byte(`{"model":"m","prompt":"a fox","n":2}`))
	if err != nil {
		t.Fatal(err)
	}
	store.finish(entry, []byte("result"))
	if _, replay, err := store.begin("acc", "k", []byte("{\n  \"n\": 2, \"prompt\": \"a fox\",\n  \"model\": \"m\"\n}")); err != nil || string(replay) != "result" {
		t.Fatalf("replay=%q err=%v, want the stored result", replay, err)
	}
	if _, _, err := store.begin("acc", "k", []byte(`{"model":"m","prompt":"a fox","n":3}`)); err == nil {
		t.Fatal("a different n is a different request")
	}
}
