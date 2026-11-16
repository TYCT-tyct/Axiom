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
		LastLate: now.Unix(), LateEnd: now.Unix(), Connected: now.Add(-3 * time.Hour).Unix(),
		Size: a.charges.pageSize,
	}).encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, next, err := m.Charges(t.Context(), scan); !errors.Is(err, adapter.ErrUnrecognised) || string(next) != string(scan) {
		t.Fatalf("Charges = %v, next %s; want ErrUnrecognised and the same position", err, next)
	}
}

func TestChargePositionWireFormat(t *testing.T) {
	t.Parallel()
	now := time.Unix(10000, 0).UTC()
	for _, tc := range []struct {
		name, before, after string
		through             int64
	}{
		{"connection", "", `{"connected":10000,"fast":0,"last_late":10000,"late_end":10000,"size":2,"span":3600,"v":1,"watermark":10000}`, 10000},
		{
			"forward", `{"v":1,"watermark":6400,"span":3600,"fast":2,"last_late":10000,"late_end":10000,"connected":2800,"size":2}`,
			`{"connected":2800,"fast":0,"last_late":10000,"late_end":10000,"size":2,"span":7200,"v":1,"watermark":9970}`, 9970,
		},
		{
			"resumed long pass", `{"v":1,"watermark":6400,"span":3600,"fast":2,"last_late":10000,"late_end":10000,"connected":2800,"size":2,"pass":{"late":false,"start":6100,"end":9000,"page":21,"pages":21}}`,
			`{"connected":2800,"fast":0,"last_late":10000,"late_end":10000,"size":2,"span":1800,"v":1,"watermark":9000}`, 9000,
		},
		{
			"changed page size", `{"v":1,"watermark":6400,"span":3600,"fast":2,"last_late":10000,"late_end":10000,"connected":2800,"size":3,"pass":{"late":false,"start":6100,"end":9000,"page":21,"pages":21}}`,
			`{"connected":2800,"fast":0,"last_late":10000,"late_end":10000,"size":2,"span":7200,"v":1,"watermark":9970}`, 9970,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := loadAcme(t)
			a.now = func() time.Time { return now }
			a.charges.pageSize = 2
			m := &manager{a: a, c: fixedClient{body: []byte(`{"data":{"items":[]}}`)}}
			page, next, err := m.Charges(t.Context(), adapter.ScanState(tc.before))
			if err != nil || !page.Done || page.Through.Unix() != tc.through || string(next) != tc.after {
				t.Fatalf("Charges = %+v, %s, %v; want through %d, %s", page, next, err, tc.through, tc.after)
			}
		})
	}
}
