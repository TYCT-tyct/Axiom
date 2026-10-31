package catalog

import (
	"bytes"
	"errors"
	"strings"

	"golang.org/x/net/html"
)

// The version keys stored snapshots: raising it keeps text extracted the old way from being compared with text extracted
// the new way, which would look like every page changed at once.
const pageExtractorVersion = 2

// zeroWidth is what rendering engines hide inside a word: Docusaurus puts one after a heading to anchor a link, and it is
// not content, so keeping it would change a page's hash when only the anchor moved.
var zeroWidth = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "")

// Layout whitespace and scripts must not change price evidence; table cells still need a word boundary.
func extractPriceSection(body []byte, start, end string) (string, error) {
	normalized, err := normalizedPriceDocument(body)
	if err != nil {
		return "", err
	}
	start, end = strings.Join(strings.Fields(start), " "), strings.Join(strings.Fields(end), " ")
	// Ambiguous markers usually mean a layout change. Keeping the last evidence is safer than hashing a guessed slice.
	if start == "" || end == "" || strings.Count(normalized, start) != 1 {
		return "", errors.New("price section: start marker missing or ambiguous")
	}
	_, rest, _ := strings.Cut(normalized, start)
	if strings.Count(rest, end) != 1 {
		return "", errors.New("price section: end marker missing or ambiguous")
	}
	section, _, _ := strings.Cut(rest, end)
	section = strings.TrimSpace(section)
	if section == "" {
		return "", errors.New("price section: empty")
	}
	return section, nil
}

// A single replacement span is an exact, linear-time diff, even when every row of a large table changes.
func pageDifference(before, after string) (removed, added string) {
	a, b := []rune(before), []rune(after)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	return string(a[prefix : len(a)-suffix]), string(b[prefix : len(b)-suffix])
}

func priceDocumentRoot(root *html.Node) *html.Node {
	var article, main *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "article" && article == nil {
			article = n
		}
		if n.Type == html.ElementNode && n.Data == "main" && main == nil {
			main = n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(root)
	if article != nil {
		return article
	}
	if main != nil {
		return main
	}
	return root
}

func normalizedPriceDocument(body []byte) (string, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var text strings.Builder
	appendPriceText(&text, priceDocumentRoot(root))
	return strings.Join(strings.Fields(zeroWidth.Replace(text.String())), " "), nil
}

func skipPriceNode(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "head", "script", "style", "noscript", "template", "nav", "header", "footer", "aside":
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "hidden" || a.Key == "aria-hidden" && a.Val == "true" {
			return true
		}
	}
	return false
}

func appendWordBoundary(text *strings.Builder) {
	if s := text.String(); s != "" && s[len(s)-1] != ' ' {
		text.WriteByte(' ')
	}
}

func appendPriceText(text *strings.Builder, n *html.Node) {
	if skipPriceNode(n) {
		return
	}
	if n.Type == html.TextNode {
		text.WriteString(n.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		// Adjacent elements are separate runs even when both tags are inline: a flex column stacks a model's name over
		// its description, and a display:block span puts a price's qualifier on its own line. Merging them would turn
		// "Claude Sonnet 5.5The best…" and "$0.10 / MTokfor prompts…" into evidence a reviewer has to guess at.
		if c.Type == html.ElementNode && c.PrevSibling != nil && c.PrevSibling.Type == html.ElementNode {
			appendWordBoundary(text)
		}
		appendPriceText(text, c)
	}
	if n.Type == html.ElementNode {
		switch n.Data {
		case "td", "th", "tr", "table", "div", "p", "br", "li", "h1", "h2", "h3", "h4", "section", "article":
			text.WriteByte(' ')
		}
	}
}
