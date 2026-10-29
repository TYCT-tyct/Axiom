package accounts

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestPasswordRecoveryLogsSafeDiagnostics(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t, Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sqlErr := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "SELECT 'private-email@example.com'::integer")
		return err
	})
	for _, c := range []struct {
		err            error
		category, code string
	}{
		{errNoResetDomain, "no_https_console_domain", ""},
		{ErrBadEmail, "invalid_email", ""},
		{sqlErr, "database", "22P02"},
		{errors.New("private-email@example.com"), "processing", ""},
	} {
		var output bytes.Buffer
		r := PasswordRecovery{logger: slog.New(slog.NewJSONHandler(&output, nil))}
		r.logFailure(t.Context(), 17, 3, c.err)
		text := output.String()
		for _, want := range []string{`"job_id":17`, `"attempt":3`, `"category":"` + c.category + `"`, `"sqlstate":"` + c.code + `"`} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing %s in %s", want, text)
			}
		}
		if strings.Contains(text, "private-email") {
			t.Fatal("SQL detail or error value leaked")
		}
	}
}
