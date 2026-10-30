package billing

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"axiom/internal/catalog"
)

// TestCountedLedgerProperty drives random calls billed per piece, second and character, each settled with usage no
// higher than the request stated. Every settlement charges no more than its reservation, the sum of what was settled
// never passes the sum of what was reserved, the ledger balances after every step, and the statement's running balance
// adds up to the account's balance.
func TestCountedLedgerProperty(t *testing.T) {
	const seeds = 3
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, ^seed)) //nolint:gosec // G404: test data
			b := newBank(t, 100_000_000_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour})
			results := []Result{Committed, Aggregated, FailedBeforeCommit, NotExecuted}
			finishes := []FinishReason{
				FinishNone, FinishStop, FinishLength, FinishToolUse, FinishRefusal, FinishError,
				FinishTruncated, FinishTimeout, FinishCancelled,
			}
			var reserved, settled int64
			for range 30 {
				d := countedDemand()
				d.TextBytes = r.Int64N(801)
				d.Counts = map[catalog.Item]int64{
					catalog.ItemImageOutPiece: r.Int64N(5),
					catalog.ItemAudioInSecond: r.Int64N(121),
					catalog.ItemCharacterIn:   r.Int64N(1501),
				}
				h, err := b.tryCountedHold(counted(t), d.TextBytes, d.Counts)
				if err != nil {
					t.Fatal(err)
				}
				reserved += h.Amount.CeilAmount()
				usage := map[catalog.Item]int64{
					in:                        r.Int64N(d.TextBytes + 1),
					catalog.ItemOutput:        r.Int64N(101),
					catalog.ItemImageOutPiece: r.Int64N(d.Counts[catalog.ItemImageOutPiece] + 1),
					catalog.ItemAudioInSecond: r.Int64N(d.Counts[catalog.ItemAudioInSecond] + 1),
					catalog.ItemCharacterIn:   r.Int64N(d.Counts[catalog.ItemCharacterIn] + 1),
				}
				f := b.settleWith(h, outcome(results[r.IntN(len(results))], finishes[r.IntN(len(finishes))], reported(t, usage)))
				settled += f.Charged.CeilAmount()
				if f.Charged.Cmp(h.Amount) > 0 {
					t.Fatalf("charged %d, reserved %d: %v", f.Charged.CeilAmount(), h.Amount.CeilAmount(), f)
				}
				b.balanced()
				b.statements()
			}
			if settled > reserved {
				t.Errorf("settled %d in all, more than the %d reserved", settled, reserved)
			}
		})
	}
}

// statements reads the account's whole statement, a page at a time, checks the chain of balances reconciles, and checks
// the newest row's balance is the one the account holds.
func (b *bank) statements() {
	b.t.Helper()
	var rows []StatementRow
	var after *StatementCursor
	for {
		page, next := b.statement(b.admitted, 7, after)
		rows = append(rows, page...)
		if next == nil {
			break
		}
		after = next
	}
	checkStatementChain(b.t, rows)
	if newest := *rows[0].Balance; newest != b.count("SELECT balance FROM billing.balances") {
		b.t.Errorf("the statement ends at %d, the balance is %d", newest, b.count("SELECT balance FROM billing.balances"))
	}
}
