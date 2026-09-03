package services

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/shopspring/decimal"
)

// fakePIIFilter is a test double for ports.PIIFilter. It returns the
// pre-configured entities when called and tracks invocation counts so tests
// can assert the masking pipeline ran.
type fakePIIFilter struct {
	enabled  bool
	err      error
	calls    int
	lastText string
	lastOpts ports.PIIAnalyzeOptions
	// byText optionally maps an exact input string to entity spans, so tests
	// can return different detections for different request fields.
	byText map[string][]domain.PIIEntity
	// entities is returned when byText does not match.
	entities []domain.PIIEntity
}

type loggedWarning struct {
	msg    string
	fields []any
}

type captureLogger struct {
	warnings []loggedWarning
}

func (l *captureLogger) Debug(string, ...any) {}
func (l *captureLogger) Info(string, ...any)  {}
func (l *captureLogger) Error(string, ...any) {}
func (l *captureLogger) Warn(msg string, fields ...any) {
	l.warnings = append(l.warnings, loggedWarning{msg: msg, fields: append([]any(nil), fields...)})
}

func (f *fakePIIFilter) Enabled() bool { return f.enabled }

func (f *fakePIIFilter) Analyze(_ context.Context, text string, opts ports.PIIAnalyzeOptions) ([]domain.PIIEntity, error) {
	f.calls++
	f.lastText = text
	f.lastOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	if f.byText != nil {
		if v, ok := f.byText[text]; ok {
			return v, nil
		}
		return nil, nil
	}
	return f.entities, nil
}

// captureProvider records the exact GenerateRequest the service forwards so
// tests can assert that user-supplied PII never reaches the upstream call.
type captureProvider struct {
	lastReq       domain.GenerateRequest
	respondMsg    string
	respondOutput []domain.OutputItem
}

func (p *captureProvider) Generate(_ context.Context, req domain.GenerateRequest, model domain.PublicModel) (domain.GenerateResult, error) {
	p.lastReq = req
	msg := p.respondMsg
	role := "assistant"
	output := p.respondOutput
	if output == nil {
		output = []domain.OutputItem{
			{Type: domain.OutputItemTypeMessage, Role: &role, Content: &msg},
		}
	}
	return domain.GenerateResult{
		ID:              "resp-1",
		PublicModelID:   req.PublicModelID,
		ProviderName:    model.ProviderConfig.ProviderName,
		ProviderModelID: model.ProviderModelID,
		Output:          output,
	}, nil
}

func (p *captureProvider) StreamGenerate(context.Context, domain.GenerateRequest, domain.PublicModel) (ports.GenerationStream, error) {
	return nil, nil
}

func buildPIITestService(filter ports.PIIFilter, prov *captureProvider) (*GenerateService, *stubUsageMeter) {
	return buildPIITestServiceWithPIIMode(filter, prov, domain.APIKeyPIIModeBalanced)
}

func buildPIITestServiceWithPIIMode(filter ports.PIIFilter, prov *captureProvider, piiMode string) (*GenerateService, *stubUsageMeter) {
	usage := &stubUsageMeter{}
	selected := domain.PublicModel{
		PublicModelID:           "openai/gpt-4.1-mini",
		ProviderModelID:         "gpt-4.1-mini",
		ProviderConfig:          domain.ProviderConfig{ProviderName: "openai"},
		SupportsChatCompletions: true,
		SupportsTools:           true,
		MaxContextWindow:        100000,
		MaxOutputTokens:         16384,
		InputPricePerMillion:    decimal.NewFromFloat(0.75),
		OutputPricePerMillion:   decimal.NewFromFloat(4.50),
	}
	svc := NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{
			Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive},
			APIKey:  domain.APIKey{ID: "key1", Active: true, PIIMode: piiMode},
		}},
		&stubModelCatalog{model: selected},
		nil,
		&captureProviderRegistry{p: prov},
		usage,
		filter,
		stubLogger{},
	)
	return svc, usage
}

// captureProviderRegistry returns the captureProvider as a GenerationProvider.
type captureProviderRegistry struct {
	p *captureProvider
}

