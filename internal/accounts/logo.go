package accounts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// A site's logo (docs/domains/accounts.md「站点」): an SVG sanitised to an allowlist, or a PNG or WebP checked for its
// real dimensions. The limits bound both the bytes stored and the work a page does to draw them; the console shows the
// logo at 20px, so 1024 is already far more than any use.
const (
	maxLogoBytes     = 256 << 10
	maxLogoDimension = 1024
	maxSVGElements   = 4096
	maxSVGDepth      = 64
)

// The content types a logo may have, as the contract and the table name them.
const (
	LogoSVG  = "image/svg+xml"
	LogoPNG  = "image/png"
	LogoWebP = "image/webp"
)

// Logo is a parsed logo: the bytes a page gets, and what the API tells the admin about them.
type Logo struct {
	ContentType string
	Width       int // 0 for an SVG with no intrinsic size
	Height      int
	Hash        [sha256.Size]byte // SHA-256 of Bytes, and the whole URL's identity
	Bytes       []byte
}

// HashHex is the hash as the URL and the API write it.
func (l Logo) HashHex() string { return hex.EncodeToString(l.Hash[:]) }

// ParseLogo checks and normalises an uploaded logo. data nil is not an empty logo: the caller clears the site's logo
// separately. The returned content type is one of the three constants, and Bytes is what will be served.
func ParseLogo(contentType string, data []byte) (Logo, error) {
	if len(data) == 0 {
		return Logo{}, logoError("the file is empty")
	}
	if len(data) > maxLogoBytes {
		return Logo{}, logoError(fmt.Sprintf("the file is larger than %d bytes", maxLogoBytes))
	}
	var (
		logo Logo
		err  error
	)
	switch contentType {
	case LogoPNG:
		logo, err = rasterLogo(contentType, data, pngSize)
	case LogoWebP:
		logo, err = rasterLogo(contentType, data, webpSize)
	case LogoSVG:
		logo, err = svgLogo(data)
	default:
		return Logo{}, logoError("the file must be an SVG, PNG or WebP image")
	}
	if err != nil {
		return Logo{}, err
	}
	logo.Hash = sha256.Sum256(logo.Bytes)
	return logo, nil
}

func logoError(reason string) *SettingError { return &SettingError{Field: "logo", Reason: reason} }

func rasterLogo(contentType string, data []byte, size func([]byte) (int, int, error)) (Logo, error) {
	width, height, err := size(data)
	if err != nil {
		return Logo{}, logoError("the file is not a readable " + strings.TrimPrefix(contentType, "image/") + " image")
	}
	if width < 1 || height < 1 || width > maxLogoDimension || height > maxLogoDimension {
		return Logo{}, logoError(fmt.Sprintf("the image is %dx%d; its sides must be from 1 to %d pixels", width, height, maxLogoDimension))
	}
	return Logo{ContentType: contentType, Width: width, Height: height, Bytes: data}, nil
}

// pngSize reads the PNG header through the standard library's decoder, which checks the signature and IHDR without
// decoding the image data.
func pngSize(data []byte) (int, int, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// webpSize reads the canvas size from the WebP header. The three chunk kinds carry it differently, and all of them are
// little-endian; parsing the header keeps the standard library out of the module for one number pair.
func webpSize(data []byte) (int, int, error) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, fmt.Errorf("not a WebP file")
	}
	chunk := string(data[12:16])
	size := int(uint32(data[16]) | uint32(data[17])<<8 | uint32(data[18])<<16 | uint32(data[19])<<24)
	if size < 10 || 20+size > len(data) {
		return 0, 0, fmt.Errorf("the WebP chunk is truncated")
	}
	switch chunk {
	case "VP8X": // extended: the canvas size, which may be larger than any one frame
		return webpVP8X(data)
	case "VP8 ": // lossy: the frame header after its 3-byte tag and the 3-byte start code
		return webpVP8(data)
	case "VP8L": // lossless: 14 bits of width-1 then 14 bits of height-1 from the first four bytes
		return webpVP8L(data)
	default:
		return 0, 0, fmt.Errorf("the WebP chunk %q has no image size", chunk)
	}
}

