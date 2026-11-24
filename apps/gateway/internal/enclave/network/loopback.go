package network

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// LoopbackTransport returns an HTTP transport that only connects to IP
// literals in 127.0.0.0/8. Services that run inside the same enclave (the PII
// analyzer, the router) are reached through it; any other destination fails,
// so a misconfigured URL cannot become a new way out of the enclave.
func LoopbackTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if err := CheckLoopbackAddress(address); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, address)
		},
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
}

// CheckLoopbackAddress accepts host:port where host is an IPv4 loopback
// literal. Names are refused: inside the enclave nothing resolves them.
func CheckLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("in-enclave destination %q is invalid: %w", address, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() || !ip.IsLoopback() {
		return fmt.Errorf("in-enclave destination %q is not an IPv4 loopback address", address)
	}
	return nil
}
