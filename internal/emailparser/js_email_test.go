package emailparser

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// TestDecodeEmailFromScript_NoVarA verifies the function fails closed.
//
// Rationale:
// The decoder is intended for ETL pipelines where false positives are worse
// than false negatives. If the canonical "var a='...'" pattern is missing,
// the safest behavior is to return empty string.
func TestDecodeEmailFromScript_NoVarA(t *testing.T) {
	t.Parallel()

	got := DecodeEmailFromScript(`console.log("no email here");`)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

// TestDecodeEmailFromScript_BasicHTMLUnescape verifies we HTML-unescape the
// var a payload.
//
// Rationale:
// Some pages embed '&#64;' for '@' to deter naive scraping. We must
// decode entities before validation.
func TestDecodeEmailFromScript_BasicHTMLUnescape(t *testing.T) {
	t.Parallel()

	script := `var a='me&#64;example.com';`
	got := DecodeEmailFromScript(script)
	if got != "me@example.com" {
		t.Fatalf("expected %q, got %q", "me@example.com", got)
	}
}

// TestDecodeEmailFromScript_MailtoPrefix verifies we strip a leading mailto:.
//
// Rationale:
// The caller wants a clean email address, not a URI.
func TestDecodeEmailFromScript_MailtoPrefix(t *testing.T) {
	t.Parallel()

	script := `var a='mailto:me@example.com';`
	got := DecodeEmailFromScript(script)
	if got != "me@example.com" {
		t.Fatalf("expected %q, got %q", "me@example.com", got)
	}
}

// TestDecodeEmailFromScript_Removals verifies rmv directives remove injected noise.
//
// Rationale:
// Many obfuscators inject substrings to break simple regex scrapers.
// The decoder must remove them before validation.
func TestDecodeEmailFromScript_Removals(t *testing.T) {
	t.Parallel()

	// email is "me+NOISE@example.com", directive removes "+NOISE".
	dir := mustB64JSON(t, map[string]string{"rmv": "+NOISE"})
	script := `var a='me+NOISE@example.com'; <span class="email ` + dir + `"></span>`
	got := DecodeEmailFromScript(script)

	if got != "me@example.com" {
		t.Fatalf("expected %q, got %q", "me@example.com", got)
	}
}

// TestDecodeEmailFromScript_Substitution verifies single-character substitution.
//
// Background:
// A directive like {"h":"q"} means: real 'h' was replaced by obfuscated 'q'.
// For decoding, we must reverse-map: 'q' -> 'h'.
//
// IMPORTANT:
// The chosen obfuscation character must not appear elsewhere in the email,
// otherwise unrelated characters would also be substituted (which is correct
// behavior, but makes the test brittle and misleading).
func TestDecodeEmailFromScript_Substitution(t *testing.T) {
	t.Parallel()

	// We want the final email "hi@example.org".
	// We'll obfuscate by replacing 'h' with 'q', so the script contains "qi@example.org".
	//
	// We intentionally use 'q' because it does not appear in "example.org",
	// ensuring the substitution only affects the intended character.
	dir := mustB64JSON(t, map[string]string{"h": "q"})
	script := `var a='qi@example.org'; <i class="emailLink ` + dir + `"></i>`

	got := DecodeEmailFromScript(script)
	if got != "hi@example.org" {
		t.Fatalf("expected %q, got %q", "hi@example.org", got)
	}
}

// TestDecodeEmailFromScript_ROT13 verifies rot13 directive application.
//
// Rationale:
// Some scrapers ROT13 the entire email string. The decoder must invert it.
func TestDecodeEmailFromScript_ROT13(t *testing.T) {
	t.Parallel()

	// ROT13("znvygb:zr@rknzcyr.pbz") == "mailto:me@example.com"
	// We include mailto: to prove both ROT13 and mailto stripping work together.
	dir := mustB64JSON(t, map[string]string{"rot": "it"})
	script := `var a='znvygb:zr@rknzcyr.pbz'; <b class="required ` + dir + `"></b>`

	got := DecodeEmailFromScript(script)
	if got != "me@example.com" {
		t.Fatalf("expected %q, got %q", "me@example.com", got)
	}
}

// TestDecodeEmailFromScript_DirectiveNotInEmailClasses verifies we ignore
// unrelated class attributes.
//
// Rationale:
// This test protects against false positives. The decoder intentionally only
// considers class attributes containing email-ish markers ("email", "emailLink",
// "required") so random Base64-looking CSS classes elsewhere are not applied.
func TestDecodeEmailFromScript_DirectiveNotInEmailClasses(t *testing.T) {
	t.Parallel()

	// This directive would remove "x", turning "mx@example.com" into "m@example.com"
	// (invalid by our conservative email regex). We keep it in a non-email class
	// so it must be ignored, and the output must be empty (invalid email).
	dir := mustB64JSON(t, map[string]string{"rmv": "x"})
	script := `var a='mx@example.com'; <div class="btn ` + dir + `"></div>`

	got := DecodeEmailFromScript(script)

	// "mx@example.com" is valid; but we used "mx@example.com" which is valid.
	// Wait: the decoder ignores the directive, so it returns mx@example.com.
	if got != "mx@example.com" {
		t.Fatalf("expected %q, got %q", "mx@example.com", got)
	}
}

// mustB64JSON is a test helper that encodes a map as JSON and returns
// a Base64 token suitable for embedding into class="...".
//
// We use standard base64 here; the decoder also supports URL-safe.
func mustB64JSON(t *testing.T, obj map[string]string) string {
	t.Helper()

	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}
