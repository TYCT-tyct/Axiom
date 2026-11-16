package accounts

import (
	"context"
	"fmt"
	"time"
	"uuid"
)

// WellFormedKey and NewKeyText expose the key format to its tests.
var (
	WellFormedKey = wellFormedKey
	NewKeyText    = newKeyText
	TOTPStep      = totpStep
)

// AccountKeysByPresetQuery is the query PresetKeys counts one account's keys with, for its plan's test.
const AccountKeysByPresetQuery = accountKeysByPreset

// LookupTimeout is how long a key lookup whose caller set no deadline may take.
const LookupTimeout = lookupTimeout

// ClientSummary and IPGroup expose the device list's summaries to its tests.
var ClientSummary, IPGroup = clientSummary, ipGroup

// SessionID is the session a sign-in issued, for tests that list or end it.
func (i Issued) SessionID() SessionID { return SessionID(i.session) }

// Reload reloads the snapshot now, as the reload loop does every 30 seconds, within the same time limit.
func (s *Service) Reload(ctx context.Context) error { return s.reloadWithin(ctx) }

// TOTPCode is the code an authenticator app shows for the base32 secret at the time.
func TOTPCode(secretText string, at time.Time) string {
	key, err := totpBase32.DecodeString(secretText)
	if err != nil {
		panic(err)
	}
	return totpCode(key, at.Unix()/totpPeriod)
}

// HashTurn takes one of the node's turns at password hashing until release.
func (s *Service) HashTurn(ctx context.Context) (release func(), err error) {
	if err := s.guards.hashing.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	return func() { s.guards.hashing.Release(1) }, nil
}

// Hash runs compute in one of the node's turns at password hashing, as every argon2id computation does.
func (s *Service) Hash(ctx context.Context, compute func()) error { return s.hash(ctx, compute) }

// UsePasswordRecovery wires a test's queue without replacing the node's other dependencies.
func (s *Service) UsePasswordRecovery(r *PasswordRecovery) { s.recovery = r }

// ProcessPasswordReset runs one durable request under explicit test synchronization.
func (r *PasswordRecovery) ProcessPasswordReset(ctx context.Context, id uuid.UUID) error {
	return r.process(ctx, passwordResetArgs{ID: id})
}

// PurgePasswordResets advances the expiry worker under explicit test control.
func (r *PasswordRecovery) PurgePasswordResets(ctx context.Context) error { return r.purge(ctx) }

// UseRegistration wires a test's open-registration worker without replacing the node's other dependencies.
func (s *Service) UseRegistration(r *Registration) { s.registration = r }

// SharedKeyForTest names the bucket a subject uses in the shared store.
func (s *Service) SharedKeyForTest(site SiteID, kind, value string) string {
	return s.sharedKey(site, kind, value)
}

// ProcessRegistration runs one pending registration's mail job under explicit test synchronization.
func (r *Registration) ProcessRegistration(ctx context.Context, id uuid.UUID) error {
	return r.process(ctx, registrationArgs{ID: id})
}

// PurgeRegistrations advances the expiry worker under explicit test control.
func (r *Registration) PurgeRegistrations(ctx context.Context) error {
	return r.cleanup(ctx, nil)
}

// ProofWorked and ProofNonce expose the page's proof of work to its tests.
var ProofWorked = proofWorked

// ProofNonce finds a nonce for the test's work.
func ProofNonce(site SiteID, email string, bitsWanted int) string {
	return ProofNonceNotFor(site, email, "", bitsWanted)
}

// ProofNonceNotFor is a nonce that proves email's work but not other's: the site id is random per run, so the first
// nonce for one address also proves another's one run in 2^bits (TestOpenRegistrationHumanCheck failed on it once).
func ProofNonceNotFor(site SiteID, email, other string, bitsWanted int) string {
	for i := uint64(0); ; i++ {
		nonce := fmt.Sprintf("%d", i)
		if proofWorked(site, email, nonce, bitsWanted) && (other == "" || !proofWorked(site, other, nonce, bitsWanted)) {
			return nonce
		}
	}
}

// PrincipalForTest is the signed-in principal a session of user would make, for operations whose test has no request.
func PrincipalForTest(site SiteID, account AccountID, user UserID) Principal {
	return Principal{site: site, account: account, user: user, level: levelPassword}
}

// ObserveLookupJoins calls f after each request joins the shared key lookup, before it waits for the result, so a test
// can wait until every target request is inside it instead of sleeping.
func (s *Service) ObserveLookupJoins(f func()) { s.lookupJoined = f }

// QueueKeyUse and FlushKeyUses let activity tests control observation and persistence independently.
func (s *Service) QueueKeyUse(key KeyID, at time.Time) {
	s.keyUses <- keyUse{uuid.UUID(key), at}
}
func (s *Service) FlushKeyUses(ctx context.Context) error { return s.flushKeyUses(ctx) }

const ReadKeyMetadataQuery = readKeyMetadata
