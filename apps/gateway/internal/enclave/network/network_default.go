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

func ConfigureDefaultTransport() error { return nil }

func StartTinfoilTLSBridge() (net.Listener, error) { return nil, nil }
