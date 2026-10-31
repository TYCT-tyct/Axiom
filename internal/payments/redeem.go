package payments

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype" //nolint:depguard // the generated parameters take an optional timestamp

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/clientip"
	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/typeid"
)

// Redeem codes (docs/domains/payments.md「兑换码」): what a site issues, what an account redeems, and the limits that keep
// guessing them expensive.

var (
	// ErrRedeemInvalid is a code that is not one of the site's: malformed, or never issued.
	ErrRedeemInvalid = errors.New("payments: the redeem code is not valid")
	// ErrRedeemUsed is a code already redeemed. A code credits once, whichever account holds it.
	ErrRedeemUsed = errors.New("payments: the redeem code was redeemed already")
	// ErrRedeemExpired is a code past its expiry.
	ErrRedeemExpired = errors.New("payments: the redeem code expired")
	// ErrRedeemDisabled is a code an administrator disabled.
	ErrRedeemDisabled = errors.New("payments: the redeem code was disabled")
	// ErrRedeemLimited is an account or an IP group that tried to redeem too often lately; a *Wait carries how long.
	ErrRedeemLimited = errors.New("payments: too many redeem attempts lately")
)

// The limits of attempts that fail. A code's alphabet is 32 characters without look-alikes, so guessing one of 20
// characters is hopeless; the limits exist so that a leak of one site's codes, or a list of them tried from many
// accounts, is slow and visible rather than instant. Thirty attempts an hour for an account follows the reference
// implementation (sub2api's redeemMaxFailedAttempts); an IP group gets a hundred, because a shared address is not one
// person. Both windows slide from an attempt's window start, not from the day.
const (
	redeemWindow          = time.Hour
	redeemAccountAttempts = 30
	redeemIPAttempts      = 100

	// redeemCodeLength is 100 bits of entropy, which the code's alphabet encodes in 20 characters.
	redeemCodeLength = 20
	// redeemNoteMax is the longest note a batch may carry, in runes.
	redeemNoteMax = 200
	// redeemIssueMax is the most codes one generation makes; a larger batch is several batches, so a mistake is found
	// before a hundred codes are posted.
	redeemIssueMax = 100
)

// redeemAlphabet is Crockford's base32, which leaves out I, L, O and U so that a typed code has one reading. 256 divides
// by 32 exactly, so taking a byte modulo the alphabet's length is uniform.
const redeemAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	codePrefix       = typeid.MustPrefix("rdm")
	redemptionPrefix = typeid.MustPrefix("rdn")
)

// CodeID is a redeem code's ID; RedemptionID is the credit a redeemed code made, which is also the credit batch's ID.
type CodeID uuid.UUID

func (id CodeID) TypeID() string { return codePrefix.Encode(uuid.UUID(id)) }

// ParseCodeID reads a code's ID as the API writes it; a malformed one is ErrNotFound, like an unknown one.
func ParseCodeID(s string) (CodeID, error) {
	id, err := codePrefix.Decode(s)
	if err != nil {
		return CodeID{}, ErrNotFound
	}
	return CodeID(id), nil
}

type RedemptionID uuid.UUID

func (id RedemptionID) TypeID() string { return redemptionPrefix.Encode(uuid.UUID(id)) }

// RedeemState is a code's state as a page shows it: an enabled code past its expiry reads expired.
type RedeemState string

const (
	RedeemEnabled  RedeemState = "enabled"
	RedeemUsed     RedeemState = "used"
	RedeemDisabled RedeemState = "disabled"
	RedeemExpired  RedeemState = "expired"
)

// RedeemCode is one code as the admin shows it. It holds no code: the text exists only in Issue's answer, at the moment
// it is made, and Hint is the four characters that let support find a code a user names.
type RedeemCode struct {
	ID         CodeID
	Site       uuid.UUID
	State      RedeemState
	Amount     int64 // billionths of a credit
	Note       string
	Hint       string
	ExpiresAt  *time.Time
	CreatedAt  time.Time
	CreatedBy  uuid.UUID
	RedeemedAt *time.Time
	DisabledAt *time.Time
	// Code is the plaintext, filled only by Issue and never read from the database.
	Code string
}

