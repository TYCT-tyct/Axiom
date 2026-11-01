package catalog

import (
	"bytes"
	"errors"
	"strings"

	"golang.org/x/net/html"
)

// The version keys stored snapshots. An upgrade does not by itself create a change review: savePageContent keeps the
// current snapshot when the version-1 extractor's exact view of the new response matches it, and refuses writes from an
// older version.
const pageExtractorVersion = 2

// zeroWidth is what rendering engines hide inside a word: Docusaurus puts one after a heading to anchor a link, and it is
// not content, so keeping it would change a page's hash when only the anchor moved.
var zeroWidth = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "")

// samePageContent compares the stored snapshot with the old extractor's view of the new response. It must not delete
// whitespace first: adjacent cells "1 23" and "12 3" would collapse to the same text and hide a price change. If the
// old extraction is unavailable, only an exact text match is safe.
func samePageContent(prior string, content PageContent) bool {
	if content.LegacyOK {
		return prior == content.LegacyText
	}
	return prior == content.Text
}

// Layout whitespace and scripts must not change price evidence; table cells still need a word boundary.
func extractPriceSection(body []byte, start, end string) (string, error) {
	normalized, err := normalizedPriceDocument(body)
	if err != nil {
		return "", err
	}
	return extractSection(normalized, start, end)
}

// legacyExtractPriceSection runs the pre-version-2 extractor on the same body. During an upgrade, comparing its exact
// output with the stored snapshot proves that the page itself did not change; comparing the new text after deleting
// whitespace cannot prove that.
func legacyExtractPriceSection(body []byte, start, end string) (string, error) {
	normalized, err := normalizedLegacyPriceDocument(body)
	if err != nil {
		return "", err
	}
	return extractSection(normalized, start, end)
}

func extractSection(normalized, start, end string) (string, error) {
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

// normalizedLegacyPriceDocument is the version-1 normalization, kept only to compare an upgrade with an existing
// snapshot. It deliberately retains the old adjacent-inline merge and zero-width behavior.
func normalizedLegacyPriceDocument(body []byte) (string, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var text strings.Builder
	appendLegacyPriceText(&text, priceDocumentRoot(root))
	return strings.Join(strings.Fields(text.String()), " "), nil
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

// appendLegacyPriceText is the version-1 walker. It reproduces the old output exactly, including the merge of
// adjacent inline elements, so an upgrade can compare apples with apples.
func appendLegacyPriceText(text *strings.Builder, n *html.Node) {
	if skipPriceNode(n) {
		return
	}
	if n.Type == html.TextNode {
		text.WriteString(n.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		appendLegacyPriceText(text, c)
	}
	if n.Type == html.ElementNode {
		switch n.Data {
		case "td", "th", "tr", "table", "div", "p", "br", "li", "h1", "h2", "h3", "h4", "section", "article":
			text.WriteByte(' ')
		}
	}
}

func appendWordBoundary(text *strings.Builder) {
	if s := text.String(); s != "" && s[len(s)-1] != ' ' {
		text.WriteByte(' ')
	}
}

// styleValue is the inline style with case and spaces removed, so "display: flex" and "DISPLAY:FLEX" compare alike.
func styleValue(value string) string { return strings.ToLower(strings.ReplaceAll(value, " ", "")) }

// separatesPriceText reports whether a node starts a new visual run. HTML block tags do; so do the layout classes these
// price pages use for a block or a flex/grid container. Adjacent inline spans with no such marker stay one word:
// "<span>$0</span><span>.10</span>" is "$0.10", and "<span>Pri</span><span>cing</span>" is "Pricing".
func separatesPriceText(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "address", "article", "aside", "blockquote", "br", "dd", "div", "dl", "dt", "fieldset", "figcaption",
		"figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main", "nav",
		"ol", "p", "pre", "section", "table", "tbody", "td", "tfoot", "th", "thead", "tr", "ul":
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "class" && separatesPriceClass(a.Val) || a.Key == "style" && separatesPriceStyle(a.Val) {
			return true
		}
	}
	return false
}

func separatesPriceClass(value string) bool {
	for class := range strings.FieldsSeq(value) {
		switch class {
		case "block", "flex", "grid", "table", "list-item", "icon",
			"material-icons", "material-icons-outlined", "material-symbols-outlined",
			"material-symbols-rounded", "material-symbols-sharp":
			return true
		}
	}
	return false
}

func separatesPriceStyle(value string) bool {
	style := styleValue(value)
	return strings.Contains(style, "display:block") || strings.Contains(style, "display:flex") ||
		strings.Contains(style, "display:grid") || strings.Contains(style, "display:table")
}

// stacksPriceChildren reports whether a node lays its children out on separate lines. A flex row does not; a flex
// column and a grid do. The pages express this with utility classes or inline styles, so both are recognized.
func stacksPriceChildren(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, a := range n.Attr {
		if a.Key == "class" && stacksPriceClass(a.Val) || a.Key == "style" && stacksPriceStyle(a.Val) {
			return true
		}
	}
	return false
}

func stacksPriceClass(value string) bool {
	for class := range strings.FieldsSeq(value) {
		switch class {
		case "flex-col", "flex-column", "grid":
			return true
		}
	}
	return false
}

func stacksPriceStyle(value string) bool {
	style := styleValue(value)
	return strings.Contains(style, "display:grid") ||
		strings.Contains(style, "display:flex") && strings.Contains(style, "flex-direction:column")
}

// wrotePriceText reports whether a child added visible text. A whitespace-only text node does not start a stacked run.
func wrotePriceText(c *html.Node, before, after int) bool {
	if c.Type == html.TextNode {
		return strings.TrimSpace(c.Data) != ""
	}
	return after > before
}

func appendPriceText(text *strings.Builder, n *html.Node) {
	if skipPriceNode(n) {
		return
	}
	if n.Type == html.TextNode {
		text.WriteString(n.Data)
	}
	stacked := stacksPriceChildren(n)
	seen := false
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		// A skipped node is invisible: it must not contribute a boundary between the text on either side of it.
		if skipPriceNode(c) {
			continue
		}
		if stacked && seen || separatesPriceText(c) {
			appendWordBoundary(text)
		}
		before := text.Len()
		appendPriceText(text, c)
		seen = seen || wrotePriceText(c, before, text.Len())
	}
	if separatesPriceText(n) {
		text.WriteByte(' ')
	}
}
