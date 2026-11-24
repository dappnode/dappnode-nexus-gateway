// Command nexus-enclave-init starts and supervises the processes that share
// one Nitro enclave, from a Compose-like file baked into the measured image:
//
//	services:
//	  presidio:
//	    command: ["gunicorn", "-b", "127.0.0.1:3000", "app:create_app()"]
//	    workdir: /app
//	    user: 1001
//	    inherit_env: true
//	    health: {url: "http://127.0.0.1:3000/health", timeout_seconds: 120}
//	  gateway:
//	    command: ["/gateway"]
//	    depends_on: [presidio]
//
// It brings up loopback, starts services in dependency order (each one only
// after the services it depends on answer their health check), restarts a
// service that exits, and on SIGTERM stops them in reverse order. If a service
// never becomes healthy the init exits, which ends the enclave visibly instead
// of serving without it.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	enclavenetwork "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/network"
	"go.yaml.in/yaml/v3"
)

// managedPIDs holds the processes the init started; their supervisors
// collect them, and the reaper leaves them alone.
var managedPIDs sync.Map

type spec struct {
	Services map[string]*service `yaml:"services"`
}

type service struct {
	Command []string          `yaml:"command"`
	Workdir string            `yaml:"workdir"`
	User    *uint32           `yaml:"user"` // Numeric uid/gid; unset runs as the init's user.
	Env     map[string]string `yaml:"env"`
	// InheritEnv passes the init's whole environment (the image's ENV) to the
	// service. Off by default, so one service's settings cannot leak into
	// another (Presidio's image sets PORT=3000, which the gateway also reads).
	InheritEnv bool     `yaml:"inherit_env"`
	DependsOn  []string `yaml:"depends_on"`
	Restart    string   `yaml:"restart"` // always (default) | no
	Health     *health  `yaml:"health"`

	name    string
	mu      sync.Mutex
	cmd     *exec.Cmd
	running bool
}

