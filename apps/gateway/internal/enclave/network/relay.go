package network

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Loopback addresses the in-enclave Router uses to leave the enclave. The
// Router runs as its own unprivileged process and cannot open vsock
// connections, so the Gateway, which can, relays for it. Both listeners are
// bound to 127.0.0.1: inside an enclave nothing else can reach them.
const (
	// RouterDatabaseAddress is where the Router finds Postgres. The Gateway
	// relays each connection to the parent over DatabaseVsockPort.
	RouterDatabaseAddress = "127.0.0.1:5432"
	// RouterEgressProxyAddress is an HTTP CONNECT proxy for the Router's
	// HTTPS calls. It only opens tunnels to RouterEgressHosts, over the same
	// measured provider routes the Gateway uses, and TLS still ends at the
	// destination, so the parent sees ciphertext only.
	RouterEgressProxyAddress = "127.0.0.1:3128"
)

// RouterEgressHosts are the only destinations the Router may reach: TypeSafe,
// which its Jev strategy asks to classify a request.
var RouterEgressHosts = []string{"api.typesafe.ai:443"}

// DialFunc opens the upstream side of a relayed connection.
type DialFunc func(ctx context.Context) (net.Conn, error)

const relayDialTimeout = 10 * time.Second

// ServeTCPRelay accepts connections on listener and joins each one to a new
// upstream connection from dial, until listener is closed.
func ServeTCPRelay(listener net.Listener, name string, dial DialFunc) {
	for {
		local, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("%s relay stopped: %v", name, err)
			}
			return
		}
		go func() {
			defer local.Close()
			ctx, cancel := context.WithTimeout(context.Background(), relayDialTimeout)
			upstream, err := dial(ctx)
			cancel()
			if err != nil {
				log.Printf("%s relay could not reach its upstream: %v", name, err)
				return
			}
			defer upstream.Close()
			join(local, upstream, nil)
		}()
	}
}

// ServeConnectProxy is a minimal HTTP CONNECT proxy. Each CONNECT names a
// host:port; only keys of routes are accepted, and the tunnel is opened with
// that route's dialer. Anything else gets a 403 or 405 and is closed. The
// proxy never sees inside the tunnel: the client runs TLS through it.
func ServeConnectProxy(listener net.Listener, routes map[string]DialFunc) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("egress proxy stopped: %v", err)
			}
			return
		}
		go handleConnect(conn, routes)
	}
}

func handleConnect(conn net.Conn, routes map[string]DialFunc) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(relayDialTimeout))
	reader := bufio.NewReader(conn)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if request.Method != http.MethodConnect {
		writeProxyStatus(conn, http.StatusMethodNotAllowed)
		return
	}
	dial, ok := routes[strings.ToLower(request.Host)]
	if !ok {
		log.Printf("egress proxy refused a tunnel to %q", request.Host)
		writeProxyStatus(conn, http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), relayDialTimeout)
	upstream, err := dial(ctx)
	cancel()
	if err != nil {
		log.Printf("egress proxy could not reach %s: %v", request.Host, err)
		writeProxyStatus(conn, http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	// The client may have sent its TLS ClientHello right behind the CONNECT;
	// the reader can hold those bytes already.
	join(conn, upstream, reader)
}

func writeProxyStatus(conn net.Conn, status int) {
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, http.StatusText(status))
}

// join copies both ways until either side ends, then closes both. buffered,
// when set, is read from instead of local for the local-to-upstream direction.
func join(local, upstream net.Conn, buffered io.Reader) {
	from := io.Reader(local)
	if buffered != nil {
		from = buffered
	}
	var once sync.Once
	closeBoth := func() {
		local.Close()
		upstream.Close()
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, from)
		once.Do(closeBoth)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(local, upstream)
		once.Do(closeBoth)
		done <- struct{}{}
	}()
	<-done
	<-done
}
