package presidio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/observability/logger"
)

func newTestAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	zap, err := logger.NewZapLogger("error")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return NewAdapter(Config{
		BaseURL:        srv.URL,
		ScoreThreshold: 0.4,
		Timeout:        2 * time.Second,
		Logger:         zap,
	})
}

func TestAdapter_Analyze_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/analyze" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body analyzeRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Language != "en" || body.ScoreThreshold != 0.4 {
			t.Errorf("unexpected body: %+v", body)
		}
		_ = json.NewEncoder(w).Encode([]analyzeResponseItem{
			{EntityType: "PERSON", Start: 11, End: 21, Score: 0.99},        // "John Smith"
			{EntityType: "EMAIL_ADDRESS", Start: 32, End: 48, Score: 0.99}, // "john@example.com"
		})
	}))
	defer srv.Close()

	a := newTestAdapter(t, srv)
	text := "My name is John Smith and email john@example.com"
	got, err := a.Analyze(context.Background(), text, ports.PIIAnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("entities = %d, want 2", len(got))
	}
	if text[got[0].Start:got[0].End] != "John Smith" {
		t.Errorf("entity 0 = %q, want John Smith", text[got[0].Start:got[0].End])
	}
	if text[got[1].Start:got[1].End] != "john@example.com" {
		t.Errorf("entity 1 = %q, want john@example.com", text[got[1].Start:got[1].End])
	}
}

func TestAdapter_Analyze_HandlesNonASCIIOffsets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Presidio returns character offsets. For "héllo John" (10 runes,
		// 11 bytes), Start=6 End=10 selects "John" by rune index.
		_ = json.NewEncoder(w).Encode([]analyzeResponseItem{
			{EntityType: "PERSON", Start: 6, End: 10, Score: 0.9},
		})
	}))
	defer srv.Close()

	a := newTestAdapter(t, srv)
	text := "héllo John"
	got, err := a.Analyze(context.Background(), text, ports.PIIAnalyzeOptions{Language: "en"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("entities = %d", len(got))
	}
	if v := text[got[0].Start:got[0].End]; v != "John" {
		t.Fatalf("byte-converted span = %q, want John", v)
	}
}

func TestAdapter_Analyze_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := newTestAdapter(t, srv)
	if _, err := a.Analyze(context.Background(), "x", ports.PIIAnalyzeOptions{Language: "en"}); err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestAdapter_Analyze_EmptyTextShortCircuits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be called for empty text")
		w.WriteHeader(500)
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	got, err := a.Analyze(context.Background(), "", ports.PIIAnalyzeOptions{Language: "en"})
	if err != nil || got != nil {
		t.Fatalf("Analyze(\"\") = %v, %v", got, err)
	}
}

func TestAdapter_Analyze_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	zap, _ := logger.NewZapLogger("error")
	a := NewAdapter(Config{BaseURL: srv.URL, Timeout: 20 * time.Millisecond, Logger: zap})
	if _, err := a.Analyze(context.Background(), "hello", ports.PIIAnalyzeOptions{Language: "en"}); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestAdapter_Analyze_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	if _, err := a.Analyze(context.Background(), "hi", ports.PIIAnalyzeOptions{Language: "en"}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestAdapter_Analyze_SendsEntitiesForPrivacyMode(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		want       []string
		wantAbsent []string
		wantNil    bool
	}{
		{
			name:       "low",
			mode:       domain.APIKeyPIIModeLow,
			want:       []string{"EMAIL_ADDRESS", "PHONE_NUMBER", "CREDIT_CARD", "US_SSN"},
			wantAbsent: []string{"PERSON", "LOCATION", "DATE_TIME", "URL"},
		},
		{
			name:       "balanced",
			mode:       domain.APIKeyPIIModeBalanced,
			want:       []string{"EMAIL_ADDRESS", "PHONE_NUMBER", "PERSON"},
			wantAbsent: []string{"LOCATION", "DATE_TIME", "URL"},
		},
		{
			name:    "high",
			mode:    domain.APIKeyPIIModeHigh,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotEntities []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body analyzeRequest
				_ = json.NewDecoder(r.Body).Decode(&body)
				gotEntities = body.Entities
				_ = json.NewEncoder(w).Encode([]analyzeResponseItem{})
			}))
			defer srv.Close()

			a := newTestAdapter(t, srv)
			if _, err := a.Analyze(context.Background(), "hello", ports.PIIAnalyzeOptions{Mode: tt.mode}); err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			if tt.wantNil {
				if gotEntities != nil {
					t.Fatalf("entities = %#v, want nil/omitted for high", gotEntities)
				}
				return
			}
			for _, want := range tt.want {
				if !contains(gotEntities, want) {
					t.Fatalf("entities missing %s: %#v", want, gotEntities)
				}
			}
			for _, absent := range tt.wantAbsent {
				if contains(gotEntities, absent) {
					t.Fatalf("entities unexpectedly include %s: %#v", absent, gotEntities)
				}
			}
		})
	}
}

func TestLowProfileEntities_AreStableIdentifiersOnly(t *testing.T) {
	if len(lowProfileEntities) == 0 {
		t.Fatal("low profile must include stable identifier entities")
	}

	for i, entity := range lowProfileEntities {
		if i > 0 && lowProfileEntities[i-1] >= entity {
			t.Fatalf("low profile must be sorted and unique, got %q before %q", lowProfileEntities[i-1], entity)
		}
	}

	semanticEntities := []string{"PERSON", "LOCATION", "DATE_TIME", "NRP", "ADDRESS", "AGE", "URL"}
	for _, entity := range semanticEntities {
		if contains(lowProfileEntities, entity) {
			t.Fatalf("low profile must not include semantic entity %q", entity)
		}
	}
}

func TestNoopFilter(t *testing.T) {
	n := NewNoopFilter()
	if n.Enabled() {
		t.Fatal("noop must report Enabled()=false")
	}
	got, err := n.Analyze(context.Background(), "anything", ports.PIIAnalyzeOptions{Language: "en"})
	if err != nil || got != nil {
		t.Fatalf("noop.Analyze = %v, %v", got, err)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