func webpVP8X(data []byte) (int, int, error) {
	width := (int(data[24]) | int(data[25])<<8 | int(data[26])<<16) + 1
	height := (int(data[27]) | int(data[28])<<8 | int(data[29])<<16) + 1
	return width, height, nil
}

func webpVP8(data []byte) (int, int, error) {
	if data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
		return 0, 0, fmt.Errorf("the WebP frame header is invalid")
	}
	width := int(data[26]) | int(data[27]&0x3f)<<8
	height := int(data[28]) | int(data[29]&0x3f)<<8
	return width, height, nil
}

func webpVP8L(data []byte) (int, int, error) {
	if data[20] != 0x2f {
		return 0, 0, fmt.Errorf("the WebP lossless header is invalid")
	}
	bits := uint32(data[21]) | uint32(data[22])<<8 | uint32(data[23])<<16 | uint32(data[24])<<24
	return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, nil
}

// The SVG elements a logo may keep. Scripts, foreign content, images, links, animation and styles are absent: an SVG is
// sanitised by keeping only what draws, so a file from a design tool loses its metadata and stays a picture.
var svgElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "symbol": true, "use": true, "switch": true,
	"path": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true,
	"text": true, "tspan": true, "title": true, "desc": true,
	"clipPath": true, "mask": true, "pattern": true, "marker": true,
	"linearGradient": true, "radialGradient": true, "stop": true,
	"filter": true, "feGaussianBlur": true, "feOffset": true, "feMerge": true, "feMergeNode": true,
	"feColorMatrix": true, "feBlend": true, "feComposite": true, "feFlood": true,
}

// The attributes worth keeping. Event handlers (`on…`), `style` and any namespace but the SVG one and xlink are
// dropped; URL-bearing values are checked by safeSVGValue below.
var svgAttributes = map[string]bool{
	"id": true, "xmlns": true,
	"width": true, "height": true, "viewBox": true, "preserveAspectRatio": true,
	"x": true, "y": true, "x1": true, "y1": true, "x2": true, "y2": true,
	"dx": true, "dy": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true, "fx": true, "fy": true,
	"d": true, "points": true, "transform": true, "clipPathUnits": true, "gradientUnits": true,
	"gradientTransform": true, "patternUnits": true, "patternContentUnits": true, "markerWidth": true,
	"markerHeight": true, "refX": true, "refY": true, "orient": true, "offset": true,
	"fill": true, "fill-opacity": true, "fill-rule": true, "stroke": true, "stroke-width": true,
	"stroke-linecap": true, "stroke-linejoin": true, "stroke-opacity": true, "stroke-dasharray": true,
	"stroke-dashoffset": true, "stroke-miterlimit": true, "opacity": true, "color": true,
	"clip-path": true, "clip-rule": true, "mask": true, "filter": true, "stop-color": true, "stop-opacity": true,
	"font-family": true, "font-size": true, "font-weight": true, "font-style": true, "text-anchor": true,
	"dominant-baseline": true, "letter-spacing": true, "word-spacing": true,
	"href": true, "xlink:href": true,
}

var svgTextElements = map[string]bool{"text": true, "tspan": true, "title": true, "desc": true}

var (
	// A paint value: a colour name, a hex colour, a functional colour, or a reference to a gradient or pattern defined
	// in this same file.
	svgPaint = regexp.MustCompile(`(?i)^(none|currentcolor|transparent|[a-z]+|#[0-9a-f]{3,8}|rgba?\([0-9.,% /-]+\)|hsla?\([0-9.,% /-]+\)|url\(\s*#[a-z_][a-z0-9_.:-]*\s*\))$`)
	// A reference to another element of this file, for filter, clip-path, mask, fill and stroke.
	svgRef  = regexp.MustCompile(`(?i)^(none|url\(\s*#[a-z_][a-z0-9_.:-]*\s*\))$`)
	svgID   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,63}$`)
	svgHref = regexp.MustCompile(`^#[A-Za-z_][A-Za-z0-9_.:-]*$`)
)

const (
	svgNamespace   = "http://www.w3.org/2000/svg"
	xlinkNamespace = "http://www.w3.org/1999/xlink"
)

