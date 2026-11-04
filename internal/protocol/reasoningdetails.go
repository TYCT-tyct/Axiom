package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go: each piece is kept as written
	"slices"
	"strconv"
)

// mergeDetails joins the reasoning details a client hands back as it streamed them, an entry per chunk: entries of
// the same type and index are one detail (OpenRouter《Reasoning tokens》"Streaming Response", read 2026-10-15), whose
// text and summary are its pieces in order and whose signature, data, ID and format are the ones given. Entries
// without an index stand alone. Each piece is copied once, so many pieces cost what their bytes do.
func mergeDetails(details []canonReasoningDetail) ([]canonReasoningDetail, error) {
	return mergeDetailsWithin(details, nil)
}

// mergeDetailsWithin is mergeDetails drawing every allocation from b first.
func mergeDetailsWithin(details []canonReasoningDetail, b *budget) ([]canonReasoningDetail, error) {
	m := detailMerge{b: b}
	at := map[string]int{} // each detail's index in out, by its type and index
	for _, d := range details {
		i, err := m.slot(at, d)
		if err != nil {
			return nil, err
		}
		m.text[i].b, m.summary[i].b = b, b
		if err := m.text[i].add(d.Text); err != nil {
			return nil, err
		}
		if err := m.summary[i].add(d.Summary); err != nil {
			return nil, err
		}
	}
	for i := range m.out {
		m.out[i].Text, m.out[i].Summary = m.text[i].value(m.out[i].Text), m.summary[i].value(m.out[i].Summary)
	}
	if b != nil && b.err != nil {
		return nil, b.err
	}
	return m.out, nil
}

// detailMerge is mergeDetails' state: the merged details and the text and summary pieces each has gathered, at the
// same index. The index by type and index stays out of it, a map of the caller's: escape analysis does not tell one
// field from another, and the slices grown here are on the heap, so a map in this struct would be too.
type detailMerge struct {
	out           []canonReasoningDetail
	text, summary []joined
	b             *budget
}

// slot is d's detail in out: the one an earlier entry of its type and index began, which takes the fields d gives, or a
// new one d begins.
func (m *detailMerge) slot(at map[string]int, d canonReasoningDetail) (int, error) {
	index, err := reasoningIndex(d.Index)
	if err != nil {
		return 0, err
	}
	if err := m.b.draw(int64(len(d.Type) + 1 + len(index))); err != nil {
		return 0, err
	}
	key := d.Type + "\x00" + string(index)
	i, seen := at[key]
	if !seen || isNull(d.Index) {
		return m.begin(at, key, d)
	}
	e := &m.out[i]
	e.Signature, e.Data, e.ID = given(e.Signature, d.Signature), given(e.Data, d.Data), given(e.ID, d.ID)
	if e.Format == "" {
		e.Format = d.Format
	}
	return i, nil
}

// begin adds the detail d begins under key.
func (m *detailMerge) begin(at map[string]int, key string, d canonReasoningDetail) (int, error) {
	i := len(m.out)
	if err := m.b.draw(mapEntryMemory); err != nil {
		return 0, err
	}
	at[key] = i
	var err error
	if m.out, err = growWithin(m.out, m.b); err != nil {
		return 0, err
	}
	m.out[i] = d
	if m.text, err = growWithin(m.text, m.b); err != nil {
		return 0, err
	}
	if m.summary, err = growWithin(m.summary, m.b); err != nil {
		return 0, err
	}
	return i, nil
}

// joined gathers the pieces of a JSON string: their contents, quotes off, one after another, as each escape lies
// whole within its piece. Its growth is drawn from b; a draw that fails is left in b.err for the caller to check.
type joined struct {
	pieces int
	only   jsontext.Value // the piece, while there is one
	body   []byte
	b      *budget
}

func (j *joined) add(piece jsontext.Value) error {
	switch {
	case isNull(piece):
		return nil
	case piece.Kind() != '"':
		return shapeError("a reasoning detail's text that is not a string")
	}
	j.pieces++
	j.only = piece
	var err error
	if j.body, err = growBytes(j.body, len(piece)-2, j.b); err != nil {
		return err
	}
	j.body = append(j.body, piece[1:len(piece)-1]...)
	return nil
}

// value is the joined string: the piece as written when there was one, unset as it was when none.
func (j *joined) value(unset jsontext.Value) jsontext.Value {
	switch j.pieces {
	case 0:
		return unset
	case 1:
		return j.only
	}
	if j.b.draw(rounded(int64(len(j.body)+2))) != nil {
		return nil
	}
	return slices.Concat([]byte{'"'}, j.body, []byte{'"'})
}

// reasoningIndex gives equivalent integer spellings the same grouping key.
func reasoningIndex(v jsontext.Value) (jsontext.Value, error) {
	if isNull(v) {
		return nil, nil
	}
	n, err := strconv.ParseInt(string(v), 10, 64)
	if err != nil || n < 0 {
		return nil, shapeError("a reasoning detail index that is not a non-negative integer")
	}
	if n == 0 {
		return zeroReasoningIndex, nil
	}
	return v, nil
}

var zeroReasoningIndex = jsontext.Value("0")
