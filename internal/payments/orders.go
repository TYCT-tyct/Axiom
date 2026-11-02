package payments

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform"
	"axiom/internal/platform/idem"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// State is where an order is (docs/domains/payments.md「订单」).
type State string

// The order states. A payment is confirmed by the callback, a query or a person (README.md「实现拆分」E1-2c).
const (
	Pending           State = "pending"
	Paid              State = "paid"
	Expired           State = "expired"
	Cancelled         State = "cancelled"
	Failed            State = "failed"
	Review            State = "review"
	PartiallyRefunded State = "partially_refunded"
	Refunded          State = "refunded"
	ChargedBack       State = "charged_back"
)

// states are all of them, in the order the design lists them, so a set of states is always the same slice.
var states = []State{Pending, Paid, Expired, Cancelled, Failed, Review, PartiallyRefunded, Refunded, ChargedBack}

// transitions is the one table of what an order may become (docs/domains/payments.md「订单」): an order's state moves
// along these edges and no others, so a lost or repeated event cannot walk it backwards.
var transitions = map[State][]State{
	Pending:           {Paid, Review, Expired, Cancelled, Failed},
	Expired:           {Paid, Review},
	Cancelled:         {Paid, Review},
	Failed:            {Paid, Review},
	Review:            {Paid, Refunded},
	Paid:              {PartiallyRefunded, Refunded, ChargedBack},
	PartiallyRefunded: {PartiallyRefunded, Refunded, ChargedBack},
	Refunded:          {ChargedBack},
}

// canMoveTo reports whether an order may go from s to next.
func (s State) canMoveTo(next State) bool { return slices.Contains(transitions[s], next) }

// predecessors is every state an order may come to next from: the condition a change of an order's state carries into
// the database, so the row changes only along an edge of the table.
func predecessors(next State) []string {
	var from []string
	for _, s := range states {
		if s.canMoveTo(next) {
			from = append(from, string(s))
		}
	}
	return from
}

// The sources and kinds of the order events this module writes; the callback and the query add theirs (README.md
// 「实现拆分」E1-2c).
const (
	sourceUser = "user"
	sourceJob  = "job"

	kindCreate   = "create"
	kindCheckout = "checkout"
	kindReject   = "reject"
	kindCancel   = "cancel"
	kindExpire   = "expire"
)

// maxRaw is what an order event's raw text holds, and so how far a gateway's answer is cut.
const maxRaw = 8 << 10

// Order is an order as the module passes it around. Checkout is the part of the channel's answer that was stored: the
// Checkout CreateOrder returns beside it is the one to show the browser, which may carry a client secret of its own.
type Order struct {
	ID         OrderID
	SiteID     uuid.UUID
	AccountID  uuid.UUID
	UserID     uuid.UUID
	MerchantID MerchantID
	Method     Method
	State      State
	Credits    int64
	Total      Money
	Fee        int64
	TradeNo    string
	Checkout   Checkout
	ExpiresAt  time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// RefundAllowed is the merchant account's policy for user refund requests. The request itself still checks the
	// channel's current capability; this only says whether the page should offer it.
	RefundAllowed bool
}

var (
	// ErrTooManyOrders is an account that has as many orders waiting for payment as it may.
	ErrTooManyOrders = errors.New("payments: too many orders wait for payment; pay or cancel one first")
	// ErrTooManyCancels is an account that cancelled too many orders lately.
	ErrTooManyCancels = errors.New("payments: too many cancellations lately; try again later")
	// ErrDailyLimit is a top-up past what the account may buy in a UTC day.
	ErrDailyLimit = errors.New("payments: this top-up would pass the account's daily limit")
	// ErrFirstLimit is a first top-up past the limit a site puts on it.
	ErrFirstLimit = errors.New("payments: a first top-up is limited; try a smaller amount")
	// ErrKeyReused is an idempotency key used before for another request: two different orders arrived under one key,
	// and only the first exists.
	ErrKeyReused = errors.New("payments: the idempotency key was used for another request")
	// ErrNotWaiting is an order that is not waiting for payment, so there is nothing to cancel.
	ErrNotWaiting = errors.New("payments: the order is not waiting for payment")
	// ErrUnconfirmed is a call to the channel whose outcome is unknown: the order is left as it was, and the caller
	// must look before it tries again.
	ErrUnconfirmed = errors.New("payments: the channel did not confirm; check the order before trying again")
	// ErrUnsafeLink is a checkout with a link the browser must not be sent to.
	ErrUnsafeLink = errors.New("payments: the channel's link is not one this site accepts")

	// ErrRefused marks the channel's definite refusal, the only outcome that fails an order.
	ErrRefused = errors.New("payments: the channel refused the order")
)