// svgLogo parses, sanitises and re-encodes an SVG. It is deliberately not a general SVG implementation: it keeps the
// drawing elements and presentation attributes a logo needs, drops everything else (script, event handlers, <style>,
// <image>, links, external references), and rejects a document with a DTD, which is where entity expansion would come
// from. The re-encoded bytes, not the upload, are what is hashed and served.
func svgLogo(data []byte) (Logo, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	b := &svgBuilder{}
	b.encoder = xml.NewEncoder(&b.out)
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Logo{}, logoError("the SVG is not a well-formed XML file")
		}
		if err := b.token(token); err != nil {
			return Logo{}, err
		}
	}
	if !b.root || len(b.stack) != 0 {
		return Logo{}, logoError("the SVG is not one complete <svg> document")
	}
	if err := b.encoder.Flush(); err != nil {
		return Logo{}, logoError("the SVG could not be encoded")
	}
	if b.out.Len() > maxLogoBytes {
		return Logo{}, logoError(fmt.Sprintf("the sanitised SVG is larger than %d bytes", maxLogoBytes))
	}
	return Logo{ContentType: LogoSVG, Width: b.width, Height: b.height, Bytes: b.out.Bytes()}, nil
}

// svgBuilder writes the kept tokens of one SVG, tracking the elements still open and how much of the file it has kept.
type svgBuilder struct {
	out      bytes.Buffer
	encoder  *xml.Encoder
	stack    []string
	skipping int
	root     bool
	elements int
	width    int
	height   int
}

func (b *svgBuilder) token(token xml.Token) error {
	switch t := token.(type) {
	case xml.Directive:
		return logoError("the SVG must not carry a document type declaration")
	case xml.ProcInst, xml.Comment:
		// An XML declaration, a stylesheet instruction or a comment draws nothing and may carry a reference.
		return nil
	case xml.StartElement:
		return b.start(t)
	case xml.EndElement:
		return b.end(t)
	case xml.CharData:
		return b.text(t)
	default:
		return nil
	}
}

// begin checks the document's one root before the element is kept.
func (b *svgBuilder) begin(t xml.StartElement) error {
	if !b.root {
		if t.Name.Local != "svg" || (t.Name.Space != "" && t.Name.Space != svgNamespace) {
			return logoError("the SVG has no <svg> root element")
		}
		b.root = true
		return nil
	}
	if len(b.stack) == 0 {
		return logoError("the SVG has more than one root element")
	}
	return nil
}

func (b *svgBuilder) start(t xml.StartElement) error {
	if b.skipping > 0 {
		b.skipping++
		return nil
	}
	if err := b.begin(t); err != nil {
		return err
	}
	name := t.Name.Local
	if !svgElements[name] {
		b.skipping = 1
		return nil
	}
	b.elements++
	if b.elements > maxSVGElements || len(b.stack) >= maxSVGDepth {
		return logoError("the SVG has too many elements or is nested too deeply")
	}
	attrs := safeSVGAttributes(t.Attr)
	if len(b.stack) == 0 {
		width, height, err := svgDimensions(t.Attr)
		if err != nil {
			return err
		}
		b.width, b.height = width, height
		attrs = rootSVGAttributes(attrs)
	}
	if err := b.encoder.EncodeToken(xml.StartElement{Name: xml.Name{Local: name}, Attr: attrs}); err != nil {
		return logoError("the SVG could not be encoded")
	}
	b.stack = append(b.stack, name)
	return nil
}

func (b *svgBuilder) end(t xml.EndElement) error {
	if b.skipping > 0 {
		b.skipping--
		return nil
	}
	if len(b.stack) == 0 {
		return logoError("the SVG is not well formed")
	}
	b.stack = b.stack[:len(b.stack)-1]
	if err := b.encoder.EncodeToken(xml.EndElement{Name: xml.Name{Local: t.Name.Local}}); err != nil {
		return logoError("the SVG could not be encoded")
	}
	return nil
}

func (b *svgBuilder) text(t xml.CharData) error {
	if b.skipping != 0 || len(b.stack) == 0 || !svgTextElements[b.stack[len(b.stack)-1]] {
		return nil
	}
	if err := b.encoder.EncodeToken(t); err != nil {
		return logoError("the SVG could not be encoded")
	}
	return nil
}

