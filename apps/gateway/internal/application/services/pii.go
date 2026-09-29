package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// PII masking works on the raw OpenAI JSON. Requests have the text of their
// messages masked before they go upstream; responses have the placeholders
// restored as they pass. Only the LLM-bound text changes; every other field
// is forwarded as sent.

// maskBody masks PII in a chat request body: message content (tool results
// as JSON, masking only string values), reasoning sent back, tool-call
// arguments (as JSON when valid), and the user field. Tool definitions and
// other static fields are not scanned. It returns the body unchanged and no
// mapping when the key doesn't mask, the filter is off, or nothing was found.
// On filter failure it fails closed, unless configured to fail open.
func (s *GenerateService) maskBody(ctx context.Context, raw []byte, piiMode string) ([]byte, *domain.PIIMapping, error) {
	mode, ok := domain.NormalizeAPIKeyPIIMode(piiMode)
	if !ok || mode == domain.APIKeyPIIModeOff || s.pii == nil || !s.pii.Enabled() {
		return raw, nil, nil
	}
	body, err := decodeObject(raw)
	if err != nil {
		return raw, nil, nil // Invalid bodies were rejected before this.
	}
	masker := &requestPIIMasker{
		service:       s,
		ctx:           ctx,
		piiMode:       mode,
		mapping:       domain.NewPIIMapping(),
		cache:         make(map[string]string),
		entityCounts:  make(map[string]int),
		surfaceCounts: make(map[string]int),
	}
	if err := masker.maskBody(body); err != nil {
		s.logger.Warn("pii filter error", "error", err, "fail_open", s.piiFailOpen)
		if s.piiFailOpen {
			return raw, nil, nil
		}
		return nil, nil, domain.ErrInternal("an internal error occurred")
	}
	if masker.mapping.Len() == 0 {
		return raw, nil, nil
	}
	s.logger.Debug("pii masked",
		"pii_mode", mode,
		"tokens", masker.mapping.Len(),
		"entity_counts", masker.entityCounts,
		"surface_counts", masker.surfaceCounts,
	)
	masked, err := marshalNoEscape(body)
	if err != nil {
		return nil, nil, domain.ErrInternal("an internal error occurred")
	}
	return masked, masker.mapping, nil
}

func (m *requestPIIMasker) maskBody(body map[string]any) error {
	if user, ok := body["user"].(string); ok {
		masked, err := m.maskText("user", user)
		if err != nil {
			return err
		}
		body["user"] = masked
	}
	messages, _ := body["messages"].([]any)
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		surface, mask := "message_content", m.maskText
		if role, _ := msg["role"].(string); role == "tool" {
			surface, mask = "tool_result_content", m.maskJSONAwareString
		}
		switch content := msg["content"].(type) {
		case string:
			masked, err := mask(surface, content)
			if err != nil {
				return err
			}
			msg["content"] = masked
		case []any:
			for _, p := range content {
				part, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if text, ok := part["text"].(string); ok {
					masked, err := mask(surface, text)
					if err != nil {
						return err
					}
					part["text"] = masked
				}
			}
		}
		for _, field := range []string{"reasoning_content", "reasoning"} {
			if text, ok := msg[field].(string); ok {
				masked, err := m.maskText("assistant_reasoning_content", text)
				if err != nil {
					return err
				}
				msg[field] = masked
			}
		}
		calls, _ := msg["tool_calls"].([]any)
		for _, c := range calls {
			call, _ := c.(map[string]any)
			fn, _ := call["function"].(map[string]any)
			if args, ok := fn["arguments"].(string); ok {
				masked, err := m.maskJSONAwareString("assistant_tool_call_arguments", args)
				if err != nil {
					return err
				}
				fn["arguments"] = masked
			}
		}
	}
	return nil
}

// decodeObject decodes a JSON object, keeping numbers exactly as sent.
func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, errors.New("not a JSON object")
	}
	return obj, nil
}

func marshalNoEscape(value any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// generated names the fields of a message the model writes, with the surface
// they're reported under.
var generated = []struct{ field, surface string }{
	{"content", "assistant_content"},
	{"reasoning_content", "assistant_reasoning_content"},
	{"reasoning", "assistant_reasoning_content"},
}

const toolArgsSurface = "assistant_tool_call_arguments"

// unmasker restores PII placeholders in a proxied response. Streamed text is
// held back while it ends in what may be the start of a placeholder, so one
// split across events is still restored; the rest is released with the
// choice's finish reason, or in a final event.
type unmasker struct {
	mapping  *domain.PIIMapping
	held     map[string]*heldChoice // By choice index.
	restored map[string]*strings.Builder
	logger   ports.Logger
	fields   []any
}

type heldChoice struct {
	text  map[string]*strings.Builder // By field.
	tools map[string]*strings.Builder // Arguments, by tool-call index.
}

// newUnmasker returns nil when nothing was masked.
func newUnmasker(mapping *domain.PIIMapping, logger ports.Logger, fields ...any) *unmasker {
	if mapping == nil || mapping.Len() == 0 {
		return nil
	}
	return &unmasker{mapping: mapping, held: map[string]*heldChoice{}, restored: map[string]*strings.Builder{}, logger: logger, fields: fields}
}

func (u *unmasker) choice(index string) *heldChoice {
	h, ok := u.held[index]
	if !ok {
		h = &heldChoice{text: map[string]*strings.Builder{}, tools: map[string]*strings.Builder{}}
		u.held[index] = h
	}
	return h
}

func buffer(m map[string]*strings.Builder, key string) *strings.Builder {
	b, ok := m[key]
	if !ok {
		b = &strings.Builder{}
		m[key] = b
	}
	return b
}

func (u *unmasker) track(surface, text string) {
	buffer(u.restored, surface).WriteString(text)
}

// event restores one streamed JSON event. Events without generated text
// pass unchanged.
func (u *unmasker) event(data []byte) []byte {
	obj, err := decodeObject(data)
	if err != nil {
		return data
	}
	choices, _ := obj["choices"].([]any)
	changed := false
	for pos, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		index := choiceIndex(choice["index"], pos)
		held := u.choice(index)
		delta, _ := choice["delta"].(map[string]any)
		for _, g := range generated {
			if text, ok := delta[g.field].(string); ok && text != "" {
				out := feedPIIStreamBuffer(buffer(held.text, g.field), text, u.mapping)
				u.track(g.surface, out)
				delta[g.field] = out
				changed = true
			}
		}
		for _, call := range toolCallObjects(delta) {
			fn, _ := call["function"].(map[string]any)
			if args, ok := fn["arguments"].(string); ok && args != "" {
				out := feedPIIStreamBuffer(buffer(held.tools, choiceIndex(call["index"], 0)), args, u.mapping)
				u.track(toolArgsSurface, out)
				fn["arguments"] = out
				changed = true
			}
		}
		if choice["finish_reason"] != nil && u.release(index, choice) {
			changed = true
		}
	}
	if !changed {
		return data
	}
	out, err := marshalNoEscape(obj)
	if err != nil {
		return data
	}
	return out
}

