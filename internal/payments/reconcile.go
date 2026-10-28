package payments

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/idem"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

type reconcileArgs struct {
	singleRun
	Merchant uuid.UUID `json:"merchant"`
	Day      time.Time `json:"day"`
	After    uuid.UUID `json:"after"`
	Queries  bool      `json:"queries"`
}

func (reconcileArgs) Kind() string { return "payments.reconcile" }

// RegisterReconcile fans out the previous UTC day to one durable job per merchant. Retries keep that day.
func RegisterReconcile(cfg PollConfig) error {
	work := func(ctx context.Context, j *river.Job[reconcileArgs]) error {
		if j.Args.Merchant == (uuid.UUID{}) {
			return cfg.Payments.enqueueReconcile(ctx, cfg.Queue, j.Args.Day)
		}
		if !j.Args.Queries {
			if err := cfg.Payments.reconcileMerchant(ctx, j.Args.Merchant, j.Args.Day); err != nil {
				return err
			}
			j.Args.Queries = true
		} else {
			next, err := cfg.Payments.reconcileQueries(ctx, j.Args.Merchant, j.Args.Day, j.Args.After)
			if err != nil {
				return err
			}
			if next == (uuid.UUID{}) {
				return nil
			}
			j.Args.After = next
		}
		return cfg.Payments.db.Tx(ctx, cfg.Payments.platform, func(tx pg.Tx) error {
			_, err := cfg.Queue.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: j.Args}})
			return err
		})
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(work)); err != nil {
		return err
	}
	cfg.Queue.Every(24*time.Hour, func() river.JobArgs {
		return reconcileArgs{Day: time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)}
	})
	return nil
}

func (p *Payments) enqueueReconcile(ctx context.Context, q *pg.Queue, day time.Time) error {
	var after uuid.UUID
	for {
		var ids []uuid.UUID
		if err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) { ids, err = queries.ReconcileMerchants(ctx, tx, after); return err }); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		jobs := make([]river.InsertManyParams, len(ids))
		for i, id := range ids {
			jobs[i] = river.InsertManyParams{Args: reconcileArgs{Merchant: id, Day: day}}
		}
		if err := p.db.Tx(ctx, p.platform, func(tx pg.Tx) error { _, err := q.InsertManyTx(ctx, tx, jobs); return err }); err != nil {
			return err
		}
		after = ids[len(ids)-1]
	}
}

// Repeated observations use a stable fact key, including after an operator investigated the first report.
func pendingDifference(merchant uuid.UUID, kind, key string, detail map[string]any) (RecordDiscrepancyParams, error) {
	raw, err := jsonx.MarshalCanonical(detail)
	if err != nil {
		return RecordDiscrepancyParams{}, err
	}
	fingerprint, err := idem.Fingerprint(key, string(raw))
	return RecordDiscrepancyParams{ID: uuid.NewV7(), MerchantID: merchant, Kind: kind, Dedupe: fmt.Sprintf("%s/%x", key, fingerprint), Detail: raw}, err
}

func (p *Payments) reconcileMerchant(ctx context.Context, merchant uuid.UUID, day time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var differences []RecordDiscrepancyParams
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) error {
		var err error
		differences, err = checkMerchant(ctx, tx, merchant, day)
		return err
	})
	if err != nil {
		return fmt.Errorf("payments: check merchant: %w", err)
	}
	for _, d := range differences {
		if err = p.db.Tx(ctx, p.platform, func(tx pg.Tx) error { _, err := recordPending(ctx, tx, d); return err }); err != nil {
			return err
		}
	}
	return nil
}

func checkMerchant(ctx context.Context, tx pg.Tx, merchant uuid.UUID, day time.Time) ([]RecordDiscrepancyParams, error) {
	expected := make(map[string]*big.Int)
	differences, err := checkBatches(ctx, tx, merchant, day, expected)
	if err != nil {
		return nil, err
	}
	cash, err := checkCash(ctx, tx, merchant, day, expected)
	differences = append(differences, cash...)
	if err != nil {
		return nil, err
	}
	usage, err := queries.ReconcileUsage(ctx, tx, merchant)
	if err != nil {
		return nil, err
	}
	for _, u := range usage {
		d, err := pendingDifference(merchant, "usage", u.Day, map[string]any{"day": u.Day, "expected": u.Expected, "actual": u.Actual})
		if err != nil {
			return nil, err
		}
		differences = append(differences, d)
	}
	return differences, nil
}