// Redemption is a code's credit as the console shows it: what was redeemed, when it was made, and when it expires. It
// never names the code, only its Hint.
type Redemption struct {
	ID        RedemptionID
	Amount    int64
	Hint      string
	ExpiresAt *time.Time
	CreatedAt time.Time
	Replayed  bool // a previous request with the same key answered, not a new redemption
}

// Issue is one generation of codes for a site: Count codes, each worth Amount billionths of a credit, all expiring at
// ExpiresAt, never if nil.
type Issue struct {
	Site      uuid.UUID
	Amount    int64
	Count     int
	Note      string
	ExpiresAt *time.Time
	Actor     audit.Actor
	CreatedBy uuid.UUID // the operator's user ID, stored as the codes' creator
	Reason    string
}

// Redeem is one account's attempt to turn a code into credit: the site the code belongs to, the account and user the
// console authorized, and the key that makes a lost answer safe to ask again.
type Redeem struct {
	Site     accounts.Site
	Account  uuid.UUID
	User     uuid.UUID
	Code     string
	Key      string
	ClientIP netip.Addr
}

func (r Redeem) check() error {
	switch {
	case uuid.UUID(r.Site.ID) == uuid.Nil():
		return errors.New("payments: a redemption needs a site")
	case r.Account == uuid.Nil() || r.User == uuid.Nil():
		return errors.New("payments: a redemption needs an account and a user")
	case r.Key == "" || len(r.Key) > 128:
		return &FieldError{"/idempotency_key", "must have 1 to 128 characters"}
	case !r.ClientIP.IsValid():
		return &FieldError{"/client_ip", "is required"}
	}
	return nil
}

// normalizeCode is the canonical text of a code a user typed: upper case, without the dashes or whitespace the page
// shows. It refuses any character the alphabet does not have, or a length other than a code's, before any database work.
func normalizeCode(s string) (string, bool) {
	if len(s) > 4*redeemCodeLength {
		return "", false
	}
	b := make([]byte, 0, redeemCodeLength)
	for _, r := range s {
		switch {
		case unicode.IsSpace(r) || r == '-':
			continue
		case r >= 'a' && r <= 'z':
			r -= 'a' - 'A'
		}
		if !strings.ContainsRune(redeemAlphabet, r) {
			return "", false
		}
		if len(b) == redeemCodeLength {
			return "", false
		}
		b = append(b, byte(r))
	}
	return string(b), len(b) == redeemCodeLength
}

// displayCode is the code with the dashes the page shows, five characters a group.
func displayCode(canonical string) string {
	var b strings.Builder
	for i := 0; i < len(canonical); i += 5 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(canonical[i:min(i+5, len(canonical))])
	}
	return b.String()
}

// newCode makes one code's canonical text from the system's random source.
func newCode() (string, error) {
	var raw [redeemCodeLength]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("payments: random code: %w", err)
	}
	for i := range raw {
		raw[i] = redeemAlphabet[raw[i]%byte(len(redeemAlphabet))]
	}
	return string(raw[:]), nil
}

func hashOf(canonical string) []byte {
	sum := sha256.Sum256([]byte(canonical))
	return sum[:]
}

