package suppliers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
)

// onboardable is an adapter a person can connect: its login is one token, and a site answers to Detect as detects says.
type onboardable struct {
	adapter.Adapter
	kind    adapter.Kind
	detects func() (adapter.Detection, error)
}

func (o onboardable) Kind() adapter.Kind          { return o.kind }
func (onboardable) Buckets() []adapter.BucketSpec { return nil }
func (o onboardable) Detect(context.Context, adapter.Client) (adapter.Detection, error) {
	return o.detects()
}
func (onboardable) NativeUnit() string { return "usd" }
func (onboardable) LoginFields(captcha bool) []adapter.LoginField {
	if captcha {
		return []adapter.LoginField{adapter.FieldAccessToken}
	}
	return []adapter.LoginField{adapter.FieldAccessToken, adapter.FieldAccessTokenExpiresAt}
}

func (onboardable) NewLogin(c adapter.Credentials) (adapter.Login, error) {
	if c.AccessToken == "" {
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldAccessToken, Reason: "is required"}
	}
	return adapter.Login{Kind: "token", Plaintext: []byte(`{"token":"` + c.AccessToken + `"}`), ExpiresAt: c.AccessTokenExpiresAt}, nil
}

var (
	recognising = func() (adapter.Detection, error) {
		return adapter.Detection{Generation: "1.0", Version: "v9", Captcha: false}, nil
	}
	notRecognising = func() (adapter.Detection, error) { return adapter.Detection{}, adapter.ErrNotRecognised }
	unreachable    = func() (adapter.Detection, error) { return adapter.Detection{}, errors.New("connection refused") }
)

// admins is the admin side on a database of the module, with the adapters it is given.
type admins struct {
	t     *testing.T
	db    *pg.DB
	admin *Admin
	keys  *secret.Keyring
	data  pgtest.Database
	actor audit.Actor
}

func newAdmins(t *testing.T, list ...adapter.Adapter) admins {
	t.Helper()
	data := pgtest.New(t, Module())
	db, err := pg.Open(t.Context(), data.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if len(list) == 0 {
		list = []adapter.Adapter{onboardable{kind: "token", detects: recognising}}
	}
	r, err := registry.New(list...)
	if err != nil {
		t.Fatal(err)
	}
	keys := newKeys(t)
	admin, err := NewAdmin(AdminConfig{Keys: keys, Egress: egress.NewForTesting(egresstest.Loopback(t)), Registry: r})
	if err != nil {
		t.Fatal(err)
	}
	return admins{t, db, admin, keys, data, audit.User(uuid.NewV7())}
}

// inTx runs f in a platform transaction, as the handlers do.
func inTx[T any](a admins, f func(context.Context, pg.Tx) (T, error)) (v T, err error) {
	err = a.db.Tx(a.t.Context(), platformScope, func(tx pg.Tx) (e error) {
		v, e = f(a.t.Context(), tx)
		return e
	})
	return v, err
}

// sql runs statements as the superuser, for rows only the sync makes.
func (a admins) sql(statements ...string) {
	a.t.Helper()
	if err := pgtest.Run(a.t.Context(), a.data.Superuser, statements...); err != nil {
		a.t.Fatal(err)
	}
}

// text is the first column of the query's one row, run as the superuser so row security does not hide it.
func (a admins) text(query string) string {
	a.t.Helper()
	v, err := inTx(a, func(ctx context.Context, tx pg.Tx) (v string, err error) {
		return v, tx.QueryRow(ctx, query).Scan(&v)
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return v
}

var usd = func() platform.Currency {
	c, err := platform.ParseCurrency("USD")
	if err != nil {
		panic(err) // a constant
	}
	return c
}()

func request(origin, account string) ConnectRequest {
	return ConnectRequest{
		Name: "Example", Kind: "token", Origin: origin, UpstreamAccount: account, SettlementCurrency: usd,
		Login: Credentials{AccessToken: "token-secret"}, Reason: "second source",
	}
}

func (a admins) tryConnect(r ConnectRequest) (Supplier, error) {
	return inTx(a, func(ctx context.Context, tx pg.Tx) (Supplier, error) { return a.admin.Connect(ctx, tx, a.actor, r) })
}

// connect connects a supplier at the origin and returns it.
func (a admins) connect(origin, account string) Supplier {
	a.t.Helper()
	made, err := a.tryConnect(request(origin, account))
	if err != nil {
		a.t.Fatal(err)
	}
	return made
}

// setStatus moves a supplier to a status the way the sync does.
func (a admins) setStatus(id uuid.UUID, status string) {
	a.t.Helper()
	a.sql(fmt.Sprintf("UPDATE suppliers.suppliers SET status = '%s' WHERE id = '%s'", status, id))
}

// same reports a difference between what was wanted and what came.
func same(t *testing.T, what string, want, got any) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("%s (-want +got):\n%s", what, diff)
	}
}

// refuses reports an error that is not the one wanted; a nil want is success.
func refuses(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Errorf("%s: %v, want %v", what, got, want)
	}
}

