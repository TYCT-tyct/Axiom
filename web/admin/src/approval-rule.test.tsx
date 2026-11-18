// @vitest-environment jsdom
import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { RuleChangePanel, WhyAsked } from "./approval-request";
import { envelopeMaximaOf, ruleChangeOf } from "./approval-rule";
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

// A fixed price is approved by its numbers: the owner reads the whole price list the rule would publish, the standard
// prices, every context band with its threshold and every service tier, each per unit, and nothing says a tier is kept
// as it is. A first price has no share of an old one to show, so the price list is all there is to judge it by.
it("shows the whole price list a fixed-price rule asks for", async () => {
  const schedule = {
    v: 1,
    currency: "USD",
    rates: { input: "0.000003", output: "0.000015", web_search: "0.01" },
    context_tiers: {
      basis: ["input"],
      method: "whole",
      tiers: [
        { above: 200000, rates: { input: "0.000006", output: "0.0000225" } },
        { above: 1000000, rates: { input: "0.000012", output: "0.00003" } },
      ],
    },
    service_tiers: {
      priority: {
        rates: { input: "0.00003", output: "0.00015" },
        context_tiers: [
          { above: 200000, rates: { input: "0.00006" } },
          { above: 1000000, rates: {} },
        ],
      },
    },
  } as const;
  const fixed: Approval = {
    ...ruleApproval,
    tool: "pricing_rule_create",
    target: { kind: "site", id: "s" },
    params: {
      rule: {
        enabled: true,
        target: { kind: "model", id: "m1" },
        audience: { kind: "all" },
        method: { kind: "fixed", schedule },
        constraints: {},
      },
    },
  };
  const { container, unmount } = await renderInDom(<RuleChangePanel approval={fixed} />);
  // Each heading with the prices under it, as the table reads row by row: item, price, unit.
  const section = (name: string) =>
    [...container.querySelectorAll("h3")].find((h) => h.textContent === name)?.parentElement?.textContent ?? "";
  expect(container.textContent).toContain("The fixed price asked for (USD)");
  expect(section("Standard prices (USD)")).toContain("Input3per 1M tokens");
  expect(section("Standard prices (USD)")).toContain("Output15per 1M tokens");
  expect(section("Standard prices (USD)")).toContain("0.01per use");
  expect(section("Above 200,000 tokens of Input")).toContain("Input6per 1M tokens");
  expect(section("Above 200,000 tokens of Input")).toContain("Output22.5per 1M tokens");
  expect(section("Above 1,000,000 tokens of Input")).toContain("Input12per 1M tokens");
  expect(section("Above 1,000,000 tokens of Input")).toContain("Output30per 1M tokens");
  expect(section("Priority service tier")).toContain("Input30per 1M tokens");
  expect(section("Priority service tier")).toContain("Output150per 1M tokens");
  expect(section("Priority service tier, Above 200,000 tokens of Input")).toContain("Input60per 1M tokens");
  expect(section("Priority service tier, Above 1,000,000 tokens of Input")).toContain("lists no prices of its own");
  expect(container.textContent).not.toContain("kept as they are");
  await unmount();
});

// A cost-plus rule's fixed markup is an amount the owner approves, shown per unit in the site's currency beside the
// markup ratio; a reference multiple says how it converts the reference price.
it("shows a cost-plus rule's fixed markup and a reference rule's conversion", async () => {
  const withMethod = (method: AdminSchemas["PricingMethod"]): Approval => ({
    ...ruleApproval,
    params: { rev: 3, rule: { ...(ruleApproval.params as { rule: object }).rule, method } as never },
  });
  const markup = await renderInDom(
    <RuleChangePanel
      approval={withMethod({ kind: "cost_plus", markup_ratio: "0.2", fixed_markup: { input: "0.0000004" } })}
    />,
  );
  const text = markup.container.textContent ?? "";
  expect(text).toContain("Cost plus 20%");
  expect(text).toContain("The fixed markup added to cost (USD)");
  expect(text).toContain("Input0.4per 1M tokens");
  await markup.unmount();
  const reference = await renderInDom(
    <RuleChangePanel
      approval={withMethod({ kind: "reference_multiplier", multiplier: "1.6", currency_conversion: "exchange" })}
    />,
  );
  expect(reference.container.textContent).toContain("Convert at the exchange rate");
  expect(reference.container.textContent).not.toContain("The fixed markup");
  await reference.unmount();
});
