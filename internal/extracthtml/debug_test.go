package extracthtml

import (
	"bytes"
	"testing"
)

// TestDebugPrintSelector_TextOnly verifies "-text" debug mode prints trimmed text
// and adds a blank line between matches.
//
// This covers the textOnly=true branch of DebugPrintSelector.
func TestDebugPrintSelector_TextOnly(t *testing.T) {
	t.Parallel()

	html := `<div id="x">  A  </div><div id="x">B</div>`
	var buf bytes.Buffer

	if err := DebugPrintSelector(&buf, html, "div#x", true); err != nil {
		t.Fatalf("DebugPrintSelector: %v", err)
	}

	// Each match prints one trimmed line, followed by an extra newline.
	want := "A\n\nB\n\n"
	if buf.String() != want {
		t.Fatalf("unexpected output:\nwant=%q\ngot=%q", want, buf.String())
	}
}

// TestDebugPrintSelector_OuterHTML verifies the non-text mode prints outer HTML.
//
// This covers the textOnly=false path, which calls goquery.OuterHtml.
func TestDebugPrintSelector_OuterHTML(t *testing.T) {
	t.Parallel()

	html := `<div id="x"><span>Hi</span></div>`
	var buf bytes.Buffer

	if err := DebugPrintSelector(&buf, html, "div#x", false); err != nil {
		t.Fatalf("DebugPrintSelector: %v", err)
	}

	out := buf.String()
	// We don't assert exact formatting (goquery may normalize), but we do assert
	// it includes the expected structure and prints a trailing blank line.
	if !(bytes.Contains([]byte(out), []byte(`<div id="x">`)) &&
		bytes.Contains([]byte(out), []byte(`<span>Hi</span>`))) {
		t.Fatalf("unexpected outer html output: %q", out)
	}
	if out[len(out)-2:] != "\n\n" {
		t.Fatalf("expected trailing blank line, got %q", out)
	}
}
