package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// PIIEntity is a single span of detected personally-identifiable information.
//
// Start / End are byte offsets into the original UTF-8 string (Presidio uses
// character offsets for its `analyzer` API; the adapter converts them to byte
// offsets before constructing PIIEntity values).
type PIIEntity struct {
	Type  string  // PRESIDIO entity type, e.g. PERSON, EMAIL_ADDRESS, PHONE_NUMBER
	Start int     // inclusive byte offset
	End   int     // exclusive byte offset
	Score float32 // detector confidence in [0, 1]
}

// PIIMapping is a request-scoped, deterministic mapping between placeholder
// tokens (e.g. `[PERSON_1]`) and the original PII values they replace.
//
// The same original value mapped under the same type always reuses the same
// placeholder, so repeated PII inside a single request collapses cleanly.
type PIIMapping struct {
	// tokenToOriginal preserves the substitution mapping used during masking.
	tokenToOriginal map[string]string
	// tokenAliases maps tolerated model rewrites back to the canonical token.
	// For example, models may strip brackets and return `EMAIL_ADDRESS_1`
	// instead of `[EMAIL_ADDRESS_1]`.
	tokenAliases map[string]string
	// normalizedTokenAliases maps case/separator-normalized placeholder forms
	// back to the canonical token for bracketed model rewrites.
	normalizedTokenAliases map[string]string
	// originalToToken accelerates dedup when the same value appears multiple times.
	originalToToken map[string]string // key = type|original
	// counters tracks the next available index per entity type.
	counters map[string]int
}

// NewPIIMapping returns an empty mapping ready for use.
func NewPIIMapping() *PIIMapping {
	return &PIIMapping{
		tokenToOriginal:        make(map[string]string),
		tokenAliases:           make(map[string]string),
		normalizedTokenAliases: make(map[string]string),
		originalToToken:        make(map[string]string),
		counters:               make(map[string]int),
	}
}

// Token returns (or assigns) the deterministic placeholder for a given (type, value) pair.
func (m *PIIMapping) Token(entityType, original string) string {
	if m == nil {
		return original
	}
	key := entityType + "|" + original
	if tok, ok := m.originalToToken[key]; ok {
		return tok
	}
	m.counters[entityType]++
	index := m.counters[entityType]
	tok := fmt.Sprintf("[%s_%d]", entityType, index)
	m.originalToToken[key] = tok
	m.tokenToOriginal[tok] = original
	m.registerTokenAliases(tok, entityType, index)
	return tok
}

// Len returns the number of distinct placeholders in the mapping.
func (m *PIIMapping) Len() int {
	if m == nil {
		return 0
	}
	return len(m.tokenToOriginal)
}

// Original returns the original value for a token, if present.
func (m *PIIMapping) Original(token string) (string, bool) {
	if m == nil {
		return "", false
	}
	if v, ok := m.tokenToOriginal[token]; ok {
		return v, true
	}
	canonical, ok := m.tokenAliases[token]
	if !ok {
		canonical, ok = m.normalizedTokenAliases[normalizePlaceholderToken(token)]
		if !ok {
			return "", false
		}
	}
	v, ok := m.tokenToOriginal[canonical]
	return v, ok
}

func (m *PIIMapping) registerTokenAliases(canonical, entityType string, index int) {
	if m == nil {
		return
	}
	m.registerTokenAlias(strings.Trim(canonical, "[]"), canonical)

	short := placeholderTypeAlias(entityType)
	if short != "" && short != entityType {
		m.registerTokenAlias(fmt.Sprintf("[%s_%d]", short, index), canonical)
		m.registerTokenAlias(fmt.Sprintf("%s_%d", short, index), canonical)
	}
}

func (m *PIIMapping) registerTokenAlias(alias, canonical string) {
	if alias == "" || alias == canonical {
		return
	}
	if existing, ok := m.tokenAliases[alias]; ok && existing != canonical {
		return
	}
	m.tokenAliases[alias] = canonical
	normalized := normalizePlaceholderToken(alias)
	if normalized == "" {
		return
	}
	if existing, ok := m.normalizedTokenAliases[normalized]; ok && existing != canonical {
		return
	}
	m.normalizedTokenAliases[normalized] = canonical
}

