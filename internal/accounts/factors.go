package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

var (
	// ErrBadCode is a TOTP or recovery code refused, or a pending session out of attempts.
	ErrBadCode = errors.New("accounts: wrong code")
	// ErrBadSecret is a TOTP secret that is not 20 bytes of base32.
	ErrBadSecret = errors.New("accounts: not a TOTP secret")
	// ErrTOTPOn is turning on two-step verification for a user who has it: replacing or turning it off is M2's
	// (README.md「注册、登录与会话」), and in M1 only the command line resets it.
	ErrTOTPOn = errors.New("accounts: two-step verification is already on")
)

const recoveryCodeCount = 10 // each 80 random bits

// VerifyMFA turns the request's pending session into a two-step-verified one when code is the user's current TOTP
// code or an unused recovery code. It spends one of the IP's attempts and, until it succeeds, one of the user's code
// failures, across challenges (ErrTooManyAttempts when none is left). Under the user's lock it checks the pending session (not revoked or expired, its
// factor version the user's, attempts left) and spends the code with a conditional update that must change one row;
// a wrong code only counts an attempt, which commits. The pending session is revoked and a new one with new tokens
// issued, so one challenge yields one session.
func (s *Service) VerifyMFA(ctx context.Context, r *http.Request, code string, ip netip.Addr, userAgent string) (out Issued, err error) {
	d0, _, domainErr := s.domain(r.Host)
	if domainErr != nil {
		return Issued{}, domainErr
	}
	event := securityAttempt{site: d0.site, surface: d0.surface}
	defer func() { s.failed(ctx, event, "security.mfa_failed", &err) }()
	found, tokenHash, d, err := s.sessionOf(ctx, r)
	if err != nil {
		return Issued{}, err
	}
	event.user, event.account, event.session = found.UserID, found.AccountID, found.SessionID
	if err := s.fromIP(ctx, d.site, ip); err != nil {
		return Issued{}, err
	}
	l := s.snapshot.Load().sites[d.site].limits
	var issued Issued
	err = failing(s, ctx, s.userLimit(d.site, found.UserID, l), func() error {
		issued, err = s.verifyMFA(ctx, r, code, found, tokenHash, d, l.challengeAttempts, ip, userAgent)
		return err
	})
	return issued, err
}

func (s *Service) verifyMFA(ctx context.Context, r *http.Request, code string, found SessionTenantRow, tokenHash []byte, d domain, attempts int, ip netip.Addr, userAgent string) (Issued, error) {
	var issued Issued
	wrong := false
	err := s.db.Tx(ctx, pg.AccountScope(found.AccountID), func(tx pg.Tx) error {
		pending, locked, err := s.lockPending(ctx, tx, found.UserID, tokenHash, attempts)
		if err != nil {
			return err
		}
		if err := checkCSRF(r, d, pending.CsrfToken); err != nil {
			return err
		}
		if spent, err := s.spendCode(ctx, tx, found.UserID, locked, code); err != nil || !spent {
			wrong = err == nil
			return errors.Join(err, s.queries.CountMFAAttempt(ctx, tx, pending.ID))
		}
		if revoked, err := s.queries.RevokeSession(ctx, tx, pending.ID); err != nil || revoked != 1 {
			return errors.Join(err, ErrNoSession)
		}
		issued, err = s.issue(ctx, tx, newSession{
			user: found.UserID, account: found.AccountID, site: s.snapshot.Load().sites[d.site], surface: d.surface,
			level: levelMFA, factorVersion: locked.FactorVersion, ip: ip, userAgent: userAgent,
		})
		if err != nil {
			return err
		}
		return (securityAttempt{site: d.site, user: found.UserID, account: found.AccountID, session: issued.session, surface: d.surface}).record(ctx, tx, "security.mfa")
	})
	if err == nil && wrong {
		return Issued{}, ErrBadCode
	}
	return issued, err
}

// lockPending locks the user first, then reads the pending session with tokenHash: it must be unrevoked, unexpired,
// of the user's current factor version, with fewer than attempts made.
func (s *Service) lockPending(ctx context.Context, tx pg.Tx, user uuid.UUID, tokenHash []byte, attempts int) (PendingSessionRow, LockUserRow, error) {
	users, err := s.queries.LockUser(ctx, tx, user)
	if err != nil {
		return PendingSessionRow{}, LockUserRow{}, err
	}
	pending, err := s.queries.PendingSession(ctx, tx, tokenHash)
	if err != nil {
		return PendingSessionRow{}, LockUserRow{}, err
	}
	if len(users) == 0 || !users[0].Active || len(pending) == 0 || pending[0].FactorVersion != users[0].FactorVersion {
		return PendingSessionRow{}, LockUserRow{}, ErrNoSession
	}
	if int(pending[0].MfaAttempts) >= attempts {
		return PendingSessionRow{}, LockUserRow{}, ErrBadCode
	}
	return pending[0], users[0], nil
}

