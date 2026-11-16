package accounts

import (
	"errors"
	"testing"
)

func TestInvitationsRequireSession(t *testing.T) {
	for _, p := range []Principal{{}, {user: UserID{1}, surface: Admin}, {user: UserID{1}, surface: Console, level: levelPending}} {
		if _, _, _, err := (&Service{}).Invitations(t.Context(), p, nil, 1); !errors.Is(err, ErrNoSession) {
			t.Fatal(err)
		}
	}
}
