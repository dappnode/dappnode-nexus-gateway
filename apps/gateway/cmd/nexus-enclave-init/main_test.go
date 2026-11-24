package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	if order[len(order)-1] != "gateway" {
		t.Fatalf("the gateway must start last, got %v", order)
	}
	if s.Services["gateway"].InheritEnv {
		t.Fatal("the gateway must not inherit the image environment (Presidio sets PORT)")
	}
	if url := s.Services["gateway"].Env["PRESIDIO_ANALYZER_URL"]; !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("PRESIDIO_ANALYZER_URL = %q, want a loopback URL", url)
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