func placeholderTypeAlias(entityType string) string {
	switch entityType {
	case "EMAIL_ADDRESS":
		return "EMAIL"
	case "PHONE_NUMBER":
		return "PHONE"
	case "CREDIT_CARD":
		return "CARD"
	case "IBAN_CODE":
		return "IBAN"
	case "IP_ADDRESS":
		return "IP"
	case "US_SSN":
		return "SSN"
	case "US_DRIVER_LICENSE":
		return "DRIVER_LICENSE"
	default:
		return entityType
	}
}

// MaskKnownOriginals replaces original values already present in the mapping
// with their placeholder tokens. It is used for diagnostics/logging paths where
// an upstream provider may echo request text back in an error body.
func (m *PIIMapping) MaskKnownOriginals(text string) string {
	if m == nil || len(m.tokenToOriginal) == 0 || text == "" {
		return text
	}

	type pair struct {
		token    string
		original string
	}
	pairs := make([]pair, 0, len(m.tokenToOriginal))
	for token, original := range m.tokenToOriginal {
		if original == "" {
			continue
		}
		pairs = append(pairs, pair{token: token, original: original})
	}
	if len(pairs) == 0 {
		return text
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		return len(pairs[i].original) > len(pairs[j].original)
	})

	out := text
	for _, p := range pairs {
		out = strings.ReplaceAll(out, p.original, p.token)
	}
	return out
}

// ApplyMask rewrites `text` by replacing every entity span with its placeholder
// token from `mapping`. Entities that overlap or fall outside the bounds of
// text are silently skipped. The mapping is mutated to record any new tokens
// allocated during this call.
func ApplyMask(text string, entities []PIIEntity, mapping *PIIMapping) string {
	if len(entities) == 0 || mapping == nil || text == "" {
		return text
	}

	// Sort by Start ascending so we can drop overlaps deterministically.
	sorted := make([]PIIEntity, 0, len(entities))
	sorted = append(sorted, entities...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Start == sorted[j].Start {
			return sorted[i].End > sorted[j].End // prefer longer span on tie
		}
		return sorted[i].Start < sorted[j].Start
	})

	// Drop invalid / overlapping spans. We keep the first occurrence and skip
	// any later span that starts before the previous one ended.
	kept := sorted[:0]
	lastEnd := -1
	for _, e := range sorted {
		if e.Start < 0 || e.End > len(text) || e.End <= e.Start {
			continue
		}
		if e.Start < lastEnd {
			continue
		}
		kept = append(kept, e)
		lastEnd = e.End
	}
	if len(kept) == 0 {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	cursor := 0
	for _, e := range kept {
		b.WriteString(text[cursor:e.Start])
		b.WriteString(mapping.Token(e.Type, text[e.Start:e.End]))
		cursor = e.End
	}
	b.WriteString(text[cursor:])
	return b.String()
}