// refusesLogin reports an error that is not a refusal of the member called field.
func refusesLogin(t *testing.T, what string, got error, field string) {
	t.Helper()
	if loginErr, ok := errors.AsType[*LoginError](got); !ok {
		t.Errorf("%s: %v, want a *LoginError", what, got)
	} else {
		same(t, what, field, loginErr.Field)
	}
}

// TestOrigin: the origin is stored the one way, and one the outbound client refuses or that is no origin is refused.
func TestOrigin(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	for raw, want := range map[string]string{
		"https://8.8.8.8":         "https://8.8.8.8",
		"HTTPS://8.8.8.8:443":     "https://8.8.8.8",
		"https://8.8.8.8:8443":    "https://8.8.8.8:8443",
		"https://8.8.8.8:0443":    "https://8.8.8.8", // the same port, written with a leading zero
		"https://8.8.8.8:08443":   "https://8.8.8.8:8443",
		"https://8.8.8.8:0":       "",
		"https://8.8.8.8:99999":   "",
		"https://127.0.0.1:8443":  "https://127.0.0.1:8443", // the test client lets loopback through
		"https://[2606:4700::1]":  "",                       // not a name this pattern takes
		"http://8.8.8.8":          "",
		"https://8.8.8.8/path":    "",
		"https://user@8.8.8.8":    "",
		"https://8.8.8.8?x=1":     "",
		"8.8.8.8":                 "",
		"":                        "",
		"https://10.0.0.1":        "",
		"https://169.254.169.254": "",
		"https://192.168.1.1:81":  "",
	} {
		got, err := a.admin.checkedOrigin(t.Context(), raw)
		same(t, fmt.Sprintf("checkedOrigin(%q) (error %v)", raw, err), want, got)
	}
	_, err := a.admin.checkedOrigin(t.Context(), "https://10.0.0.1")
	refuses(t, "a private address", err, ErrAddressRefused)
	_, err = a.admin.checkedOrigin(t.Context(), "https://8.8.8.8/path")
	refuses(t, "a path", err, ErrBadOrigin)
}

// TestConnectSealsTheLogin: a supplier is made `connecting` with its login sealed to its own row, which opens there and
// nowhere else, and the table holds no plaintext.
func TestConnectSealsTheLogin(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	made := a.connect("https://api.example.com", "ops@example.com")
	same(t, "the supplier", Supplier{
		ID: made.ID, Name: "Example", Kind: "token", Origin: "https://api.example.com", UpstreamAccount: "ops@example.com",
		NativeUnit: "usd", SettlementCurrency: "USD", Status: Connecting,
	}, made)
	opened, err := a.keys.Open(a.sealedLogin(made.ID), loginAt(made.ID))
	if err != nil {
		t.Fatal(err)
	}
	same(t, "the login opened", `{"token":"token-secret"}`, opened.Reveal())
	if _, err := a.keys.Open(a.sealedLogin(made.ID), loginAt(uuid.NewV7())); err == nil {
		t.Error("the login opens at another supplier's row")
	}
	same(t, "rows holding the login as written", "0",
		a.text(`SELECT count(*)::text FROM suppliers.supplier_logins WHERE position('token-secret'::bytea in ciphertext) > 0`))
}