// Issue makes Count codes for the site in the caller's transaction: each is written with only its hash, and the codes
// themselves are in the answer and nowhere else. The generation, the audit entry and the caller's idempotency claim
// commit together.
func IssueCodes(ctx context.Context, tx pg.Tx, g Issue) ([]RedeemCode, error) {
	switch {
	case g.Site == uuid.Nil():
		return nil, errors.New("payments: issue codes: no site")
	case g.CreatedBy == uuid.Nil():
		return nil, errors.New("payments: issue codes: no operator")
	case g.Amount <= 0:
		return nil, &FieldError{"/amount", "must be more than 0"}
	case g.Count < 1 || g.Count > redeemIssueMax:
		return nil, &FieldError{"/count", fmt.Sprintf("must be between 1 and %d", redeemIssueMax)}
	case utf8.RuneCountInString(g.Note) > redeemNoteMax:
		return nil, &FieldError{"/note", fmt.Sprintf("must have at most %d characters", redeemNoteMax)}
	case g.ExpiresAt != nil && !g.ExpiresAt.After(time.Now()):
		return nil, &FieldError{"/expires_at", "must be in the future"}
	}
	codes := make([]RedeemCode, 0, g.Count)
	for range g.Count {
		canonical, err := newCode()
		if err != nil {
			return nil, err
		}
		id := uuid.NewV7()
		c := RedeemCode{
			ID: CodeID(id), Site: g.Site, State: RedeemEnabled, Amount: g.Amount, Note: g.Note,
			Hint: canonical[len(canonical)-4:], ExpiresAt: g.ExpiresAt, CreatedAt: time.Now().UTC(),
			CreatedBy: g.CreatedBy,
		}
		if err := queries.InsertRedeemCode(ctx, tx, InsertRedeemCodeParams{
			ID: id, SiteID: g.Site, CodeHash: hashOf(canonical), CodeHint: c.Hint, Amount: g.Amount,
			Note: g.Note, ExpiresAt: timestamp(g.ExpiresAt), CreatedBy: c.CreatedBy,
		}); err != nil {
			return nil, fmt.Errorf("payments: write a redeem code: %w", err)
		}
		c.Code = displayCode(canonical)
		codes = append(codes, c)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: g.Actor, Action: "redeem_code.issue", Reason: g.Reason,
		Target: audit.Target{Module: "payments", Kind: "site", ID: g.Site.String(), Site: g.Site},
		Detail: map[string]any{
			"count": g.Count, "amount": g.Amount, "note": g.Note, "expires_at": g.ExpiresAt,
			"codes": codeIDs(codes),
		},
	}); err != nil {
		return nil, err
	}
	return codes, nil
}

// codeIDs is the IDs of a generation, for the audit log: IDs are not secrets.
func codeIDs(codes []RedeemCode) []string {
	ids := make([]string, len(codes))
	for i, c := range codes {
		ids[i] = c.ID.TypeID()
	}
	return ids
}

