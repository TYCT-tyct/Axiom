// @vitest-environment jsdom
import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { RuleChangePanel, WhyAsked } from "./approval-request";
import { envelopeMaximaOf, ruleChangeOf } from "./approvals";
import { renderInDom } from "./test-dom";

type Approval = AdminSchemas["OperationsApproval"];

// A pricing rule change over the envelope, as internal/operations ProposePrices keeps it: the site's envelope with the
// day's use, the first moves and how many there are.
const ruleApproval: Approval = {
  id: "a",
  state: "pending",
  tier: "human",
  requested_at: "2026-11-08T08:00:00Z",
  expires_at: "2026-11-08T08:30:00Z",
  operation_id: "o",
  credential_id: "c",
  tool: "pricing_rule_update",
  target: { kind: "pricing_rule", id: "r" },
  risk: "price",
  params: {
    rev: 3,
    rule: {
      enabled: true,
      target: { kind: "all" },
      audience: { kind: "all" },
      method: { kind: "reference_multiplier", multiplier: "1.6", currency_conversion: "face" },
      constraints: {},
    },
  },
  summary: {
    rule_id: "r",
    rule_rev: 3,
    currency: "USD",
    envelope: {
      rev: 1,
      min_margin_bps: 0,
      model_daily_change_bps: 500,
      daily_changes: 5,
      daily_changes_used: 1,
      daily_models: 5,
      daily_models_used: 1,
    },
    moves_total: 14,
    moves: [
      {
        model_id: "m1",
        slug: "anthropic/claude-opus-5-5",
        audience: { kind: "tier", id: "tier_1" },
        change_bps: 667,
        margin_bps: 9000,
        model_change_used: 100,
        model_moved_today: true,
      },
      {
        model_id: "m2",
        audience: { kind: "account", id: "acct_1" },
        change_bps: null,
        margin_bps: null,
        model_change_used: 0,
      },
    ],
  },
  reasons: [{ code: "envelope_model_daily_change" }],
  agent_reason: "costs fell",
  operation_state: "pending_approval",
  decidable: true,
};

it("reads a pricing rule change: the rule, its moves, how many there are and the day's use", () => {
  const change = ruleChangeOf(ruleApproval);
  expect(change?.rule?.method.kind).toBe("reference_multiplier");
  expect(change?.rev).toBe(3);
  expect(change?.total).toBe(14);
  expect(change?.day).toEqual({ changes: { used: 1, max: 5 }, models: { used: 1, max: 5 } });
  expect(change?.moves).toEqual([
    { model: "m1", slug: "anthropic/claude-opus-5-5", audience: "tier", change: 667, margin: 9000, modelUsed: 100 },
    { model: "m2", slug: undefined, audience: "account", change: null, margin: null, modelUsed: 0 },
  ]);
  // A publication's approval, or a rule change from a node that kept no moves, is not read as one.
  expect(ruleChangeOf({ ...ruleApproval, tool: "price_publish" })).toBeUndefined();
  expect(ruleChangeOf({ ...ruleApproval, summary: {} })).toBeUndefined();
  // The envelope's limits read from the site's envelope alone, which a rule change's summary has.
  expect(envelopeMaximaOf(ruleApproval.summary)).toEqual({ minMargin: 0, modelChange: 500, changes: 5, models: 5 });
});

// The owner reads what the rule is, each price it moves and that more were moved than shown, and why it waits with the
// envelope's limit filled in, without opening the raw data.
it("shows a pricing rule change and why it waits", async () => {
  const { container, unmount } = await renderInDom(
    <>
      <RuleChangePanel approval={ruleApproval} />
      <WhyAsked approval={ruleApproval} />
    </>,
  );
  const text = container.textContent ?? "";
  for (const want of [
    "1.6× the reference price",
    "anthropic/claude-opus-5-5",
    "Change 6.67%",
    "this model's change today before it 1%",
    "Cannot be measured",
    "It moves 14 prices; the server kept the first 2.",
    "1 of 5 price changes",
    "over the envelope's daily change (5%)",
  ]) {
    expect(text).toContain(want);
  }
  await unmount();
});