func checkCash(ctx context.Context, tx pg.Tx, merchant uuid.UUID, day time.Time, expected map[string]*big.Int) ([]RecordDiscrepancyParams, error) {
	until := day.Add(24 * time.Hour)
	actual, err := billing.MerchantCash(ctx, tx, merchant, day, until)
	if err != nil {
		return nil, err
	}
	want, got := map[string]string{}, map[string]string{}
	for code, total := range expected {
		c, err := platform.ParseCurrency(code)
		if err != nil {
			return nil, err
		}
		n := new(big.Int).Set(total)
		n.Mul(n, new(big.Int).Quo(big.NewInt(creditScale), minorScale(c)))
		want[code] = n.String()
	}
	for _, r := range actual {
		got[r.Currency] = r.Amount
		if _, ok := want[r.Currency]; !ok {
			want[r.Currency] = "0"
		}
	}
	var differences []RecordDiscrepancyParams
	for currency, amount := range want {
		value := got[currency]
		if value == "" {
			value = "0"
		}
		if amount == value {
			continue
		}
		key := day.Format(time.DateOnly) + "/" + currency
		d, err := pendingDifference(merchant, "ledger", key, map[string]any{"day": day.Format(time.DateOnly), "currency": currency, "expected": amount, "actual": value, "unit": "billionths"})
		if err != nil {
			return nil, err
		}
		differences = append(differences, d)
	}
	return differences, nil
}

func checkBatches(ctx context.Context, tx pg.Tx, merchant uuid.UUID, day time.Time, expected map[string]*big.Int) ([]RecordDiscrepancyParams, error) {
	var after uuid.UUID
	var differences []RecordDiscrepancyParams
	for {
		orders, err := queries.ReconcileOrders(ctx, tx, ReconcileOrdersParams{MerchantID: merchant, After: after})
		if err != nil {
			return nil, err
		}
		if len(orders) == 0 {
			break
		}
		for _, o := range orders {
			found, err := checkOrderBatch(ctx, tx, merchant, o, day, expected)
			if err != nil {
				return nil, err
			}
			differences = append(differences, found...)
		}
		after = orders[len(orders)-1].ID
	}
	orphaned, err := checkOrphanBatches(ctx, tx, merchant)
	return append(differences, orphaned...), err
}

func batchMatches(o ReconcileOrdersRow, b billing.CreditBatch, exists bool, merchant uuid.UUID) bool {
	if o.PaidTotal == nil {
		return !exists
	}
	credited := slices.Contains([]State{Paid, PartiallyRefunded, Refunded, ChargedBack}, State(o.State))
	identity, wantIdentity := [2]uuid.UUID{b.Site, b.Account}, [2]uuid.UUID{o.SiteID, o.AccountID}
	return exists && credited && b.Source == "paid" && identity == wantIdentity && b.Channel == (billing.Channel{Merchant: merchant}) && b.Payment != nil &&
		b.Payment.Currency.String() == o.PayCurrency && o.PaidFee != nil && *b.Payment == (billing.Payment{Currency: b.Payment.Currency, Paid: *o.PaidTotal, Fee: *o.PaidFee}) && o.Credited != nil && b.Amount == *o.Credited
}

func checkOrphanBatches(ctx context.Context, tx pg.Tx, merchant uuid.UUID) ([]RecordDiscrepancyParams, error) {
	var after uuid.UUID
	var differences []RecordDiscrepancyParams
	for {
		ids, err := billing.MerchantBatches(ctx, tx, merchant, after)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return differences, nil
		}
		for _, id := range ids {
			found, err := checkBatchOwner(ctx, tx, merchant, id)
			if err != nil {
				return nil, err
			}
			differences = append(differences, found...)
		}
		after = ids[len(ids)-1]
	}
}

func (p *Payments) reconcileQueries(ctx context.Context, merchant uuid.UUID, day time.Time, after uuid.UUID) (uuid.UUID, error) {
	var ids []uuid.UUID
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) {
		ids, err = queries.ReconcileQueries(ctx, tx, ReconcileQueriesParams{MerchantID: merchant, After: after, Since: day, Until: day.Add(24 * time.Hour)})
		return err
	})
	if err != nil || len(ids) == 0 {
		return uuid.UUID{}, err
	}
	err = p.queryReconcilePage(ctx, ids)
	if errors.Is(err, ErrQueryPaused) {
		return uuid.UUID{}, river.JobSnooze(time.Minute)
	}
	if err != nil {
		return uuid.UUID{}, err
	}
	if len(ids) < 100 {
		return uuid.UUID{}, nil
	}
	return ids[len(ids)-1], nil
}

