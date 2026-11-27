// Package routerservice hands the in-enclave Router the settings that arrive
// in the KMS-sealed secret bundle.
//
// Only the Gateway can decrypt the bundle: KMS releases it to the Gateway's
// attested bootstrap. The Router is a separate process started by
// nexus-enclave-init, so once the bundle is in, the Gateway writes the
// Router's share of it to EnvFile and the init passes that file to the Router
// as its environment (services.yaml `env_file`).
package routerservice

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	enclavenetwork "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/network"
)

// EnvFile is where the Router's secrets are written. /run is the enclave's
// RAM; the directory and file are readable by root only, and the init (root)
// reads it to start the Router.
const EnvFile = "/run/nexus/router.env"

// Environment returns the Router's secret settings from the decrypted bundle:
// the database URL, which must point at the in-enclave relay, and the TypeSafe
// key its Jev strategy needs (optional, as on an ordinary Router).
func Environment(getenv func(string) string) (map[string]string, error) {
	databaseURL := strings.TrimSpace(getenv("ROUTER_DATABASE_URL"))
	if databaseURL == "" {
		return nil, errors.New("ROUTER_DATABASE_URL is required inside the enclave")
	}
	if err := checkDatabaseURL(databaseURL); err != nil {
		return nil, err
	}
	env := map[string]string{"DATABASE_URL": databaseURL}
	if key := strings.TrimSpace(getenv("TYPESAFE_API_KEY")); key != "" {
		env["TYPESAFE_API_KEY"] = key
	}
	return env, nil
}

// checkDatabaseURL accepts only a Postgres URL whose host is the enclave's
// database relay, so a bundle cannot point the Router anywhere else.
func checkDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return errors.New("ROUTER_DATABASE_URL must be a postgres:// URL")
	}
	if parsed.Host != enclavenetwork.RouterDatabaseAddress {
		host, port, _ := net.SplitHostPort(parsed.Host)
		return fmt.Errorf("ROUTER_DATABASE_URL must connect to %s, the enclave's database relay (got host %q port %q)",
			enclavenetwork.RouterDatabaseAddress, host, port)
	}
	if strings.Trim(parsed.Path, "/") == "" {
		return errors.New("ROUTER_DATABASE_URL must name a database")
	}
	return nil
}

// WriteEnvFile replaces path with env as sorted KEY=VALUE lines, readable by
// root only. It writes a temporary file and renames it, so a reader never
// sees a partial file.
func WriteEnvFile(path string, env map[string]string) error {
	keys := make([]string, 0, len(env))
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\n\r") {
			return fmt.Errorf("invalid environment key %q", key)
		}
		if strings.ContainsAny(value, "\n\r\x00") {
			return fmt.Errorf("environment value for %s contains a line break or NUL", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var body strings.Builder
	for _, key := range keys {
		body.WriteString(key + "=" + env[key] + "\n")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".router-env-*")
	if err != nil {
		return fmt.Errorf("create temporary env file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(body.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
