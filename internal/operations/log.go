package operations

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// maxInput is the most of a call's input the record keeps (docs/domains/operations.md「审计」); a larger input is
// recorded as its size alone.
const maxInput = 8 << 10

// LoggedCall is one request of the operations API as its record keeps it: which credential asked for which tool, why, with
// what, and how it was answered. Credential is the operations credential's ID; Run is the agent's own run ID, if it
// sent one; Error is the answer's error code, empty on success; Operation is the operation a write made or found, the
// zero ID for none.
type LoggedCall struct {
	ID         uuid.UUID
	Credential uuid.UUID
	Run        string
	Tool       string
	Reason     string
	Input      map[string]any
	Status     int
	Error      string
	RequestID  string
	Started    time.Time
	Ended      time.Time
	Operation  uuid.UUID
}

// RecordCall appends c to the operation log in tx, which must be in the platform scope, and gives it an ID. Its error
// must fail the call: an unrecorded call is not answered.
func RecordCall(ctx context.Context, tx pg.Tx, c LoggedCall) (uuid.UUID, error) {
	input, err := jsonx.MarshalCanonical(c.Input)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("record an operations call: encode the input: %w", err)
	}
	if len(input) > maxInput {
		input = fmt.Appendf(nil, `{"truncated":true,"bytes":%d}`, len(input))
	}
	id := uuid.NewV7()
	if err := New().InsertLoggedCall(ctx, tx, InsertLoggedCallParams{
		ID: id, CredentialID: c.Credential, RunID: optionalText(c.Run), Tool: c.Tool, Reason: c.Reason, Input: input,
		Status: int16(c.Status), ErrorCode: optionalText(c.Error), RequestID: c.RequestID, StartedAt: c.Started, EndedAt: c.Ended, // #nosec G115 -- an HTTP status
		OperationID: optionalID(c.Operation),
	}); err != nil {
		return uuid.UUID{}, fmt.Errorf("record an operations call: %w", err)
	}
	return id, nil
}

// LoggedCalls is a page of credential's recorded calls in tx, newest first, after the call after (the zero ID: from the
// start), and whether more follow.
func LoggedCalls(ctx context.Context, tx pg.Tx, credential, after uuid.UUID, pageSize int) ([]LoggedCall, bool, error) {
	rows, err := New().LoggedCalls(ctx, tx, LoggedCallsParams{CredentialID: credential, After: after, PageSize: int32(pageSize + 1)}) // #nosec G115 -- the API bounds the page to 200
	if err != nil {
		return nil, false, fmt.Errorf("read the operations calls: %w", err)
	}
	more := len(rows) > pageSize
	rows = rows[:min(len(rows), pageSize)]
	calls := make([]LoggedCall, len(rows))
	for i, r := range rows {
		var input map[string]any
		if err := jsonx.Unmarshal(r.Input, &input); err != nil {
			return nil, false, fmt.Errorf("read the operations calls: the input of %s: %w", r.ID, err)
		}
		calls[i] = LoggedCall{
			ID: r.ID, Credential: credential, Run: deref(r.RunID), Tool: r.Tool, Reason: r.Reason, Input: input,
			Status: int(r.Status), Error: deref(r.ErrorCode), RequestID: r.RequestID, Started: r.StartedAt, Ended: r.EndedAt,
		}
		if r.OperationID != nil {
			calls[i].Operation = *r.OperationID
		}
	}
	return calls, more, nil
}

func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalID(id uuid.UUID) *uuid.UUID {
	if id == (uuid.UUID{}) {
		return nil
	}
	return &id
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
