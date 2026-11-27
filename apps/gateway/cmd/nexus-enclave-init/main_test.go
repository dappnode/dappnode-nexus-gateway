package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	enclavenetwork "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/network"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/routerservice"
)

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "services.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStartOrderFollowsDependencies(t *testing.T) {
	_, order, err := load(writeSpec(t, `
services:
  gateway:
    command: ["/gateway"]
    depends_on: [presidio, router]
  router:
    command: ["/router"]
  presidio:
    command: ["gunicorn"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"presidio", "router", "gateway"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestShippedServicesFile(t *testing.T) {
	s, order, err := load(filepath.Join("..", "..", "enclave", "services.yaml"))
	if err != nil {
		t.Fatalf("the services file baked into the image does not load: %v", err)
	}
	if want := []string{"presidio", "gateway", "router"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("start order = %v, want %v", order, want)
	}
	gateway, router := s.Services["gateway"], s.Services["router"]
	if gateway.InheritEnv || router.InheritEnv {
		t.Fatal("only Presidio may inherit the image environment (it sets PORT)")
	}
	for _, key := range []string{"PRESIDIO_ANALYZER_URL", "ROUTER_URL"} {
		if url := gateway.Env[key]; !strings.HasPrefix(url, "http://127.0.0.1:") {
			t.Fatalf("%s = %q, want a loopback URL", key, url)
		}
	}
	if gateway.Env["ROUTER_URL"] != "http://127.0.0.1:"+router.Env["PORT"] {
		t.Fatalf("ROUTER_URL %q does not match the Router's PORT %q", gateway.Env["ROUTER_URL"], router.Env["PORT"])
	}
	if router.User == nil || *router.User == 0 {
		t.Fatal("the Router must run as an unprivileged user")
	}
	if !reflect.DeepEqual([]string(router.EnvFiles), []string{routerservice.EnvFile}) {
		t.Fatalf("Router env_file = %v, want the file the gateway writes (%s)", router.EnvFiles, routerservice.EnvFile)
	}
	if router.Env["HTTPS_PROXY"] != "http://"+enclavenetwork.RouterEgressProxyAddress {
		t.Fatalf("Router HTTPS_PROXY = %q, want the gateway's egress proxy", router.Env["HTTPS_PROXY"])
	}
	if _, ok := router.Env["ROUTER_ADMIN_TOKEN"]; ok {
		t.Fatal("the Router's admin API must stay off inside the enclave")
	}
}

func TestInvalidSpecsAreRejected(t *testing.T) {
	cases := map[string]string{
		"unknown dependency": `
services:
  gateway: {command: ["/gateway"], depends_on: [missing]}`,
		"cycle": `
services:
  a: {command: ["/a"], depends_on: [b]}
  b: {command: ["/b"], depends_on: [a]}`,
		"no command": `
services:
  gateway: {depends_on: []}`,
		"bad restart policy": `
services:
  gateway: {command: ["/gateway"], restart: sometimes}`,
		"unknown field": `
services:
  gateway: {command: ["/gateway"], restat: always}`,
		"no services": `services: {}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := load(writeSpec(t, body)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestBaseEnvironmentIsMinimal(t *testing.T) {
	t.Setenv("PORT", "3000")
	t.Setenv("METERING_TOKEN", "secret")
	for _, kv := range baseEnv() {
		if strings.HasPrefix(kv, "PORT=") || strings.HasPrefix(kv, "METERING_TOKEN=") {
			t.Fatalf("base environment leaks %q", kv)
		}
	}
}

func TestEnvFileAcceptsComposeSpellings(t *testing.T) {
	s, _, err := load(writeSpec(t, `
services:
  one: {command: ["/one"], env_file: /run/one.env}
  many: {command: ["/many"], env_file: [/run/a.env, /run/b.env]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := []string(s.Services["one"].EnvFiles); !reflect.DeepEqual(got, []string{"/run/one.env"}) {
		t.Fatalf("one = %v", got)
	}
	if got := []string(s.Services["many"].EnvFiles); !reflect.DeepEqual(got, []string{"/run/a.env", "/run/b.env"}) {
		t.Fatalf("many = %v", got)
	}
	if _, _, err := load(writeSpec(t, `
services:
  rel: {command: ["/rel"], env_file: relative.env}
`)); err == nil {
		t.Fatal("a relative env_file must be rejected")
	}
}

func TestWaitEnvFilesWaitsForTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.env")
	svc := &service{name: "router", EnvFiles: envFiles{path}}
	go func() {
		time.Sleep(400 * time.Millisecond)
		_ = os.WriteFile(path, []byte("# written by the gateway\nDATABASE_URL=postgres://u:p=w@127.0.0.1:5432/db\n\nTYPESAFE_API_KEY=k\n"), 0o600)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	env, err := waitEnvFiles(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DATABASE_URL=postgres://u:p=w@127.0.0.1:5432/db", "TYPESAFE_API_KEY=k"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestWaitEnvFilesStopsWithContext(t *testing.T) {
	svc := &service{name: "router", EnvFiles: envFiles{filepath.Join(t.TempDir(), "never.env")}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := waitEnvFiles(ctx, svc); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's error", err)
	}
}

func TestReadEnvFileRejectsMalformedLines(t *testing.T) {
	for name, body := range map[string]string{
		"no equals":  "DATABASE_URL\n",
		"empty key":  "=value\n",
		"spaced key": " KEY=value\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.env")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readEnvFile(path); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestEnvironmentOverridesEnvFile(t *testing.T) {
	svc := &service{
		name:    "printenv",
		Command: []string{"/usr/bin/env"},
		Env:     map[string]string{"PORT": "8083"},
	}
	cmd, err := start(svc, []string{"PORT=9999", "FROM_FILE=yes"})
	if err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	got := map[string]string{}
	for _, kv := range cmd.Env {
		key, value, _ := strings.Cut(kv, "=")
		got[key] = value // last one wins, as exec does
	}
	if got["PORT"] != "8083" || got["FROM_FILE"] != "yes" {
		t.Fatalf("PORT=%q FROM_FILE=%q", got["PORT"], got["FROM_FILE"])
	}
}