// Unmask replaces every `[TYPE_N]` placeholder found in `text` with its
// original value from `mapping`. It also tolerates common model rewrites such
// as bracket stripping (`EMAIL_ADDRESS_1`) and short aliases (`EMAIL_1`).
// Tokens that are not in the mapping (e.g. hallucinated by an upstream model)
// are passed through verbatim.
func Unmask(text string, mapping *PIIMapping) string {
	if mapping == nil || mapping.Len() == 0 || text == "" {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	bareAliases := mapping.bareAliasesByLength()
	i := 0
	for i < len(text) {
		if text[i] == '[' {
			close := strings.IndexByte(text[i:], ']')
			if close >= 0 {
				end := i + close + 1
				tok := text[i:end]
				if original, ok := mapping.Original(tok); ok {
					b.WriteString(original)
				} else {
					b.WriteString(tok)
				}
				i = end
				continue
			}
			// Unterminated bracket — flush rest verbatim.
			b.WriteString(text[i:])
			break
		}

		if isBareTokenBoundary(text, i-1) {
			if alias, original, ok := mapping.matchBareAlias(text[i:], bareAliases); ok && isBareTokenBoundary(text, i+len(alias)) {
				b.WriteString(original)
				i += len(alias)
				continue
			}
		}

		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

func (m *PIIMapping) bareAliasesByLength() []string {
	aliases := make([]string, 0, len(m.tokenAliases)+len(m.tokenToOriginal))
	for token := range m.tokenToOriginal {
		aliases = append(aliases, strings.Trim(token, "[]"))
	}
	for alias := range m.tokenAliases {
		if !strings.HasPrefix(alias, "[") {
			aliases = append(aliases, alias)
		}
	}
	sort.SliceStable(aliases, func(i, j int) bool {
		if len(aliases[i]) == len(aliases[j]) {
			return aliases[i] < aliases[j]
		}
		return len(aliases[i]) > len(aliases[j])
	})
	return aliases
}

// BareTokenAliases returns tolerated bracketless placeholder forms sorted from
// longest to shortest. It is used by streaming restorers to avoid emitting a
// partial alias before the next chunk arrives.
func (m *PIIMapping) BareTokenAliases() []string {
	if m == nil || m.Len() == 0 {
		return nil
	}
	return m.bareAliasesByLength()
}

func (m *PIIMapping) matchBareAlias(text string, aliases []string) (string, string, bool) {
	for _, alias := range aliases {
		if len(text) < len(alias) || !strings.EqualFold(text[:len(alias)], alias) {
			continue
		}
		if original, ok := m.Original(alias); ok {
			return alias, original, true
		}
	}
	return "", "", false
}

func isBareTokenBoundary(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	c := text[index]
	return !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_')
}

var placeholderCandidateRE = regexp.MustCompile(`\[[A-Za-z][A-Za-z0-9_ -]*[ _-][0-9]+\]|\b[A-Z][A-Z0-9_]*_[0-9]+\b`)

// UnresolvedTokens returns placeholder-looking tokens that remain after an
// attempted restore and whose entity type matches this request's PII mapping.
// The tokens themselves are synthetic and safe to log; original values are not
// returned.
func (m *PIIMapping) UnresolvedTokens(text string) []string {
	if m == nil || m.Len() == 0 || text == "" {
		return nil
	}
	knownTypes := m.knownPlaceholderTypes()
	if len(knownTypes) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, token := range placeholderCandidateRE.FindAllString(text, -1) {
		if _, ok := m.Original(token); ok {
			continue
		}
		typ := placeholderTokenType(token)
		if _, ok := knownTypes[typ]; !ok {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	sort.Strings(out)
	return out
}

func (m *PIIMapping) knownPlaceholderTypes() map[string]struct{} {
	types := make(map[string]struct{})
	for token := range m.tokenToOriginal {
		if typ := placeholderTokenType(token); typ != "" {
			types[typ] = struct{}{}
		}
	}
	for alias := range m.tokenAliases {
		if typ := placeholderTokenType(alias); typ != "" {
			types[typ] = struct{}{}
		}
	}
	return types
}

func placeholderTokenType(token string) string {
	token = normalizePlaceholderToken(token)
	idx := strings.LastIndexByte(token, '_')
	if idx <= 0 || idx == len(token)-1 {
		return ""
	}
	return token[:idx]
}

func normalizePlaceholderToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "[")
	token = strings.TrimSuffix(token, "]")
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(token))
	lastWasSep := false
	for i := 0; i < len(token); i++ {
		c := token[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
			lastWasSep = false
		case (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
			lastWasSep = false
		case c == '_' || c == '-' || c == ' ':
			if b.Len() > 0 && !lastWasSep {
				b.WriteByte('_')
				lastWasSep = true
			}
		default:
			return ""
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return ""
	}
	return out
}