// RedeemCode turns r's code into a credit batch of the site's currency, booked as the credit billing's ledger records
// (billing.CreditRedeem): one code credits once, and the claim, the code's state and the credit commit together.
//
// An attempt that fails takes one of the account's and its IP group's places in the hourly limits, committed before the
// code is looked at, so a response cannot be a free guess. A replay of Key is not an attempt: it answers the redemption
// the first request made. Two requests with one code, or with one key, cannot both credit: the code's row and the
// account's lock serialize them.
func (p *Payments) Redeem(ctx context.Context, r Redeem) (Redemption, error) {
	if err := r.check(); err != nil {
		return Redemption{}, err
	}
	canonical, ok := normalizeCode(r.Code)
	if !ok {
		return Redemption{}, ErrRedeemInvalid
	}
	hash := hashOf(canonical)
	var replayed Redemption
	var found bool
	if err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) error {
		row, err := queries.RedemptionByKey(ctx, tx, RedemptionByKeyParams{AccountID: r.Account, IdemKey: r.Key})
		if errors.Is(err, pg.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("payments: read the redemption of the key: %w", err)
		}
		if !bytes.Equal(row.CodeHash, hash) {
			return ErrKeyReused
		}
		replayed, found = redemptionOf(row), true
		replayed.Replayed = true
		return nil
	}); err != nil || found {
		return replayed, err
	}
	if err := p.db.Tx(ctx, p.platform, func(tx pg.Tx) error { return p.spendRedeemAttempt(ctx, tx, r) }); err != nil {
		return Redemption{}, err
	}
	var out Redemption
	err := p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		if err := queries.LockRedeemAccount(ctx, tx, advisoryKey(0x52, r.Account)); err != nil {
			return fmt.Errorf("payments: lock the redeeming account: %w", err)
		}
		// Another request with this key may have committed after the read above and before the lock.
		existing, err := queries.RedemptionByKey(ctx, tx, RedemptionByKeyParams{AccountID: r.Account, IdemKey: r.Key})
		switch {
		case err == nil:
			if !bytes.Equal(existing.CodeHash, hash) {
				return ErrKeyReused
			}
			out = redemptionOf(existing)
			out.Replayed = true
			return nil
		case !errors.Is(err, pg.ErrNoRows):
			return fmt.Errorf("payments: read the redemption of the key: %w", err)
		}
		row, err := queries.RedeemCodeByHash(ctx, tx, RedeemCodeByHashParams{
			SiteID: uuid.UUID(r.Site.ID), CodeHash: hash,
		})
		if errors.Is(err, pg.ErrNoRows) {
			return ErrRedeemInvalid
		}
		if err != nil {
			return fmt.Errorf("payments: read the redeem code: %w", err)
		}
		code := row.PaymentsRedeemCode
		switch RedeemState(code.State) {
		case RedeemUsed:
			return ErrRedeemUsed
		case RedeemDisabled:
			return ErrRedeemDisabled
		}
		if code.ExpiresAt.Valid && !code.ExpiresAt.Time.After(row.Now) {
			return ErrRedeemExpired
		}
		if used, err := queries.UseRedeemCode(ctx, tx, code.ID); err != nil {
			return fmt.Errorf("payments: mark the redeem code used: %w", err)
		} else if used == 0 {
			return ErrRedeemUsed // its row is locked, so this is only a state that changed without the lock
		}
		id := uuid.NewV7()
		expires := optionalTimestamp(code.ExpiresAt)
		if err := queries.InsertRedemption(ctx, tx, InsertRedemptionParams{
			ID: id, SiteID: uuid.UUID(r.Site.ID), CodeID: code.ID, AccountID: r.Account, UserID: r.User,
			Amount: code.Amount, CodeHint: code.CodeHint, ExpiresAt: timestamp(expires), IdemKey: r.Key,
		}); err != nil {
			return fmt.Errorf("payments: write the redemption: %w", err)
		}
		if err := billing.CreditRedeem(ctx, tx, billing.RedeemCredit{
			ID: id, Site: uuid.UUID(r.Site.ID), Account: r.Account, Amount: code.Amount, ExpiresAt: expires,
			Actor: audit.User(r.User), Reason: "redeem code " + codeIDOf(code.ID),
		}); err != nil {
			return err
		}
		// Read the redemption back rather than build it from the arguments: the replay reads the same row, and the two
		// answers must be the same.
		written, err := queries.RedemptionByKey(ctx, tx, RedemptionByKeyParams{AccountID: r.Account, IdemKey: r.Key})
		if err != nil {
			return fmt.Errorf("payments: read the redemption: %w", err)
		}
		out = redemptionOf(written)
		return nil
	})
	if err != nil {
		return Redemption{}, err
	}
	return out, nil
}

// spendRedeemAttempt takes one of the account's and the IP group's places in their windows, committing even when the
// code is then refused. A limit used up answers a *Wait whose time is the window's end, and gives the account's place
// back with the transaction.
func (p *Payments) spendRedeemAttempt(ctx context.Context, tx pg.Tx, r Redeem) error {
	window := int64(redeemWindow / time.Second)
	if err := takeRedeemAttempt(ctx, tx, uuid.UUID(r.Site.ID), "account", r.Account.String(), window, redeemAccountAttempts); err != nil {
		return err
	}
	return takeRedeemAttempt(ctx, tx, uuid.UUID(r.Site.ID), "ip", clientip.Group(r.ClientIP).String(), window, redeemIPAttempts)
}