func (r *captureProviderRegistry) GetProvider(string) (ports.GenerationProvider, error) {
	return r.p, nil
}

func newMessageInput(text string) []domain.InputItem {
	role := "user"
	t := text
	return []domain.InputItem{
		{Type: domain.InputItemTypeMessage, Role: &role, Content: &t},
	}
}

// captureProvider implements ports.GenerationProvider directly so we can
// observe the exact request the service forwards upstream.

func TestGenerateServiceExecute_MasksRequestAndUnmasksResponse(t *testing.T) {
	prompt := "My name is John Smith, email john@x.com"
	// Pre-computed byte offsets for "John Smith" (11..21) and "john@x.com" (29..39).
	filter := &fakePIIFilter{
		enabled: true,
		entities: []domain.PIIEntity{
			{Type: "PERSON", Start: 11, End: 21, Score: 0.99},
			{Type: "EMAIL", Start: 29, End: 39, Score: 0.99},
		},
	}
	prov := &captureProvider{respondMsg: "Hi [PERSON_1], I see your email [EMAIL_1]."}

	svc, _ := buildPIITestService(filter, prov)

	result, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Input:         newMessageInput(prompt),
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The provider must have seen only placeholders.
	gotContent := *prov.lastReq.Input[0].Content
	if strings.Contains(gotContent, "John Smith") || strings.Contains(gotContent, "john@x.com") {
		t.Fatalf("upstream prompt still contains PII: %q", gotContent)
	}
	if !strings.Contains(gotContent, "[PERSON_1]") || !strings.Contains(gotContent, "[EMAIL_1]") {
		t.Fatalf("upstream prompt missing placeholders: %q", gotContent)
	}

	// The client-facing result must have the original PII restored.
	if got := *result.Output[0].Content; got != "Hi John Smith, I see your email john@x.com." {
		t.Fatalf("response not unmasked: %q", got)
	}
	if filter.calls != 1 {
		t.Fatalf("filter calls = %d, want 1", filter.calls)
	}
}

func TestGenerateServiceExecute_MasksInstructionsContentReasoningAndToolArgs(t *testing.T) {
	instructions := "Call Jane at jane@example.com"
	prompt := "Email Jane at jane@example.com"
	toolArgs := `{"email":"jane@example.com","note":"Call Jane"}`
	role := "user"
	assistantInputRole := "assistant"
	assistantRole := "assistant"
	reasoning := "I should contact Jane"
	filter := &fakePIIFilter{
		enabled: true,
		byText: map[string][]domain.PIIEntity{
			instructions: {
				{Type: "PERSON", Start: 5, End: 9, Score: 0.99},
				{Type: "EMAIL", Start: 13, End: 29, Score: 0.99},
			},
			prompt: {
				{Type: "PERSON", Start: 6, End: 10, Score: 0.99},
				{Type: "EMAIL", Start: 14, End: 30, Score: 0.99},
			},
			reasoning: {
				{Type: "PERSON", Start: 17, End: 21, Score: 0.99},
			},
			"jane@example.com": {
				{Type: "EMAIL", Start: 0, End: 16, Score: 0.99},
			},
			"Call Jane": {
				{Type: "PERSON", Start: 5, End: 9, Score: 0.99},
			},
		},
	}
	prov := &captureProvider{
		respondOutput: []domain.OutputItem{
			{
				Type:    domain.OutputItemTypeMessage,
				Role:    &assistantRole,
				Content: strPtr("Message [PERSON_1] at [EMAIL_1]."),
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "send_email", ArgumentsJSON: `{"email":"[EMAIL_1]"}`},
				},
			},
		},
	}
	svc, _ := buildPIITestService(filter, prov)

	result, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Instructions:  &instructions,
		Input: []domain.InputItem{
			{
				Type:    domain.InputItemTypeMessage,
				Role:    &role,
				Content: strPtr(prompt),
			},
			{
				Type:             domain.InputItemTypeMessage,
				Role:             &assistantInputRole,
				ReasoningContent: &reasoning,
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "send_email", ArgumentsJSON: toolArgs},
				},
			},
		},
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := *prov.lastReq.Instructions; strings.Contains(got, "Jane") || strings.Contains(got, "jane@example.com") {
		t.Fatalf("upstream instructions still contain PII: %q", got)
	}
	if got := *prov.lastReq.Input[0].Content; strings.Contains(got, "Jane") || strings.Contains(got, "jane@example.com") {
		t.Fatalf("upstream content still contains PII: %q", got)
	}
	if got := *prov.lastReq.Input[1].ReasoningContent; strings.Contains(got, "Jane") {
		t.Fatalf("upstream reasoning content still contains PII: %q", got)
	}
	wantArgs := `{"email":"[EMAIL_1]","note":"Call [PERSON_1]"}`
	if got := prov.lastReq.Input[1].ToolCalls[0].ArgumentsJSON; got != wantArgs {
		t.Fatalf("tool-call arguments not masked:\n got = %q\nwant = %q", got, wantArgs)
	}
	if filter.calls != 5 {
		t.Fatalf("filter calls = %d, want 5 for instructions, content, reasoning, and JSON values", filter.calls)
	}

	if got := *result.Output[0].Content; got != "Message Jane at jane@example.com." {
		t.Fatalf("response content not restored: %q", got)
	}
	if got := result.Output[0].ToolCalls[0].ArgumentsJSON; got != `{"email":"jane@example.com"}` {
		t.Fatalf("response tool-call arguments not restored, got %q", got)
	}
}

