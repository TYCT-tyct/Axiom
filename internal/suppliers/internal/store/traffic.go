package store

import (
	"context"
	"uuid"
)

// The items are members of a jsonb object, which sqlc cannot type, so these queries are written here. Both read the
// index attempt_costs_endpoint_finished.

const endpointRequests = `
SELECT count(*) FROM suppliers.attempt_costs
WHERE endpoint_id = $1 AND NOT test AND finished_at > clock_timestamp() - interval '24 hours'`

const endpointUnits = `
SELECT i.key, sum(i.value::bigint)::bigint
FROM suppliers.attempt_costs a, jsonb_each_text(a.usage->'items') i
WHERE a.endpoint_id = $1 AND NOT a.test AND a.finished_at > clock_timestamp() - interval '24 hours'
GROUP BY i.key`

// EndpointTraffic is how many attempts the endpoint finished in the last 24 hours and the units of each item, by its
// name, they were priced at.
func EndpointTraffic(ctx context.Context, db DBTX, endpoint uuid.UUID) (int64, map[string]int64, error) {
	var requests int64
	if err := db.QueryRow(ctx, endpointRequests, endpoint).Scan(&requests); err != nil {
		return 0, nil, err
	}
	rows, err := db.Query(ctx, endpointUnits, endpoint)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	units := map[string]int64{}
	for rows.Next() {
		var item string
		var n int64
		if err := rows.Scan(&item, &n); err != nil {
			return 0, nil, err
		}
		units[item] = n
	}
	return requests, units, rows.Err()
}
