package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"axiom/internal/operations"
	"axiom/internal/platform/operationsapi"
)

// TestAwaitDecision: the wait reads the operation again every poll and answers the first read nobody can decide any
// more at the poll that read it; one nobody decides is answered as last read when the wait ends; a read's error and
// the caller's context ending end it at once.
func TestAwaitDecision(t *testing.T) {
	t.Parallel()
	waiting := operations.Operation{State: operations.StatePending, Approval: &operations.Approval{State: operations.ApprovalPending, Decidable: true}}
	decided := operations.Operation{State: operations.StateSucceeded, Approval: &operations.Approval{State: operations.ApprovalApproved}}
	late := operations.Operation{State: operations.StatePending, Approval: &operations.Approval{State: operations.ApprovalPending}}
	gone := errors.New("the database went away")
	for _, c := range []struct {
		name    string
		reads   []operations.Operation // what each read answers; the last one again after them
		failAt  int                    // the read, counted from 1, that fails with gone; 0 for none
		within  time.Duration          // the caller's deadline; 0 for none
		want    operations.State
		wantErr error
		took    time.Duration
	}{
		{name: "decided before the wait", reads: []operations.Operation{decided}, want: operations.StateSucceeded},
		{name: "decided during the wait", reads: []operations.Operation{waiting, waiting, decided}, want: operations.StateSucceeded, took: 2 * time.Second},
		{name: "past its expiry", reads: []operations.Operation{waiting, late}, want: operations.StatePending, took: time.Second},
		{name: "nobody decides", reads: []operations.Operation{waiting}, want: operations.StatePending, took: 10 * time.Second},
		{name: "a read fails", reads: []operations.Operation{waiting}, failAt: 3, wantErr: gone, took: 2 * time.Second},
		{name: "the caller gives up", reads: []operations.Operation{waiting}, within: 3500 * time.Millisecond, wantErr: context.DeadlineExceeded, took: 3500 * time.Millisecond},
	} {
		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			if c.within > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.within)
				defer cancel()
			}
			n := 0
			read := func() (operations.Operation, error) {
				n++
				if n == c.failAt {
					return operations.Operation{}, gone
				}
				return c.reads[min(n, len(c.reads))-1], nil
			}
			start := time.Now()
			op, err := awaitDecision(ctx, approvalWaitLimit, approvalWaitPoll, read)
			if !errors.Is(err, c.wantErr) || err == nil && op.State != c.want || time.Since(start) != c.took {
				t.Errorf("%s: %s %v after %v, want %s %v after %v", c.name, op.State, err, time.Since(start), c.want, c.wantErr, c.took)
			}
		})
	}
}

// agentApprovals reads a page of the credential's approvals as the agent, and the approval IDs on it.
func (f catalogFixture) agentApprovals(token, query string) (operationsapi.OperationList, []string) {
	f.t.Helper()
	rec := f.agent(token, because("/approvals"+query, "what waits"))
	var page operationsapi.OperationList
	decode(f.t, rec, &page)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("approvals%s: %d %s", query, rec.Code, rec.Body)
	}
	ids := make([]string, len(page.Data))
	for i, op := range page.Data {
		ids[i] = op.Approval.ApprovalId
	}
	return page, ids
}

// TestOperationsApprovalsList: the agent lists the approvals its own proposals asked for, newest first, in a state
// and paged by approval; another credential's are not listed.
func TestOperationsApprovalsList(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	_, token := f.writer()
	_, first := f.listedAndAsked(token, "acme/l1")
	_, second := f.listedAndAsked(token, "acme/l2")
	_, other := f.writer()
	_, others := f.listedAndAsked(other, "acme/l3")
	expectAdmin(t, "rejectOperationsApproval", f.decide(first.Approval.ApprovalId, "reject"), http.StatusOK, "")

	for _, c := range []struct {
		token, query string
		want         []string
		next         bool
	}{
		{token, "?limit=1", []string{second.Approval.ApprovalId}, true},
		{token, "?limit=1&cursor=" + second.Approval.ApprovalId, []string{first.Approval.ApprovalId}, false},
		{token, "?state=pending", []string{second.Approval.ApprovalId}, false},
		{token, "?state=rejected", []string{first.Approval.ApprovalId}, false},
		{other, "", []string{others.Approval.ApprovalId}, false},
	} {
		page, ids := f.agentApprovals(c.token, c.query)
		if !slices.Equal(ids, c.want) || (page.NextCursor != nil) != c.next {
			t.Errorf("approvals%s: %v next %v, want %v", c.query, ids, page.NextCursor, c.want)
		}
	}
}

// TestOperationsApprovalWait: a wait on an approval nobody decides answers it still pending when the wait ends, and
// one on an approval already decided its outcome; a wait longer than ten seconds is refused, and another
// credential's approval is not found.
func TestOperationsApprovalWait(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	_, token := f.writer()
	_, waiting := f.listedAndAsked(token, "acme/w1")
	_, decided := f.listedAndAsked(token, "acme/w2")
	_, other := f.writer()
	expectAdmin(t, "approveOperationsApproval", f.decide(decided.Approval.ApprovalId, "approve"), http.StatusOK, "")
	wait := func(token, approval, query string) *httptest.ResponseRecorder {
		return f.agent(token, because("/approvals/"+approval+"/wait"+query, "is it decided"))
	}

	expectOperation(t, wait(token, waiting.Approval.ApprovalId, "?wait_seconds=1"), "pending_approval", "pending")
	expectOperation(t, wait(token, decided.Approval.ApprovalId, ""), "succeeded", "approved")
	expectAdmin(t, "a wait of 11 seconds", wait(token, waiting.Approval.ApprovalId, "?wait_seconds=11"), http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "waiting on another credential's approval", wait(other, waiting.Approval.ApprovalId, ""), http.StatusNotFound, codeNotFound)
}