func TestGenerateServiceExecute_MasksToolResultJSONAndUserField(t *testing.T) {
	toolRole := "tool"
	user := "alice@example.com"
	toolResult := `{"customer":{"name":"Alice","email":"alice@example.com"},"paid":false}`
	filter := &fakePIIFilter{
		enabled: true,
		byText: map[string][]domain.PIIEntity{
			"user prompt": {
				{Type: "PERSON", Start: 0, End: 0, Score: 0.99}, // invalid span proves no accidental fallback masking
			},
			"alice@example.com": {
				{Type: "EMAIL", Start: 0, End: 17, Score: 0.99},
			},
			"Alice": {
				{Type: "PERSON", Start: 0, End: 5, Score: 0.99},
			},
		},
	}
	prov := &captureProvider{respondMsg: "ok"}
	svc, _ := buildPIITestService(filter, prov)

	_, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		User:          &user,
		Input: []domain.InputItem{
			{
				Type:       domain.InputItemTypeMessage,
				Role:       &toolRole,
				Content:    &toolResult,
				ToolCallID: strPtr("call_1"),
			},
		},
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := *prov.lastReq.User; got != "[EMAIL_1]" {
		t.Fatalf("user field not masked: %q", got)
	}
	gotContent := *prov.lastReq.Input[0].Content
	if strings.Contains(gotContent, "Alice") || strings.Contains(gotContent, "alice@example.com") {
		t.Fatalf("tool result still contains PII: %q", gotContent)
	}
	if !strings.Contains(gotContent, "[PERSON_1]") || !strings.Contains(gotContent, "[EMAIL_1]") {
		t.Fatalf("tool result missing placeholders: %q", gotContent)
	}
}

func TestGenerateServiceExecute_MasksInvalidJSONToolArgumentsAsPlainText(t *testing.T) {
	assistantRole := "assistant"
	args := `email=jane@example.com note=Call Jane`
	filter := &fakePIIFilter{
		enabled: true,
		byText: map[string][]domain.PIIEntity{
			args: {
				{Type: "EMAIL", Start: 6, End: 22, Score: 0.99},
				{Type: "PERSON", Start: 33, End: 37, Score: 0.99},
			},
		},
	}
	prov := &captureProvider{respondMsg: "ok"}
	svc, _ := buildPIITestService(filter, prov)

	_, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Input: []domain.InputItem{
			{
				Type: domain.InputItemTypeMessage,
				Role: &assistantRole,
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "send_email", ArgumentsJSON: args},
				},
			},
		},
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := `email=[EMAIL_1] note=Call [PERSON_1]`
	if got := prov.lastReq.Input[0].ToolCalls[0].ArgumentsJSON; got != want {
		t.Fatalf("invalid JSON tool args not masked as plain text:\n got = %q\nwant = %q", got, want)
	}
}

