package network

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// echoUpstream returns a dialer to a server that echoes what it receives with
// a prefix, and counts how many times it was dialled.
func echoUpstream(t *testing.T, prefix string) (DialFunc, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					if _, err := io.WriteString(conn, prefix+scanner.Text()+"\n"); err != nil {
						return
					}
				}
			}()
		}
	}()
	dials := new(atomic.Int32)
	return func(ctx context.Context) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "tcp4", listener.Addr().String())
	}, dials
}

func serve(t *testing.T, run func(net.Listener)) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go run(listener)
	return listener.Addr().String()
}

func dial(t *testing.T, address string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn, bufio.NewReader(conn)
}

func TestTCPRelayJoinsBothDirections(t *testing.T) {
	upstream, _ := echoUpstream(t, "db:")
	address := serve(t, func(l net.Listener) { ServeTCPRelay(l, "database", upstream) })
	conn, reader := dial(t, address)
	for _, line := range []string{"SELECT 1", "SELECT 2"} {
		if _, err := io.WriteString(conn, line+"\n"); err != nil {
			t.Fatal(err)
		}
		got, err := reader.ReadString('\n')
		if err != nil || got != "db:"+line+"\n" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
}

func TestTCPRelayClosesWhenUpstreamIsUnreachable(t *testing.T) {
	address := serve(t, func(l net.Listener) {
		ServeTCPRelay(l, "database", func(context.Context) (net.Conn, error) {
			return nil, errors.New("parent relay down")
		})
	})
	_, reader := dial(t, address)
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("want the connection closed")
	}
}

func TestConnectProxyTunnelsOnlyAllowedHosts(t *testing.T) {
	upstream, dials := echoUpstream(t, "typesafe:")
	address := serve(t, func(l net.Listener) {
		ServeConnectProxy(l, map[string]DialFunc{"api.typesafe.ai:443": upstream})
	})

	t.Run("allowed host, data sent right behind the CONNECT", func(t *testing.T) {
		conn, reader := dial(t, address)
		// The client's first bytes can arrive in the same read as the CONNECT.
		if _, err := io.WriteString(conn, "CONNECT API.TypeSafe.ai:443 HTTP/1.1\r\nHost: api.typesafe.ai:443\r\n\r\nhello\n"); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT response: %v, %v", response, err)
		}
		got, err := reader.ReadString('\n')
		if err != nil || got != "typesafe:hello\n" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	for name, request := range map[string]string{
		"other host":          "CONNECT api.deepseek.com:443 HTTP/1.1\r\nHost: api.deepseek.com:443\r\n\r\n",
		"other port":          "CONNECT api.typesafe.ai:80 HTTP/1.1\r\nHost: api.typesafe.ai:80\r\n\r\n",
		"plain proxy request": "GET http://api.typesafe.ai/ HTTP/1.1\r\nHost: api.typesafe.ai\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			before := dials.Load()
			conn, reader := dial(t, address)
			if _, err := io.WriteString(conn, request); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode == http.StatusOK {
				t.Fatal("tunnel opened")
			}
			if dials.Load() != before {
				t.Fatal("upstream was dialled for a refused request")
			}
			if _, err := reader.ReadByte(); err == nil {
				t.Fatal("want the connection closed")
			}
		})
	}
}

func TestConnectProxyReportsUnreachableUpstream(t *testing.T) {
	address := serve(t, func(l net.Listener) {
		ServeConnectProxy(l, map[string]DialFunc{"api.typesafe.ai:443": func(context.Context) (net.Conn, error) {
			return nil, errors.New("parent relay down")
		}})
	})
	conn, reader := dial(t, address)
	if _, err := io.WriteString(conn, "CONNECT api.typesafe.ai:443 HTTP/1.1\r\nHost: api.typesafe.ai:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("response %v, %v", response, err)
	}
}

func TestRouterRelayAddressesAreLoopback(t *testing.T) {
	for _, address := range []string{RouterDatabaseAddress, RouterEgressProxyAddress} {
		if err := CheckLoopbackAddress(address); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range RouterEgressHosts {
		if !strings.HasSuffix(host, ":443") {
			t.Fatalf("Router egress %q is not HTTPS", host)
		}
	}
}