// spendCode spends code as the user's TOTP code of a step not used yet, or as an unused recovery code of the current
// factor version; each update must change exactly one row.
func (s *Service) spendCode(ctx context.Context, tx pg.Tx, user uuid.UUID, locked LockUserRow, code string) (bool, error) {
	if totp, err := s.openTOTP(user, locked); err == nil {
		if step, ok := totpStep(totp, code, s.now()); ok {
			changed, err := s.queries.AdvanceTOTP(ctx, tx, AdvanceTOTPParams{Step: step, ID: user})
			return changed == 1, err
		}
	} else if !errors.Is(err, errNoTOTP) {
		return false, err
	}
	hash := recoveryHash(code)
	changed, err := s.queries.UseRecoveryCode(ctx, tx, UseRecoveryCodeParams{
		UserID: user, CodeHash: hash[:], FactorVersion: locked.FactorVersion,
	})
	return changed == 1, err
}

var errNoTOTP = errors.New("accounts: no TOTP secret")

// openTOTP decrypts the user's TOTP secret, sealed to its row and purpose.
func (s *Service) openTOTP(user uuid.UUID, locked LockUserRow) ([]byte, error) {
	if locked.TotpKeyVersion == nil {
		return nil, errNoTOTP
	}
	opened, err := s.keyring.Open(secret.Sealed{
		Ciphertext: locked.TotpCiphertext, Nonce: locked.TotpNonce, WrappedDEK: locked.TotpWrappedDek,
		KeyVersion: uint32(*locked.TotpKeyVersion), // #nosec G115 -- the column holds a uint32
	}, totpLocation(user))
	if err != nil {
		return nil, fmt.Errorf("open the TOTP secret: %w", err)
	}
	return totpBase32.DecodeString(opened.Reveal())
}

func totpLocation(user uuid.UUID) secret.Location {
	return secret.Location{Table: "accounts.users", RowID: user.String(), Purpose: "totp"}
}

// BeginTOTP is a new TOTP secret for p to add to an authenticator app, as base32 and as the app's URI. Nothing is
// stored until ConfirmTOTP.
func (*Service) BeginTOTP(p Principal) (secretText, uri string) {
	key := newTOTPSecret()
	return totpBase32.EncodeToString(key), totpURI(key, p.user.TypeID())
}

// FactorChange is a change to a user's factors: a new TOTP secret confirmed with the password and one of its codes,
// or new recovery codes confirmed with a current TOTP code.
type FactorChange struct {
	Secret, Password, Code string
	ClientIP               netip.Addr
	UserAgent              string
}

// ConfirmTOTP turns on two-step verification for p with the secret BeginTOTP gave, when password is the user's and
// code is the secret's, and the user has none yet: under the user's lock, one already on is ErrTOTPOn and changes
// nothing. The factor version goes up, so every other session and recovery code stops working; the new recovery codes
// and a two-step-verified session for this device are returned. A wrong code or password spends one of the user's
// code failures, as in VerifyMFA.
func (s *Service) ConfirmTOTP(ctx context.Context, p Principal, c FactorChange) (out Issued, recovery []string, err error) {
	defer func() { s.failed(ctx, p.securityAttempt(), "security.factor_failed", &err) }()
	l := s.snapshot.Load().sites[p.site].limits
	var issued Issued
	var codes []string
	var refused error
	err = failing(s, ctx, s.userLimit(p.site, uuid.UUID(p.user), l), func() error {
		issued, codes, refused = s.confirmTOTP(ctx, p, c)
		if errors.Is(refused, ErrBadCode) || errors.Is(refused, ErrBadCredentials) {
			return refused // a wrong guess is the only thing that counts; the bucket is the user's, shared with sign-in's
		}
		return nil
	})
	if err == nil {
		err = refused
	}
	return issued, codes, err
}

func (s *Service) confirmTOTP(ctx context.Context, p Principal, c FactorChange) (Issued, []string, error) {
	key, err := totpBase32.DecodeString(strings.ToUpper(c.Secret))
	if err != nil || len(key) != 20 {
		return Issued{}, nil, ErrBadSecret
	}
	step, ok := totpStep(key, c.Code, s.now())
	if !ok {
		return Issued{}, nil, ErrBadCode
	}
	if err := s.checkPassword(ctx, p, c.Password); err != nil {
		return Issued{}, nil, err
	}
	user := uuid.UUID(p.user)
	sealed := s.keyring.Seal(secret.New(totpBase32.EncodeToString(key)), totpLocation(user))
	version := int64(sealed.KeyVersion)
	return s.changeFactors(ctx, p, c, "user.totp_enable", func(tx pg.Tx, locked LockUserRow) error {
		if locked.Mfa {
			return ErrTOTPOn
		}
		// A passkey is already a second factor: adding TOTP must not be a way to replace it with a factor the
		// admin listener can set up from a password session, so this ceremony needs the passkey proved first.
		if locked.Passkeys {
			if !p.Verified() {
				return ErrMFARequired
			}
			// The database trigger is the same boundary an older node cannot bypass: only this transaction, after the
			// passkey check above, may mark the enrollment as having proved the passkey.
			if _, err := tx.Exec(ctx, "SELECT set_config('axiom.passkey_proved', $1, true)", user.String()); err != nil {
				return err
			}
		}
		return s.queries.EnableTOTP(ctx, tx, EnableTOTPParams{
			Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: &version,
			Step: step, ID: user,
		})
	})
}