func TestGenerateServiceExecute_StaticFieldsAreNotScanned(t *testing.T) {
	description := "Send email to Jane"
	filter := &fakePIIFilter{
		enabled: true,
		byText: map[string][]domain.PIIEntity{
			description: {
				{Type: "PERSON", Start: 14, End: 18, Score: 0.99},
			},
		},
	}

	prov := &captureProvider{respondMsg: "ok"}
	svc, _ := buildPIITestService(filter, prov)
	_, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Input:         newMessageInput("hello"),
		Tools: []domain.ToolDefinition{
			{Name: "send_email", Description: description, Parameters: map[string]any{"type": "object"}},
		},
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := prov.lastReq.Tools[0].Description; got != description {
		t.Fatalf("static description should not be masked, got %q", got)
	}
}

func TestGenerateServiceExecute_FailsClosedWhenFilterErrors(t *testing.T) {
	filter := &fakePIIFilter{enabled: true, err: io.ErrUnexpectedEOF}
	prov := &captureProvider{}
	svc, usage := buildPIITestService(filter, prov)

	_, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Input:         newMessageInput("hello"),
	}, "sk-test")
	if err == nil {
		t.Fatal("expected fail-closed error when filter errors")
	}
	if usage.failureCalls == 0 {
		t.Fatalf("expected failure recorded")
	}
	if prov.lastReq.PublicModelID != "" {
		t.Fatalf("provider should not have been called")
	}
}

func TestGenerateServiceExecute_FailOpenContinuesWhenFilterErrors(t *testing.T) {
	filter := &fakePIIFilter{enabled: true, err: io.ErrUnexpectedEOF}
	prov := &captureProvider{respondMsg: "ok"}
	svc, _ := buildPIITestService(filter, prov)
	svc.SetPIIOptions("en", true)

	_, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Input:         newMessageInput("hello"),
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute (fail-open): %v", err)
	}
	if got := *prov.lastReq.Input[0].Content; got != "hello" {
		t.Fatalf("prompt should pass through on fail-open, got %q", got)
	}
}

func TestGenerateServiceExecute_BypassesWhenFilterDisabled(t *testing.T) {
	filter := &fakePIIFilter{enabled: false}
	prov := &captureProvider{respondMsg: "ok"}
	svc, _ := buildPIITestService(filter, prov)

	_, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Input:         newMessageInput("My name is John Smith"),
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if filter.calls != 0 {
		t.Fatalf("filter should not be called when disabled")
	}
	if got := *prov.lastReq.Input[0].Content; got != "My name is John Smith" {
		t.Fatalf("prompt should pass through, got %q", got)
	}
}

func TestGenerateServiceExecute_BypassesWhenAPIKeyPIIModeOff(t *testing.T) {
	filter := &fakePIIFilter{enabled: true, entities: []domain.PIIEntity{{Type: "PERSON", Start: 11, End: 21, Score: 0.99}}}
	prov := &captureProvider{respondMsg: "ok"}
	svc, _ := buildPIITestServiceWithPIIMode(filter, prov, domain.APIKeyPIIModeOff)

	_, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Input:         newMessageInput("My name is John Smith"),
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if filter.calls != 0 {
		t.Fatalf("filter should not be called when pii_mode=off, calls=%d", filter.calls)
	}
	if got := *prov.lastReq.Input[0].Content; got != "My name is John Smith" {
		t.Fatalf("prompt should pass through, got %q", got)
	}
}

func TestGenerateServiceExecute_PassesAPIKeyPIIModeToFilter(t *testing.T) {
	filter := &fakePIIFilter{enabled: true, entities: []domain.PIIEntity{{Type: "EMAIL_ADDRESS", Start: 6, End: 22, Score: 0.99}}}
	prov := &captureProvider{respondMsg: "Email [EMAIL_ADDRESS_1]"}
	svc, _ := buildPIITestServiceWithPIIMode(filter, prov, domain.APIKeyPIIModeLow)

	_, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Input:         newMessageInput("email jane@example.com"),
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if filter.lastOpts.Mode != domain.APIKeyPIIModeLow {
		t.Fatalf("filter mode = %q, want low", filter.lastOpts.Mode)
	}
}

