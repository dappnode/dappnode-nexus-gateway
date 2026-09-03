package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// maskRequest detects PII in the dynamic LLM-bound fields of `req` and
// rewrites those fields in place with deterministic placeholder tokens.
// It covers instructions, input content, assistant reasoning content,
// assistant tool-call arguments, and the OpenAI-compatible user field.
// The returned mapping must be passed to unmaskResult / piiUnmaskingStream so
// the upstream model's response can be restored to its original form.
//
// Returns (nil, nil) when the filter is disabled or there is nothing to mask.
// On filter failure, behavior depends on s.piiFailOpen: fail-closed (default)
// returns an error; fail-open logs and returns (nil, nil) so the request
// proceeds with the original text.
func (s *GenerateService) maskRequest(ctx context.Context, req *domain.GenerateRequest, piiMode string) (*domain.PIIMapping, error) {
	mode, ok := domain.NormalizeAPIKeyPIIMode(piiMode)
	if !ok || mode == domain.APIKeyPIIModeOff {
		return nil, nil
	}
	if s.pii == nil || !s.pii.Enabled() {
		return nil, nil
	}

	original := cloneGenerateRequest(*req)
	masking := &requestPIIMasker{
		service:       s,
		ctx:           ctx,
		piiMode:       mode,
		mapping:       domain.NewPIIMapping(),
		cache:         make(map[string]string),
		entityCounts:  make(map[string]int),
		surfaceCounts: make(map[string]int),
	}

	maskPtr := func(surface string, ptr **string) error {
		if ptr == nil || *ptr == nil || **ptr == "" {
			return nil
		}
		masked, err := masking.maskText(surface, **ptr)
		if err != nil {
			return err
		}
		**ptr = masked
		return nil
	}

	maskJSONPtr := func(surface string, ptr *string) (string, error) {
		if ptr == nil || *ptr == "" {
			return "", nil
		}
		return masking.maskJSONAwareString(surface, *ptr)
	}

	if err := maskPtr("instructions", &req.Instructions); err != nil {
		return s.handlePIIMaskError(req, original, err)
	}
	if err := maskPtr("user", &req.User); err != nil {
		return s.handlePIIMaskError(req, original, err)
	}
	for i := range req.Input {
		item := &req.Input[i]
		if item.Content != nil && *item.Content != "" {
			var (
				masked string
				err    error
			)
			if item.Role != nil && *item.Role == "tool" {
				masked, err = masking.maskJSONAwareString("tool_result_content", *item.Content)
			} else {
				masked, err = masking.maskText("message_content", *item.Content)
			}
			if err != nil {
				return s.handlePIIMaskError(req, original, err)
			}
			*item.Content = masked
		}
		if err := maskPtr("assistant_reasoning_content", &item.ReasoningContent); err != nil {
			return s.handlePIIMaskError(req, original, err)
		}
		for j := range item.ToolCalls {
			masked, err := maskJSONPtr("assistant_tool_call_arguments", &item.ToolCalls[j].ArgumentsJSON)
			if err != nil {
				return s.handlePIIMaskError(req, original, err)
			}
			if masked != "" {
				item.ToolCalls[j].ArgumentsJSON = masked
			}
		}
	}

	if masking.mapping.Len() == 0 {
		return nil, nil
	}
	s.logger.Debug("pii masked",
		"pii_mode", mode,
		"tokens", masking.mapping.Len(),
		"entity_counts", masking.entityCounts,
		"surface_counts", masking.surfaceCounts,
	)
	return masking.mapping, nil
}

func (s *GenerateService) handlePIIMaskError(req *domain.GenerateRequest, original domain.GenerateRequest, err error) (*domain.PIIMapping, error) {
	s.logger.Warn("pii filter error", "error", err, "fail_open", s.piiFailOpen)
	if s.piiFailOpen {
		*req = original
		return nil, nil
	}
	return nil, domain.ErrInternal("an internal error occurred")
}

type requestPIIMasker struct {
	service       *GenerateService
	ctx           context.Context
	piiMode       string
	mapping       *domain.PIIMapping
	cache         map[string]string
	entityCounts  map[string]int
	surfaceCounts map[string]int
}

