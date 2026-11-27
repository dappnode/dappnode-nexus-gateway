package routerservice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	enclavenetwork "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/network"
	"github.com/dappnode/dappnode-nexus-gateway/internal/enclavebundle"
)

func getenv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestEnvironmentPassesDatabaseAndTypeSafe(t *testing.T) {
	env, err := Environment(getenv(map[string]string{
		"ROUTER_DATABASE_URL": "postgres://nexus@127.0.0.1:5432/nexus?sslmode=disable",
		"TYPESAFE_API_KEY":    "ts-key",
		"METERING_TOKEN":      "not for the router",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 2 || env["DATABASE_URL"] != "postgres://nexus@127.0.0.1:5432/nexus?sslmode=disable" || env["TYPESAFE_API_KEY"] != "ts-key" {
		t.Fatalf("env = %v", env)
	}
}

func TestEnvironmentWithoutTypeSafe(t *testing.T) {
	env, err := Environment(getenv(map[string]string{
		"ROUTER_DATABASE_URL": "postgres://nexus@127.0.0.1:5432/nexus",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["TYPESAFE_API_KEY"]; ok {
		t.Fatal("an empty TypeSafe key must not be passed")
	}
}

func TestEnvironmentRejectsDatabasesOutsideTheRelay(t *testing.T) {
	for name, value := range map[string]string{
		"missing":         "",
		"other host":      "postgres://nexus@10.200.200.31:5432/nexus",
		"other port":      "postgres://nexus@127.0.0.1:5433/nexus",
		"name not ip":     "postgres://nexus@localhost:5432/nexus",
		"no port":         "postgres://nexus@127.0.0.1/nexus",
		"not postgres":    "mysql://nexus:pw@127.0.0.1:5432/nexus",
		"no database":     "postgres://nexus@127.0.0.1:5432/",
		"multiple hosts":  "postgres://nexus@127.0.0.1:5432,10.0.0.1:5432/nexus",
		"unparseable url": "postgres://%zz",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Environment(getenv(map[string]string{"ROUTER_DATABASE_URL": value})); err == nil {
				t.Fatalf("accepted %q", value)
			}
		})
	}
}

func TestWriteEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nexus", "router.env")
	if err := WriteEnvFile(path, map[string]string{"B": "two=2", "A": "one"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "A=one\nB=two=2\n" {
		t.Fatalf("file = %q", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v, want 0700", dir.Mode().Perm())
	}

	// Rewriting replaces the file and leaves no temporary files behind.
	if err := WriteEnvFile(path, map[string]string{"A": "new"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want 1", len(entries))
	}
}

func TestWriteEnvFileRejectsLineBreaks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.env")
	for _, env := range []map[string]string{
		{"A": "one\nB=injected"},
		{"A": "carriage\rreturn"},
		{"A=B": "x"},
		{"": "x"},
	} {
		if err := WriteEnvFile(path, env); err == nil {
			t.Fatalf("accepted %v", env)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a rejected environment must not leave a file")
	}
}

// The bundle is validated against its own copy of the relay address; this
// keeps the two from drifting apart.
func TestBundleValidationAgreesOnTheRelayAddress(t *testing.T) {
	now := time.Now().UTC()
	databaseURL := "postgres://nexus@" + enclavenetwork.RouterDatabaseAddress + "/nexus"
	bundle := enclavebundle.SecretBundle{
		SchemaVersion:  enclavebundle.SchemaVersion,
		SourceRevision: strings.Repeat("a", 40),
		IssuedAt:       now.Add(-time.Minute),
		Environment: map[string]string{
			"METERING_URL":        "https://metering.example.com",
			"METERING_TOKEN":      "secret",
			"ROUTER_DATABASE_URL": databaseURL,
		},
	}
	if err := enclavebundle.ValidateSecretBundle(bundle, strings.Repeat("a", 40), now); err != nil {
		t.Fatalf("the bundle rejects the relay address the gateway listens on: %v", err)
	}
	if _, err := Environment(getenv(map[string]string{"ROUTER_DATABASE_URL": databaseURL})); err != nil {
		t.Fatal(err)
	}
}