func TestGenerateServiceExecute_SkipsEmptyPIIFields(t *testing.T) {
	instructions := ""
	content := ""
	filter := &fakePIIFilter{enabled: true, entities: []domain.PIIEntity{{Type: "PERSON", Start: 0, End: 4}}}
	prov := &captureProvider{respondMsg: "ok"}
	svc, _ := buildPIITestService(filter, prov)

	_, _, err := svc.Execute(context.Background(), domain.EndpointChatCompletions, domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
		Instructions:  &instructions,
		Input: []domain.InputItem{
			{Type: domain.InputItemTypeMessage, Content: &content},
		},
	}, "sk-test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if filter.calls != 0 {
		t.Fatalf("filter calls = %d, want 0 for empty fields", filter.calls)
	}
	if got := *prov.lastReq.Input[0].Content; got != "" {
		t.Fatalf("empty content should pass through, got %q", got)
	}
}

// --- streaming unmask wrapper tests ---

// scriptedStream replays a fixed sequence of events to the wrapper under test.
type scriptedStream struct {
	events []domain.StreamEvent
	errs   []error
	idx    int
	closed bool
}

func (s *scriptedStream) Recv() (domain.StreamEvent, error) {
	if s.idx >= len(s.events) {
		return domain.StreamEvent{}, io.EOF
	}
	ev := s.events[s.idx]
	var err error
	if s.idx < len(s.errs) {
		err = s.errs[s.idx]
	}
	s.idx++
	return ev, err
}

func (s *scriptedStream) Close() error { s.closed = true; return nil }

func textDelta(s string) domain.StreamEvent {
	v := s
	return domain.StreamEvent{Type: domain.StreamEventOutputTextDelta, ContentDelta: &v}
}

func completedEvent() domain.StreamEvent {
	reason := "stop"
	return domain.StreamEvent{Type: domain.StreamEventCompleted, FinishReason: &reason}
}

func toolCallDelta(args string) domain.StreamEvent {
	return domain.StreamEvent{
		Type: domain.StreamEventToolCallDelta,
		ToolCallDelta: &domain.ToolCallDelta{
			Index:          0,
			ArgumentsDelta: &args,
		},
	}
}

func collectStream(t *testing.T, st ports.GenerationStream) string {
	t.Helper()
	var b strings.Builder
	for {
		ev, err := st.Recv()
		if err == io.EOF {
			return b.String()
		}
		if err != nil {
			t.Fatalf("Recv error: %v", err)
		}
		if ev.ContentDelta != nil {
			b.WriteString(*ev.ContentDelta)
		}
	}
}

func collectStreamUntilCompleted(t *testing.T, st ports.GenerationStream) string {
	t.Helper()
	var b strings.Builder
	for {
		ev, err := st.Recv()
		if err == io.EOF {
			return b.String()
		}
		if err != nil {
			t.Fatalf("Recv error: %v", err)
		}
		if ev.ContentDelta != nil {
			b.WriteString(*ev.ContentDelta)
		}
		if ev.Type == domain.StreamEventCompleted {
			for {
				if _, err := st.Recv(); err != nil {
					return b.String()
				}
			}
		}
	}
}

func TestPIIUnmaskingStream_PassthroughWithoutTokens(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("PERSON", "John")
	inner := &scriptedStream{events: []domain.StreamEvent{textDelta("hello "), textDelta("world")}}
	got := collectStream(t, newPIIUnmaskingStream(inner, m))
	if got != "hello world" {
		t.Fatalf("got %q, want hello world", got)
	}
}

