package payments

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"uuid"
)

func TestUnknownRefundManualConclusionNeedsEvidenceAndSecondPerson(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			testRefundManualConclusion(t, outcome)
		})
	}
}

func TestOfflineRefundUsesTheSameFreezeAndLedger(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{})
	request := refundRequest(w, o, o.Credits, "offline")
	request.Via = "offline"
	if _, err := w.p.Refund(t.Context(), w.accountA(), request); !errors.Is(err, ErrRefundForbidden) {
		t.Fatal(err)
	}
	r, err := w.p.Refund(t.Context(), siteScope(w.siteA), request)
	if err != nil {
		t.Fatal(err)
	}
	r = w.approve(r)
	if r.State != "frozen" {
		t.Fatal(r)
	}
	proposal := Resolution{Order: o.ID, Refund: r.ID, Outcome: "succeeded", Key: "receipt", User: w.user, Reason: "offline return", Evidence: "bank transfer receipt"}
	if err = w.p.Resolve(t.Context(), siteScope(w.siteA), proposal); err != nil {
		t.Fatal(err)
	}
	if err = w.p.ApproveResolution(t.Context(), siteScope(w.siteA), o.ID, proposal.Key, uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	state, _, _ := w.stored(o.ID)
	if state != "refunded" {
		t.Fatal(state)
	}
}

func TestAmountMismatchPolicyAndManualCredit(t *testing.T) {
	for _, policy := range []string{"manual", "actual"} {
		t.Run(policy, func(t *testing.T) {
			testPaymentMismatchPolicy(t, policy)
		})
	}
}

func testRefundManualConclusion(t *testing.T, outcome string) {
	t.Helper()

	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) { return RefundState{}, context.DeadlineExceeded }})
	r := w.approve(w.mustRefund(o, o.Credits, "unknown"))
	proposal := Resolution{Order: o.ID, Refund: r.ID, Outcome: outcome, Key: "decision", User: w.user, Reason: "verified at channel", Evidence: "channel refund record / no refund was made"}
	missing := proposal
	missing.Evidence = ""
	if err := w.p.Resolve(t.Context(), siteScope(w.siteA), missing); err == nil {
		t.Fatal("no evidence accepted")
	}
	if err := w.p.Resolve(t.Context(), w.accountA(), proposal); !errors.Is(err, ErrRefundForbidden) {
		t.Fatal(err)
	}
	if err := w.p.Resolve(t.Context(), siteScope(w.siteA), proposal); err != nil {
		t.Fatal(err)
	}
	if err := w.p.ApproveResolution(t.Context(), siteScope(w.siteA), o.ID, proposal.Key, w.user); !errors.Is(err, ErrSecondPerson) {
		t.Fatal(err)
	}
	got := w.mustGetRefund(r.ID)
	if got.State != "pending" {
		t.Fatal(got)
	}
	assertResolutionStored(w, o, proposal)

	assertResolutionImmutable(w, proposal)

	for range 2 {
		if err := w.p.ApproveResolution(t.Context(), siteScope(w.siteA), o.ID, proposal.Key, uuid.NewV7()); err != nil {
			t.Fatal(err)
		}
	}
	got = w.mustGetRefund(r.ID)
	if got.State != outcome {
		t.Fatal(got)
	}
	kind := "refund"
	if outcome == "failed" {
		kind = "unfreeze"
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind=$1", kind); n != 1 {
		t.Fatal(n)
	}
}

func testPaymentMismatchPolicy(t *testing.T, policy string) {
	t.Helper()

	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	w.limits(fmt.Sprintf("amount_mismatch_policy='%s'", policy))
	o := w.mustPlace(10*yuan, "mismatch")
	c := receipt(o)
	c.payment.Paid.Minor -= 37
	w.receive(c)
	if policy == "manual" {
		state, _, _ := w.stored(o.ID)
		if state != "review" {
			t.Fatal(state)
		}
		r := Resolution{Order: o.ID, Outcome: "credit", Payment: c.payment, Key: "credit", User: w.user, Reason: "accept actual payment", Evidence: "gateway trade receipt"}
		if err := w.p.Resolve(t.Context(), siteScope(w.siteA), r); err != nil {
			t.Fatal(err)
		}
		if err := w.p.ApproveResolution(t.Context(), siteScope(w.siteA), o.ID, r.Key, uuid.NewV7()); err != nil {
			t.Fatal(err)
		}
	}
	want, _, err := creditAt(c.payment.Paid, Fee{PPM: 30000}, big.NewRat(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	w.assertCredit(o, want)
	for range 2 {
		w.receive(c)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM billing.credit_batches"); n != 1 {
		t.Fatal(n)
	}
}

func assertResolutionImmutable(w *world, r Resolution) {
	w.t.Helper()
	t := w.t
	altered := r
	altered.Outcome = "failed"
	if r.Outcome == "failed" {
		altered.Outcome = "succeeded"
	}
	if err := w.p.Resolve(t.Context(), siteScope(w.siteA), altered); !errors.Is(err, ErrKeyReused) {
		t.Fatal(err)
	}
}

func assertResolutionStored(w *world, o Order, proposal Resolution) {
	w.t.Helper()
	t := w.t
	stored, err := w.p.GetResolution(t.Context(), siteScope(w.siteA), o.ID, proposal.Key)
	if err != nil || stored != proposal {
		t.Fatal(stored, err)
	}
}
