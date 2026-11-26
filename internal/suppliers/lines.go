package suppliers

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// Line is the supplier group an endpoint serves through: the line a call took (docs/domains/observability.md
// 「决策与理由」).
type Line struct {
	Supplier, Group uuid.UUID
}

// EndpointLines reads the line of each of endpoints in tx, which must hold the platform scope; an endpoint the table
// does not have is left out of the map.
func EndpointLines(ctx context.Context, tx pg.Tx, endpoints []uuid.UUID) (map[uuid.UUID]Line, error) {
	rows, err := tx.Query(ctx, `SELECT id, supplier_id, supplier_group_id FROM suppliers.endpoints WHERE id = ANY($1::uuid[])`, endpoints)
	if err != nil {
		return nil, fmt.Errorf("suppliers: read the endpoints' lines: %w", err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]Line, len(endpoints))
	for rows.Next() {
		var id uuid.UUID
		var l Line
		if err := rows.Scan(&id, &l.Supplier, &l.Group); err != nil {
			return nil, err
		}
		out[id] = l
	}
	return out, rows.Err()
}
