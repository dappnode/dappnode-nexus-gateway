package domain_test

import (
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestPIIMapping_DeterministicNumberingAndDedup(t *testing.T) {
	m := domain.NewPIIMapping()
	if got := m.Token("PERSON", "John Smith"); got != "[PERSON_1]" {
		t.Fatalf("first PERSON token = %q, want [PERSON_1]", got)
	}
	if got := m.Token("PERSON", "John Smith"); got != "[PERSON_1]" {
		t.Fatalf("repeated PERSON token = %q, want [PERSON_1]", got)
	}
	if got := m.Token("PERSON", "Jane Doe"); got != "[PERSON_2]" {
		t.Fatalf("second PERSON token = %q, want [PERSON_2]", got)
	}
	if got := m.Token("EMAIL", "a@b.com"); got != "[EMAIL_1]" {
		t.Fatalf("first EMAIL token = %q, want [EMAIL_1]", got)
	}
	if m.Len() != 3 {
		t.Fatalf("mapping size = %d, want 3", m.Len())
	}
	if v, ok := m.Original("[PERSON_2]"); !ok || v != "Jane Doe" {
		t.Fatalf("Original([PERSON_2]) = %q,%v want Jane Doe,true", v, ok)
	}
}

func TestApplyMask_BasicAndDedup(t *testing.T) {
	text := "Hi John, email John at john@x.com or +1-415-555-0124."
	entities := []domain.PIIEntity{
		{Type: "PERSON", Start: 3, End: 7, Score: 0.99},             // John
		{Type: "PERSON", Start: 15, End: 19, Score: 0.99},           // John
		{Type: "EMAIL", Start: 23, End: 33, Score: 0.99},            // john@x.com
		{Type: "PHONE", Start: 37, End: len(text) - 1, Score: 0.99}, // +1-415-555-0124
	}
	m := domain.NewPIIMapping()
	got := domain.ApplyMask(text, entities, m)
	want := "Hi [PERSON_1], email [PERSON_1] at [EMAIL_1] or [PHONE_1]."
	if got != want {
		t.Fatalf("ApplyMask:\n got = %q\nwant = %q", got, want)
	}
	if m.Len() != 3 {
		t.Fatalf("mapping size = %d, want 3 (PERSON_1 dedup)", m.Len())
	}
}

func TestApplyMask_DropsOverlapsAndInvalidSpans(t *testing.T) {
	text := "hello world"
	entities := []domain.PIIEntity{
		{Type: "A", Start: 0, End: 5},   // "hello"
		{Type: "B", Start: 3, End: 8},   // overlaps with A — drop
		{Type: "C", Start: -1, End: 2},  // invalid — drop
		{Type: "D", Start: 6, End: 100}, // out of range — drop
		{Type: "E", Start: 6, End: 11},  // "world"
	}
	got := domain.ApplyMask(text, entities, domain.NewPIIMapping())
	want := "[A_1] [E_1]"
	if got != want {
		t.Fatalf("ApplyMask:\n got = %q\nwant = %q", got, want)
	}
}

func TestUnmask_RestoresOriginalAndKeepsUnknownTokens(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("PERSON", "John")
	m.Token("EMAIL", "john@x.com")
	got := domain.Unmask("hi [PERSON_1], your email [EMAIL_1] (and [GHOST_5]).", m)
	want := "hi John, your email john@x.com (and [GHOST_5])."
	if got != want {
		t.Fatalf("Unmask:\n got = %q\nwant = %q", got, want)
	}
}

func TestUnmask_RestoresBracketlessAndShortAliases(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL_ADDRESS", "jane@example.com")
	m.Token("PHONE_NUMBER", "+1-415-555-0100")

	got := domain.Unmask("email=EMAIL_ADDRESS_1 alt=[EMAIL_1] phone=PHONE_1", m)
	want := "email=jane@example.com alt=jane@example.com phone=+1-415-555-0100"
	if got != want {
		t.Fatalf("Unmask aliases:\n got = %q\nwant = %q", got, want)
	}
}

func TestUnmask_RestoresSafePlaceholderRewrites(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL_ADDRESS", "jane@example.com")
	m.Token("PERSON", "Jane Doe")

	got := domain.Unmask("email=[Email Address 1] short=[email-1] person=[Person_1] bare=EMAIL_address_1", m)
	want := "email=jane@example.com short=jane@example.com person=Jane Doe bare=jane@example.com"
	if got != want {
		t.Fatalf("Unmask rewrites:\n got = %q\nwant = %q", got, want)
	}
}

func TestUnmask_DoesNotRestoreBareNaturalLanguagePhrase(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL_ADDRESS", "jane@example.com")

	got := domain.Unmask("leave bare words email address 1 alone, restore [email address 1]", m)
	want := "leave bare words email address 1 alone, restore jane@example.com"
	if got != want {
		t.Fatalf("Unmask natural phrase:\n got = %q\nwant = %q", got, want)
	}
}

func TestUnmask_DoesNotRestoreBareAliasInsideLongerIdentifier(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL_ADDRESS", "jane@example.com")

	got := domain.Unmask("keep XEMAIL_ADDRESS_1Y but restore EMAIL_ADDRESS_1", m)
	want := "keep XEMAIL_ADDRESS_1Y but restore jane@example.com"
	if got != want {
		t.Fatalf("Unmask boundary handling:\n got = %q\nwant = %q", got, want)
	}
}

func TestUnresolvedTokens_FindsKnownPIIPlaceholderTypesOnly(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL_ADDRESS", "jane@example.com")
	m.Token("PERSON", "Jane")

	got := m.UnresolvedTokens(`{"email":"EMAIL_ADDRESS_2","person":"[Person 9]","id":"ORDER_ID_1"}`)
	want := []string{"EMAIL_ADDRESS_2", "[Person 9]"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("UnresolvedTokens = %#v, want %#v", got, want)
	}
}

func TestPIIMapping_MaskKnownOriginals(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("PERSON", "John")
	m.Token("EMAIL", "john@x.com")

	got := m.MaskKnownOriginals("provider echoed John <john@x.com>")
	want := "provider echoed [PERSON_1] <[EMAIL_1]>"
	if got != want {
		t.Fatalf("MaskKnownOriginals:\n got = %q\nwant = %q", got, want)
	}
}

func TestUnmask_HandlesUnterminatedAndEmpty(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("PERSON", "John")
	if got := domain.Unmask("", m); got != "" {
		t.Fatalf("Unmask(\"\") = %q, want empty", got)
	}
	if got := domain.Unmask("no tokens here", m); got != "no tokens here" {
		t.Fatalf("Unmask passthrough = %q", got)
	}
	if got := domain.Unmask("hi [PERSON_1 partial", m); got != "hi [PERSON_1 partial" {
		t.Fatalf("Unmask unterminated = %q", got)
	}
}

func TestApplyMask_RoundTripWithUnmask(t *testing.T) {
	text := "Contact Alice at alice@example.com about the meeting with Alice."
	entities := []domain.PIIEntity{
		{Type: "PERSON", Start: 8, End: 13},  // Alice
		{Type: "EMAIL", Start: 17, End: 36},  // alice@example.com
		{Type: "PERSON", Start: 58, End: 63}, // Alice
	}
	m := domain.NewPIIMapping()
	masked := domain.ApplyMask(text, entities, m)
	if strings.Contains(masked, "Alice") || strings.Contains(masked, "alice@example.com") {
		t.Fatalf("masked text still contains PII: %q", masked)
	}
	if got := domain.Unmask(masked, m); got != text {
		t.Fatalf("round trip failed:\n got = %q\nwant = %q", got, text)
	}
}
