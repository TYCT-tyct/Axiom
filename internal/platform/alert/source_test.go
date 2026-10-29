package alert

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestSourceClaimSurvivesConsumptionAndRollsBackWithSignal(t *testing.T) {
	database := pgtest.New(t)
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scope := pg.PlatformScope()
	s := Signal{Source: "test.source/one", Kind: "test.kind", Key: "key", Severity: "critical"}
	rollback := errors.New("rollback")
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		if err := Persist(t.Context(), tx, uuid.NewV7(), s); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	for round := range 2 {
		if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error { return Persist(t.Context(), tx, uuid.NewV7(), s) }); err != nil {
			t.Fatal(err)
		}
		n := 0
		if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			return Consume(t.Context(), tx, func(Signal) error { n++; return nil })
		}); err != nil {
			t.Fatal(err)
		}
		if n != 1-round {
			t.Fatalf("round %d produced %d signals", round, n)
		}
	}
}
