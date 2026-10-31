package billing

import (
	"maps"
	"math/big"
	"slices"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func TestExchangeCurrentReadIsBoundedByPairs(t *testing.T) {
	t.Parallel()

	f := newRules(t)
	codes := slices.Sorted(maps.Keys(minorDigits))
	pairs := len(codes) * (len(codes) - 1) / 2
	const versions = 200
	seedExchangeReadHistory(t, f, codes, versions)
	err := f.tx(func(tx pg.Tx) error {
		rows, err := ExchangeRates(t.Context(), tx)
		if err != nil {
			return err
		}
		checkCurrentExchangeHeads(t, rows, pairs, versions)
		var raw []byte
		if err := tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) "+currentExchangeRates, codes).Scan(&raw); err != nil {
			return err
		}
		var plans []struct {
			Plan exchangeReadPlan `json:"Plan"`
		}
		if err := jsonx.Unmarshal(raw, &plans); err != nil {
			return err
		}
		if scanned := plans[0].Plan.historyRows(t); scanned.Cmp(big.NewRat(int64(pairs), 1)) > 0 {
			t.Fatalf("read %s history rows for %d pairs across %d versions", scanned, pairs, versions)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type exchangeReadPlan struct {
	Relation string             `json:"Relation Name"`
	Rows     jsonx.Raw          `json:"Actual Rows"`
	Loops    jsonx.Raw          `json:"Actual Loops"`
	Filtered jsonx.Raw          `json:"Rows Removed by Filter"`
	Plans    []exchangeReadPlan `json:"Plans"`
}

func (p exchangeReadPlan) historyRows(t *testing.T) *big.Rat {
	t.Helper()
	rows := new(big.Rat)
	if p.Relation == "exchange_rates" {
		rows.Add(exchangePlanCount(t, p.Rows), exchangePlanCount(t, p.Filtered))
		rows.Mul(rows, exchangePlanCount(t, p.Loops))
	}
	for _, child := range p.Plans {
		rows.Add(rows, child.historyRows(t))
	}
	return rows
}

func exchangePlanCount(t *testing.T, raw jsonx.Raw) *big.Rat {
	t.Helper()
	if len(raw) == 0 {
		return new(big.Rat)
	}
	value, ok := new(big.Rat).SetString(string(raw))
	if !ok {
		t.Fatalf("invalid explain count: %s", raw)
	}
	return value
}

func seedExchangeReadHistory(t *testing.T, f rules, codes []string, versions int64) {
	t.Helper()
	err := f.tx(func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO billing.exchange_rates
            (id,base,quote,version,numerator,denominator,kind,reason,created_by)
            SELECT gen_random_uuid(),b.code,q.code,v,10,71,CASE WHEN v=1 THEN 'initial' ELSE 'adjustment' END,'bounded history fixture','billing.test'
            FROM unnest($1::text[]) b(code) CROSS JOIN unnest($1::text[]) q(code)
            CROSS JOIN generate_series(1,$2::bigint) v WHERE b.code<q.code`, codes, versions)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkCurrentExchangeHeads(t *testing.T, rows []BillingExchangeRate, pairs int, versions int64) {
	t.Helper()
	if len(rows) != pairs {
		t.Fatalf("current pairs: %d, want %d", len(rows), pairs)
	}
	for _, row := range rows {
		if row.Version != versions {
			t.Fatalf("stale head: %+v", row)
		}
	}
}
