package accounts_test

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// TestSiteBrand: a colour is #rrggbb or empty, upper case is stored in the lower case the contract writes, and a default
// palette is the empty colour.
func TestSiteBrand(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		color string
		want  string
		field string
	}{
		"the default palette":      {color: "", want: ""},
		"a lower-case colour":      {color: "#335ddf", want: "#335ddf"},
		"an upper-case colour":     {color: "#335DDF", want: "#335ddf"},
		"a colour without a hash":  {color: "335ddf", field: "brand_color"},
		"a colour of three digits": {color: "#35d", field: "brand_color"},
		"a colour that is not hex": {color: "#335ddg", field: "brand_color"},
		"a named colour":           {color: "blue", field: "brand_color"},
	} {
		b := accounts.SiteBrand{Color: c.color}
		err := b.Check()
		if c.field == "" && (err != nil || b.Color != c.want) {
			t.Errorf("%s: %q, %v, want %q", name, b.Color, err, c.want)
		}
		if c.field != "" && !refuses(err, c.field) {
			t.Errorf("%s: %v, want a refusal of %q", name, err, c.field)
		}
	}
}

// brandOf is a node on a seeded database and the means to read and write a site's brand colour and count audit entries.
type brandOf struct {
	t  *testing.T
	db *pg.DB
	s  *accounts.Service
}

func newBrandOf(t *testing.T) brandOf {
	t.Helper()
	db, _ := open(t)
	return brandOf{t, db, node(t, db, hashKeys(t, 1), nil)}
}

func (o brandOf) read(site uuid.UUID) (b accounts.SiteBrand, err error) {
	err = o.db.ReadTx(o.t.Context(), platform, func(tx pg.Tx) (err error) {
		b, err = accounts.ReadSiteBrand(o.t.Context(), tx, accounts.SiteID(site))
		return err
	})
	return b, err
}

func (o brandOf) write(site uuid.UUID, b accounts.SiteBrand) error {
	return o.db.Tx(o.t.Context(), platform, func(tx pg.Tx) error {
		return o.s.SetSiteBrand(o.t.Context(), tx, operatorActor, accounts.SiteID(site), b, "because")
	})
}

func (o brandOf) entries() (n int) {
	o.t.Helper()
	if err := o.db.Tx(o.t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(o.t.Context(), `SELECT count(*) FROM platform.audit_log WHERE action = 'site_brand.update'`).Scan(&n)
	}); err != nil {
		o.t.Fatal(err)
	}
	return n
}

// TestSiteBrandIsReadAndAudited: a site with none reads as empty, a write stores it lower case and is audited once, and
// clearing returns to the default palette.
func TestSiteBrandIsReadAndAudited(t *testing.T) {
	t.Parallel()
	o := newBrandOf(t)
	if b, err := o.read(s1); err != nil || b.Color != "" {
		t.Fatalf("a site with no brand colour: %+v, %v", b, err)
	}
	if err := o.write(s1, accounts.SiteBrand{Color: "#335DDF"}); err != nil {
		t.Fatal(err)
	}
	if b, err := o.read(s1); err != nil || b.Color != "#335ddf" {
		t.Errorf("the colour as written: %+v, %v", b, err)
	}
	if n := o.entries(); n != 1 {
		t.Errorf("%d audit entries, want 1", n)
	}
	if err := o.write(s1, accounts.SiteBrand{}); err != nil {
		t.Fatal(err)
	}
	if b, err := o.read(s1); err != nil || b.Color != "" {
		t.Errorf("the colour after clearing: %+v, %v", b, err)
	}
	if n := o.entries(); n != 2 {
		t.Errorf("%d audit entries, want 2", n)
	}
}

// TestSiteBrandRefusals: a refused colour writes nothing and leaves no entry, and a site that is not there is
// ErrNotFound for the read and the write alike.
func TestSiteBrandRefusals(t *testing.T) {
	t.Parallel()
	o := newBrandOf(t)
	if err := o.write(s1, accounts.SiteBrand{Color: "blue"}); !refuses(err, "brand_color") || o.entries() != 0 {
		t.Errorf("a colour of another form: %v, %d entries", err, o.entries())
	}
	if _, err := o.read(uuid.NewV7()); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("reading a site that is not there: %v", err)
	}
	if err := o.write(uuid.NewV7(), accounts.SiteBrand{Color: "#335ddf"}); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("writing a site that is not there: %v", err)
	}
}
