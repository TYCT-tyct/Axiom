package gateway

import (
	"slices"
	"strconv"
)

// orRoom is what the buffer is given beyond the page's entries: the envelope's own members and the next page's link.
const orRoom = 256

// encode is the list as the key sees it and the query keeps it, in the canonical form writeJSON gives (members in key
// order), made in one buffer sized for the page it serves. It counts first, so a key that sees few models, a query that
// keeps few and a page that holds few do not pay for the catalog: the buffer grows with the page, not with the matches.
func (l *openRouterListing) encode(v visibility, q orQuery) []byte {
	if q.sort != orCatalogOrder {
		return l.encodeOrdered(v, q)
	}
	matched, size := l.pageStats(v, q)
	return l.encodePage(v, q, matched, size)
}

func (l *openRouterListing) encodePage(v visibility, q orQuery, matched, size int) []byte {
	first, last := q.page(matched)
	next := q.next(last, matched)
	buf := append(make([]byte, 0, orRoom+len(next)+size), `{"data":[`...)
	seen, written := 0, 0
	for i := range l.models {
		e := l.models[i].forAudience(v.audience)
		if e == nil || !l.shows(v, e.model) || !q.keeps(e) {
			continue
		}
		if seen++; seen <= first || seen > last {
			continue
		}
		if written > 0 {
			buf = append(buf, ',')
		}
		written++
		buf = append(buf, e.body...)
	}
	return orFooter(buf, next, matched)
}

// encodeOrdered is encode with one of the orderings: the matches are gathered, ordered, and the page copied out of them.
// It allocates the matches as well as the buffer, which is why the list is served in the catalog's order unless a sort is
// asked for; the buffer itself is the page's, as in encode.
func (l *openRouterListing) encodeOrdered(v visibility, q orQuery) []byte {
	matches := make([]*orModel, 0, len(l.models))
	for i := range l.models {
		if e := l.models[i].forAudience(v.audience); e != nil && l.shows(v, e.model) && q.keeps(e) {
			matches = append(matches, e)
		}
	}
	slices.SortFunc(matches, q.compare)
	first, last := q.page(len(matches))
	next := q.next(last, len(matches))
	size := orRoom + len(next)
	for _, e := range matches[first:last] {
		size += len(e.body) + 1
	}
	buf := append(make([]byte, 0, size), `{"data":[`...)
	for i, e := range matches[first:last] {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, e.body...)
	}
	return orFooter(buf, next, len(matches))
}

// pageStats counts all matches but sizes only the page; subtraction avoids adding an unbounded offset to the limit.
func (l *openRouterListing) pageStats(v visibility, q orQuery) (matched, size int) {
	for i := range l.models {
		e := l.models[i].forAudience(v.audience)
		if e == nil || !l.shows(v, e.model) || !q.keeps(e) {
			continue
		}
		matched++
		if matched > q.offset && (q.limit == 0 || matched-q.offset <= q.limit) {
			size += len(e.body) + 1
		}
	}
	return matched, size
}

// orFooter writes the members after the data: the next page's link, null on the last page, and how many models the query
// matched in all.
func orFooter(buf []byte, next string, total int) []byte {
	buf = append(buf, `],"links":{"next":`...)
	if next == "" {
		buf = append(buf, "null"...)
	} else {
		buf = append(buf, '"')
		buf = append(buf, next...)
		buf = append(buf, '"')
	}
	buf = append(buf, `},"total_count":`...)
	buf = strconv.AppendInt(buf, int64(total), 10)
	return append(buf, '}')
}
