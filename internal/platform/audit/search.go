package audit

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// Entry is an immutable audit record, including its structured change detail.
type Entry struct {
	ID         uuid.UUID  `json:"id"`
	At         time.Time  `json:"at"`
	ActorKind  string     `json:"actor_kind"`
	ActorID    string     `json:"actor_id"`
	RequestID  *uuid.UUID `json:"request_id,omitempty"`
	Action     string     `json:"action"`
	Module     string     `json:"module"`
	TargetKind string     `json:"target_kind"`
	TargetID   string     `json:"target_id"`
	Site       *uuid.UUID `json:"site_id,omitempty"`
	Account    *uuid.UUID `json:"account_id,omitempty"`
	Reason     string     `json:"reason"`
	Detail     jsonx.Raw  `json:"detail"`
}

// Search uses exact filters. Since is inclusive, Until exclusive; the caller bounds the window and limit.
type Search struct {
	Since, Until, BeforeAt                                                             time.Time
	BeforeID                                                                           uuid.UUID
	Site, Account, RequestID, ActorKind, ActorID, Action, Module, TargetKind, TargetID string
	Limit                                                                              int
}

const (
	entryColumns = `id,at,actor_kind,actor_id,request_id,action,module,target_kind,target_id,site_id,account_id,coalesce(reason,''),detail`
	searchSQL    = `SELECT ` + entryColumns + ` FROM platform.audit_log
 WHERE (at,id) < ($1::timestamptz,$2::uuid) AND at >= $3 AND at < $4
 AND ($5::text='' OR site_id=nullif($5,'')::uuid)
 AND ($6::text='' OR account_id=nullif($6,'')::uuid)
 AND ($7::text='' OR request_id=nullif($7,'')::uuid)
 AND ($8::text='' OR actor_kind=$8) AND ($9::text='' OR actor_id=$9)
 AND ($10::text='' OR action=$10) AND ($11::text='' OR module=$11)
 AND ($12::text='' OR target_kind=$12) AND ($13::text='' OR target_id=$13)
 ORDER BY audit_log.at DESC,audit_log.id DESC LIMIT $14`
)

// List returns at most Limit+1 entries; the extra entry proves another page exists.
func List(ctx context.Context, tx pg.Tx, q Search) ([]Entry, error) {
	if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan; SET LOCAL enable_sort = off"); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, searchSQL, q.BeforeAt, q.BeforeID, q.Since, q.Until, q.Site, q.Account, q.RequestID, q.ActorKind, q.ActorID, q.Action, q.Module, q.TargetKind, q.TargetID, q.Limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Entry, 0, q.Limit+1)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.At, &e.ActorKind, &e.ActorID, &e.RequestID, &e.Action, &e.Module, &e.TargetKind, &e.TargetID, &e.Site, &e.Account, &e.Reason, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns a single entry within the caller's authorized scope.
func Get(ctx context.Context, tx pg.Tx, id uuid.UUID) (Entry, error) {
	var e Entry
	err := tx.QueryRow(ctx, `SELECT `+entryColumns+` FROM platform.audit_log WHERE id=$1`, id).Scan(&e.ID, &e.At, &e.ActorKind, &e.ActorID, &e.RequestID, &e.Action, &e.Module, &e.TargetKind, &e.TargetID, &e.Site, &e.Account, &e.Reason, &e.Detail)
	return e, err
}
