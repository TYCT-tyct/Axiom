package accounts_test

import (
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/pgtest"
)

func TestInvitations(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p, err := x.principal(t, console, x.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	code, rows, next, err := x.s.Invitations(t.Context(), p, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if code == "" || rows == nil || len(rows) != 0 || next != nil {
		t.Fatalf("empty invitations: %q %+v %v %v", code, rows, next, err)
	}
	users := registerInvitees(t, x, code)
	first, next := invitationPage(t, x, p, nil, 2, []bool{true, true})
	if next == nil || *next != uuid.UUID(users[1]) {
		t.Fatal("missing next page")
	}
	last, end := invitationPage(t, x, p, next, 2, []bool{true})
	if end != nil {
		t.Fatal("unexpected next page")
	}
	if first[0].RegisteredAt.Before(first[1].RegisteredAt) || first[1].RegisteredAt.Before(last[0].RegisteredAt) {
		t.Fatal("registration times are not descending")
	}
	checkInvitationIsolation(t, x, p)
	checkInvitationTombstone(t, x, p, users[2], first[0])
}

func checkInvitationIsolation(t *testing.T, x site, p accounts.Principal) {
	t.Helper()
	issued, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: "alice@gmail.com", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	other, err := x.principal(t, console, issued)
	if err != nil {
		t.Fatal(err)
	}
	otherCode, unrelated, _, err := x.s.Invitations(t.Context(), other, nil, 200)
	if err != nil || len(unrelated) != 0 {
		t.Fatalf("other inviter: %+v %v", unrelated, err)
	}
	if _, err := x.s.Register(t.Context(), accounts.RegisterRequest{Host: console, InviteCode: otherCode, Email: "other@example.com", Password: password}); err != nil {
		t.Fatal(err)
	}
	_, own, _, err := x.s.Invitations(t.Context(), p, nil, 200)
	if err != nil || len(own) != 3 {
		t.Fatalf("own invitations: %+v %v", own, err)
	}
}

func registerInvitees(t *testing.T, x site, code string) []accounts.UserID {
	t.Helper()
	var users []accounts.UserID
	for _, email := range []string{"alice@gmail.com", "bob@example.com", "carol@example.com"} {
		registered, err := x.s.Register(t.Context(), accounts.RegisterRequest{Host: console, InviteCode: code, Email: email, Password: password})
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, registered.User)
	}
	return users
}

func invitationPage(t *testing.T, x site, p accounts.Principal, after *uuid.UUID, limit int, want []bool) ([]accounts.Invitee, *uuid.UUID) {
	t.Helper()
	_, rows, next, err := x.s.Invitations(t.Context(), p, after, limit)
	if err != nil {
		t.Fatal(err)
	}
	available := make([]bool, len(rows))
	for i, row := range rows {
		available[i] = row.Available
	}
	if diff := cmp.Diff(want, available); diff != "" {
		t.Fatalf("invitees (-want +got): %s", diff)
	}
	return rows, next
}

func checkInvitationTombstone(t *testing.T, x site, p accounts.Principal, user accounts.UserID, first accounts.Invitee) {
	t.Helper()
	// A retained tombstone does not erase its immutable relation or registration time.
	if err := pgtest.Run(t.Context(), x.database.Superuser, "UPDATE accounts.users SET status='disabled', email='erased@invalid' WHERE id='"+uuid.UUID(user).String()+"'"); err != nil {
		t.Fatal(err)
	}
	retained, _ := invitationPage(t, x, p, nil, 2, []bool{false, true})
	if retained[0].RegisteredAt != first.RegisteredAt {
		t.Fatal("registration time changed")
	}
}
