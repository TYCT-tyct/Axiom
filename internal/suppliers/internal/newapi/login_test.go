package newapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

func keyring(t *testing.T) *secret.Keyring {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(path, []byte("1:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// TestLoginSurvivesSealing: the credentials encode, seal, open and decode to what they were, open only where they
// were sealed, and show nothing when printed or logged.
func TestLoginSurvivesSealing(t *testing.T) {
	t.Parallel()
	keys := keyring(t)
	for _, expires := range []time.Time{{}, time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)} {
		raw, err := EncodeLogin(Login{PAT: secret.New(fakePAT), ExpiresAt: expires})
		if err != nil {
			t.Fatal(err)
		}
		at := secret.Location{Table: "suppliers.supplier_logins", RowID: "supplier-1", Purpose: "login"}
		sealed := keys.Seal(secret.New(string(raw)), at)
		opened, err := keys.Open(sealed, at)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeLogin([]byte(opened.Reveal()))
		if err != nil || got.PAT.Reveal() != fakePAT || !got.ExpiresAt.Equal(expires) {
			t.Errorf("round trip of expiry %v: %v, %v", expires, got.ExpiresAt, err)
		}
		if _, err := keys.Open(sealed, secret.Location{Table: "suppliers.supplier_logins", RowID: "supplier-2", Purpose: "login"}); err == nil {
			t.Error("the login opens at another supplier's row")
		}
		var logged strings.Builder
		slog.New(slog.NewJSONHandler(&logged, nil)).Info("login", "token", got.PAT)
		if strings.Contains(logged.String(), fakePAT) {
			t.Errorf("logging the token shows it: %s", logged.String())
		}
		if text := fmt.Sprintf("%v %+v %#v", got, got, got); strings.Contains(text, fakePAT) {
			t.Errorf("printing the login shows the token: %s", text)
		}
	}
}

// TestADecodeThatIsNotALoginSaysOnlyThat: unknown members, a wrong version and a missing token are refused, and the
// error holds none of what was given.
func TestADecodeThatIsNotALoginSaysOnlyThat(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		``, `{`, `{"v":2,"pat":"planted-token"}`, `{"v":1,"pat":""}`, `{"v":1,"pat":"planted-token","extra":1}`,
		`{"v":1,"pat":"planted-token","expires_at":-1}`, `{"v":1,"pat":"planted-token","pat":"again"}`,
	} {
		if _, err := DecodeLogin([]byte(raw)); err == nil || strings.Contains(err.Error(), "planted-token") {
			t.Errorf("DecodeLogin(%q): %v", raw, err)
		}
	}
}

// TestOpenRefusesALoginItCannotUse: an expired token is reported before a request is spent on it, a stored form from a
// version this code does not know is refused, and neither error holds the token.
func TestOpenRefusesALoginItCannotUse(t *testing.T) {
	t.Parallel()
	a := &Adapter{now: func() time.Time { return now }}
	for name, store := range map[string]loginStore{
		"expired":  testLogin(t, now.Add(-time.Second)),
		"unknown":  {raw: []byte(`{"v":9,"pat":"` + fakePAT + `"}`)},
		"not json": {raw: []byte(fakePAT)},
	} {
		f := newFake()
		_, err := a.Open(t.Context(), f, store)
		if !errors.Is(err, adapter.ErrAuthRejected) || strings.Contains(err.Error(), fakePAT) || len(f.sent) != 0 {
			t.Errorf("%s: %v, %d requests", name, err, len(f.sent))
		}
	}
	if _, err := a.Open(t.Context(), newFake(), testLogin(t, now.Add(time.Hour))); err != nil {
		t.Errorf("a token that has not expired: %v", err)
	}
}
