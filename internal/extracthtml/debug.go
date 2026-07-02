package extracthtml

import (
	"fmt"
	"io"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// DebugPrintSelector prints either outer HTML or text of matches for a selector.
// This is used by the command's "-selector" debug mode.
func DebugPrintSelector(w io.Writer, html, selector string, textOnly bool) error {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return fmt.Errorf("parse html: %w", err)
	}

	doc.Find(selector).Each(func(_ int, s *goquery.Selection) {
		if textOnly {
			fmt.Fprintln(w, strings.TrimSpace(s.Text()))
			fmt.Fprintln(w)
			return
		}
		out, err := goquery.OuterHtml(s)
		if err != nil {
			in, _ := s.Html()
			fmt.Fprintln(w, in)
			fmt.Fprintln(w)
			return
		}
		fmt.Fprintln(w, out)
		fmt.Fprintln(w)
	})
	return nil
}