func (m *requestPIIMasker) maskText(surface, text string) (string, error) {
	if text == "" {
		return text, nil
	}
	if masked, ok := m.cache[text]; ok {
		return masked, nil
	}
	entities, err := m.service.pii.Analyze(m.ctx, text, ports.PIIAnalyzeOptions{
		Language: m.service.piiLang,
		Mode:     m.piiMode,
	})
	if err != nil {
		return "", err
	}
	m.trackEntities(surface, entities)
	masked := domain.ApplyMask(text, entities, m.mapping)
	m.cache[text] = masked
	return masked, nil
}

func (m *requestPIIMasker) trackEntities(surface string, entities []domain.PIIEntity) {
	for _, entity := range entities {
		m.entityCounts[entity.Type]++
		if surface != "" {
			m.surfaceCounts[surface]++
		}
	}
}

func (m *requestPIIMasker) maskJSONAwareString(surface, raw string) (string, error) {
	if raw == "" {
		return raw, nil
	}

	var value any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return m.maskText(surface, raw)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return m.maskText(surface, raw)
	}

	masked, changed, err := m.maskAnyWithChanged(surface, value)
	if err != nil {
		return "", err
	}
	if !changed {
		return raw, nil
	}
	return marshalJSONNoEscape(masked)
}

func (m *requestPIIMasker) maskAnyWithChanged(surface string, value any) (any, bool, error) {
	switch v := value.(type) {
	case string:
		masked, err := m.maskText(surface, v)
		return masked, masked != v, err
	case []any:
		out := make([]any, len(v))
		changed := false
		for i := range v {
			masked, itemChanged, err := m.maskAnyWithChanged(surface, v[i])
			if err != nil {
				return nil, false, err
			}
			out[i] = masked
			changed = changed || itemChanged
		}
		return out, changed, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		changed := false
		for key, val := range v {
			masked, itemChanged, err := m.maskAnyWithChanged(surface, val)
			if err != nil {
				return nil, false, err
			}
			out[key] = masked
			changed = changed || itemChanged
		}
		return out, changed, nil
	default:
		return value, false, nil
	}
}

