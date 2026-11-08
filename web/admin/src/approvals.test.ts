import type { AdminSchemas } from "@axiom/api";
import { describe, expect, it } from "vitest";
import {
  approvalState,
  capOf,
  capOver,
  decidable,
  envelopeOf,
  envelopeOver,
  minutesLeft,
  outcomeOf,
  priceRows,
  reasonsOf,
  targetName,
} from "./approvals";

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
      rev: 2,
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
      rev: 1,
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

it.each([
  [{ decidable: true }, true],
  [{ decidable: false }, false],
  // A node older than the answer: a pending one is offered, and the server refuses a lapsed one.
  [{}, true],
  [{ state: "expired" }, false],
] as const)("is decidable as the server says: %o", (over, want) => {
  expect(decidable(approval(over))).toBe(want);
});

it("keeps only a state the page knows", () => {
  expect(approvalState("expired")).toBe("expired");
  expect(approvalState("done")).toBeUndefined();
});

describe("why it asked", () => {
  type Code = AdminSchemas["OperationsApprovalReason"]["code"];
  const asked = (...codes: string[]) => approval({ reasons: codes.map((code) => ({ code: code as Code })) });

  it("reads the server's reasons and the rows they mark", () => {
    const a = asked("always", "cap_paused", "cap_single", "cap_daily_count", "envelope_min_margin", "later_code");
    expect(reasonsOf(a)).toEqual([
      { kind: "always" },
      { kind: "paused" },
      { kind: "cap", limit: "single" },
      { kind: "cap", limit: "count" },
      { kind: "envelope", limit: "margin" },
      { kind: "unknown" },
    ]);
    expect([...capOver(a)]).toEqual(["single", "count"]);
    expect([...envelopeOver(a)]).toEqual(["margin"]);
  });

  it("cannot tell when an older node asked", () => {
    expect(reasonsOf(approval({}))).toBeUndefined();
    expect(capOver(approval({})).size).toBe(0);
  });
});

describe("targetName", () => {
  it.each([
    [
      "account",
      { account: "Northwind", email: "ops@northwind.example" },
      { name: "Northwind", detail: "ops@northwind.example" },
    ],
    ["supplier", { supplier: "Relay One" }, { name: "Relay One", detail: undefined }],
    [
      "model",
      { name: "Claude Opus", slug: "anthropic/claude-opus" },
      { name: "Claude Opus", detail: "anthropic/claude-opus" },
    ],
    ["model", { slug: "vendor/old-model" }, { name: "vendor/old-model", detail: undefined }],
    ["account", {}, undefined],
    ["node", { name: "n1" }, undefined],
  ])("reads a %s's name from %o", (kind, summary, want) => {
    expect(targetName(approval({ target: { kind, id: "x" }, summary }))).toEqual(want);
  });
});

it("lists the prices a publication changes, with their cost, in the catalog's order", () => {
  const summary = {
    current: { input: "0.000001", output: "0.000002", cache_read: "0.0000001" },
    proposed: { input: "0.0000012", output: "0.000002", web_search: "0.01" },
    cost: { input: "0.0000008" },
  };
  expect(priceRows(summary, ["input", "cache_read", "output", "web_search"])).toEqual([
    { item: "input", current: "0.000001", proposed: "0.0000012", cost: "0.0000008" },
    { item: "cache_read", current: "0.0000001", proposed: undefined, cost: undefined },
    { item: "web_search", current: undefined, proposed: "0.01", cost: undefined },
  ]);
  expect(priceRows({ current: summary.current }, ["input"])).toBeUndefined();
});

it("counts whole minutes left, never below none", () => {
  const a = approval({});
  expect(minutesLeft(a, Date.parse("2026-11-06T08:02:30Z"))).toBe(27);
  expect(minutesLeft(a, Date.parse("2026-11-06T08:31:00Z"))).toBe(0);
});
