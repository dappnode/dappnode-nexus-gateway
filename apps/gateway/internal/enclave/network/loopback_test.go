package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckLoopbackAddress(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:3000", "127.0.0.2:8083"} {
		if err := CheckLoopbackAddress(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"localhost:3000",         // names never resolve inside the enclave
		"presidio-analyzer:3000", // the regular stack's analyzer
		"10.200.200.31:3000",     // another host
		"[::1]:3000",             // IPv6 loopback is not configured
		"127.0.0.1",              // no port
		"api.deepseek.com:443",   // a provider
	} {
		if err := CheckLoopbackAddress(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestLoopbackTransportReachesLoopbackServices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := &http.Client{Transport: LoopbackTransport()}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("loopback service unreachable: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestLoopbackTransportRefusesOtherHosts(t *testing.T) {
	dial := LoopbackTransport().DialContext
	if _, err := dial(context.Background(), "tcp", "93.184.216.34:443"); err == nil {
		t.Fatal("dialed a public address")
	}
}
