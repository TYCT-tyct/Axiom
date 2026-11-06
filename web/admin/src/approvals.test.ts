import type { AdminSchemas } from "@axiom/api";
import { describe, expect, it } from "vitest";
import { approvalState, capOf, envelopeOf, open, outcomeOf } from "./approvals";

type Approval = AdminSchemas["OperationsApproval"];

const cap = {
  currency: "USD",
  amount: "5000000000",
  policy_rev: 2,
  single_max: "1000000000",
  hard_single_max: "9000000000",
  daily_used: "0",
  daily_max: "3000000000",
  hard_daily_max: "20000000000",
  weekly_used: "7000000000",
  weekly_max: "9000000000",
  hard_weekly_max: "90000000000",
  object_daily_used: "0",
  object_daily_max: "3000000000",
  daily_count: 1,
  daily_count_max: 10,
  paused: false,
};

const envelope = {
  change_bps: 250,
  margin_bps: null,
  rev: 1,
  min_margin_bps: 1500,
  model_daily_change_bps: 1000,
  model_change_used: 300,
  daily_changes: 20,
  daily_changes_used: 4,
  daily_models: 5,
  daily_models_used: 2,
  model_moved_today: true,
};

describe("capOf", () => {
  it("reads each window in the cap's currency", () => {
    const usd = (amount: string) => ({ amount, currency: "USD" });
    expect(capOf({ supplier: "Relay", cap: { ...cap, paused: true } })).toEqual({
      amount: usd("5000000000"),
      single: { max: usd("1000000000"), hard: usd("9000000000") },
      daily: { used: usd("0"), max: usd("3000000000"), hard: usd("20000000000") },
      weekly: { used: usd("7000000000"), max: usd("9000000000"), hard: usd("90000000000") },
      objectDaily: { used: usd("0"), max: usd("3000000000") },
      count: { used: 1, max: 10 },
      paused: true,
    });
  });

  it.each([
    ["no cap", { slug: "gpt-x" }],
    ["no currency", { cap: { ...cap, currency: undefined } }],
    ["a missing limit", { cap: { ...cap, hard_weekly_max: undefined } }],
    ["a number for an amount", { cap: { ...cap, daily_max: 3 } }],
    ["a fraction", { cap: { ...cap, amount: "1.5" } }],
    ["a count as text", { cap: { ...cap, daily_count: "1" } }],
    ["a summary that is no object", "cap"],
  ])("has none with %s", (_, summary) => {
    expect(capOf(summary)).toBeUndefined();
  });
});

describe("envelopeOf", () => {
  it("keeps a measurement that could not be made apart from the limits", () => {
    expect(envelopeOf({ model_id: "m", envelope })).toEqual({
      change: 250,
      margin: null,
      minMargin: 1500,
      modelChange: { used: 300, max: 1000 },
      changes: { used: 4, max: 20 },
      models: { used: 2, max: 5 },
      movedToday: true,
    });
  });

  it.each([
    ["no envelope", { cap }],
    ["a missing change", { envelope: { ...envelope, change_bps: undefined } }],
    ["a limit as text", { envelope: { ...envelope, daily_models: "5" } }],
  ])("has none with %s", (_, summary) => {
    expect(envelopeOf(summary)).toBeUndefined();
  });
});

const approval = (over: Partial<Approval>): Approval => ({
  id: "a",
  state: "pending",
  tier: "human",
  requested_at: "2026-11-06T08:00:00Z",
  expires_at: "2026-11-06T08:30:00Z",
  operation_id: "o",
  credential_id: "c",
  tool: "credit_grant",
  target: { kind: "account", id: "acct_1" },
  risk: "gift",
  params: {},
  summary: {},
  agent_reason: "r",
  operation_state: "pending_approval",
  ...over,
});

it.each([
  [{ state: "approved", operation_state: "succeeded" }, "done"],
  [{ state: "approved", operation_state: "rejected", error_code: "stale_revision" }, "refused"],
  [{ state: "approved", operation_state: "executing" }, "other"],
  [{ state: "canceled", operation_state: "canceled" }, "canceled"],
  [{ state: "rejected", operation_state: "rejected" }, "rejected"],
  [{ state: "pending" }, "other"],
] as const)("an answer %o is %s", (over, outcome) => {
  expect(outcomeOf(approval(over))).toBe(outcome);
});

it("is open while pending and before its expiry", () => {
  const before = Date.parse("2026-11-06T08:29:59Z");
  const at = Date.parse("2026-11-06T08:30:00Z");
  expect(open(approval({}), before)).toBe(true);
  expect(open(approval({}), at)).toBe(false);
  expect(open(approval({ state: "approved" }), before)).toBe(false);
});

it("keeps only a state the page knows", () => {
  expect(approvalState("expired")).toBe("expired");
  expect(approvalState("done")).toBeUndefined();
});