func checkOrderBatch(ctx context.Context, tx pg.Tx, merchant uuid.UUID, o ReconcileOrdersRow, day time.Time, expected map[string]*big.Int) ([]RecordDiscrepancyParams, error) {
	b, exists, err := billing.ReadCredit(ctx, tx, o.ID)
	if err != nil {
		return nil, err
	}
	addExpectedCash(o, b, exists, day, expected)
	if batchMatches(o, b, exists, merchant) {
		return nil, nil
	}
	d, err := pendingDifference(merchant, "ledger", "batch/"+o.ID.String(), batchDifferenceDetail(o, b, exists))
	d.OrderID, d.AccountID = &o.ID, &o.AccountID
	return []RecordDiscrepancyParams{d}, err
}

func checkBatchOwner(ctx context.Context, tx pg.Tx, merchant, id uuid.UUID) ([]RecordDiscrepancyParams, error) {
	owned, err := queries.HasPaymentBatchOwner(ctx, tx, HasPaymentBatchOwnerParams{ID: id, MerchantID: merchant})
	if err != nil {
		return nil, err
	}
	if owned {
		return nil, nil
	}
	d, err := pendingDifference(merchant, "ledger", "orphan/"+id.String(), map[string]any{"batch": id.String(), "reason": "paid batch has no payment record"})
	return []RecordDiscrepancyParams{d}, err
}

// The credit batch and its cash share an accounting day even when a receipt transaction spans UTC midnight.
func addExpectedCash(o ReconcileOrdersRow, b billing.CreditBatch, exists bool, day time.Time, expected map[string]*big.Int) {
	if o.PaidTotal == nil {
		return
	}
	at := o.PaidAt
	if exists {
		at = b.CreatedAt
	}
	if at.Before(day) || !at.Before(day.Add(24*time.Hour)) {
		return
	}
	if expected[o.PayCurrency] == nil {
		expected[o.PayCurrency] = new(big.Int)
	}
	expected[o.PayCurrency].Add(expected[o.PayCurrency], big.NewInt(*o.PaidTotal))
}

func (p *Payments) queryReconcilePage(ctx context.Context, ids []uuid.UUID) error {
	var workers sync.WaitGroup
	failures := make([]error, min(2, len(ids)))
	for worker := range len(failures) {
		workers.Go(func() { failures[worker] = p.queryReconcileWorker(ctx, ids, worker) })
	}
	workers.Wait()
	for _, err := range failures {
		if errors.Is(err, ErrQueryPaused) {
			return ErrQueryPaused
		}
	}
	return errors.Join(failures...)
}

func (p *Payments) queryReconcileWorker(ctx context.Context, ids []uuid.UUID, offset int) error {
	for i := offset; i < len(ids) && ctx.Err() == nil; i += 2 {
		o, err := p.GetOrder(ctx, p.platform, OrderID(ids[i]))
		if err == nil {
			err = p.queryOrder(ctx, o)
		}
		if err != nil {
			if _, known := errors.AsType[*Rejected](err); !known {
				return err
			}
			p.logger.Warn("payments: daily query refused", "order", OrderID(ids[i]).TypeID(), "error", err)
		}
	}
	return ctx.Err()
}

func batchDifferenceDetail(o ReconcileOrdersRow, b billing.CreditBatch, exists bool) map[string]any {
	detail := map[string]any{"order": OrderID(o.ID).TypeID(), "order_state": o.State, "reason": "paid order and credit batch differ", "batch_exists": exists, "batch_source": b.Source, "batch_site": b.Site.String(), "batch_account": b.Account.String(), "batch_merchant": b.Channel.Merchant.String(), "batch_credited": fmt.Sprint(b.Amount), "order_currency": o.PayCurrency}
	for key, value := range map[string]*int64{"order_paid_total": o.PaidTotal, "order_paid_fee": o.PaidFee, "order_credited": o.Credited} {
		if value != nil {
			detail[key] = fmt.Sprint(*value)
		}
	}
	if b.Payment != nil {
		detail["batch_currency"] = b.Payment.Currency.String()
		detail["batch_paid_total"] = fmt.Sprint(b.Payment.Paid)
		detail["batch_paid_fee"] = fmt.Sprint(b.Payment.Fee)
	}
	return detail
}
