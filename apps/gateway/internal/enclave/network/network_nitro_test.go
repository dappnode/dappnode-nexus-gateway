//go:build nitro_enclave

package network

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestConfigureEgressTransportsUseDedicatedVsockDialers(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*http.Transport)
	}{
		{name: "provider", configure: ConfigureProviderTransport},
		{name: "metering", configure: ConfigureMeteringTransport},
		{name: "router", configure: ConfigureRouterTransport},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			test.configure(transport)
			if transport.Proxy != nil {
				t.Fatal("host-controlled HTTP proxy remained enabled")
			}
			if transport.DialContext == nil {
				t.Fatal("vsock dialer was not installed")
			}
		})
	}
}

func TestProviderVsockPortUsesMeasuredAllowlist(t *testing.T) {
	tests := []struct {
		address string
		want    uint32
		wantErr string
	}{
		{address: "api.deepseek.com:443", want: 8443},
		{address: "api.novita.ai:443", want: 8446},
		{address: "inference.tinfoil.sh:443", want: 8447},
		{address: "github-proxy.tinfoil.sh:443", want: 8448},
		{address: "tuf-repo-cdn.sigstore.dev:443", want: 8449},
		{address: "kds-proxy.tinfoil.sh:443", want: 8450},
		{address: "tdx-proxy.tinfoil.sh:443", want: 8451},
		{address: "API.DEEPSEEK.COM.:443", want: 8443},
		{address: "api.deepseek.com:80", wantErr: "not measured or allowed"},
		{address: "example.com:443", wantErr: "not measured or allowed"},
		{address: "missing-port", wantErr: "is invalid"},
	}

	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			got, err := providerVsockPort(test.address)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("providerVsockPort() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("providerVsockPort() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("providerVsockPort() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestTinfoilTLSBridgeUsesMeasuredInferenceRoute(t *testing.T) {
	got, err := providerVsockPort(tinfoilInferenceAddress)
	if err != nil {
		t.Fatalf("providerVsockPort() error = %v", err)
	}
	if got != 8447 {
		t.Fatalf("providerVsockPort() = %d, want 8447", got)
	}
	if tinfoilLoopbackAddress != "127.0.0.1:443" {
		t.Fatalf("Tinfoil loopback address = %q", tinfoilLoopbackAddress)
	}
}

func TestTinfoilLoopbackResolverIsSingleHostAndFailClosed(t *testing.T) {
	original := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = original })
	configureTinfoilLoopbackResolver()

	addresses, err := net.DefaultResolver.LookupHost(context.Background(), "inference.tinfoil.sh")
	if err != nil {
		t.Fatalf("LookupHost(inference.tinfoil.sh) error = %v", err)
	}
	if len(addresses) != 1 || addresses[0] != "127.0.0.1" {
		t.Fatalf("LookupHost(inference.tinfoil.sh) = %v, want [127.0.0.1]", addresses)
	}
	if _, err := net.DefaultResolver.LookupHost(context.Background(), "example.com"); err == nil {
		t.Fatal("LookupHost(example.com) unexpectedly succeeded")
	}
}

func TestConfigureDefaultTransportInstallsFailClosedDialer(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })

	if err := ConfigureDefaultTransport(); err != nil {
		t.Fatalf("ConfigureDefaultTransport() error = %v", err)
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport type = %T", http.DefaultTransport)
	}
	if transport.Proxy != nil {
		t.Fatal("host-controlled HTTP proxy remained enabled")
	}
	if transport.DialContext == nil {
		t.Fatal("provider vsock dialer was not installed")
	}
}