func takeRedeemAttempt(ctx context.Context, tx pg.Tx, site uuid.UUID, kind, subject string, window int64, max int32) error {
	taken, err := queries.ClaimRedeemAttempt(ctx, tx, ClaimRedeemAttemptParams{
		SiteID: site, Kind: kind, Subject: subject, WindowSeconds: window, MaxAttempts: max,
	})
	if err != nil {
		return fmt.Errorf("payments: count redeem attempts: %w", err)
	}
	if len(taken) > 0 {
		return nil
	}
	wait, err := queries.RedeemAttemptWindow(ctx, tx, RedeemAttemptWindowParams{
		SiteID: site, Kind: kind, Subject: subject, WindowSeconds: window,
	})
	if err != nil {
		return fmt.Errorf("payments: read the redeem attempt window: %w", err)
	}
	return &Wait{Err: ErrRedeemLimited, After: time.Duration(wait) * time.Second}
}

// codeIDOf is a code's TypeID, for an audit reason.
func codeIDOf(id uuid.UUID) string { return CodeID(id).TypeID() }

// ListCodes is one page of a site's codes, newest first, filtered by the state the page shows and by a note prefix. The
// cursor is the last row of the previous page; a further page exists exactly when cursor is not nil.
func (p *Payments) ListCodes(ctx context.Context, s pg.Scope, site uuid.UUID, state RedeemState, note string, after *page.Cursor, size int) ([]RedeemCode, *page.Cursor, error) {
	if site == uuid.Nil() {
		return nil, nil, errors.New("payments: list redeem codes: no site")
	}
	if size < 1 || size > 200 {
		return nil, nil, fmt.Errorf("payments: list redeem codes: page size %d, want 1 to 200", size)
	}
	switch state {
	case "", RedeemEnabled, RedeemUsed, RedeemDisabled, RedeemExpired:
	default:
		return nil, nil, &FieldError{"/state", "must be enabled, used, disabled or expired"}
	}
	at, id := endOfOrders, uuid.UUID{}
	if after != nil {
		at, id = after.At, after.ID
	}
	params := ListRedeemCodesParams{SiteID: site, AfterAt: at, AfterID: id, Max: int32(size + 1)}
	if state != "" {
		value := string(state)
		params.State = &value
	}
	if note != "" {
		params.Note = &note
	}
	rows, err := readRedeemCodes(ctx, p, s, params)
	if err != nil {
		return nil, nil, err
	}
	var next *page.Cursor
	if len(rows) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		next = &page.Cursor{At: last.CreatedAt, ID: uuid.UUID(last.ID)}
	}
	return rows, next, nil
}

// readRedeemCodes runs the page's query, so that ListCodes stays readable.
func readRedeemCodes(ctx context.Context, p *Payments, s pg.Scope, params ListRedeemCodesParams) ([]RedeemCode, error) {
	var rows []ListRedeemCodesRow
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		rows, err = queries.ListRedeemCodes(ctx, tx, params)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("payments: list redeem codes: %w", err)
	}
	codes := make([]RedeemCode, len(rows))
	for i, row := range rows {
		codes[i] = codeOf(row.PaymentsRedeemCode, row.Now)
	}
	return codes, nil
}