// RegenerateRecoveryCodes replaces p's recovery codes when code is a current TOTP code, raising the factor version
// as ConfirmTOTP does. A wrong code counts among the user's code failures, as in VerifyMFA.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, p Principal, c FactorChange) (issued Issued, codes []string, err error) {
	defer func() { s.failed(ctx, p.securityAttempt(), "security.factor_failed", &err) }()
	l := s.snapshot.Load().sites[p.site].limits
	err = failing(s, ctx, s.userLimit(p.site, uuid.UUID(p.user), l), func() error {
		issued, codes, err = s.regenerateRecoveryCodes(ctx, p, c)
		return err
	})
	return issued, codes, err
}

func (s *Service) regenerateRecoveryCodes(ctx context.Context, p Principal, c FactorChange) (Issued, []string, error) {
	user := uuid.UUID(p.user)
	return s.changeFactors(ctx, p, c, "user.recovery_codes_regenerate", func(tx pg.Tx, locked LockUserRow) error {
		key, err := s.openTOTP(user, locked)
		if err != nil {
			return errors.Join(err, ErrBadCode)
		}
		step, ok := totpStep(key, c.Code, s.now())
		if !ok {
			return ErrBadCode
		}
		if changed, err := s.queries.AdvanceTOTP(ctx, tx, AdvanceTOTPParams{Step: step, ID: user}); err != nil || changed != 1 {
			return errors.Join(err, ErrBadCode)
		}
		return s.queries.BumpFactorVersion(ctx, tx, user)
	})
}

// changeFactors locks p's user, refuses factors changed since p's session, runs change, which raises the factor
// version, then stores new recovery codes, issues a two-step-verified session under the new version and records
// action in the audit log, all in one transaction: no change without its entry. The entry names the user, never a
// secret or a code.
func (s *Service) changeFactors(ctx context.Context, p Principal, c FactorChange, action string, change func(pg.Tx, LockUserRow) error) (Issued, []string, error) {
	user, account := uuid.UUID(p.user), uuid.UUID(p.account)
	var codes []string
	var issued Issued
	err := s.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) error {
		locked, err := s.lockUser(ctx, tx, user, p.factorVersion)
		if err != nil {
			return err
		}
		if err := change(tx, locked); err != nil {
			return err
		}
		version := locked.FactorVersion + 1
		codes, err = s.insertRecoveryCodes(ctx, tx, user, account, locked.SiteID, version)
		if err != nil {
			return err
		}
		if issued, err = s.issue(ctx, tx, newSession{
			user: user, account: account, site: s.snapshot.Load().sites[p.site], surface: p.surface, level: levelMFA,
			factorVersion: version, ip: c.ClientIP, userAgent: c.UserAgent,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{Actor: audit.User(user), Action: action, Target: audit.Target{
			Module: "accounts", Kind: "user", ID: p.user.TypeID(), Site: locked.SiteID, Account: account,
		}})
	})
	if err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

// checkPassword checks password against p's user's, outside any transaction that holds a lock.
func (s *Service) checkPassword(ctx context.Context, p Principal, password string) error {
	var stored []UserPasswordRow
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		stored, err = s.queries.UserPassword(ctx, tx, uuid.UUID(p.user))
		return err
	}); err != nil {
		return fmt.Errorf("check the password: %w", err)
	}
	if len(stored) == 0 {
		return ErrBadCredentials
	}
	matched := false
	if err := s.hash(ctx, func() { matched = passwordMatches(stored[0].PasswordHash, password) }); err != nil {
		return err
	}
	if !matched {
		return ErrBadCredentials
	}
	return nil
}

var recoveryAlphabet = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newRecoveryCode is 80 random bits as four groups of four characters.
func newRecoveryCode() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	t := recoveryAlphabet.EncodeToString(b)
	return t[0:4] + "-" + t[4:8] + "-" + t[8:12] + "-" + t[12:16]
}

// recoveryHash is the SHA-256 of a code as typed, ignoring case, spaces and hyphens. 80 random bits need no slow hash.
func recoveryHash(code string) [32]byte {
	normal := strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToLower(code))
	return sha256.Sum256([]byte(normal))
}

func (s *Service) insertRecoveryCodes(ctx context.Context, tx pg.Tx, user, account, site uuid.UUID, version int64) ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		codes[i] = newRecoveryCode()
		hash := recoveryHash(codes[i])
		if err := s.queries.InsertRecoveryCode(ctx, tx, InsertRecoveryCodeParams{
			ID: uuid.NewV7(), SiteID: site, AccountID: account, UserID: user, FactorVersion: version, CodeHash: hash[:],
		}); err != nil {
			return nil, err
		}
	}
	return codes, nil
}