// rootSVGAttributes gives the output one self-contained SVG namespace; a prefix from the upload is not kept.
func rootSVGAttributes(attrs []xml.Attr) []xml.Attr {
	return append([]xml.Attr{
		{Name: xml.Name{Local: "xmlns"}, Value: svgNamespace},
		{Name: xml.Name{Local: "xmlns:xlink"}, Value: xlinkNamespace},
	}, attrs...)
}

// safeSVGAttributes keeps the attributes that draw, in the SVG namespace or xlink's, and checks every value that can
// name another resource: a URL must point inside this file.
func safeSVGAttributes(attrs []xml.Attr) []xml.Attr {
	kept := make([]xml.Attr, 0, len(attrs))
	for _, a := range attrs {
		if safe, ok := safeSVGAttribute(a); ok {
			kept = append(kept, safe)
		}
	}
	return kept
}

func safeSVGAttribute(a xml.Attr) (xml.Attr, bool) {
	local, space := a.Name.Local, a.Name.Space
	switch {
	case space == "xmlns" || local == "xmlns" || local == "xlink":
		return xml.Attr{}, false // the root's own declarations replace these
	case space != "" && (space != xlinkNamespace || local != "href"):
		return xml.Attr{}, false
	case !svgAttributes[local] || strings.HasPrefix(strings.ToLower(local), "on"):
		return xml.Attr{}, false
	}
	value := strings.TrimSpace(a.Value)
	if !safeSVGValue(local, value) {
		return xml.Attr{}, false
	}
	name := local
	if space == xlinkNamespace {
		name = "xlink:href"
	}
	return xml.Attr{Name: xml.Name{Local: name}, Value: value}, true
}

func safeSVGValue(local, value string) bool {
	switch local {
	case "id":
		return svgID.MatchString(value)
	case "href":
		return svgHref.MatchString(value)
	case "fill", "stroke", "color", "stop-color":
		return svgPaint.MatchString(value)
	case "filter", "clip-path", "mask":
		return svgRef.MatchString(value)
	default:
		return !strings.ContainsAny(value, "\\\x00")
	}
}

// svgDimensions reads a root's pixel size, falling back to its viewBox, and bounds both. A percentage or a missing size
// is 0: it scales, and the page decides how large it draws.
func svgDimensions(attrs []xml.Attr) (int, int, error) {
	width, height, viewBox := svgSizeAttributes(attrs)
	if width == 0 || height == 0 {
		viewWidth, viewHeight := svgViewBoxSize(viewBox)
		if width == 0 {
			width = viewWidth
		}
		if height == 0 {
			height = viewHeight
		}
	}
	if width < 0 || height < 0 || width > maxLogoDimension || height > maxLogoDimension {
		return 0, 0, logoError(fmt.Sprintf("the SVG is %dx%d; its sides must be at most %d", width, height, maxLogoDimension))
	}
	return width, height, nil
}

func svgSizeAttributes(attrs []xml.Attr) (int, int, string) {
	var width, height int
	var viewBox string
	for _, a := range attrs {
		switch a.Name.Local {
		case "width":
			width = svgLength(a.Value)
		case "height":
			height = svgLength(a.Value)
		case "viewBox":
			viewBox = a.Value
		}
	}
	return width, height, viewBox
}

func svgViewBoxSize(viewBox string) (int, int) {
	fields := strings.FieldsFunc(viewBox, func(r rune) bool { return r == ' ' || r == ',' })
	if len(fields) != 4 {
		return 0, 0
	}
	width, height := 0, 0
	if value, err := strconv.ParseFloat(fields[2], 64); err == nil {
		width = int(value + 0.5)
	}
	if value, err := strconv.ParseFloat(fields[3], 64); err == nil {
		height = int(value + 0.5)
	}
	return width, height
}

// svgLength reads a root width or height in pixels; a percentage, a unit other than px or an unreadable value is 0.
func svgLength(raw string) int {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimSuffix(raw, "px")
	if raw == "" || strings.HasSuffix(raw, "%") {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 {
		return 0
	}
	return int(value + 0.5)
}

// logoDetail is what an audit entry records of a logo: the hash and shape, never the bytes.
func logoDetail(l *Logo) map[string]any {
	if l == nil {
		return map[string]any{"present": false}
	}
	return map[string]any{
		"present": true, "content_type": l.ContentType, "hash": l.HashHex(),
		"width": l.Width, "height": l.Height, "size": len(l.Bytes),
	}
}
