package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	piicache "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/pii/cache"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/pii/presidio"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Measures PII masking on a growing agent conversation against a real
// Presidio analyzer, with and without the cross-request cache. Run with
//
//	PRESIDIO_BENCH_URL=http://127.0.0.1:13001 go test -run TestPIICacheOnAgentConversation -v ./apps/gateway/internal/application/services
type countingPII struct {
	next  ports.PIIFilter
	calls atomic.Int64
}

func (c *countingPII) Enabled() bool { return true }
func (c *countingPII) Analyze(ctx context.Context, text string, opts ports.PIIAnalyzeOptions) ([]domain.PIIEntity, error) {
	c.calls.Add(1)
	return c.next.Analyze(ctx, text, opts)
}

var benchWords = strings.Fields("the deploy failed again because the config points at the old database host so " +
	"please update the connection string restart the service and check the logs for errors before " +
	"the review tomorrow the client asked for a summary of the changes and the test results")
var benchPII = []string{"John Smith", "maria.garcia@example.com", "+34 612 345 678", "Alice Johnson", "10.0.4.17", "Berlin"}

func benchText(r *rand.Rand, n int) string {
	var b strings.Builder
	for b.Len() < n {
		if r.Intn(25) == 0 {
			b.WriteString(benchPII[r.Intn(len(benchPII))])
		} else {
			b.WriteString(benchWords[r.Intn(len(benchWords))])
		}
		b.WriteByte(' ')
	}
	return b.String()
}

// benchConversation returns the request body of turn n: a system prompt and,
// per earlier turn, a user message, an assistant message with a tool call,
// and the tool's result.
func benchConversation(turns int) []byte {
	r := rand.New(rand.NewSource(42))
	msgs := []map[string]any{{"role": "system", "content": benchText(r, 1500)}}
	for i := 0; i < turns; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": benchText(r, 300)},
			map[string]any{"role": "assistant", "content": benchText(r, 600), "tool_calls": []any{map[string]any{
				"id": fmt.Sprintf("call_%d", i), "type": "function",
				"function": map[string]any{"name": "read_file", "arguments": fmt.Sprintf(`{"path":"/srv/app/%d.log","note":%q}`, i, benchText(r, 120))},
			}}},
			map[string]any{"role": "tool", "tool_call_id": fmt.Sprintf("call_%d", i), "content": benchText(r, 1500)},
		)
	}
	body, _ := json.Marshal(map[string]any{"model": "x", "messages": msgs})
	return body
}

func TestPIICacheOnAgentConversation(t *testing.T) {
	url := os.Getenv("PRESIDIO_BENCH_URL")
	if url == "" {
		t.Skip("set PRESIDIO_BENCH_URL to run against a real analyzer")
	}
	const turns = 25
	lastBodies := map[bool][]byte{}
	for _, cached := range []bool{false, true} {
		counter := &countingPII{next: presidio.NewAdapter(presidio.Config{BaseURL: url, ScoreThreshold: 0.4, Timeout: 10 * time.Second})}
		var filter ports.PIIFilter = counter
		if cached {
			filter = piicache.New(counter, 0)
		}
		svc, _ := piiService(filter, domain.APIKeyPIIModeBalanced, &stubProvider{})
		var perTurn []time.Duration
		var total time.Duration
		masked := 0
		for turn := 1; turn <= turns; turn++ {
			body := benchConversation(turn)
			start := time.Now()
			out, mapping, err := svc.maskBody(context.Background(), body, domain.APIKeyPIIModeBalanced, domain.EndpointChatCompletions)
			d := time.Since(start)
			if err != nil {
				t.Fatalf("turn %d: %v", turn, err)
			}
			if mapping != nil {
				masked = mapping.Len()
			}
			if turn == turns {
				lastBodies[cached] = out
			}
			perTurn = append(perTurn, d)
			total += d
		}
		sorted := append([]time.Duration(nil), perTurn...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		t.Logf("cache=%v: %d analyzer calls over %d turns (%.1f per turn); masking time per turn p50 %s, last turn %s, total %s; last turn masked %d values",
			cached, counter.calls.Load(), turns, float64(counter.calls.Load())/turns,
			sorted[len(sorted)/2].Round(time.Millisecond), perTurn[len(perTurn)-1].Round(time.Millisecond),
			total.Round(time.Millisecond), masked)
	}
	if string(lastBodies[false]) != string(lastBodies[true]) {
		t.Fatal("the cache changed the masked request")
	}
	t.Log("masked request on the last turn is identical with and without the cache")
}
