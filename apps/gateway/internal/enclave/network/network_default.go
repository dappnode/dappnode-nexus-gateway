//go:build !nitro_enclave

package network

import (
	"fmt"
	"net"
	"net/http"
)

func ListenGateway(port uint32) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf(":%d", port))
}

func ConfigureProviderTransport(*http.Transport) {}

func ConfigureMeteringTransport(*http.Transport) {}

func ConfigureRouterTransport(*http.Transport) {}

// StartRouterRelays only exists inside the enclave, where the Router has no
// network of its own.
func StartRouterRelays() ([]net.Listener, error) {
	return nil, fmt.Errorf("router relays only exist inside the enclave")
}

func ConfigureDefaultTransport() error { return nil }

func StartTinfoilTLSBridge() (net.Listener, error) { return nil, nil }

// ListenMetrics is only used inside the enclave; elsewhere metrics listen on
// METRICS_PORT.
func ListenMetrics() (net.Listener, error) {
	return nil, fmt.Errorf("metrics over vsock only exist inside the enclave")
}

// ConfigureLoopback is a no-op outside the enclave, where the OS already
// brings up loopback.
func ConfigureLoopback() error { return nil }
