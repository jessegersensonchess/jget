package emailparser

import (
	"encoding/base64"
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

// Pre-compiled regexes avoid recompilation on every call.
var (
	reVarA      = regexp.MustCompile(`\bvar\s+a\s*=\s*'([^']*)'`)
	reClassAttr = regexp.MustCompile(`\bclass\s*=\s*"([^"]+)"`)

	// looksLikeEmail is intentionally conservative; see function docs below.
	reEmail = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
)

// DecodeEmailFromScript extracts an email address from an inline JavaScript snippet
// that uses simple obfuscation.
//
// This function is intentionally "no browser" and "no JS execution": it does not
// evaluate JavaScript, does not emulate the DOM, and does not load third-party
// resources. Instead, it relies on common patterns found in the wild:
//
//   - The candidate email is embedded as the value of "var a='...'" where
//     characters may be HTML-entity encoded (e.g. '&#64;' for '@').
//
//   - A Base64-encoded JSON token is embedded in the generated HTML class
//     attribute, often alongside the classes "email", "emailLink", or "required".
//     The decoded JSON provides directives describing how to de-obfuscate:
//
//     {"rot":"it"}          -> apply ROT13 to the whole email string
//     {"rmv":"<substr>"}    -> remove injected substring(s)
//     {"h":"m"}             -> single-character substitution; here it means
//     real 'h' was replaced by obfuscated 'm'
//
// DecodeEmailFromScript applies these directives in the following order:
//
//  1. HTML-unescape the var a string.
//  2. Apply all "rmv" removals.
//  3. Apply single-character substitutions (reverse mapping obf -> real).
//  4. Apply ROT13 if requested.
//  5. Validate the final string with a conservative email regex.
//
// If the script does not match the expected patterns or the decoded value does
// not look like an email, the function returns an empty string.
//
// This function is designed for extraction pipelines: callers should treat an
// empty string as "no email found / could not decode".
// DecodeEmailFromScript extracts an email address from an inline JavaScript snippet
// that uses simple obfuscation.
//
// (doc comment unchanged)
func DecodeEmailFromScript(script string) string {
	// Extract the email candidate: var a='...'
	m := reVarA.FindStringSubmatch(script)
	if len(m) != 2 {
		return ""
	}

	email := strings.TrimSpace(html.UnescapeString(m[1]))

	// Strip mailto early when it is present in the clear.
	// Note: this is not sufficient for ROT13-obfuscated "mailto:" (znvygb:),
	// so we strip again near the end after all transformations.
	email = strings.TrimPrefix(email, "mailto:")

	// Extract directives from Base64 JSON tokens in relevant class attributes.
	dirs := extractDirectivesFromClasses(script)

	// Apply substring removals first. These typically remove injected noise.
	for _, rm := range dirs.removals {
		if rm == "" {
			continue
		}
		email = strings.ReplaceAll(email, rm, "")
	}

	// Apply single-character substitutions (reverse mapping: obf -> real).
	if len(dirs.substObfToReal) > 0 {
		email = applyCharSubst(email, dirs.substObfToReal)
	}

	// Apply ROT13 if requested by directive.
	if dirs.rot13 {
		email = rot13(email)
	}

	email = strings.TrimSpace(email)

	// Strip mailto again after transformations to handle ROT13 cases where
	// "mailto:" appears only after decoding.
	email = strings.TrimPrefix(email, "mailto:")

	email = strings.TrimSpace(email)
	if looksLikeEmail(email) {
		return email
	}
	return ""
}

// decodeDirectives holds de-obfuscation instructions discovered in the script.
type decodeDirectives struct {
	rot13          bool
	removals       []string      // substrings to remove (rmv)
	substObfToReal map[rune]rune // reverse mapping for single-char substitutions
}

// extractDirectivesFromClasses scans class="..." attributes in the script and
// extracts Base64 JSON directive tokens.
//
// To reduce false positives, it only considers class attributes typically used
// for the email element: those containing "email", "emailLink", or "required".
// This avoids accidentally treating unrelated CSS classes as directive tokens.
//
// The returned substitution map is reverse-mapped (obfuscated -> real) so it
// can be applied directly to the obfuscated email string.
func extractDirectivesFromClasses(script string) decodeDirectives {
	out := decodeDirectives{
		substObfToReal: map[rune]rune{},
	}

	classAttrs := reClassAttr.FindAllStringSubmatch(script, -1)
	for _, ca := range classAttrs {
		if len(ca) != 2 {
			continue
		}
		classVal := ca[1]

		// Only consider directive-bearing email classes.
		if !(strings.Contains(classVal, "email") ||
			strings.Contains(classVal, "emailLink") ||
			strings.Contains(classVal, "required")) {
			continue
		}

		for _, tok := range strings.Fields(classVal) {
			// Base64 JSON tokens in your data are short and "eyJ...".
			// Keep bounds conservative to avoid wasting cycles on regular CSS classes.
			if len(tok) < 8 || len(tok) > 80 {
				continue
			}

			obj, ok := tryDecodeBase64JSON(tok)
			if !ok {
				continue
			}

			if v, ok := obj["rot"]; ok && v == "it" {
				out.rot13 = true
			}
			if v, ok := obj["rmv"]; ok && v != "" {
				out.removals = append(out.removals, v)
			}

			// Single-char substitution entries: {"h":"m"} means real 'h' -> obf 'm'.
			// Reverse for decoding: obf 'm' -> real 'h'.
			for k, v := range obj {
				if k == "rot" || k == "rmv" {
					continue
				}
				kr := []rune(k)
				vr := []rune(v)
				if len(kr) == 1 && len(vr) == 1 {
					out.substObfToReal[vr[0]] = kr[0]
				}
			}
		}
	}

	return out
}

// tryDecodeBase64JSON attempts to decode token as Base64 (standard or URL-safe)
// and unmarshal it as a JSON object of string keys and values.
//
// It returns (obj, true) on success and (nil, false) otherwise.
func tryDecodeBase64JSON(token string) (map[string]string, bool) {
	dec := func(s string, urlSafe bool) ([]byte, error) {
		for len(s)%4 != 0 {
			s += "="
		}
		if urlSafe {
			return base64.URLEncoding.DecodeString(s)
		}
		return base64.StdEncoding.DecodeString(s)
	}

	b, err := dec(token, false)
	if err != nil {
		b, err = dec(token, true)
		if err != nil {
			return nil, false
		}
	}

	var obj map[string]string
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, false
	}
	if len(obj) == 0 {
		return nil, false
	}
	return obj, true
}

// applyCharSubst replaces runes in s using the provided obfuscated->real map.
func applyCharSubst(s string, obfToReal map[rune]rune) string {
	rs := []rune(s)
	for i, r := range rs {
		if rr, ok := obfToReal[r]; ok {
			rs[i] = rr
		}
	}
	return string(rs)
}

// looksLikeEmail returns true if s matches a conservative email pattern.
//
// This is not a full RFC 5322 parser. It intentionally avoids complex edge cases
// because the extractor is meant for typical business contact emails.
func looksLikeEmail(s string) bool {
	return reEmail.MatchString(strings.TrimSpace(s))
}

// rot13 applies the ROT13 substitution cipher to ASCII letters in s.
func rot13(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune('a' + (r-'a'+13)%26)
		case r >= 'A' && r <= 'Z':
			b.WriteRune('A' + (r-'A'+13)%26)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
