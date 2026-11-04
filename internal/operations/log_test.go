package operations_test

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var platformScope = pg.NewScope(true, nil, nil)

// TestOperationLog: a credential reads its own calls newest first, a page at a time, and never another credential's.
func TestOperationLog(t *testing.T) {
	t.Parallel()
	db := openOperations(t)
	mine, theirs := uuid.NewV7(), uuid.NewV7()
	at := time.Now().UTC().Truncate(time.Microsecond)
	recordCall(t, db, mine, "suppliers_list", map[string]any{"query": map[string]any{"status": "active"}}, at)
	recordCall(t, db, theirs, "whoami", map[string]any{}, at)
	recordCall(t, db, mine, "models_list", map[string]any{}, at)
	recordCall(t, db, mine, "whoami", map[string]any{}, at)
	first, more := readCalls(t, db, platformScope, mine, uuid.UUID{}, 2)
	if len(first) != 2 || !more || first[0].Tool != "whoami" || first[1].Tool != "models_list" {
		t.Fatalf("the first page: %+v more=%v", first, more)
	}
	rest, more := readCalls(t, db, platformScope, mine, first[1].ID, 2)
	if len(rest) != 1 || more || rest[0].Tool != "suppliers_list" || rest[0].Reason != "patrol" || !rest[0].Started.Equal(at) {
		t.Fatalf("the second page: %+v more=%v", rest, more)
	}
	if query, _ := rest[0].Input["query"].(map[string]any); query["status"] != "active" {
		t.Errorf("the input reads back as %v", rest[0].Input)
	}
}

// TestOperationLogBounds: an input of exactly 8 KiB as canonical JSON is kept as it is, one byte more is kept as its
// size, and the database measures the same bytes (a jsonb column would add a space after each colon and refuse the
// first); a site scope sees none of the log, which is platform data.
func TestOperationLogBounds(t *testing.T) {
	t.Parallel()
	db := openOperations(t)
	frame := len(`{"query":{"x":""}}`)
	for _, c := range []struct {
		size      int
		truncated bool
	}{{8192, false}, {8193, true}} {
		mine := uuid.NewV7()
		recordCall(t, db, mine, "whoami", map[string]any{"query": map[string]any{"x": strings.Repeat("x", c.size-frame)}}, time.Now())
		page, _ := readCalls(t, db, platformScope, mine, uuid.UUID{}, 10)
		if len(page) != 1 || (page[0].Input["truncated"] == true) != c.truncated {
			t.Errorf("an input of %d bytes is kept as %.80v", c.size, page)
		}
		if others, _ := readCalls(t, db, pg.NewScope(false, []uuid.UUID{uuid.NewV7()}, nil), mine, uuid.UUID{}, 10); len(others) != 0 {
			t.Errorf("a site scope read %d entries of the log", len(others))
		}
	}
}

func openOperations(t *testing.T) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t, operations.Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func recordCall(t *testing.T, db *pg.DB, credential uuid.UUID, tool string, input map[string]any, at time.Time) {
	t.Helper()
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := operations.RecordCall(t.Context(), tx, operations.LoggedCall{
			Credential: credential, Tool: tool, Reason: "patrol", Input: input, Status: 200,
			RequestID: uuid.NewV7().String(), Started: at, Ended: at.Add(time.Millisecond),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func readCalls(t *testing.T, db *pg.DB, scope pg.Scope, credential, after uuid.UUID, size int) ([]operations.LoggedCall, bool) {
	t.Helper()
	var page []operations.LoggedCall
	var more bool
	if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) {
		page, more, err = operations.LoggedCalls(t.Context(), tx, credential, after, size)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return page, more
}
