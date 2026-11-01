package payments

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

func (w *world) issue(amount int64, count int, expires *time.Time) []RedeemCode {
	w.t.Helper()
	var codes []RedeemCode
	if err := w.tx(siteScope(w.siteA), func(tx pg.Tx) error {
		var err error
		codes, err = IssueCodes(w.t.Context(), tx, Issue{
			Site: w.siteA, Amount: amount, Count: count, Note: "summer promotion", ExpiresAt: expires,
			Actor: admin, CreatedBy: w.user, Reason: "test issue",
		})
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return codes
}

func (w *world) issueNote(note string) {
	w.t.Helper()
	if err := w.tx(siteScope(w.siteA), func(tx pg.Tx) error {
		_, err := IssueCodes(w.t.Context(), tx, Issue{
			Site: w.siteA, Amount: 100, Count: 1, Note: note,
			Actor: admin, CreatedBy: w.user, Reason: "test note",
		})
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) redeem(code, key string) (Redemption, error) {
	return w.p.Redeem(w.t.Context(), Redeem{
		Site: w.site(w.siteA, "USD"), Account: w.account, User: w.user, Code: code, Key: key,
		ClientIP: netip.MustParseAddr("203.0.113.7"),
	})
}

// accountAt makes another account of a site, with an empty balance, for the tests that need two accounts of one site.
func (w *world) accountAt(site uuid.UUID) uuid.UUID {
	w.t.Helper()
	account := uuid.NewV7()
	w.exec("INSERT INTO platform.tenants VALUES ('" + site.String() + "', '" + account.String() + "')")
	w.exec("INSERT INTO billing.balances (site_id, account_id) VALUES ('" + site.String() + "', '" + account.String() + "')")
	return account
}

func (w *world) redeemAs(account uuid.UUID, code, key, ip string) (Redemption, error) {
	return w.p.Redeem(w.t.Context(), Redeem{
		Site: w.site(w.siteA, "USD"), Account: account, User: w.user, Code: code, Key: key,
		ClientIP: netip.MustParseAddr(ip),
	})
}

func (w *world) attempts(kind, subject string) int {
	return w.count(w.p.platform, "SELECT attempts FROM payments.redeem_attempts WHERE kind = $1 AND subject = $2", kind, subject)
}

// TestIssueWritesOnlyHashes: a generation writes a hash and a four-character hint, never the code, and the code appears
// once, in the answer; the list that reads it back has no code to show.
//
//nolint:cyclop,gocognit // one generation checked through hash, hint, list and audit.
func TestIssueWritesOnlyHashes(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	codes := w.issue(500, 3, nil)
	if len(codes) != 3 {
		t.Fatalf("%d codes, want 3", len(codes))
	}
	for _, c := range codes {
		if c.State != RedeemEnabled || c.Amount != 500 || c.Hint == "" || c.Code == "" || len(c.Code) != 23 {
			t.Fatalf("issued %+v", c)
		}
		canonical, ok := normalizeCode(c.Code)
		if !ok || !strings.HasSuffix(canonical, c.Hint) {
			t.Fatalf("the code %q does not normalize to its hint %q", c.Code, c.Hint)
		}
		sum := sha256.Sum256([]byte(canonical))
		if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_codes WHERE id = $1 AND code_hash = $2", uuid.UUID(c.ID), sum[:]); got != 1 {
			t.Fatalf("the hash of %q is not stored", c.Code)
		}
		if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_codes t WHERE to_jsonb(t)::text LIKE '%' || $1 || '%'", canonical); got != 0 {
			t.Fatalf("the code %q is in the database", canonical)
		}
	}
	list, next, err := w.p.ListCodes(w.t.Context(), siteScope(w.siteA), w.siteA, "", "", nil, 50)
	if err != nil || next != nil || len(list) != 3 {
		t.Fatalf("ListCodes = %d codes, %v, %v", len(list), next, err)
	}
	for _, c := range list {
		if c.Code != "" {
			t.Fatalf("the list shows the code %q", c.Code)
		}
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM platform.audit_log WHERE action = 'redeem_code.issue' AND target_id = $1", w.siteA.String()); n != 1 {
		t.Fatalf("%d issue audit entries, want 1", n)
	}
}

// TestIssueRefusesWhatItCannotHold: a batch outside the rules is a field error, before any code is written.
func TestIssueRefusesWhatItCannotHold(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	expired := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name string
		g    Issue
		path string
	}{
		{"no amount", Issue{Site: w.siteA, Count: 1, CreatedBy: w.user, Actor: admin}, "/amount"},
		{"no codes", Issue{Site: w.siteA, Amount: 1, CreatedBy: w.user, Actor: admin}, "/count"},
		{"too many", Issue{Site: w.siteA, Amount: 1, Count: redeemIssueMax + 1, CreatedBy: w.user, Actor: admin}, "/count"},
		{"a note too long", Issue{Site: w.siteA, Amount: 1, Count: 1, Note: strings.Repeat("x", redeemNoteMax+1), CreatedBy: w.user, Actor: admin}, "/note"},
		{"an expiry past", Issue{Site: w.siteA, Amount: 1, Count: 1, ExpiresAt: &expired, CreatedBy: w.user, Actor: admin}, "/expires_at"},
	} {
		err := w.tx(siteScope(w.siteA), func(tx pg.Tx) error {
			_, err := IssueCodes(w.t.Context(), tx, tc.g)
			return err
		})
		var field *FieldError
		if !errors.As(err, &field) || field.Field != tc.path {
			t.Errorf("%s: %v, want a field error at %s", tc.name, err, tc.path)
		}
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_codes"); n != 0 {
		t.Fatalf("%d codes written by refused batches", n)
	}
}

// TestRedeemCreditsOnce: a code credits its amount into a redeem batch with the code's expiry, and does so once, however
// the code is typed; the same key answers the redemption instead of crediting again, and the code alone cannot.
//
//nolint:cyclop // the credit, the batch, the ledger and the replay are the test.
func TestRedeemCreditsOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	codes := w.issue(500, 1, &expires)
	code := codes[0].Code
	typed := strings.ToLower(code[:4] + " " + code[4:])

	r, err := w.redeem(typed, "first")
	if err != nil {
		t.Fatal(err)
	}
	if r.Amount != 500 || r.Hint != codes[0].Hint || r.Replayed || r.ExpiresAt == nil || !r.ExpiresAt.Equal(expires) {
		t.Fatalf("the redemption: %+v", r)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM billing.credit_batches WHERE id = $1 AND source = 'redeem' AND amount = 500 AND expires_at = $2", uuid.UUID(r.ID), expires); got != 1 {
		t.Fatalf("the credit batch of the redemption: %d", got)
	}
	if got := w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", w.account); got != 500 {
		t.Fatalf("balance %d, want 500", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_codes WHERE id = $1 AND state = 'used'", uuid.UUID(codes[0].ID)); got != 1 {
		t.Fatalf("the code is not used")
	}

	again, err := w.redeem(typed, "first")
	if err != nil || !again.Replayed || again.ID != r.ID {
		t.Fatalf("a replay: %+v, %v", again, err)
	}
	if _, err := w.redeem(typed, "second"); !errors.Is(err, ErrRedeemUsed) {
		t.Fatalf("the code redeemed with another key: %v, want ErrRedeemUsed", err)
	}
	if got := w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", w.account); got != 500 {
		t.Fatalf("balance after the replay and the second key: %d, want 500", got)
	}
}

