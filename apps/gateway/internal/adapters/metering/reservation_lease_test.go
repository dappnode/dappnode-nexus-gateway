package metering

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestReservationRenewsBeyondOriginalExpiryAndStops(t *testing.T) {
	var calls atomic.Int32
	var lost atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(reserveResponse{ReservationID: "lease", LeaseMS: 300})
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", time.Second)
	client.reservations.Store("lease", newLease(time.Now(), 300))
	stop := client.KeepReservation(context.Background(), "lease", func() { lost.Store(true) })
	time.Sleep(600 * time.Millisecond)
	stop()
	if lost.Load() || calls.Load() < 3 {
		t.Fatalf("renewal did not preserve hold: lost=%v calls=%d", lost.Load(), calls.Load())
	}
	before := calls.Load()
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("renewal continued after stop")
	}
}

func TestLostReservationStopsOnlyWhenLeaseExpires(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	client := NewClient(server.URL, "fixture", time.Second)
	client.reservations.Store("lease", newLease(time.Now(), 120))
	lost := make(chan struct{})
	stop := client.KeepReservation(context.Background(), "lease", func() { close(lost) })
	defer stop()
	select {
	case <-lost:
		t.Fatal("temporary error canceled a valid lease")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-lost:
	case <-time.After(time.Second):
		t.Fatal("lost hold did not stop inference")
	}
}

// Metering refusing a renewal means the hold is gone; retrying cannot help.
func TestRejectedRenewalIsLostImmediately(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "conflict", "message": "reservation is no longer active"}})
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", time.Second)
	client.reservations.Store("lease", newLease(time.Now(), 3000))
	lost := make(chan struct{})
	stop := client.KeepReservation(context.Background(), "lease", func() { close(lost) })
	defer stop()
	select {
	case <-lost:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("rejected renewal waited for the lease to run out")
	}
	if calls.Load() != 1 {
		t.Fatalf("retried a rejected renewal: %d calls", calls.Load())
	}
}

// During a Metering outage each request renews a bounded number of times
// instead of an accelerating burst as the lease runs out.
func TestRenewalRetriesAreSpacedDuringOutage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", time.Second)
	client.reservations.Store("lease", newLease(time.Now(), 1000))
	lost := make(chan struct{})
	stop := client.KeepReservation(context.Background(), "lease", func() { close(lost) })
	defer stop()
	select {
	case <-lost:
	case <-time.After(3 * time.Second):
		t.Fatal("lease never expired")
	}
	// One tenth of a 1 s lease is 100 ms, so at most about ten attempts.
	if got := calls.Load(); got > 11 {
		t.Fatalf("renewal attempts were not spaced: %d", got)
	}
}

// The lease is measured on the Gateway's clock from before the request, so a
// skewed Metering or enclave clock cannot shorten or extend it.
func TestReserveStoresLeaseRelativeToGatewayClock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(reserveResponse{ReservationID: "lease", LeaseMS: 60_000})
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", time.Second)
	before := time.Now()
	id, err := client.Reserve(context.Background(), domain.AuthContext{Account: domain.Account{ID: "a"}, APIKey: domain.APIKey{ID: "k"}}, "chat/completions", domain.GenerateRequest{}, domain.PublicModel{PublicModelID: "m"}, "request")
	if err != nil || id != "lease" {
		t.Fatalf("reserve = %q, %v", id, err)
	}
	value, ok := client.reservations.Load("lease")
	if !ok {
		t.Fatal("lease not stored")
	}
	held := value.(lease)
	if held.length != time.Minute || held.expires.Before(before.Add(time.Minute)) || held.expires.After(time.Now().Add(time.Minute)) {
		t.Fatalf("lease = %+v", held)
	}
}

func TestRenewalMetricsDoNotIncludeReservationIDs(t *testing.T) {
	if got := meteringOperation(http.MethodPost, "/internal/metering/reservations/private-id/renew"); got != "renew" {
		t.Fatalf("renewal metric operation = %q", got)
	}
}