func TestPIIUnmaskingStream_RestoresCompleteToken(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("PERSON", "John")
	inner := &scriptedStream{events: []domain.StreamEvent{textDelta("hi [PERSON_1] there")}}
	got := collectStream(t, newPIIUnmaskingStream(inner, m))
	if got != "hi John there" {
		t.Fatalf("got %q", got)
	}
}

func TestPIIUnmaskingStream_BuffersTokenSplitAcrossChunks(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("PERSON", "John")
	inner := &scriptedStream{events: []domain.StreamEvent{
		textDelta("hi [PER"),
		textDelta("SON_1] there"),
	}}
	got := collectStream(t, newPIIUnmaskingStream(inner, m))
	if got != "hi John there" {
		t.Fatalf("got %q", got)
	}
}

func TestPIIUnmaskingStream_KeepsUnknownTokensVerbatim(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("PERSON", "John")
	inner := &scriptedStream{events: []domain.StreamEvent{textDelta("see [GHOST_5] and [PERSON_1]")}}
	got := collectStream(t, newPIIUnmaskingStream(inner, m))
	if got != "see [GHOST_5] and John" {
		t.Fatalf("got %q", got)
	}
}

func TestPIIUnmaskingStream_FlushesTailOnEOF(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("PERSON", "John")
	// Last chunk leaves an unterminated `[PER` in the buffer.
	inner := &scriptedStream{events: []domain.StreamEvent{textDelta("end with [PER")}}
	got := collectStream(t, newPIIUnmaskingStream(inner, m))
	if got != "end with [PER" {
		t.Fatalf("got %q", got)
	}
}

func TestPIIUnmaskingStream_FlushesTailBeforeCompleted(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("PERSON", "John")
	inner := &scriptedStream{events: []domain.StreamEvent{
		textDelta("hi [PER"),
		completedEvent(),
	}}
	got := collectStreamUntilCompleted(t, newPIIUnmaskingStream(inner, m))
	if got != "hi [PER" {
		t.Fatalf("got %q", got)
	}
}

func TestPIIUnmaskingStream_RestoresToolCallArgumentDeltas(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL", "jane@example.com")
	inner := &scriptedStream{events: []domain.StreamEvent{
		toolCallDelta(`{"email":"[EMAIL_1]"}`),
	}}
	st := newPIIUnmaskingStream(inner, m)

	ev, err := st.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.ToolCallDelta == nil || ev.ToolCallDelta.ArgumentsDelta == nil {
		t.Fatalf("expected tool-call argument delta, got %#v", ev)
	}
	if got := *ev.ToolCallDelta.ArgumentsDelta; got != `{"email":"jane@example.com"}` {
		t.Fatalf("tool-call argument delta not restored, got %q", got)
	}
}

func TestPIIUnmaskingStream_RestoresBracketlessToolCallArgumentAlias(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL_ADDRESS", "jane@example.com")
	inner := &scriptedStream{events: []domain.StreamEvent{
		toolCallDelta(`{"email":"EMAIL_ADDRESS_1"}`),
	}}
	st := newPIIUnmaskingStream(inner, m)

	ev, err := st.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if got := *ev.ToolCallDelta.ArgumentsDelta; got != `{"email":"jane@example.com"}` {
		t.Fatalf("tool-call argument delta not restored, got %q", got)
	}
}

func TestPIIUnmaskingStream_BuffersBracketlessAliasSplitAcrossToolCallArgumentDeltas(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL_ADDRESS", "jane@example.com")
	inner := &scriptedStream{events: []domain.StreamEvent{
		toolCallDelta(`{"email":"EMAIL_ADD`),
		toolCallDelta(`RESS_1"}`),
	}}
	st := newPIIUnmaskingStream(inner, m)

	ev, err := st.Recv()
	if err != nil {
		t.Fatalf("Recv first: %v", err)
	}
	if got := *ev.ToolCallDelta.ArgumentsDelta; got != `{"email":"` {
		t.Fatalf("first tool-call argument delta = %q", got)
	}

	ev, err = st.Recv()
	if err != nil {
		t.Fatalf("Recv second: %v", err)
	}
	if got := *ev.ToolCallDelta.ArgumentsDelta; got != `jane@example.com"}` {
		t.Fatalf("second tool-call argument delta = %q", got)
	}
}