// release adds what a choice still holds to its delta.
func (u *unmasker) release(index string, choice map[string]any) bool {
	held, ok := u.held[index]
	if !ok {
		return false
	}
	delete(u.held, index)
	delta, _ := choice["delta"].(map[string]any)
	if delta == nil {
		delta = map[string]any{}
	}
	released := false
	for _, g := range generated {
		if b, ok := held.text[g.field]; ok {
			if tail := flushPIIStreamBuffer(b, u.mapping); tail != "" {
				existing, _ := delta[g.field].(string)
				delta[g.field] = existing + tail
				u.track(g.surface, tail)
				released = true
			}
		}
	}
	keys := make([]string, 0, len(held.tools))
	for key := range held.tools {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		tail := flushPIIStreamBuffer(held.tools[key], u.mapping)
		if tail == "" {
			continue
		}
		u.track(toolArgsSurface, tail)
		released = true
		calls, _ := delta["tool_calls"].([]any)
		appended := false
		for _, call := range toolCallObjects(delta) {
			if choiceIndex(call["index"], 0) == key {
				fn, _ := call["function"].(map[string]any)
				if fn == nil {
					fn = map[string]any{}
					call["function"] = fn
				}
				existing, _ := fn["arguments"].(string)
				fn["arguments"] = existing + tail
				appended = true
			}
		}
		if !appended {
			delta["tool_calls"] = append(calls, map[string]any{"index": json.Number(key), "function": map[string]any{"arguments": tail}})
		}
	}
	if released {
		choice["delta"] = delta
	}
	return released
}

// tail returns an event with the text still held for choices the provider
// never finished, or nil.
func (u *unmasker) tail(id, model string) []byte {
	indexes := make([]string, 0, len(u.held))
	for index := range u.held {
		indexes = append(indexes, index)
	}
	sort.Strings(indexes)
	var choices []any
	for _, index := range indexes {
		choice := map[string]any{"index": json.Number(index), "delta": map[string]any{}, "finish_reason": nil}
		if u.release(index, choice) {
			choices = append(choices, choice)
		}
	}
	if len(choices) == 0 {
		return nil
	}
	out, err := marshalNoEscape(map[string]any{"id": id, "object": "chat.completion.chunk", "model": model, "choices": choices})
	if err != nil {
		return nil
	}
	return out
}

// body restores a non-streaming response.
func (u *unmasker) body(data []byte) []byte {
	obj, err := decodeObject(data)
	if err != nil {
		return data
	}
	choices, _ := obj["choices"].([]any)
	for _, item := range choices {
		choice, _ := item.(map[string]any)
		msg, _ := choice["message"].(map[string]any)
		for _, g := range generated {
			if text, ok := msg[g.field].(string); ok && text != "" {
				msg[g.field] = domain.Unmask(text, u.mapping)
				u.track(g.surface, msg[g.field].(string))
			}
		}
		for _, call := range toolCallObjects(msg) {
			fn, _ := call["function"].(map[string]any)
			if args, ok := fn["arguments"].(string); ok && args != "" {
				fn["arguments"] = domain.Unmask(args, u.mapping)
				u.track(toolArgsSurface, fn["arguments"].(string))
			}
		}
	}
	out, err := marshalNoEscape(obj)
	if err != nil {
		return data
	}
	return out
}

// report logs placeholders the model changed so they could not be restored.
func (u *unmasker) report(stream bool) {
	groups := map[string][]string{}
	for surface, text := range u.restored {
		trackUnresolvedPIITokens(groups, u.mapping, surface, text.String())
	}
	logUnresolvedPIITokenGroups(u.logger, groups, append(u.fields, "stream", stream)...)
}

func toolCallObjects(parent map[string]any) []map[string]any {
	calls, _ := parent["tool_calls"].([]any)
	out := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		if call, ok := c.(map[string]any); ok {
			out = append(out, call)
		}
	}
	return out
}

// choiceIndex reads a JSON index, or uses the position when there is none.
func choiceIndex(v any, pos int) string {
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	return strconv.Itoa(pos)
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