// CreateOrder asks for a top-up: what to buy, how to pay, and the key that makes the request happen once.
type CreateOrder struct {
	Site      accounts.Site
	Account   uuid.UUID
	User      uuid.UUID
	Method    Method
	Credits   int64 // billionths of a credit, as everywhere in billing
	Key       string
	ReturnURL string // where the browser comes back to, built from the site and never from the request
	ClientIP  netip.Addr
	Mobile    bool
}

// check refuses an order request that is malformed before it reaches the database.
func (r CreateOrder) check() error {
	switch {
	case r.Account == uuid.UUID{} || r.User == uuid.UUID{}:
		return errors.New("payments: an order needs an account and a user")
	case r.Key == "" || len(r.Key) > 128:
		return &FieldError{"/idempotency_key", "must have 1 to 128 characters"}
	case !r.ClientIP.IsValid():
		return &FieldError{"/client_ip", "is required"}
	}
	if u, err := url.Parse(r.ReturnURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return &FieldError{"/return_url", "must be an https address"}
	}
	return nil
}

// CreateOrder places an order for credits paid by method: it checks the account's limits under the account's own
// transaction lock, chooses the merchant account, writes the order, and then asks the channel for its checkout, which
// it stores. The order exists here before the channel knows of it, so a payment is always found
// (docs/domains/payments.md「订单」).
//
// The same Key with the same request returns the order it made, and the checkout that was stored: the client secret
// the channel gave the first time is never stored, so a replay carries none. A channel that refuses the order fails it
// and gives the merchant's daily reservation back; any other channel error leaves it waiting for payment and returns
// ErrUnconfirmed, because the order may exist at the gateway. A replay reports stored State and Checkout rather than
// the first error; callers must distinguish failed orders and pending orders without a checkout. The order also
// takes one of its IP group's places in the site's hourly limits (iplimit.go) in that first transaction, and a group at its
// limit of orders or of failed orders is refused with a *Wait.
// ErrRefused is exported so that a caller can answer a refused order alike for the request that made it and its replays.
func (p *Payments) CreateOrder(ctx context.Context, s pg.Scope, r CreateOrder) (Order, Checkout, error) {
	if err := r.check(); err != nil {
		return Order{}, Checkout{}, err
	}
	var replayed Order
	var replay bool
	if err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		replayed, replay, err = readPlacedOrder(ctx, tx, r)
		return err
	}); err != nil || replay {
		return replayed, replayed.Checkout, err
	}
	method := string(r.Method)
	m, err := p.menu(ctx, s, r.Site, &method)
	if err != nil {
		return Order{}, Checkout{}, err
	}
	if err = m.settings.admits(r.Credits); err != nil {
		return Order{}, Checkout{}, err
	}
	order, placed, err := p.reserveOrder(ctx, s, r, m)
	if err != nil {
		if errors.Is(err, ErrTooManyIPOrders) || errors.Is(err, ErrTooManyIPFailures) {
			p.rateLimited("orders")
		}
		return Order{}, Checkout{}, err
	}
	if !placed {
		return order, order.Checkout, nil
	}
	checkout, err := p.checkout(ctx, s, &order, r)
	switch {
	case errors.Is(err, ErrRefused):
		order.TradeNo = checkout.TradeNo
		return order, Checkout{}, p.refuseOrder(ctx, s, order, r.ClientIP, err)
	case err != nil:
		return order, Checkout{}, fmt.Errorf("%w: %w", ErrUnconfirmed, err)
	}
	order.TradeNo, order.Checkout = checkout.TradeNo, checkout.stored()
	return order, checkout, nil
}

