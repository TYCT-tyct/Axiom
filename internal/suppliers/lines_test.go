package suppliers

import (
	"context"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

// TestEndpointLinesNameEachEndpointsGroup: an endpoint maps to its supplier and group; one the table lacks is left out.
func TestEndpointLinesNameEachEndpointsGroup(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	one, two := a.group(id, "one", true), a.group(id, "two", true)
	first, second, missing := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	a.sql(fmt.Sprintf(`INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
		VALUES ('%s', '%[2]s', '%[3]s', uuidv7(), 'a', ARRAY['chat']), ('%[4]s', '%[2]s', '%[5]s', uuidv7(), 'b', ARRAY['chat'])`,
		first, id, one, second, two))
	lines, err := inTx(a, func(ctx context.Context, tx pg.Tx) (map[uuid.UUID]Line, error) {
		return EndpointLines(ctx, tx, []uuid.UUID{first, second, missing})
	})
	refuses(t, "the lines", err, nil)
	same(t, "the lines", map[uuid.UUID]Line{first: {Supplier: id, Group: one}, second: {Supplier: id, Group: two}}, lines)
}