func marshalJSONNoEscape(value any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// unmaskResult walks every output text and tool-call argument field of a
// non-streaming generation result and restores PII using `mapping`. Safe to
// call with a nil mapping.
func unmaskResult(result *domain.GenerateResult, mapping *domain.PIIMapping, logger ports.Logger, logFields ...any) {
	if result == nil || mapping == nil || mapping.Len() == 0 {
		return
	}
	unresolved := make(map[string][]string)
	for i := range result.Output {
		item := &result.Output[i]
		if item.Content != nil && *item.Content != "" {
			restored := domain.Unmask(*item.Content, mapping)
			trackUnresolvedPIITokens(unresolved, mapping, "assistant_content", restored)
			item.Content = &restored
		}
		if item.ReasoningContent != nil && *item.ReasoningContent != "" {
			restored := domain.Unmask(*item.ReasoningContent, mapping)
			trackUnresolvedPIITokens(unresolved, mapping, "assistant_reasoning_content", restored)
			item.ReasoningContent = &restored
		}
		for j := range item.ToolCalls {
			if item.ToolCalls[j].ArgumentsJSON != "" {
				item.ToolCalls[j].ArgumentsJSON = domain.Unmask(item.ToolCalls[j].ArgumentsJSON, mapping)
				trackUnresolvedPIITokens(unresolved, mapping, "assistant_tool_call_arguments", item.ToolCalls[j].ArgumentsJSON)
			}
		}
	}
	logUnresolvedPIITokenGroups(logger, unresolved, append(logFields, "stream", false)...)
}

// piiUnmaskingStream wraps a GenerationStream and restores PII tokens in
// output content, reasoning, and tool-call argument deltas before they reach
// the client. It buffers partial tokens that span chunk boundaries
// (e.g. "[PER" + "SON_1]") so substitution always sees a complete token.
type piiUnmaskingStream struct {
	inner            ports.GenerationStream
	mapping          *domain.PIIMapping
	logger           ports.Logger
	logFields        []any
	textBuf          strings.Builder
	reasonBuf        strings.Builder
	toolArgBufs      map[int]*strings.Builder
	restoredText     strings.Builder
	restoredReason   strings.Builder
	restoredToolArgs map[int]*strings.Builder
	pending          []domain.StreamEvent
	eofPending       bool
	reported         bool
	closed           bool
}

func newPIIUnmaskingStream(inner ports.GenerationStream, mapping *domain.PIIMapping) *piiUnmaskingStream {
	return newPIIUnmaskingStreamWithLogger(inner, mapping, nil)
}

func newPIIUnmaskingStreamWithLogger(inner ports.GenerationStream, mapping *domain.PIIMapping, logger ports.Logger, logFields ...any) *piiUnmaskingStream {
	return &piiUnmaskingStream{
		inner:            inner,
		mapping:          mapping,
		logger:           logger,
		logFields:        append([]any(nil), logFields...),
		toolArgBufs:      make(map[int]*strings.Builder),
		restoredToolArgs: make(map[int]*strings.Builder),
	}
}

// Recv pulls the next event from the wrapped stream and rewrites any text
// delta. Non-text events pass through untouched.
func (s *piiUnmaskingStream) Recv() (domain.StreamEvent, error) {
	if len(s.pending) > 0 {
		event := s.pending[0]
		s.pending = s.pending[1:]
		return event, nil
	}
	if s.eofPending {
		return domain.StreamEvent{}, io.EOF
	}

	event, err := s.inner.Recv()

	// When the upstream stream finishes, flush any text we held back so the
	// client doesn't lose a trailing fragment. The tail is returned with a nil
	// error first because the HTTP handler ignores events returned with EOF.
	if err == io.EOF {
		if tails := s.flushBufferedEvents(); len(tails) > 0 {
			s.reportUnresolvedToolArgs()
			s.eofPending = true
			s.pending = append(s.pending, tails[1:]...)
			return tails[0], nil
		}
		s.reportUnresolvedToolArgs()
		return event, err
	}

	if err != nil {
		return event, err
	}

	if event.Type == domain.StreamEventCompleted {
		return s.rewriteCompleted(event), nil
	}

	if event.Type == domain.StreamEventError && event.Error != nil {
		if sanitized, ok := sanitizeErrorWithPIIMapping(event.Error, s.mapping).(*domain.GatewayError); ok {
			event.Error = sanitized
		}
		return event, nil
	}

	if event.ContentDelta != nil && *event.ContentDelta != "" {
		emit := feedPIIStreamBuffer(&s.textBuf, *event.ContentDelta, s.mapping)
		s.trackRestoredText(emit)
		event.ContentDelta = &emit
	}
	if event.ReasoningDelta != nil && *event.ReasoningDelta != "" {
		emit := feedPIIStreamBuffer(&s.reasonBuf, *event.ReasoningDelta, s.mapping)
		s.trackRestoredReasoning(emit)
		event.ReasoningDelta = &emit
	}
	if event.ToolCallDelta != nil && event.ToolCallDelta.ArgumentsDelta != nil && *event.ToolCallDelta.ArgumentsDelta != "" {
		buf := s.toolArgBuffer(event.ToolCallDelta.Index)
		emit := feedPIIStreamBuffer(buf, *event.ToolCallDelta.ArgumentsDelta, s.mapping)
		s.trackRestoredToolArg(event.ToolCallDelta.Index, emit)
		event.ToolCallDelta.ArgumentsDelta = &emit
	}
	return event, nil
}

func (s *piiUnmaskingStream) rewriteCompleted(event domain.StreamEvent) domain.StreamEvent {
	if event.ContentDelta != nil && *event.ContentDelta != "" {
		content := feedPIIStreamBuffer(&s.textBuf, *event.ContentDelta, s.mapping)
		s.trackRestoredText(content)
		event.ContentDelta = &content
	}
	if event.ReasoningDelta != nil && *event.ReasoningDelta != "" {
		reasoning := feedPIIStreamBuffer(&s.reasonBuf, *event.ReasoningDelta, s.mapping)
		s.trackRestoredReasoning(reasoning)
		event.ReasoningDelta = &reasoning
	}
	if event.ToolCallDelta != nil && event.ToolCallDelta.ArgumentsDelta != nil && *event.ToolCallDelta.ArgumentsDelta != "" {
		buf := s.toolArgBuffer(event.ToolCallDelta.Index)
		args := feedPIIStreamBuffer(buf, *event.ToolCallDelta.ArgumentsDelta, s.mapping)
		s.trackRestoredToolArg(event.ToolCallDelta.Index, args)
		event.ToolCallDelta.ArgumentsDelta = &args
	}

	prefixes := completedDeltaPrefixEvents(event)
	tails := s.flushBufferedEvents()
	s.reportUnresolvedToolArgs()
	if len(prefixes) == 0 && len(tails) == 0 {
		return event
	}

	event.ContentDelta = nil
	event.ReasoningDelta = nil
	event.ToolCallDelta = nil

	events := make([]domain.StreamEvent, 0, len(prefixes)+len(tails)+1)
	events = append(events, prefixes...)
	events = append(events, tails...)
	events = append(events, event)
	s.pending = append(s.pending, events[1:]...)
	return events[0]
}

// Close releases the wrapped stream. Buffered text is discarded — Recv already
// returns the tail on EOF, and a Close before EOF means the client gave up.
func (s *piiUnmaskingStream) Close() error {
	s.closed = true
	s.textBuf.Reset()
	s.reasonBuf.Reset()
	for _, buf := range s.toolArgBufs {
		buf.Reset()
	}
	return s.inner.Close()
}

func (s *piiUnmaskingStream) toolArgBuffer(index int) *strings.Builder {
	if buf, ok := s.toolArgBufs[index]; ok {
		return buf
	}
	buf := &strings.Builder{}
	s.toolArgBufs[index] = buf
	return buf
}

func (s *piiUnmaskingStream) flushBufferedEvents() []domain.StreamEvent {
	events := make([]domain.StreamEvent, 0, 2+len(s.toolArgBufs))
	if tail := flushPIIStreamBuffer(&s.textBuf, s.mapping); tail != "" {
		s.trackRestoredText(tail)
		events = append(events, contentDeltaEvent(tail))
	}
	if tail := flushPIIStreamBuffer(&s.reasonBuf, s.mapping); tail != "" {
		s.trackRestoredReasoning(tail)
		events = append(events, reasoningDeltaEvent(tail))
	}
	indexes := make([]int, 0, len(s.toolArgBufs))
	for index := range s.toolArgBufs {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		buf := s.toolArgBufs[index]
		if tail := flushPIIStreamBuffer(buf, s.mapping); tail != "" {
			s.trackRestoredToolArg(index, tail)
			events = append(events, toolCallArgumentDeltaEvent(index, tail))
		}
	}
	return events
}

func (s *piiUnmaskingStream) trackRestoredToolArg(index int, delta string) {
	if delta == "" {
		return
	}
	buf, ok := s.restoredToolArgs[index]
	if !ok {
		buf = &strings.Builder{}
		s.restoredToolArgs[index] = buf
	}
	buf.WriteString(delta)
}

func (s *piiUnmaskingStream) trackRestoredText(delta string) {
	if delta != "" {
		s.restoredText.WriteString(delta)
	}
}

func (s *piiUnmaskingStream) trackRestoredReasoning(delta string) {
	if delta != "" {
		s.restoredReason.WriteString(delta)
	}
}

func (s *piiUnmaskingStream) reportUnresolvedToolArgs() {
	if s.reported {
		return
	}
	s.reported = true
	unresolved := make(map[string][]string)
	trackUnresolvedPIITokens(unresolved, s.mapping, "assistant_content", s.restoredText.String())
	trackUnresolvedPIITokens(unresolved, s.mapping, "assistant_reasoning_content", s.restoredReason.String())
	for _, buf := range s.restoredToolArgs {
		trackUnresolvedPIITokens(unresolved, s.mapping, "assistant_tool_call_arguments", buf.String())
	}
	logUnresolvedPIITokenGroups(s.logger, unresolved, append(s.logFields, "stream", true)...)
}

// feedPIIStreamBuffer appends `delta` to the internal buffer and returns the largest prefix
// that contains only complete (or no) placeholder tokens, with those tokens
// already replaced by their original values.
//
// The buffer holds back from the last unmatched '[' onward so we never split
// a token across two emitted chunks.
func feedPIIStreamBuffer(buf *strings.Builder, delta string, mapping *domain.PIIMapping) string {
	buf.WriteString(delta)
	full := buf.String()

	// Find the last '[' that has no matching ']' after it. Everything up to
	// that index is safe to emit; everything from it onward stays buffered.
	cut := len(full)
	for i := len(full) - 1; i >= 0; i-- {
		if full[i] == '[' {
			if !strings.ContainsRune(full[i:], ']') {
				cut = i
				break
			}
			// Has a closing bracket — entire string is safe.
			break
		}
		if full[i] == ']' {
			// We hit a closing bracket before any opener — safe.
			break
		}
	}
	if bareCut := bareAliasHoldStart(full, mapping); bareCut >= 0 && bareCut < cut {
		cut = bareCut
	}

	safe := full[:cut]
	tail := full[cut:]

	buf.Reset()
	buf.WriteString(tail)

	return domain.Unmask(safe, mapping)
}

func bareAliasHoldStart(text string, mapping *domain.PIIMapping) int {
	if mapping == nil || mapping.Len() == 0 || text == "" {
		return -1
	}
	aliases := mapping.BareTokenAliases()
	if len(aliases) == 0 {
		return -1
	}
	maxLen := 0
	for _, alias := range aliases {
		if len(alias) > maxLen {
			maxLen = len(alias)
		}
	}
	startAt := len(text) - maxLen
	if startAt < 0 {
		startAt = 0
	}
	for start := len(text) - 1; start >= startAt; start-- {
		if !isBareTokenBoundary(text, start-1) {
			continue
		}
		suffix := text[start:]
		for _, alias := range aliases {
			if len(suffix) <= len(alias) && strings.EqualFold(alias[:len(suffix)], suffix) {
				return start
			}
		}
	}
	return -1
}

func isBareTokenBoundary(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	c := text[index]
	return !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_')
}

// flushPIIStreamBuffer returns any buffered text, unmasking whatever placeholders are
// complete and leaving partial ones intact.
func flushPIIStreamBuffer(buf *strings.Builder, mapping *domain.PIIMapping) string {
	if buf.Len() == 0 {
		return ""
	}
	tail := buf.String()
	buf.Reset()
	return domain.Unmask(tail, mapping)
}

func completedDeltaPrefixEvents(event domain.StreamEvent) []domain.StreamEvent {
	events := make([]domain.StreamEvent, 0, 2)
	if (event.ContentDelta != nil && *event.ContentDelta != "") || (event.ReasoningDelta != nil && *event.ReasoningDelta != "") {
		events = append(events, domain.StreamEvent{
			Type:           domain.StreamEventOutputTextDelta,
			ContentDelta:   event.ContentDelta,
			ReasoningDelta: event.ReasoningDelta,
		})
	}
	if event.ToolCallDelta != nil && event.ToolCallDelta.ArgumentsDelta != nil && *event.ToolCallDelta.ArgumentsDelta != "" {
		events = append(events, domain.StreamEvent{
			Type:          domain.StreamEventToolCallDelta,
			ToolCallDelta: event.ToolCallDelta,
		})
	}
	return events
}

func contentDeltaEvent(delta string) domain.StreamEvent {
	return domain.StreamEvent{
		Type:         domain.StreamEventOutputTextDelta,
		ContentDelta: &delta,
	}
}

func reasoningDeltaEvent(delta string) domain.StreamEvent {
	return domain.StreamEvent{
		Type:           domain.StreamEventOutputTextDelta,
		ReasoningDelta: &delta,
	}
}

func toolCallArgumentDeltaEvent(index int, delta string) domain.StreamEvent {
	return domain.StreamEvent{
		Type: domain.StreamEventToolCallDelta,
		ToolCallDelta: &domain.ToolCallDelta{
			Index:          index,
			ArgumentsDelta: &delta,
		},
	}
}

func sanitizeErrorWithPIIMapping(err error, mapping *domain.PIIMapping) error {
	if err == nil || mapping == nil || mapping.Len() == 0 {
		return err
	}
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) {
		return errors.New(mapping.MaskKnownOriginals(err.Error()))
	}
	cp := *gwErr
	cp.Message = mapping.MaskKnownOriginals(cp.Message)
	if len(gwErr.Metadata) > 0 {
		cp.Metadata = make(map[string]any, len(gwErr.Metadata))
		for key, value := range gwErr.Metadata {
			cp.Metadata[key] = sanitizePIILogValue(value, mapping)
		}
	}
	return &cp
}