func TestPIIUnmaskingStream_BuffersTokenSplitAcrossToolCallArgumentDeltas(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL", "jane@example.com")
	inner := &scriptedStream{events: []domain.StreamEvent{
		toolCallDelta(`{"email":"[EMA`),
		toolCallDelta(`IL_1]"}`),
	}}
	st := newPIIUnmaskingStream(inner, m)

	ev, err := st.Recv()
	if err != nil {
		t.Fatalf("Recv first: %v", err)
	}
	if got := *ev.ToolCallDelta.ArgumentsDelta; got != `{"email":"` {
		t.Fatalf("first tool-call argument delta = %q", got)
	}

	ev, err = st.Recv()
	if err != nil {
		t.Fatalf("Recv second: %v", err)
	}
	if got := *ev.ToolCallDelta.ArgumentsDelta; got != `jane@example.com"}` {
		t.Fatalf("second tool-call argument delta = %q", got)
	}
}

func TestUnmaskResult_LogsUnresolvedToolCallArgumentTokens(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL_ADDRESS", "jane@example.com")
	logger := &captureLogger{}
	role := "assistant"
	result := domain.GenerateResult{
		Output: []domain.OutputItem{
			{
				Type: domain.OutputItemTypeMessage,
				Role: &role,
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "send_email", ArgumentsJSON: `{"email":"EMAIL_ADDRESS_2"}`},
				},
			},
		},
	}

	unmaskResult(&result, m, logger, "request_id", "req-1")

	if len(logger.warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(logger.warnings))
	}
	if logger.warnings[0].msg != "pii restoration unresolved tokens" {
		t.Fatalf("warning msg = %q", logger.warnings[0].msg)
	}
	if got := result.Output[0].ToolCalls[0].ArgumentsJSON; got != `{"email":"EMAIL_ADDRESS_2"}` {
		t.Fatalf("arguments changed unexpectedly: %q", got)
	}
}

func TestPIIUnmaskingStream_LogsUnresolvedToolCallArgumentTokensOnCompleted(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL_ADDRESS", "jane@example.com")
	logger := &captureLogger{}
	inner := &scriptedStream{events: []domain.StreamEvent{
		toolCallDelta(`{"email":"EMAIL_ADDRESS_2"}`),
		completedEvent(),
	}}
	st := newPIIUnmaskingStreamWithLogger(inner, m, logger, "request_id", "req-1")

	for {
		_, err := st.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}

	if len(logger.warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(logger.warnings))
	}
	if logger.warnings[0].msg != "pii restoration unresolved tokens" {
		t.Fatalf("warning msg = %q", logger.warnings[0].msg)
	}
}

func TestSanitizeErrorWithPIIMapping_MasksKnownOriginalsInMessageAndMetadata(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL", "jane@example.com")
	err := domain.ErrProviderError(502, "provider echoed jane@example.com").WithMeta(
		"upstream_error", `bad request for jane@example.com`,
		"nested", map[string]any{"body": "jane@example.com"},
	)

	sanitized, ok := sanitizeErrorWithPIIMapping(err, m).(*domain.GatewayError)
	if !ok {
		t.Fatalf("expected GatewayError")
	}
	if strings.Contains(sanitized.Message, "jane@example.com") {
		t.Fatalf("message still contains PII: %q", sanitized.Message)
	}
	if got := sanitized.Metadata["upstream_error"]; got != `bad request for [EMAIL_1]` {
		t.Fatalf("upstream_error = %#v", got)
	}
	nested := sanitized.Metadata["nested"].(map[string]any)
	if got := nested["body"]; got != "[EMAIL_1]" {
		t.Fatalf("nested body = %#v", got)
	}
}

func TestPIIUnmaskingStream_CloseDelegates(t *testing.T) {
	m := domain.NewPIIMapping()
	inner := &scriptedStream{}
	st := newPIIUnmaskingStream(inner, m)
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !inner.closed {
		t.Fatal("inner stream not closed")
	}
}

func strPtr(v string) *string {
	return &v
}