// reserveOrder commits the order and its merchant reservation together before any gateway call.
func (p *Payments) reserveOrder(ctx context.Context, s pg.Scope, r CreateOrder, m menu) (Order, bool, error) {
	var (
		order  Order
		placed bool
		err    error
	)
	err = p.db.Tx(ctx, s, func(tx pg.Tx) error {
		if err = boundOrderTx(ctx, tx); err != nil {
			return err
		}
		if err = queries.LockAccountOrders(ctx, tx, accountKey(r.Account)); err != nil {
			return fmt.Errorf("payments: lock the account's orders: %w", err)
		}
		var replay bool
		if order, replay, err = readPlacedOrder(ctx, tx, r); err != nil || replay {
			return err
		}
		var counts OrderCountsRow
		if counts, err = queries.OrderCounts(ctx, tx, OrderCountsParams{
			AccountID: r.Account, CancelWindow: int64(m.settings.cancelWindow / time.Second),
		}); err != nil {
			return fmt.Errorf("payments: read the account's order counts: %w", err)
		}
		if err = m.settings.admitsCounts(counts, r.Credits); err != nil {
			return err
		}
		if err = spendIPOrder(ctx, tx, uuid.UUID(r.Site.ID), r.ClientIP, m.settings); err != nil {
			return err
		}
		choice, err := p.choose(ctx, tx, m, r.Method, r.Credits)
		if err != nil {
			return err
		}
		order, placed, err = placeOrder(ctx, tx, r, m.site.Currency, choice)
		return err
	})
	return order, placed, err
}

// placeOrder writes an order and its creation event, and reports whether it wrote one: false means the account's
// idempotency key had placed this request already.
func placeOrder(ctx context.Context, tx pg.Tx, r CreateOrder, site platform.Currency, c candidate) (Order, bool, error) {
	pay, err := platform.ParseCurrency(c.offer.Currency)
	if err != nil {
		return Order{}, false, fmt.Errorf("payments: merchant account %s is in %q: %w", MerchantID(c.offer.MerchantID).TypeID(), c.offer.Currency, err)
	}
	fingerprint, err := idem.Fingerprint(r.Method, r.Credits)
	if err != nil {
		return Order{}, false, err
	}
	var rateID *uuid.UUID
	refNum, refDen, appliedNum, appliedDen := "1", "1", "1", "1"
	if pay != site {
		reference, ok := c.rate.Reference.Between(site, pay)
		if !ok || c.rate.Applied == nil {
			return Order{}, false, fmt.Errorf("payments: the order has no rate from %s to %s", site, pay)
		}
		id := c.rate.Reference.ID
		rateID = &id
		refNum, refDen = reference.Num().String(), reference.Denom().String()
		appliedNum, appliedDen = c.rate.Applied.Num().String(), c.rate.Applied.Denom().String()
	}
	o := Order{
		ID: OrderID(uuid.NewV7()), SiteID: uuid.UUID(r.Site.ID), AccountID: r.Account, UserID: r.User,
		MerchantID: MerchantID(c.offer.MerchantID), Method: r.Method, State: Pending, Credits: r.Credits,
		Total: c.quote.Total, Fee: c.quote.Fee,
	}
	times, err := queries.InsertOrder(ctx, tx, InsertOrderParams{
		ID: uuid.UUID(o.ID), SiteID: o.SiteID, AccountID: o.AccountID, UserID: o.UserID, MerchantID: uuid.UUID(o.MerchantID),
		Method: string(o.Method), IdemKey: r.Key, Fingerprint: fingerprint[:], Credits: o.Credits,
		SiteCurrency: site.String(), PayCurrency: pay.String(), Total: o.Total.Minor, Fee: o.Fee,
		FeePpm: c.offer.FeePpm, FeeFixed: c.offer.FeeFixed, RateID: rateID,
		RefNum: refNum, RefDen: refDen, AppliedNum: appliedNum, AppliedDen: appliedDen,
		TtlSeconds: c.offer.TtlSeconds, ClientIp: r.ClientIP,
	})
	if err != nil {
		return Order{}, false, fmt.Errorf("payments: write the order: %w", err)
	}
	o.CreatedAt, o.ExpiresAt, o.UpdatedAt = times.CreatedAt, times.ExpiresAt, times.UpdatedAt
	err = queries.OrderEvent(ctx, tx, OrderEventParams{
		ID: uuid.NewV7(), OrderID: uuid.UUID(o.ID), SiteID: o.SiteID, AccountID: o.AccountID,
		Source: sourceUser, Kind: kindCreate, FromState: "", ToState: string(Pending),
	})
	if err != nil {
		return Order{}, false, fmt.Errorf("payments: record the order's first event: %w", err)
	}
	return o, true, nil
}