func trackUnresolvedPIITokens(groups map[string][]string, mapping *domain.PIIMapping, surface, text string) {
	if groups == nil || mapping == nil || mapping.Len() == 0 || text == "" {
		return
	}
	tokens := mapping.UnresolvedTokens(text)
	if len(tokens) == 0 {
		return
	}
	groups[surface] = mergeStringSets(groups[surface], tokens)
}

func logUnresolvedPIITokenGroups(logger ports.Logger, groups map[string][]string, fields ...any) {
	if logger == nil || len(groups) == 0 {
		return
	}
	surfaces := make([]string, 0, len(groups))
	for surface := range groups {
		surfaces = append(surfaces, surface)
	}
	sort.Strings(surfaces)
	for _, surface := range surfaces {
		tokens := groups[surface]
		sort.Strings(tokens)
		logFields := append([]any(nil), fields...)
		logFields = append(logFields,
			"surface", surface,
			"unresolved_token_count", len(tokens),
			"unresolved_tokens", tokens,
		)
		logger.Warn("pii restoration unresolved tokens", logFields...)
	}
}

func mergeStringSets(existing []string, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, token := range existing {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	for _, token := range incoming {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func sanitizePIILogValue(value any, mapping *domain.PIIMapping) any {
	switch v := value.(type) {
	case string:
		return mapping.MaskKnownOriginals(v)
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = sanitizePIILogValue(v[i], mapping)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i := range v {
			out[i] = mapping.MaskKnownOriginals(v[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = sanitizePIILogValue(item, mapping)
		}
		return out
	default:
		return value
	}
}

func cloneGenerateRequest(req domain.GenerateRequest) domain.GenerateRequest {
	clone := req
	clone.RouterID = cloneStringPtr(req.RouterID)
	clone.RoutedPublicModelID = cloneStringPtr(req.RoutedPublicModelID)
	clone.MatchedCategory = cloneStringPtr(req.MatchedCategory)
	clone.DecisionReason = cloneStringPtr(req.DecisionReason)
	clone.Instructions = cloneStringPtr(req.Instructions)
	clone.User = cloneStringPtr(req.User)
	clone.ServiceTier = cloneStringPtr(req.ServiceTier)
	if req.Input != nil {
		clone.Input = make([]domain.InputItem, len(req.Input))
		for i := range req.Input {
			clone.Input[i] = req.Input[i]
			clone.Input[i].Role = cloneStringPtr(req.Input[i].Role)
			clone.Input[i].Content = cloneStringPtr(req.Input[i].Content)
			clone.Input[i].ReasoningContent = cloneStringPtr(req.Input[i].ReasoningContent)
			clone.Input[i].ToolCallID = cloneStringPtr(req.Input[i].ToolCallID)
			if req.Input[i].ToolCalls != nil {
				clone.Input[i].ToolCalls = append([]domain.ToolCall(nil), req.Input[i].ToolCalls...)
			}
		}
	}
	if req.Tools != nil {
		clone.Tools = make([]domain.ToolDefinition, len(req.Tools))
		for i := range req.Tools {
			clone.Tools[i] = req.Tools[i]
			clone.Tools[i].Parameters = cloneMap(req.Tools[i].Parameters)
		}
	}
	if req.Stop != nil {
		clone.Stop = append([]string(nil), req.Stop...)
	}
	if req.RoutingCategoryScores != nil {
		clone.RoutingCategoryScores = append([]domain.RoutingCategoryScore(nil), req.RoutingCategoryScores...)
	}
	clone.Metadata = cloneMap(req.Metadata)
	clone.ProviderOptions = cloneMap(req.ProviderOptions)
	if req.TextConfig != nil {
		tc := *req.TextConfig
		tc.FormatType = cloneStringPtr(req.TextConfig.FormatType)
		tc.JSONSchema = cloneMap(req.TextConfig.JSONSchema)
		clone.TextConfig = &tc
	}
	if req.ToolChoice != nil {
		tc := *req.ToolChoice
		tc.FunctionName = cloneStringPtr(req.ToolChoice.FunctionName)
		clone.ToolChoice = &tc
	}
	if req.LogitBias != nil {
		clone.LogitBias = make(map[string]int, len(req.LogitBias))
		for key, value := range req.LogitBias {
			clone.LogitBias[key] = value
		}
	}
	return clone
}

func cloneStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		shallow := make(map[string]any, len(in))
		for key, value := range in {
			shallow[key] = value
		}
		return shallow
	}
	return out
}