type health struct {
	URL            string `yaml:"url"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("[init] ")
	path := flag.String("config", "/etc/nexus/enclave-services.yaml", "services file")
	flag.Parse()

	s, order, err := load(*path)
	if err != nil {
		log.Fatalf("invalid services file: %v", err)
	}
	if err := enclavenetwork.ConfigureLoopback(); err != nil {
		log.Fatalf("configure loopback: %v", err)
	}
	startReaper(func(pid int) bool { _, ok := managedPIDs.Load(pid); return ok })

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var wg sync.WaitGroup
	for _, name := range order {
		svc := s.Services[name]
		started := make(chan error, 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervise(ctx, svc, started)
		}()
		if err := <-started; err != nil {
			log.Printf("%s did not start: %v", name, err)
			stop()
			shutdown(s, order)
			wg.Wait()
			os.Exit(1)
		}
		if svc.Health != nil {
			if err := waitHealthy(ctx, svc); err != nil {
				log.Printf("%s never became healthy: %v", name, err)
				stop()
				shutdown(s, order)
				wg.Wait()
				os.Exit(1)
			}
		}
		log.Printf("%s is up", name)
	}

	<-ctx.Done()
	log.Printf("stopping services")
	shutdown(s, order)
	wg.Wait()
}

// load reads the services file and returns a start order in which every
// service comes after its dependencies; ties sort by name.
func load(path string) (*spec, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var s spec
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, nil, err
	}
	if len(s.Services) == 0 {
		return nil, nil, errors.New("no services")
	}
	for name, svc := range s.Services {
		svc.name = name
		if len(svc.Command) == 0 {
			return nil, nil, fmt.Errorf("%s: command is required", name)
		}
		if svc.Restart != "" && svc.Restart != "always" && svc.Restart != "no" {
			return nil, nil, fmt.Errorf("%s: restart must be always or no", name)
		}
		for _, dep := range svc.DependsOn {
			if _, ok := s.Services[dep]; !ok {
				return nil, nil, fmt.Errorf("%s depends on unknown service %s", name, dep)
			}
		}
	}
	order, err := startOrder(s.Services)
	return &s, order, err
}

func startOrder(services map[string]*service) ([]string, error) {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	state := map[string]int{} // 1 visiting, 2 done
	var order []string
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("dependency cycle through %s", name)
		case 2:
			return nil
		}
		state[name] = 1
		deps := append([]string(nil), services[name].DependsOn...)
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = 2
		order = append(order, name)
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// supervise runs svc until ctx ends, restarting it with backoff when it
// exits. started receives the result of the first start.
func supervise(ctx context.Context, svc *service, started chan<- error) {
	backoff := time.Second
	for attempt := 0; ; attempt++ {
		cmd, err := start(svc)
		if attempt == 0 {
			started <- err
		}
		if err != nil {
			if attempt > 0 && ctx.Err() == nil {
				log.Printf("%s failed to restart: %v", svc.name, err)
			}
			return
		}
		began := time.Now()
		err = cmd.Wait()
		managedPIDs.Delete(cmd.Process.Pid)
		// A service's leftovers (a worker whose parent died) can still hold
		// its port, so clear its whole process group before any restart.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		svc.mu.Lock()
		svc.running = false
		svc.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		log.Printf("%s exited after %s: %v", svc.name, time.Since(began).Round(time.Millisecond), err)
		if svc.Restart == "no" {
			return
		}
		if time.Since(began) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
		log.Printf("restarting %s", svc.name)
	}
}

func start(svc *service) (*exec.Cmd, error) {
	cmd := exec.Command(svc.Command[0], svc.Command[1:]...)
	cmd.Dir = svc.Workdir
	if svc.InheritEnv {
		cmd.Env = os.Environ()
	} else {
		cmd.Env = baseEnv()
	}
	keys := make([]string, 0, len(svc.Env))
	for k := range svc.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+svc.Env[k])
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if svc.User != nil {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: *svc.User, Gid: *svc.User}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	managedPIDs.Store(cmd.Process.Pid, struct{}{})
	go prefix(svc.name, stdout, os.Stdout)
	go prefix(svc.name, stderr, os.Stderr)
	svc.mu.Lock()
	svc.cmd = cmd
	svc.running = true
	svc.mu.Unlock()
	return cmd, nil
}

// baseEnv is what a service gets when it does not inherit the environment.
func baseEnv() []string {
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/"}
	if lang, ok := os.LookupEnv("LANG"); ok {
		env = append(env, "LANG="+lang)
	}
	return env
}

func prefix(name string, r io.Reader, w io.Writer) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fmt.Fprintf(w, "[%s] %s\n", name, sc.Bytes())
	}
}

func waitHealthy(ctx context.Context, svc *service) error {
	timeout := time.Duration(svc.Health.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	// Health checks only ever go to loopback.
	client := &http.Client{Timeout: 2 * time.Second, Transport: enclavenetwork.LoopbackTransport()}
	deadline := time.Now().Add(timeout)
	began := time.Now()
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := client.Get(svc.Health.URL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				log.Printf("%s healthy after %s", svc.name, time.Since(began).Round(100*time.Millisecond))
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("no healthy answer from %s within %s", svc.Health.URL, timeout)
}

// shutdown sends SIGTERM to each running service, dependents first, and
// SIGKILL to any still running 10 seconds later.
func shutdown(s *spec, order []string) {
	for i := len(order) - 1; i >= 0; i-- {
		signalGroup(s.Services[order[i]], syscall.SIGTERM)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !anyRunning(s) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, name := range order {
		signalGroup(s.Services[name], syscall.SIGKILL)
	}
}

func signalGroup(svc *service, sig syscall.Signal) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.running && svc.cmd != nil && svc.cmd.Process != nil {
		_ = syscall.Kill(-svc.cmd.Process.Pid, sig)
	}
}

func anyRunning(s *spec) bool {
	for _, svc := range s.Services {
		svc.mu.Lock()
		running := svc.running
		svc.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}