// readPlacedOrder answers a request whose idempotency key was used before: the order it made, and replayed true when
// this request is the one that made it.
func readPlacedOrder(ctx context.Context, tx pg.Tx, r CreateOrder) (Order, bool, error) {
	rows, err := queries.OrderByKey(ctx, tx, OrderByKeyParams{AccountID: r.Account, IdemKey: r.Key})
	if err != nil {
		return Order{}, false, fmt.Errorf("payments: read the account's order by key: %w", err)
	}
	if len(rows) == 0 {
		return Order{}, false, nil
	}
	fingerprint, err := idem.Fingerprint(r.Method, r.Credits)
	if err != nil {
		return Order{}, false, err
	}
	if string(rows[0].Fingerprint) != string(fingerprint[:]) {
		return Order{}, false, ErrKeyReused
	}
	order, err := loadOrder(ctx, tx, OrderID(rows[0].ID))
	if err != nil {
		return Order{}, false, err
	}
	return order, true, nil
}

// readOrder reads one order of the transaction's scope.
func loadOrder(ctx context.Context, tx pg.Tx, id OrderID) (Order, error) {
	rows, err := queries.ReadOrder(ctx, tx, uuid.UUID(id))
	if err != nil {
		return Order{}, fmt.Errorf("payments: read order %s: %w", id.TypeID(), err)
	}
	if len(rows) == 0 {
		return Order{}, ErrNotFound
	}
	return orderOf(rows[0])
}

// orderOf is a stored order as the module passes it around.
func orderOf(r ReadOrderRow) (Order, error) {
	pay, err := platform.ParseCurrency(r.PayCurrency)
	if err != nil {
		return Order{}, fmt.Errorf("payments: order %s is paid in %q: %w", OrderID(r.ID).TypeID(), r.PayCurrency, err)
	}
	var c Checkout
	if len(r.Checkout) > 0 {
		if err = jsonx.UnmarshalKnown(r.Checkout, &c); err != nil {
			return Order{}, fmt.Errorf("payments: order %s has a checkout that does not parse: %w", OrderID(r.ID).TypeID(), err)
		}
	}
	return Order{
		ID: OrderID(r.ID), SiteID: r.SiteID, AccountID: r.AccountID, UserID: r.UserID, MerchantID: MerchantID(r.MerchantID),
		Method: Method(r.Method), State: State(r.State), Credits: r.Credits, Total: Money{pay, r.Total}, Fee: r.Fee,
		TradeNo: r.TradeNo, Checkout: c, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		RefundAllowed: r.RefundAllowed,
	}, nil
}

func paymentNotifyURL(returnURL string, merchant MerchantID) string {
	// CreateOrder has already validated the server-provided return URL.
	u, err := url.Parse(returnURL)
	if err != nil {
		panic(err)
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: NotifyPath + merchant.TypeID()}).String()
}

func (p *Payments) checkoutChannel(ctx context.Context, id MerchantID, method Method) (Channel, string, error) {
	var ch Channel
	var upstream string
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) error {
		var err error
		ch, err = p.channel(ctx, tx, id)
		if err != nil {
			return err
		}
		methods, err := queries.ListMethods(ctx, tx, []uuid.UUID{uuid.UUID(id)})
		if err != nil {
			return err
		}
		for _, m := range methods {
			if Method(m.Method) == method {
				upstream = m.Upstream
				return nil
			}
		}
		return ErrNotFound
	})
	return ch, upstream, err
}

