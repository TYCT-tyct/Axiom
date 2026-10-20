package accounts

import (
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"uuid"

	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
)

// Security events are one row per sign-in, registration and two-step verification, refused or not, so that an operator
// can answer who tried what, from where and when without a store of personal data (README.md「安全事件」). The address
// of the client is kept only as an HMAC under the key API keys are hashed with, so events of one address can be
// recognized together without the address being kept anywhere.

// The kinds of event: the three ways a person proves who they are.
const (
	eventSignIn = "sign_in"
	eventSignUp = "sign_up"
	eventMFA    = "mfa"
)

// The reasons a person is refused, as the table names them.
const (
	reasonBadCredentials  = "bad_credentials"
	reasonTooManyAttempts = "too_many_attempts"
	reasonBadCode         = "bad_code"
	reasonBadEmail        = "bad_email"
	reasonWeakPassword    = "weak_password"
	reasonInvalidInvite   = "invalid_invite"
	reasonEmailTaken      = "email_taken"
)

// refusals pairs the errors a person is refused with, in the order a reason is looked for, with the event's name for
// the refusal. An error that is not here is no security event: the database failed or the node was busy, and neither
// says anything about the person.
var refusals = []struct {
	err    error
	reason string
}{
	{ErrBadCredentials, reasonBadCredentials},
	{ErrBadCode, reasonBadCode},
	{ErrTooManyAttempts, reasonTooManyAttempts},
	{ErrBadEmail, reasonBadEmail},
	{ErrWeakPassword, reasonWeakPassword},
	{ErrInvalidInvite, reasonInvalidInvite},
	{ErrEmailTaken, reasonEmailTaken},
}

// securityEvent is one attempt at proving who someone is, as it is recorded: what was tried and where, and the user
// and session it was about once the attempt learned them. An attempt that never learned its site — the domain is no
// domain of this node's — is not recorded: there is no tenant to file it under.
type securityEvent struct {
	kind    string
	site    SiteID
	account AccountID
	user    UserID
	session SessionID
	ip      netip.Addr
	refused bool
	reason  string
}

// outcome records how the attempt ended and reports whether it is an event at all: one that went through, or a refusal
// with a reason of its own. A failure of another kind is left unrecorded.
func (e *securityEvent) outcome(err error) bool {
	if err == nil {
		return true
	}
	for _, r := range refusals {
		if errors.Is(err, r.err) {
			e.refused, e.reason = true, r.reason
			return true
		}
	}
	return false
}

// record writes the event, in its own transaction in the platform scope, and never changes the answer the attempt got:
// an event that cannot be written is counted and the person is told what happened anyway, because refusing a sign-in
// over its record would say more than the refusal does. The insert is the only statement, so a retry is not needed:
// the request that is gone takes its event with it.
func (s *Service) record(ctx context.Context, e securityEvent) {
	if e.site == (SiteID{}) {
		return
	}
	err := s.db.Tx(ctx, s.platform, func(tx pg.Tx) error {
		return s.queries.InsertSecurityEvent(ctx, tx, InsertSecurityEventParams{
			SiteID: uuid.UUID(e.site), AccountID: optional(e.account), UserID: optional(e.user),
			SessionID: optional(e.session), RequestID: httpserver.RequestID(ctx), Kind: e.kind, Refused: e.refused,
			Reason: e.reason, IpHash: s.ipHash(e.ip),
		})
	})
	if err != nil {
		s.lost.Add(ctx, 1)
	}
}

// ipHash is the client's address as the table keeps it: HMAC under the newest key, so a rotation makes new events
// unrecognizable beside old ones instead of wrong, or empty when the request had no address. The prefix keeps these
// apart from the hashes of API keys, which use the same key.
func (s *Service) ipHash(ip netip.Addr) string {
	if !ip.IsValid() {
		return ""
	}
	sum := s.hashes.Sum(s.versions[0], []byte("security-event ip\n"+ip.String()))
	return hex.EncodeToString(sum[:])
}

// optional is id as a nullable column, null for the zero ID, which names nothing.
func optional[T ~[16]byte](id T) *uuid.UUID {
	var zero T
	if id == zero {
		return nil
	}
	value := uuid.UUID(id)
	return &value
}
