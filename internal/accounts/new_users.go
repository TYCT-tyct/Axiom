package accounts

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// NewUsers is how many users signed up on a site over a window, in total and by the UTC day's start.
type NewUsers struct {
	Total int64
	Days  map[time.Time]int64
}

// A user's ID is a UUIDv7 made at sign-up, so the primary key orders users by when they signed up and a window is a
// range of it; the version test leaves out an ID made any other way, which no sign-up path does.
const newUsersSQL = `SELECT date_trunc('day', uuid_extract_timestamp(id) AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', count(*)
 FROM accounts.users WHERE id >= $2 AND id < $3 AND site_id = $1 AND uuid_extract_version(id) = 7
 GROUP BY 1`

// ReadNewUsers counts site's users who signed up in [since, until), to the millisecond a UUIDv7 holds, in tx, which
// must hold the platform scope or the site's.
func ReadNewUsers(ctx context.Context, tx pg.Tx, site SiteID, since, until time.Time) (NewUsers, error) {
	rows, err := tx.Query(ctx, newUsersSQL, uuid.UUID(site), v7Floor(since), v7Floor(until))
	if err != nil {
		return NewUsers{}, fmt.Errorf("accounts: count the new users: %w", err)
	}
	defer rows.Close()
	out := NewUsers{Days: map[time.Time]int64{}}
	for rows.Next() {
		var day time.Time
		var n int64
		if err := rows.Scan(&day, &n); err != nil {
			return NewUsers{}, err
		}
		out.Days[day.UTC()] = n
		out.Total += n
	}
	return out, rows.Err()
}

// v7Floor is the least UUIDv7 made at t's millisecond or later: its 48-bit timestamp, then zeros. Every v7 made in an
// earlier millisecond sorts below it, and every one made at or after t's above or equal.
func v7Floor(t time.Time) uuid.UUID {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(t.UnixMilli())<<16)
	return uuid.UUID(b)
}
