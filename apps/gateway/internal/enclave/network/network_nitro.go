//go:build nitro_enclave

package network

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/profile"
	"github.com/mdlayher/vsock"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"
)

const (
	tinfoilInferenceAddress = "inference.tinfoil.sh:443"
	tinfoilLoopbackAddress  = "127.0.0.1:443"
)

func ListenGateway(port uint32) (net.Listener, error) {
	return vsock.Listen(port, nil)
}

func ConfigureProviderTransport(transport *http.Transport) {
	configureProviderTransport(transport)
}

func ConfigureMeteringTransport(transport *http.Transport) {
	configureTransport(transport, profile.MeteringVsockPort)
}

func ConfigureRouterTransport(transport *http.Transport) {
	configureTransport(transport, profile.RouterVsockPort)
}

// ConfigureDefaultTransport routes package-level HTTP clients through the
// measured provider allowlist. Tinfoil's verifier and EHBP client intentionally
// use http.DefaultTransport internally, so they need the same fail-closed
// routing as the direct provider adapters.
func ConfigureDefaultTransport() error {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return fmt.Errorf("default HTTP transport has unexpected type %T", http.DefaultTransport)
	}
	transport := base.Clone()
	configureProviderTransport(transport)
	http.DefaultTransport = transport
	return nil
}

// StartTinfoilTLSBridge serves the SDK verifier's raw TLS certificate-binding
// connection over the same measured Tinfoil vsock route used by HTTP. The
// verifier currently uses tls.Dial directly for this one check, so it cannot
// inherit the fail-closed http.DefaultTransport configured above. A
// process-local resolver maps only inference.tinfoil.sh to this listener; the
// TLS handshake still terminates at Tinfoil and is checked against attestation.
func StartTinfoilTLSBridge() (net.Listener, error) {
	providerPort, err := providerVsockPort(tinfoilInferenceAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve Tinfoil inference route: %w", err)
	}
	if err := configureIPv4Loopback(); err != nil {
		return nil, fmt.Errorf("configure enclave loopback: %w", err)
	}
	configureTinfoilLoopbackResolver()
	listener, err := net.Listen("tcp4", tinfoilLoopbackAddress)
	if err != nil {
		return nil, fmt.Errorf("listen for Tinfoil TLS verification: %w", err)
	}
	go serveTinfoilTLSBridge(listener, providerPort)
	return listener, nil
}

func configureIPv4Loopback() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	address, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := address.SetInet4Addr([]byte{127, 0, 0, 1}); err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFADDR, address); err != nil {
		return err
	}

	netmask, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := netmask.SetInet4Addr([]byte{255, 0, 0, 0}); err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFNETMASK, netmask); err != nil {
		return err
	}

	flags, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, flags); err != nil {
		return err
	}
	flags.SetUint16(flags.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, flags)
}

func configureTinfoilLoopbackResolver() {
	net.DefaultResolver = &net.Resolver{
		PreferGo:     true,
		StrictErrors: true,
		Dial: func(_ context.Context, _, _ string) (net.Conn, error) {
			client, server := net.Pipe()
			go serveTinfoilDNSConnection(server)
			return client, nil
		},
	}
}

func serveTinfoilDNSConnection(connection net.Conn) {
	defer connection.Close()
	for {
		request, err := readDNSMessage(connection)
		if err != nil {
			return
		}
		response, err := buildTinfoilDNSResponse(request)
		if err != nil {
			return
		}
		if err := writeDNSMessage(connection, response); err != nil {
			return
		}
	}
}

func readDNSMessage(connection net.Conn) ([]byte, error) {
	var length [2]byte
	if _, err := io.ReadFull(connection, length[:]); err != nil {
		return nil, err
	}
	message := make([]byte, binary.BigEndian.Uint16(length[:]))
	_, err := io.ReadFull(connection, message)
	return message, err
}

func writeDNSMessage(connection net.Conn, message []byte) error {
	if len(message) > 65535 {
		return fmt.Errorf("DNS response exceeds TCP framing limit")
	}
	framed := make([]byte, len(message)+2)
	binary.BigEndian.PutUint16(framed[:2], uint16(len(message)))
	copy(framed[2:], message)
	_, err := connection.Write(framed)
	return err
}

func buildTinfoilDNSResponse(request []byte) ([]byte, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(request)
	if err != nil {
		return nil, fmt.Errorf("parse DNS header: %w", err)
	}
	questions, err := parser.AllQuestions()
	if err != nil || len(questions) != 1 {
		return nil, fmt.Errorf("parse DNS question: got %d questions: %w", len(questions), err)
	}
	question := questions[0]
	knownHost := strings.EqualFold(question.Name.String(), "inference.tinfoil.sh.") && question.Class == dnsmessage.ClassINET
	rcode := dnsmessage.RCodeSuccess
	if !knownHost {
		rcode = dnsmessage.RCodeNameError
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               header.ID,
		Response:         true,
		Authoritative:    true,
		RecursionDesired: header.RecursionDesired,
		RCode:            rcode,
	})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(question); err != nil {
		return nil, err
	}
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	if knownHost && question.Type == dnsmessage.TypeA {
		err = builder.AResource(dnsmessage.ResourceHeader{
			Name:  question.Name,
			Class: dnsmessage.ClassINET,
			TTL:   60,
		}, dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})
		if err != nil {
			return nil, err
		}
	}
	return builder.Finish()
}

func serveTinfoilTLSBridge(listener net.Listener, providerPort uint32) {
	for {
		local, err := listener.Accept()
		if err != nil {
			return
		}
		go bridgeTinfoilTLS(local, providerPort)
	}
}

func bridgeTinfoilTLS(local net.Conn, providerPort uint32) {
	defer local.Close()
	upstream, err := vsock.Dial(profile.ParentCID, providerPort, nil)
	if err != nil {
		return
	}
	defer upstream.Close()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, local)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(local, upstream)
		done <- struct{}{}
	}()
	<-done
}

func configureProviderTransport(transport *http.Transport) {
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		port, err := providerVsockPort(address)
		if err != nil {
			return nil, err
		}
		return vsock.Dial(profile.ParentCID, port, nil)
	}
}

func providerVsockPort(address string) (uint32, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return 0, fmt.Errorf("provider egress destination %q is invalid: %w", address, err)
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, route := range profile.ProviderEgressRoutes() {
		if host == route.Host && port == strconv.Itoa(int(route.Port)) {
			return route.VsockPort, nil
		}
	}
	return 0, fmt.Errorf("provider egress destination %q is not measured or allowed", address)
}

func configureTransport(transport *http.Transport, port uint32) {
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return vsock.Dial(profile.ParentCID, port, nil)
	}
}