// DisableCode disables an enabled code in the caller's transaction, with its audit entry, and returns it as it now is.
// Disabling a disabled code changes nothing; a used code cannot be disabled, because its credit is already somebody's.
func DisableCode(ctx context.Context, tx pg.Tx, actor audit.Actor, by uuid.UUID, id CodeID, reason string) (RedeemCode, error) {
	row, err := queries.LockRedeemCode(ctx, tx, uuid.UUID(id))
	if errors.Is(err, pg.ErrNoRows) {
		return RedeemCode{}, ErrNotFound
	}
	if err != nil {
		return RedeemCode{}, fmt.Errorf("payments: lock redeem code: %w", err)
	}
	code := codeOf(row.PaymentsRedeemCode, row.Now)
	switch code.State {
	case RedeemUsed:
		return RedeemCode{}, ErrRedeemUsed
	case RedeemDisabled:
		return code, nil
	}
	changed, err := queries.DisableRedeemCode(ctx, tx, DisableRedeemCodeParams{ID: uuid.UUID(id), DisabledBy: &by})
	if err != nil {
		return RedeemCode{}, fmt.Errorf("payments: disable redeem code: %w", err)
	}
	if changed == 0 {
		return RedeemCode{}, ErrRedeemUsed
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "redeem_code.disable", Reason: reason,
		Target: audit.Target{Module: "payments", Kind: "redeem_code", ID: id.TypeID(), Site: code.Site},
		Detail: map[string]any{"from": "enabled", "to": "disabled"},
	}); err != nil {
		return RedeemCode{}, err
	}
	row, err = queries.LockRedeemCode(ctx, tx, uuid.UUID(id))
	if err != nil {
		return RedeemCode{}, fmt.Errorf("payments: read the disabled code: %w", err)
	}
	return codeOf(row.PaymentsRedeemCode, row.Now), nil
}

// ListRedemptions is one page of the account's redeemed codes, newest first. It reads no code, only the hint and the
// credit's facts, so history never becomes a way to recover what was redeemed.
func (p *Payments) ListRedemptions(ctx context.Context, s pg.Scope, account uuid.UUID, after *page.Cursor, size int) ([]Redemption, *page.Cursor, error) {
	if account == uuid.Nil() {
		return nil, nil, errors.New("payments: list redemptions: no account")
	}
	if size < 1 || size > 200 {
		return nil, nil, fmt.Errorf("payments: list redemptions: page size %d, want 1 to 200", size)
	}
	at, id := endOfOrders, uuid.UUID{}
	if after != nil {
		at, id = after.At, after.ID
	}
	var rows []ListRedemptionsRow
	if err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		rows, err = queries.ListRedemptions(ctx, tx, ListRedemptionsParams{
			AccountID: account, AfterAt: at, AfterID: id, Max: int32(size + 1),
		})
		return err
	}); err != nil {
		return nil, nil, fmt.Errorf("payments: list redemptions: %w", err)
	}
	var next *page.Cursor
	if len(rows) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		next = &page.Cursor{At: last.CreatedAt, ID: last.ID}
	}
	out := make([]Redemption, len(rows))
	for i, row := range rows {
		out[i] = Redemption{
			ID: RedemptionID(row.ID), Amount: row.Amount, Hint: row.CodeHint,
			ExpiresAt: optionalTimestamp(row.ExpiresAt), CreatedAt: row.CreatedAt,
		}
	}
	return out, next, nil
}

// codeOf is a stored code as the page shows it: an enabled code past its expiry reads expired.
func codeOf(row PaymentsRedeemCode, now time.Time) RedeemCode {
	state := RedeemState(row.State)
	if state == RedeemEnabled && row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(now) {
		state = RedeemExpired
	}
	return RedeemCode{
		ID: CodeID(row.ID), Site: row.SiteID, State: state, Amount: row.Amount, Note: row.Note, Hint: row.CodeHint,
		ExpiresAt: optionalTimestamp(row.ExpiresAt), CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy,
		RedeemedAt: optionalTimestamp(row.RedeemedAt), DisabledAt: optionalTimestamp(row.DisabledAt),
	}
}

// redemptionOf is a stored redemption, its answer to a replay.
func redemptionOf(row RedemptionByKeyRow) Redemption {
	return Redemption{
		ID: RedemptionID(row.ID), Amount: row.Amount, Hint: row.CodeHint,
		ExpiresAt: optionalTimestamp(row.ExpiresAt), CreatedAt: row.CreatedAt,
	}
}

// timestamp is a Go expiry as the generated parameters take it.
func timestamp(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// optionalTimestamp is the reverse: no time, never the zero instant.
func optionalTimestamp(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return timePtr(t.Time)
}

func timePtr(t time.Time) *time.Time { return &t }
