package billing

import (
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestTopUpBufferReadsAndWritesTheSitesValue(t *testing.T) {
	r := newRules(t)
	r.register()
	read := func() int32 {
		t.Helper()
		var ppm int32
		if err := r.tx(func(tx pg.Tx) (err error) { ppm, err = TopUpBufferPPM(t.Context(), tx, r.site); return err }); err != nil {
			t.Fatal(err)
		}
		return ppm
	}
	if got := read(); got != 20_000 {
		t.Fatalf("default buffer %d, want 20000", got)
	}
	for _, ppm := range []int32{50_000, 0, 100_000} {
		if err := r.tx(func(tx pg.Tx) error {
			return SetTopUpBuffer(t.Context(), tx, system, r.site, ppm, "chosen by the site")
		}); err != nil {
			t.Fatal(err)
		}
		if got := read(); got != ppm {
			t.Fatalf("buffer %d, want %d", got, ppm)
		}
	}
	for _, ppm := range []int32{-1, 100_001} {
		if err := r.tx(func(tx pg.Tx) error { return SetTopUpBuffer(t.Context(), tx, system, r.site, ppm, "bad") }); err == nil {
			t.Fatalf("accepted buffer %d", ppm)
		}
	}
	var entries string
	if err := r.tx(func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*)::text FROM platform.audit_log WHERE action = 'billing.top_up_buffer.update'`).Scan(&entries)
	}); err != nil {
		t.Fatal(err)
	}
	if entries != "3" {
		t.Fatalf("%s audit entries, want 3", entries)
	}
}

func TestTopUpBufferIsPerSite(t *testing.T) {
	r := newRules(t)
	r.register()
	other := uuid.NewV7()
	if err := r.tx(func(tx pg.Tx) error { return SetTopUpBuffer(t.Context(), tx, system, r.site, 30_000, "first site") }); err != nil {
		t.Fatal(err)
	}
	var ppm int32
	if err := r.tx(func(tx pg.Tx) (err error) { ppm, err = TopUpBufferPPM(t.Context(), tx, other); return err }); err != nil {
		t.Fatal(err)
	}
	if ppm != 20_000 {
		t.Fatalf("another site's buffer %d, want the default", ppm)
	}
}

// register makes the site a registered tenant, which the audit log's foreign key requires.
func (r rules) register() {
	r.t.Helper()
	if err := r.tx(func(tx pg.Tx) error {
		_, err := tx.Exec(r.t.Context(), `INSERT INTO platform.tenants VALUES ($1, $1)`, r.site)
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
}
