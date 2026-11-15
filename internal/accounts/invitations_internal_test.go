package accounts

import (
	"errors"
	"testing"
)

func TestInvitationDisplay(t *testing.T) {
	for _, tc := range []struct{ email, want string }{
		{"alice@gmail.com", "a***@g***.com"},
		{"x@domain", "x***@d***"},
		{"界@例.测试", "界***@例***.测试"},
		{"", "Unavailable user"},
		{"bad", "Unavailable user"},
		{"@domain", "Unavailable user"},
		{"local@", "Unavailable user"},
	} {
		if got := invitationDisplay(tc.email); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.email, got, tc.want)
		}
	}
}

func TestInvitationsRequireSession(t *testing.T) {
	for _, p := range []Principal{{}, {user: UserID{1}, surface: Admin}, {user: UserID{1}, surface: Console, level: levelPending}} {
		if _, _, _, err := (&Service{}).Invitations(t.Context(), p, nil, 1); !errors.Is(err, ErrNoSession) {
			t.Fatal(err)
		}
	}
}
