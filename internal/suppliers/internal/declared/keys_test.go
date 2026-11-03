package declared

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"axiom/internal/suppliers/internal/adapter"
)

func TestDecodeKeysStartsOverWhenThePageSizeChanges(t *testing.T) {
	t.Parallel()
	old := keysState{V: keysVersion, Page: 2, Size: 2, Rows: []keyRow{{Ref: "k1", Status: "active"}}}
	raw := encodeKeys(&old)
	st := decodeKeys(raw, 3)
	if st.Page != 1 || st.Size != 3 || len(st.Rows) != 0 {
		t.Fatalf("decoded %+v, want a fresh pass at size 3", st)
	}
}

// TestKeysRestartsWithADuplicate: a supplier that reorders its listing can repeat a ref on the next page. The pass
// starts over once; a second repeat is an error, not an endless loop.
func TestKeysRestartsWithADuplicate(t *testing.T) {
	t.Parallel()
	c := &keyPages{pages: [][]string{
		{"k1", "k2"},
		{"k2", "k3"},
		{"k1", "k2"},
		{"k3"},
	}}
	m := &manager{a: loadAcme(t), c: c}
	page, err := m.Keys(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Done || len(page.Items) != 3 {
		t.Fatalf("Keys = %+v, %v", page.Items, err)
	}
	if c.calls != 4 {
		t.Errorf("%d requests, want 4", c.calls)
	}
}

// TestKeysRestartsWhenTheTotalChanges: rows already read may include a key the supplier removed while the pass was in
// flight, so a changed total makes the pass start over.
func TestKeysRestartsWhenTheTotalChanges(t *testing.T) {
	t.Parallel()
	c := &keyPages{
		pages:  [][]string{{"k1", "k2"}, {"k3"}, {"k1", "k2"}, {"k3"}},
		totals: []int64{3, 2, 3, 3},
	}
	m := &manager{a: loadAcme(t), c: c}
	page, err := m.Keys(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Done || len(page.Items) != 3 {
		t.Fatalf("Keys = %+v, %v", page.Items, err)
	}
	if c.calls != 4 {
		t.Errorf("%d requests, want 4", c.calls)
	}
}

type keyPages struct {
	pages  [][]string
	totals []int64
	calls  int
}

func (c *keyPages) Do(context.Context, adapter.Request) (adapter.Response, error) {
	if c.calls >= len(c.pages) {
		return adapter.Response{}, fmt.Errorf("unexpected request %d", c.calls)
	}
	rows := make([]string, 0, len(c.pages[c.calls]))
	for _, ref := range c.pages[c.calls] {
		rows = append(rows, fmt.Sprintf(`{"id":%q,"group_id":"g1","name":"n-%s","status":"enabled","models":[]}`, ref, ref))
	}
	total := int64(3)
	if len(c.totals) > 0 {
		total = c.totals[c.calls]
	}
	c.calls++
	return adapter.Response{Status: 200, Body: []byte(fmt.Sprintf(`{"data":{"total":%d,"items":[%s]}}`, total, strings.Join(rows, ",")))}, nil
}