// TestRedeemRefusesStates: an unknown, expired or disabled code is refused, and an attempt is committed before the code
// is looked at, so no refusal is a free guess.
func TestRedeemRefusesStates(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	future := time.Now().Add(time.Hour)
	expired := w.issue(100, 1, &future)
	w.exec("UPDATE payments.redeem_codes SET expires_at = now() - interval '1 hour' WHERE id = '" + uuid.UUID(expired[0].ID).String() + "'")
	disabled := w.issue(100, 1, nil)
	if err := w.tx(siteScope(w.siteA), func(tx pg.Tx) error {
		_, err := DisableCode(w.t.Context(), tx, admin, w.user, disabled[0].ID, "test disable")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.redeem("AAAAA-BBBBB-CCCCC-DDDDD", "unknown"); !errors.Is(err, ErrRedeemInvalid) {
		t.Fatalf("an unknown code: %v", err)
	}
	if _, err := w.redeem(expired[0].Code, "expired"); !errors.Is(err, ErrRedeemExpired) {
		t.Fatalf("an expired code: %v", err)
	}
	if _, err := w.redeem(disabled[0].Code, "disabled"); !errors.Is(err, ErrRedeemDisabled) {
		t.Fatalf("a disabled code: %v", err)
	}
	if got := w.attempts("account", w.account.String()); got != 3 {
		t.Fatalf("%d attempts, want 3", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_codes WHERE id = $1 AND state = 'enabled'", uuid.UUID(expired[0].ID)); got != 1 {
		t.Fatalf("the expired code is no longer enabled")
	}
}

// TestDisableIsIdempotentAndAudited: disabling a code changes it once and records once; doing it again changes nothing,
// and a used code cannot be disabled because its credit is somebody's.
func TestDisableIsIdempotentAndAudited(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	codes := w.issue(100, 2, nil)
	disable := func(id CodeID) (RedeemCode, error) {
		var c RedeemCode
		err := w.tx(siteScope(w.siteA), func(tx pg.Tx) error {
			var err error
			c, err = DisableCode(w.t.Context(), tx, admin, w.user, id, "test disable")
			return err
		})
		return c, err
	}
	first, err := disable(codes[0].ID)
	if err != nil || first.State != RedeemDisabled || first.DisabledAt == nil {
		t.Fatalf("disable = %+v, %v", first, err)
	}
	second, err := disable(codes[0].ID)
	if err != nil || !second.DisabledAt.Equal(*first.DisabledAt) {
		t.Fatalf("disable again = %+v, %v", second, err)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM platform.audit_log WHERE action = 'redeem_code.disable' AND target_id = $1", codes[0].ID.TypeID()); n != 1 {
		t.Fatalf("%d disable audit entries, want 1", n)
	}
	if _, err := w.redeem(codes[1].Code, "used"); err != nil {
		t.Fatal(err)
	}
	if _, err := disable(codes[1].ID); !errors.Is(err, ErrRedeemUsed) {
		t.Fatalf("disable a used code: %v, want ErrRedeemUsed", err)
	}
}

// TestRedeemLimitsPerAccountAndIP: thirty failed attempts spend an account's hour, the refusal names the wait, another
// address does not give the account another hour, a group at its own limit refuses without spending the account's
// place, and another account of the site is still served.
func TestRedeemLimitsPerAccountAndIP(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for i := range redeemAccountAttempts {
		if _, err := w.redeem("AAAAA-BBBBB-CCCCC-DDDDD", fmt.Sprintf("try-%d", i)); !errors.Is(err, ErrRedeemInvalid) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var wait *Wait
	_, err := w.redeem("AAAAA-BBBBB-CCCCC-DDDDD", "over")
	if !errors.As(err, &wait) || !errors.Is(err, ErrRedeemLimited) || wait.After <= 0 || wait.After > redeemWindow {
		t.Fatalf("one attempt past the account's limit: %v", err)
	}
	// Another address does not give the account another hour, and the refused attempt left the group's place alone.
	if _, err := w.redeemAs(w.account, "AAAAA-BBBBB-CCCCC-DDDDD", "other-ip", "198.51.100.9"); !errors.As(err, &wait) {
		t.Fatalf("a fresh address under an exhausted account: %v", err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_attempts WHERE kind = 'ip' AND subject = '198.51.100.9'"); got != 0 {
		t.Fatalf("the refused attempt spent a place of the fresh group")
	}
	// A group at its own limit refuses, and the account's place goes with the transaction.
	w.exec("UPDATE payments.redeem_attempts SET attempts = 0 WHERE kind = 'account' AND subject = '" + w.account.String() + "'")
	w.exec("UPDATE payments.redeem_attempts SET attempts = " + fmt.Sprint(redeemIPAttempts) + " WHERE kind = 'ip' AND subject = '203.0.113.7'")
	if _, err := w.redeem("AAAAA-BBBBB-CCCCC-DDDDD", "ip-over"); !errors.As(err, &wait) {
		t.Fatalf("an attempt past the group's limit: %v", err)
	}
	if got := w.attempts("account", w.account.String()); got != 0 {
		t.Fatalf("the refused attempt spent the account's place: %d", got)
	}
	// Another account of the same site, from the same group, is still served.
	other := uuid.NewV7()
	w.exec("INSERT INTO platform.tenants VALUES ('" + w.siteA.String() + "', '" + other.String() + "')")
	w.exec("INSERT INTO billing.balances (site_id, account_id) VALUES ('" + w.siteA.String() + "', '" + other.String() + "')")
	w.exec("UPDATE payments.redeem_attempts SET attempts = 0 WHERE kind = 'ip'")
	valid := w.issue(100, 1, nil)
	if _, err := w.redeemAs(other, valid[0].Code, "other-account", "203.0.113.7"); err != nil {
		t.Fatalf("another account of the site: %v", err)
	}
}

// TestRedeemConcurrentOneCode: two requests for one code from two accounts, and two with one key from one account, each
// credit once: the code's row and the account's lock serialize them, and the loser is told what happened rather than
// credited twice.
//
//nolint:cyclop,gocognit // the two races and their balance, redemption and ledger checks are the test.
func TestRedeemConcurrentOneCode(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	other := w.accountAt(w.siteA)
	codes := w.issue(700, 2, nil)
	race := func(accounts [2]uuid.UUID, codes, keys, ips [2]string) ([]Redemption, []error) {
		out, errs := make([]Redemption, 2), make([]error, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Go(func() {
				<-start
				out[i], errs[i] = w.redeemAs(accounts[i], codes[i], keys[i], ips[i])
			})
		}
		close(start)
		wg.Wait()
		return out, errs
	}
	// One code, two keys, two accounts: one credit, the other told it is used. The account lock does not serialize these,
	// only the code's row does.
	accounts := [2]uuid.UUID{w.account, other}
	out, errs := race(accounts, [2]string{codes[0].Code, codes[0].Code}, [2]string{"one-a", "one-b"}, [2]string{"203.0.113.11", "203.0.113.12"})
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("one code, two accounts: %v, %v", errs[0], errs[1])
	}
	winner := 0
	if errs[0] != nil {
		winner = 1
	}
	if !errors.Is(errs[1-winner], ErrRedeemUsed) || out[winner].Amount != 700 {
		t.Fatalf("one code, two accounts: %+v, %v", out, errs)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_redemptions WHERE account_id = $1", accounts[winner]); got != 1 {
		t.Fatalf("the winner has %d redemptions, want 1", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_redemptions WHERE account_id = $1", accounts[1-winner]); got != 0 {
		t.Fatalf("the loser has %d redemptions, want 0", got)
	}
	if got := w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", accounts[winner]); got != 700 {
		t.Fatalf("the winner's balance %d, want 700", got)
	}
	if got := w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", accounts[1-winner]); got != 0 {
		t.Fatalf("the loser's balance %d, want 0", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind = 'credit'"); got != 1 {
		t.Fatalf("%d credit transactions, want 1", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM billing.entries WHERE book = 'user_balance'"); got != 1 {
		t.Fatalf("%d user_balance entries, want 1", got)
	}
	// One key, one code, one account: both requests answer, the same redemption, and one credit.
	out, errs = race([2]uuid.UUID{w.account, w.account}, [2]string{codes[1].Code, codes[1].Code}, [2]string{"same", "same"}, [2]string{"203.0.113.13", "203.0.113.13"})
	if errs[0] != nil || errs[1] != nil || out[0].ID != out[1].ID {
		t.Fatalf("one key, two requests: %+v, %v", out, errs)
	}
	if out[0].Replayed == out[1].Replayed {
		t.Fatalf("neither or both answered a replay: %+v", out)
	}
	if got := w.count(w.p.platform, "SELECT coalesce(sum(balance), 0) FROM billing.balances WHERE site_id = $1", w.siteA); got != 1400 {
		t.Fatalf("the site's balances total %d, want 1400", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_redemptions"); got != 2 {
		t.Fatalf("%d redemptions, want 2", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM billing.entries WHERE book = 'user_balance'"); got != 2 {
		t.Fatalf("%d user_balance entries, want 2", got)
	}
}

// TestRedeemExpiryAcrossTheLock: a redemption that waits for the account's lock past the code's expiry is refused, and
// changes neither the code nor the balance. The decision reads the clock after the lock, not the transaction's start.
func TestRedeemExpiryAcrossTheLock(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	expires := time.Now().Add(time.Hour)
	codes := w.issue(100, 1, &expires)
	locked := make(chan struct{})
	release := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- w.db.Tx(context.Background(), pg.PlatformScope(), func(tx pg.Tx) error {
			if err := queries.LockRedeemAccount(context.Background(), tx, advisoryKey(0x52, w.account)); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	done := make(chan error, 1)
	go func() {
		_, err := w.redeem(codes[0].Code, "late")
		done <- err
	}()
	// The database reports the redemption waiting for the account's advisory lock while the code is still valid; the
	// interleaving is observed, not inferred from how long a sleep lasted.
	for deadline := time.Now().Add(10 * time.Second); ; {
		select {
		case err := <-done:
			t.Fatalf("the redemption did not wait for the lock: %v", err)
		default:
		}
		if w.count(w.p.platform, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE l.locktype = 'advisory' AND NOT l.granted AND a.datname = current_database()
			AND a.wait_event_type = 'Lock' AND a.wait_event = 'advisory'`) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the redemption never asked for the account's lock")
		}
		time.Sleep(time.Millisecond)
	}
	// The code expires while the redemption waits; its state is set directly rather than by waiting out a short expiry.
	w.exec(fmt.Sprintf("UPDATE payments.redeem_codes SET expires_at = now() - interval '1 second' WHERE id = '%s'", uuid.UUID(codes[0].ID)))
	close(release)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrRedeemExpired) {
		t.Fatalf("a redemption that waited past the expiry: %v", err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_codes WHERE id = $1 AND state = 'enabled'", uuid.UUID(codes[0].ID)); got != 1 {
		t.Fatalf("the expired code's state changed")
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.redeem_redemptions"); got != 0 {
		t.Fatalf("%d redemptions after the refusal, want 0", got)
	}
	if got := w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", w.account); got != 0 {
		t.Fatalf("the balance after the refusal: %d", got)
	}
}

// TestRedeemIsTenantScoped: a code of one site is unknown at another, and an account's history is its own.
func TestRedeemIsTenantScoped(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	codes := w.issue(100, 1, nil)
	if _, err := w.p.Redeem(w.t.Context(), Redeem{
		Site: w.site(w.siteB, "USD"), Account: w.acctB, User: w.user, Code: codes[0].Code, Key: "other-site",
		ClientIP: netip.MustParseAddr("203.0.113.8"),
	}); !errors.Is(err, ErrRedeemInvalid) {
		t.Fatalf("a site A code at site B: %v", err)
	}
	if _, err := w.redeem(codes[0].Code, "mine"); err != nil {
		t.Fatal(err)
	}
	history, _, err := w.p.ListRedemptions(w.t.Context(), w.accountA(), w.account, nil, 50)
	if err != nil || len(history) != 1 || history[0].Hint != codes[0].Hint {
		t.Fatalf("the account's history: %+v, %v", history, err)
	}
	if other, _, err := w.p.ListRedemptions(w.t.Context(), w.accountB(), w.account, nil, 50); err != nil || len(other) != 0 {
		t.Fatalf("another account's history: %+v, %v", other, err)
	}
	// The account's page reads its history, never the codes themselves, not even the hashes of its own site.
	if n := w.count(w.accountA(), "SELECT count(*) FROM payments.redeem_codes"); n != 0 {
		t.Fatalf("an account scope read %d code rows", n)
	}
}

// TestListCodesNoteIsLiteral: the note filter is a prefix, not a LIKE pattern: `_`, `%` and `\` match themselves.
func TestListCodesNoteIsLiteral(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.issueNote("A_2026")
	w.issueNote("AB2026")
	w.issueNote("_private")
	w.issueNote("%off")
	w.issueNote(`\path`)
	for _, tc := range []struct {
		note string
		want string
	}{
		{"A_", "A_2026"},
		{"_", "_private"},
		{"%", "%off"},
		{`\`, `\path`},
		{"a_", "A_2026"},
	} {
		codes, _, err := w.p.ListCodes(w.t.Context(), siteScope(w.siteA), w.siteA, "", tc.note, nil, 50)
		if err != nil || len(codes) != 1 || codes[0].Note != tc.want {
			t.Fatalf("the note %q: %+v, %v", tc.note, codes, err)
		}
	}
}

// TestListCodesFiltersAndPages: the state a page shows filters, a note prefix filters, and a page ends at the cursor it
// answers, never repeating or skipping a code.
//
//nolint:cyclop,gocognit // every filter and both page boundaries are the test.
func TestListCodesFiltersAndPages(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	future := time.Now().Add(time.Hour)
	enabled := w.issue(100, 2, &future)
	expired := w.issue(200, 1, &future)
	w.exec("UPDATE payments.redeem_codes SET expires_at = now() - interval '1 hour' WHERE id = '" + uuid.UUID(expired[0].ID).String() + "'")
	used := w.issue(300, 1, nil)
	if _, err := w.redeem(used[0].Code, "used"); err != nil {
		t.Fatal(err)
	}
	list := func(state RedeemState, after *page.Cursor, size int) ([]RedeemCode, *page.Cursor) {
		w.t.Helper()
		codes, next, err := w.p.ListCodes(w.t.Context(), siteScope(w.siteA), w.siteA, state, "", after, size)
		if err != nil {
			w.t.Fatal(err)
		}
		return codes, next
	}
	if codes, _ := list(RedeemEnabled, nil, 50); len(codes) != 2 {
		t.Fatalf("%d enabled codes, want 2", len(codes))
	}
	if codes, _ := list(RedeemExpired, nil, 50); len(codes) != 1 || codes[0].Amount != 200 {
		t.Fatalf("the expired codes: %+v", codes)
	}
	if codes, _ := list(RedeemUsed, nil, 50); len(codes) != 1 || codes[0].ID != used[0].ID {
		t.Fatalf("the used codes: %+v", codes)
	}
	all, next := list("", nil, 2)
	if len(all) != 2 || next == nil {
		t.Fatalf("the first page: %d codes, next %v", len(all), next)
	}
	second, rest := list("", next, 2)
	if len(second) != 2 || rest != nil {
		t.Fatalf("the second page: %d codes, next %v", len(second), rest)
	}
	seen := map[CodeID]bool{}
	for _, c := range append(all, second...) {
		if seen[c.ID] {
			t.Fatalf("code %s on two pages", c.ID.TypeID())
		}
		seen[c.ID] = true
	}
	for _, c := range enabled {
		if !seen[c.ID] {
			t.Fatalf("code %s on no page", c.ID.TypeID())
		}
	}
}
