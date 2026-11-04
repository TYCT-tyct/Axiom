package declared

import (
	"context"
	"errors"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// fixedClient answers every request with one body. It is enough to exercise a listing's error path without a relay.
type fixedClient struct{ body []byte }

func (c fixedClient) Do(context.Context, adapter.Request) (adapter.Response, error) {
	return adapter.Response{Status: 200, Body: c.body}, nil
}

// TestChargesKeepsItsPositionOnANullPage: null is not an empty page. The error must reach the caller with the scan
// position it passed in, so the watermark does not move past records that were never read.
func TestChargesKeepsItsPositionOnANullPage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	a := loadAcme(t)
	a.now = func() time.Time { return now }
	m := &manager{a: a, c: fixedClient{body: []byte(`{"data":{"items":null}}`)}}
	scan, err := (scanState{
		V: scanVersion, Watermark: now.Add(-2 * time.Hour).Unix(), Span: 3600,
		LastLate: now.Unix(), LateEnd: now.Unix(), Connected: now.Add(-3 * time.Hour).Unix(), Size: a.charges.pageSize,
	}).encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, next, err := m.Charges(t.Context(), scan); !errors.Is(err, adapter.ErrUnrecognised) || string(next) != string(scan) {
		t.Fatalf("Charges = %v, next %s; want ErrUnrecognised and the same position", err, next)
	}
}
