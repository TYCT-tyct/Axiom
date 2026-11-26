import type { useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { expect, it } from "vitest";
import { diffOf, limitLine } from "./approval-diff";

// The words are the keys, with what they were given, so a test says what a row holds without a language.
const t = ((key: string, options?: Record<string, unknown>) =>
  options ? `${key} ${JSON.stringify(options)}` : key) as unknown as TFunction;
// Money as its currency and amount, a date as its ISO text: what the rows hold, not how a language writes it.
const format = {
  money: ({ amount, currency }: { amount: string; currency: string }) => `${currency} ${amount}`,
  decimal: (text: string) => text,
  dateTimeMinute: (at: Date) => at.toISOString(),
} as unknown as ReturnType<typeof useFormat>;

const approval = (over: Record<string, unknown>) =>
  ({
    id: "a",
    state: "pending",
    tier: "owner",
    requested_at: "2026-11-26T01:00:00Z",
    expires_at: "2026-11-26T01:30:00Z",
    operation_id: "o",
    credential_id: "c",
    tool: "credit_grant",
    target: { kind: "account", id: "x" },
    risk: "gift",
    params: {},
    summary: {},
    agent_reason: "",
    operation_state: "pending_approval",
    ...over,
  }) as unknown as Parameters<typeof diffOf>[2];

it("says what a grant gives, from what the server read", () => {
  const rows = diffOf(
    t,
    format,
    approval({ summary: { amount: "25000000000", currency: "USD", expires_at: "2026-12-01T00:00:00Z" } }),
  );
  expect(rows.map((row) => [row.label, row.before, row.after])).toEqual([
    ["approvals.board.diff.amount", undefined, "USD 25000000000"],
    ["approvals.board.diff.expires", undefined, "2026-12-01T00:00:00.000Z"],
  ]);
  expect(diffOf(t, format, approval({ summary: { amount: "1", currency: "USD" } }))[1]?.after).toBe(
    "approvals.request.never",
  );
});

it("shows each price a publication moves, before and after, and leaves the ones that stay", () => {
  const rows = diffOf(
    t,
    format,
    approval({
      risk: "price",
      tool: "price_publish",
      summary: {
        currency: "USD",
        current: { input: "0.000004", output: "0.00002" },
        proposed: { input: "0.0000045", output: "0.00002" },
        prices_complete: true,
      },
    }),
  );
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ before: "4 USD", after: "4.5 USD" });
  expect(rows[0]?.label).toContain("models.item.input");
});

it("says the status a model leaves and the one it goes to", () => {
  const rows = diffOf(
    t,
    format,
    approval({
      risk: "delisting",
      tool: "model_set_status",
      summary: { status: "listed" },
      params: { status: "retired" },
    }),
  );
  expect(rows).toEqual([
    { label: "approvals.board.diff.status", before: "models.state.listed", after: "models.state.retired" },
  ]);
});

it("has no rows for a kind it does not know, and no limit line without limits", () => {
  expect(diffOf(t, format, approval({ risk: "routine", summary: {} }))).toEqual([]);
  expect(limitLine(t, format, approval({ summary: {} }))).toBeUndefined();
});

it("reads the day's use of a transfer cap and of the price envelope into one line", () => {
  const cap = {
    currency: "USD",
    amount: "25000000000",
    single_max: "10000000000",
    hard_single_max: "50000000000",
    daily_used: "100000000000",
    daily_max: "500000000000",
    hard_daily_max: "900000000000",
    weekly_used: "0",
    weekly_max: "1",
    hard_weekly_max: "1",
    object_daily_used: "0",
    object_daily_max: "1",
    daily_count: 1,
    daily_count_max: 5,
  };
  const line = limitLine(t, format, approval({ summary: { cap } }));
  expect(line).toContain("approvals.board.limit.daily");
  expect(line).toContain('"used":"USD 100000000000"');
  expect(line).toContain('"max":"USD 500000000000"');
  expect(line).toContain('"amount":"USD 25000000000"');
  const envelope = {
    change_bps: 125,
    margin_bps: 3000,
    min_margin_bps: 1000,
    model_daily_change_bps: 2000,
    model_change_used: 0,
    daily_changes: 10,
    daily_changes_used: 3,
    daily_models: 5,
    daily_models_used: 1,
  };
  expect(limitLine(t, format, approval({ risk: "price", summary: { envelope } }))).toBe(
    'approvals.board.limit.changes {"used":3,"max":10}',
  );
});