// checkout asks the order's channel for a checkout and stores what outlives the request. A channel that refuses, or a
// link the browser must not follow, is ErrRefused; the order stays as it is for any other outcome.
func (p *Payments) checkout(ctx context.Context, s pg.Scope, o *Order, r CreateOrder) (Checkout, error) {
	ch, upstream, err := p.checkoutChannel(ctx, o.MerchantID, o.Method)
	if err != nil {
		return Checkout{}, fmt.Errorf("%w: open the channel before creating an order: %w", ErrRefused, err)
	}
	c, err := ch.Create(ctx, CreateRequest{
		Order: o.ID, Merchant: o.MerchantID, Method: o.Method, Upstream: upstream, Total: o.Total, Subject: "Top-up", ReturnURL: r.ReturnURL, NotifyURL: paymentNotifyURL(r.ReturnURL, o.MerchantID),
		ClientIP: r.ClientIP, Mobile: r.Mobile, ExpiresAt: o.ExpiresAt,
	})
	if rejected, ok := errors.AsType[*Rejected](err); ok {
		return Checkout{}, fmt.Errorf("%w: %s %s", ErrRefused, rejected.Code, rejected.Message)
	}
	if err != nil {
		return Checkout{}, err
	}
	c = c.withoutNUL()
	if err = c.check(); err != nil {
		return Checkout{TradeNo: c.TradeNo}, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	stored, err := jsonx.MarshalCanonical(c.stored())
	if err != nil {
		return Checkout{}, fmt.Errorf("payments: encode the checkout that is stored: %w", err)
	}
	var updatedAt time.Time
	err = p.db.Tx(ctx, s, func(tx pg.Tx) error {
		saved, err := queries.SaveCheckout(ctx, tx, SaveCheckoutParams{ID: uuid.UUID(o.ID), TradeNo: c.TradeNo, Checkout: stored})
		if err != nil {
			return fmt.Errorf("payments: save the order's checkout: %w", err)
		}
		if len(saved) == 0 {
			return fmt.Errorf("payments: order %s is no longer waiting for its checkout", o.ID.TypeID())
		}
		updatedAt = saved[0]
		return queries.OrderEvent(ctx, tx, OrderEventParams{
			ID: uuid.NewV7(), OrderID: uuid.UUID(o.ID), SiteID: o.SiteID, AccountID: o.AccountID,
			Source: sourceUser, Kind: kindCheckout, FromState: string(Pending), ToState: string(Pending), TradeNo: c.TradeNo,
		})
	})
	if err != nil {
		return Checkout{}, err
	}
	o.UpdatedAt = updatedAt
	return c, nil
}

// refuseOrder fails an order the channel refused, in the caller's scope, and returns cause once that is committed:
// an order that never had a checkout gives its reservation of the merchant's daily limit back.
func (p *Payments) refuseOrder(ctx context.Context, s pg.Scope, o Order, ip netip.Addr, cause error) error {
	err := p.db.Tx(ctx, s, func(tx pg.Tx) error {
		locked, err := lockForChange(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		if err = countIPFailure(ctx, tx, o.SiteID, ip); err != nil {
			return err
		}
		return move(ctx, tx, locked, Failed, event{source: sourceUser, kind: kindReject, tradeNo: o.TradeNo, raw: cause.Error()})
	})
	if err != nil {
		// The refusal was not saved: the order is still waiting and its reservation held, so the caller must not tell the
		// user it was refused. The cause is text only, so that this error is not ErrRefused; a retry with the same key
		// finds the order as it is.
		return fmt.Errorf("payments: save the refusal of order %s (%s): %w", o.ID.TypeID(), cause.Error(), err)
	}
	if errors.Is(cause, ErrUnsafeLink) {
		p.closeOrder(ctx, o)
	}
	return cause
}

// Cancel cancels an order that still waits for payment: it stops being payable here and gives its reservation of the
// merchant's daily limit back. An order that is cancelled already is returned as it is. The channel is then asked to
// close its order, and one that cannot or will not is only logged: the money decision is ours, and a payment that still
// arrives is what the query is for (README.md「实现拆分」E1-2c).
func (p *Payments) Cancel(ctx context.Context, s pg.Scope, id OrderID) (Order, error) {
	var order Order
	moved := false
	err := p.db.Tx(ctx, s, func(tx pg.Tx) error {
		locked, err := lockForChange(ctx, tx, id)
		if err != nil {
			return err
		}
		if State(locked.State) == Pending {
			if err = move(ctx, tx, locked, Cancelled, event{source: sourceUser, kind: kindCancel}); err != nil {
				return err
			}
			moved = true
		} else if State(locked.State) != Cancelled {
			return ErrNotWaiting
		}
		order, err = loadOrder(ctx, tx, id)
		return err
	})
	if err != nil {
		return Order{}, err
	}
	if moved {
		p.closeOrder(ctx, order)
	}
	return order, nil
}

// closeOrder asks the channel to close an order that can no longer be paid here. A failure is logged and goes no
// further: the order has already moved, and a gateway that keeps it payable is covered by the expiry's query.
// The result tells the poll to skip further closes at a merchant whose deadline expired.
func (p *Payments) closeOrder(ctx context.Context, o Order) bool {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	ch, err := p.channelOf(ctx, o.MerchantID)
	if err != nil {
		p.logger.Error("payments: a cancelled order's channel does not open", "order", o.ID.TypeID(), "error", err)
		return errors.Is(err, context.DeadlineExceeded)
	}
	if !ch.Caps().Has(CapClose) {
		return false
	}
	if err = ch.Close(ctx, o.ID, o.TradeNo); err != nil {
		p.logger.Warn("payments: could not close the order at the channel", "order", o.ID.TypeID(), "error", err)
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// move changes an order along an edge of the transition table in tx and appends the event that says so. An order that
// leaves payment behind without being paid gives its reservation of the merchant's daily limit back, which is the one
// place that gives it back.
func move(ctx context.Context, tx pg.Tx, o LockOrderRow, to State, e event) error {
	from := State(o.State)
	if !from.canMoveTo(to) {
		return fmt.Errorf("payments: order %s cannot go from %s to %s", OrderID(o.ID).TypeID(), from, to)
	}
	changed, err := queries.MoveOrder(ctx, tx, MoveOrderParams{State: string(to), ID: o.ID, FromStates: predecessors(to), TradeNo: e.tradeNo})
	if err != nil {
		return fmt.Errorf("payments: move order %s to %s: %w", OrderID(o.ID).TypeID(), to, err)
	}
	if changed == 0 {
		return fmt.Errorf("payments: order %s was no longer %s", OrderID(o.ID).TypeID(), from)
	}
	if to == Expired || to == Cancelled || to == Failed {
		if released, err := queries.ReleaseUsage(ctx, tx, o.ID); err != nil {
			return fmt.Errorf("payments: release the merchant's reservation: %w", err)
		} else if released == 0 {
			return fmt.Errorf("payments: order %s had no reservation to release", OrderID(o.ID).TypeID())
		}
	}
	err = queries.OrderEvent(ctx, tx, OrderEventParams{
		ID: uuid.NewV7(), OrderID: o.ID, SiteID: o.SiteID, AccountID: o.AccountID, Source: e.source, Kind: e.kind,
		FromState: string(from), ToState: string(to), TradeNo: e.tradeNo, Amount: e.amount, Raw: cut(e.raw),
	})
	if err != nil {
		return fmt.Errorf("payments: record the order's event: %w", err)
	}
	return nil
}

// event is what happened to an order, as an order event records it.
type event struct {
	source  string
	kind    string
	tradeNo string
	amount  *int64
	raw     string
}

// cut is raw as far as the event's column holds.
func cut(raw string) string {
	raw = strings.ReplaceAll(raw, "\x00", "")
	if len(raw) <= maxRaw {
		return strings.ToValidUTF8(raw, "")
	}
	return strings.ToValidUTF8(raw[:maxRaw], "")
}

// lockOrder locks one order's row for a change of its state.
func lockForChange(ctx context.Context, tx pg.Tx, id OrderID) (LockOrderRow, error) {
	rows, err := queries.LockOrder(ctx, tx, uuid.UUID(id))
	if err != nil {
		return LockOrderRow{}, fmt.Errorf("payments: lock order %s: %w", id.TypeID(), err)
	}
	if len(rows) == 0 {
		return LockOrderRow{}, ErrNotFound
	}
	return rows[0], nil
}

// boundOrderTx bounds the waits of an order's transaction, so a lock another request holds answers instead of holding
// the connection (README.md「事务与任务」).
func boundOrderTx(ctx context.Context, tx pg.Tx) error {
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '2s'; SET LOCAL statement_timeout = '5s'"); err != nil {
		return fmt.Errorf("payments: bound the order's waits: %w", err)
	}
	return nil
}

// accountKey is the advisory lock key of an account's orders: its limits are read and its order written under it, so
// two orders at once cannot both take the last place. The leading namespace byte separates these keys from the
// exchange rate's pairs.
func accountKey(account uuid.UUID) int64 {
	return advisoryKey(0x4f, account)
}

// merchantKey keeps merchant identity locks separate from order (0x4f) and exchange-rate (0x46) locks.
func merchantKey(id MerchantID) int64 { return advisoryKey(0x4d, uuid.UUID(id)) }

func advisoryKey(namespace byte, id uuid.UUID) int64 {
	hash := sha256.Sum256(id[:])
	return int64(binary.BigEndian.Uint64(hash[:8])>>8) | int64(namespace)<<56
}

// channelOf opens a merchant account's channel in the platform scope, which alone reads merchant_configs.
func (p *Payments) channelOf(ctx context.Context, id MerchantID) (Channel, error) {
	var ch Channel
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) {
		ch, err = p.channel(ctx, tx, id)
		return err
	})
	return ch, err
}
