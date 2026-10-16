package catalog

import "strings"

// maxNameLen bounds a name, in bytes, so that Normalize, which never lengthens a name, fits the stack buffer
// ResolvePublic and Recognize use.
const maxNameLen = 200

// Normalize is the form names are compared in, as docs/domains/catalog.md「名字」 defines it: ASCII lower case, '.', '_'
// and white space as '-', runs of '-' as one, none beside a '/', and a trailing -YYYY-MM-DD as -YYYYMMDD. Digits are
// never added or removed, so versions that differ in their date stay apart.
func Normalize(name string) string {
	var buf [maxNameLen]byte
	if len(name) > maxNameLen {
		return string(appendNormalized(nil, name))
	}
	return string(appendNormalized(buf[:0], name))
}

// appendNormalized appends the normalized name to dst. It allocates only when dst is too small.
func appendNormalized(dst []byte, name string) []byte {
	name = strings.Trim(name, " \t\n\v\f\r")
	start := len(dst)
	for i := range len(name) {
		dst = appendFolded(dst, start, name[i])
	}
	return dashedDateToCompact(dst)
}

// separators are what a name's words may be joined with: they all become '-'.
const separators = " ._\t\n\v\f\r"

// fold is the byte in lower case, or '-' for a separator.
func fold(c byte) byte {
	switch {
	case 'A' <= c && c <= 'Z':
		return c + 'a' - 'A'
	case strings.IndexByte(separators, c) >= 0:
		return '-'
	}
	return c
}

// appendFolded adds one byte of the name. A '-' beside a '-' or a '/' is not added, and a '-' before a '/' is replaced
// by it. start is where the name began in dst, so a name starts as if after a '/', which a leading '-' does not follow.
func appendFolded(dst []byte, start int, c byte) []byte {
	c = fold(c)
	last := byte('/')
	if len(dst) > start {
		last = dst[len(dst)-1]
	}
	switch {
	case c == '-' && (last == '-' || last == '/'):
		return dst
	case c == '/' && last == '-':
		dst[len(dst)-1] = '/'
		return dst
	}
	return append(dst, c)
}

// dashedDateToCompact rewrites a trailing -YYYY-MM-DD as -YYYYMMDD, in place.
func dashedDateToCompact(b []byte) []byte {
	n := len(b)
	const pattern = "-dddd-dd-dd"
	if n < len(pattern) {
		return b
	}
	for i, p := range []byte(pattern) {
		c := b[n-len(pattern)+i]
		if p == 'd' && (c < '0' || c > '9') || p == '-' && c != '-' {
			return b
		}
	}
	copy(b[n-6:], b[n-5:n-3]) // the month over the second dash
	copy(b[n-4:], b[n-2:n])   // the day over the third
	return b[:n-2]
}

// hasDate reports whether a normalized name ends in a date: -YYYYMMDD, -MMDD or -MM-DD (-YYYY-MM-DD is already
// -YYYYMMDD). A name with one names its version.
func hasDate(norm string) bool {
	n := len(norm)
	switch {
	case n >= 9 && norm[n-9] == '-' && digits(norm[n-8:]):
		return true
	case n >= 5 && norm[n-5] == '-' && digits(norm[n-4:]):
		return true
	case n >= 6 && norm[n-6] == '-' && digits(norm[n-5:n-3]) && norm[n-3] == '-' && digits(norm[n-2:]):
		return true
	}
	return false
}

func digits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
