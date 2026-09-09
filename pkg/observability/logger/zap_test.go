package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestWarningsAreNotSampledDuringBurst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	cfg, _ := requestLoggingConfig("info")
	cfg.OutputPaths = []string{path}
	log, err := cfg.Build()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 250 {
		log.Sugar().Warnw("stream completed without usage", "request_id", i)
	}
	if err = log.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "stream completed without usage"); got != 250 {
		t.Fatalf("retained %d request warnings, want 250", got)
	}
}