func (a admins) sealedLogin(id uuid.UUID) secret.Sealed {
	a.t.Helper()
	s, err := inTx(a, func(ctx context.Context, tx pg.Tx) (s secret.Sealed, err error) {
		var version int64
		err = tx.QueryRow(ctx, `SELECT ciphertext, nonce, wrapped_dek, key_version FROM suppliers.supplier_logins WHERE supplier_id = $1`, id).
			Scan(&s.Ciphertext, &s.Nonce, &s.WrappedDEK, &version)
		s.KeyVersion = uint32(version) //nolint:gosec // the column holds 1 to 2³²-1
		return s, err
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return s
}

// TestConnectIsAudited: the entry names the reason and the actor, and holds neither the login nor the account.
func TestConnectIsAudited(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	a.connect("https://api.example.com", "ops@example.com")
	entry := a.text(`SELECT action || '|' || reason || '|' || actor_id || '|' || detail::text FROM platform.audit_log WHERE action = 'supplier.connect'`)
	if !strings.HasPrefix(entry, "supplier.connect|second source|"+a.actor.String()[len("user:"):]) {
		t.Errorf("the audit entry: %s", entry)
	}
	for _, leak := range []string{"token-secret", "ops@example.com"} {
		if strings.Contains(entry, leak) {
			t.Errorf("the audit entry %q holds %q", entry, leak)
		}
	}
}

// TestConnectRefusals: an origin and account connect once; a kind with no adapter, or a login of members the kind does
// not use or lacks, is refused whole, and nothing is left behind.
func TestConnectRefusals(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	a.connect("https://api.example.com", "ops@example.com")
	for name, c := range map[string]struct {
		change func(*ConnectRequest)
		want   error
		field  string
	}{
		"the same origin and account":    {func(*ConnectRequest) {}, ErrSupplierExists, ""},
		"a kind with no adapter":         {func(r *ConnectRequest) { r.Kind = "ghost" }, ErrNoAdapter, ""},
		"a login without its token":      {func(r *ConnectRequest) { r.Login = Credentials{} }, nil, "access_token"},
		"a member the kind does not use": {func(r *ConnectRequest) { r.Login.Password = "pw" }, nil, "password"},
	} {
		r := request("https://api.example.com", "ops@example.com")
		c.change(&r)
		_, err := a.tryConnect(r)
		if c.field == "" {
			refuses(t, name, err, c.want)
			continue
		}
		refusesLogin(t, name, err, c.field)
	}
	_, err := a.tryConnect(request("https://api.example.com", "other@example.com"))
	refuses(t, "another account at the same origin", err, nil)
	same(t, "suppliers made", "2", a.text(`SELECT count(*)::text FROM suppliers.suppliers`))
}

// TestDetect: the first adapter that recognises the site answers, with what its login takes, narrowed for a site that
// asks for a CAPTCHA; a site none recognises is a detection with no kind; a site that does not answer is ErrUnreachable.
func TestDetect(t *testing.T) {
	t.Parallel()
	captcha := func() (adapter.Detection, error) { return adapter.Detection{Generation: "2", Captcha: true}, nil }
	const origin = "https://127.0.0.1:9"
	for name, c := range map[string]struct {
		adapters []adapter.Adapter
		want     Detection
	}{
		"recognised": {
			[]adapter.Adapter{onboardable{kind: "a", detects: notRecognising}, onboardable{kind: "b", detects: recognising}},
			Detection{Origin: origin, Kind: "b", Generation: "1.0", Version: "v9", LoginFields: []string{"access_token", "access_token_expires_at"}},
		},
		"a captcha": {
			[]adapter.Adapter{onboardable{kind: "a", detects: captcha}},
			Detection{Origin: origin, Kind: "a", Generation: "2", LoginFields: []string{"access_token"}},
		},
		"not recognised": {
			[]adapter.Adapter{onboardable{kind: "a", detects: notRecognising}, onboardable{kind: "b", detects: notRecognising}},
			Detection{Origin: origin, LoginFields: []string{}},
		},
		"one that could not tell and one that says no": {
			[]adapter.Adapter{onboardable{kind: "a", detects: unreachable}, onboardable{kind: "b", detects: notRecognising}},
			Detection{Origin: origin, LoginFields: []string{}},
		},
	} {
		got, err := newAdmins(t, c.adapters...).admin.Detect(t.Context(), origin)
		refuses(t, name, err, nil)
		same(t, name, c.want, got)
	}
}

// TestDetectRefusals: a site that does not answer, and an address the outbound client refuses, are told apart.
func TestDetectRefusals(t *testing.T) {
	t.Parallel()
	a := newAdmins(t, onboardable{kind: "a", detects: unreachable})
	_, err := a.admin.Detect(t.Context(), "https://127.0.0.1:9")
	refuses(t, "a site that does not answer", err, ErrUnreachable)
	_, err = a.admin.Detect(t.Context(), "https://10.0.0.1")
	refuses(t, "a private address", err, ErrAddressRefused)
}
