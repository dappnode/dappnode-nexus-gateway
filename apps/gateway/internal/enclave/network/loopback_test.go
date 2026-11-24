package network

import (
	"context"
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

func TestLoopbackTransportRefusesOtherHosts(t *testing.T) {
	dial := LoopbackTransport().DialContext
	if _, err := dial(context.Background(), "tcp", "93.184.216.34:443"); err == nil {
		t.Fatal("dialed a public address")
	}
}
