package billing

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// ReadKeyUsages reads a page's usage in one statement, under the account's scope. Keys are distinct and each carries
// its own reset window; absent counters and keys outside the scope have zero usage, as ReadKeyUsage does.
func ReadKeyUsages(ctx context.Context, tx pg.Tx, keys []KeyWindow) (map[uuid.UUID]KeyUsage, error) {
	ids, resets := make([]uuid.UUID, len(keys)), make([]string, len(keys))
	for i, k := range keys {
		ids[i], resets[i] = k.Key, string(k.Reset)
	}
	rows, err := queries.ReadKeyUsages(ctx, tx, ReadKeyUsagesParams{Keys: ids, Resets: resets})
	if err != nil {
		return nil, fmt.Errorf("billing: read the usage of keys: %w", err)
	}
	usage := make(map[uuid.UUID]KeyUsage, len(rows))
	for _, row := range rows {
		usage[row.KeyID] = KeyUsage{Total: row.Total, Day: row.Daily, Week: row.Weekly, Month: row.Monthly, Counted: row.Limited}
	}
	return usage, nil
}
