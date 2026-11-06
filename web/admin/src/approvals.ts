import type { AdminSchemas } from "@axiom/api";

type Approval = AdminSchemas["OperationsApproval"];
type Money = AdminSchemas["Money"];

// The states a page can narrow the list by (api/admin.yaml OperationsApprovalState).
export const approvalStates = ["pending", "approved", "rejected", "expired", "canceled"] as const;

// approvalState is the state in the address, or nothing: anything else does not narrow.
export function approvalState(value: unknown) {
  return approvalStates.find((state) => state === value);
}

// open says whether the owner can still decide: the server answers a decision on one past its expiry with a conflict
// even before the minutely sweep has marked it expired.
export function open(approval: Approval, now: number) {
  return approval.state === "pending" && Date.parse(approval.expires_at) > now;
}

// The caps a transfer was weighed against when the agent asked (internal/operations capDetail): each window's use
// before this one, its autonomous limit and its hard one, all in the cap's currency. A summary that lacks any of it, as
// an operation that moves no money does, has none.
export type CapLimits = {
  amount: Money;
  single: { max: Money; hard: Money };
  daily: { used: Money; max: Money; hard: Money };
  weekly: { used: Money; max: Money; hard: Money };
  objectDaily: { used: Money; max: Money };
  count: { used: number; max: number };
  paused: boolean;
  // The revision of the cap policy the server weighed it against.
  rev?: number;
};

const record = (value: unknown): Record<string, unknown> | undefined =>
  typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;

const integerText = (value: unknown) => (typeof value === "string" && /^\d+$/.test(value) ? value : undefined);
const count = (value: unknown) => (typeof value === "number" && Number.isSafeInteger(value) ? value : undefined);

// complete is values when every one of them was read, and nothing otherwise.
function complete<T extends Record<string, unknown>>(values: T): { [K in keyof T]: NonNullable<T[K]> } | undefined {
  return Object.values(values).every((value) => value !== undefined)
    ? (values as { [K in keyof T]: NonNullable<T[K]> })
    : undefined;
}

export function capOf(summary: unknown): CapLimits | undefined {
  const cap = record(record(summary)?.cap);
  const currency = cap?.currency;
  if (!cap || typeof currency !== "string") {
    return undefined;
  }
  const money = (key: string): Money | undefined => {
    const amount = integerText(cap[key]);
    return amount === undefined ? undefined : { amount, currency };
  };
  const v = complete({
    amount: money("amount"),
    single: money("single_max"),
    hardSingle: money("hard_single_max"),
    dailyUsed: money("daily_used"),
    daily: money("daily_max"),
    hardDaily: money("hard_daily_max"),
    weeklyUsed: money("weekly_used"),
    weekly: money("weekly_max"),
    hardWeekly: money("hard_weekly_max"),
    objectUsed: money("object_daily_used"),
    object: money("object_daily_max"),
    countUsed: count(cap.daily_count),
    countMax: count(cap.daily_count_max),
  });
  return (
    v && {
      amount: v.amount,
      single: { max: v.single, hard: v.hardSingle },
      daily: { used: v.dailyUsed, max: v.daily, hard: v.hardDaily },
      weekly: { used: v.weeklyUsed, max: v.weekly, hard: v.hardWeekly },
      objectDaily: { used: v.objectUsed, max: v.object },
      count: { used: v.countUsed, max: v.countMax },
      paused: cap.paused === true,
      rev: count(cap.policy_rev),
    }
  );
}

// The price envelope a sale price was weighed against (internal/operations envelopeDetail), in basis points and counts.
// change is null when the move cannot be measured as a ratio, margin when the cost is unknown: either sends it to the
// owner.
export type EnvelopeLimits = {
  change: number | null;
  margin: number | null;
  minMargin: number;
  modelChange: { used: number; max: number };
  changes: { used: number; max: number };
  models: { used: number; max: number };
  movedToday: boolean;
  rev?: number;
};

export function envelopeOf(summary: unknown): EnvelopeLimits | undefined {
  const e = record(record(summary)?.envelope);
  if (!e) {
    return undefined;
  }
  // null is a measurement that could not be made, which complete keeps; a missing or malformed one it does not.
  const measured = (value: unknown) => (value === null ? ({ none: true } as const) : count(value));
  const v = complete({
    change: measured(e.change_bps),
    margin: measured(e.margin_bps),
    minMargin: count(e.min_margin_bps),
    modelUsed: count(e.model_change_used),
    modelMax: count(e.model_daily_change_bps),
    changesUsed: count(e.daily_changes_used),
    changesMax: count(e.daily_changes),
    modelsUsed: count(e.daily_models_used),
    modelsMax: count(e.daily_models),
  });
  const bps = (value: number | { none: true }) => (typeof value === "number" ? value : null);
  return (
    v && {
      change: bps(v.change),
      margin: bps(v.margin),
      minMargin: v.minMargin,
      modelChange: { used: v.modelUsed, max: v.modelMax },
      changes: { used: v.changesUsed, max: v.changesMax },
      models: { used: v.modelsUsed, max: v.modelsMax },
      movedToday: e.model_moved_today === true,
      rev: count(e.rev),
    }
  );
}

// What a decision did, read from the answer rather than from what the dialog opened for: the target may have changed
// since the agent asked (the approval stands and the operation ends rejected with the target's code), and the
// credential that asked may be gone (nothing ran; both end canceled).
export type Outcome = "done" | "refused" | "canceled" | "rejected" | "other";

export function outcomeOf(approval: Approval): Outcome {
  switch (approval.state) {
    case "approved":
      if (approval.operation_state === "succeeded") {
        return "done";
      }
      return approval.operation_state === "rejected" ? "refused" : "other";
    case "canceled":
      return "canceled";
    case "rejected":
      return "rejected";
    default:
      return "other";
  }
}

// The autonomous limits a transfer goes over, as internal/operations weigh decides them: why it waited for the owner.
// None means it waited because the category always asks (a credit grant) or the scope is paused.
export type CapLimit = "single" | "daily" | "weekly" | "objectDaily" | "count";

export function capOver(cap: CapLimits): CapLimit[] {
  const n = (m: Money) => BigInt(m.amount);
  const amount = n(cap.amount);
  const over: [CapLimit, boolean][] = [
    ["single", amount > n(cap.single.max)],
    ["daily", n(cap.daily.used) + amount > n(cap.daily.max)],
    ["weekly", n(cap.weekly.used) + amount > n(cap.weekly.max)],
    ["objectDaily", n(cap.objectDaily.used) + amount > n(cap.objectDaily.max)],
    ["count", cap.count.used >= cap.count.max],
  ];
  return over.filter(([, is]) => is).map(([limit]) => limit);
}

// The parts of a price envelope a publication does not keep within, as internal/operations within decides them. A move
// or margin the server could not measure is never within: the envelope cannot vouch for it.
export type EnvelopeLimit = "unmeasured" | "unknownCost" | "margin" | "modelChange" | "changes" | "models";

export function envelopeOver(e: EnvelopeLimits): EnvelopeLimit[] {
  const over: [EnvelopeLimit, boolean][] = [
    ["unmeasured", e.change === null],
    ["unknownCost", e.margin === null],
    ["margin", e.margin !== null && e.margin < e.minMargin],
    ["modelChange", e.change !== null && e.modelChange.used + e.change > e.modelChange.max],
    ["changes", e.changes.used >= e.changes.max],
    ["models", !e.movedToday && e.models.used >= e.models.max],
  ];
  return over.filter(([, is]) => is).map(([limit]) => limit);
}

// Who or what an approval acts on, in the words the server read when the agent asked: an account by its name and its
// owner's email, a supplier by its name, a model by its public name or slug. An approval asked on an older node may
// have none of them; the page shows the ID alone then.
export function targetName(approval: Approval): { name: string; detail?: string } | undefined {
  const summary = record(approval.summary);
  const text = (key: string) => (typeof summary?.[key] === "string" && summary[key] !== "" ? summary[key] : undefined);
  const names: Record<string, [string | undefined, string | undefined]> = {
    account: [text("account"), text("email")],
    supplier: [text("supplier"), undefined],
    model: [text("name") ?? text("slug"), text("name") ? text("slug") : undefined],
  };
  const [name, detail] = names[approval.target.kind] ?? [undefined, undefined];
  return name ? { name, detail: detail === name ? undefined : detail } : undefined;
}

// A price item a publication sets, beside what it is now and what it costs, per unit of the site's currency.
export type PriceRow<I extends string> = { item: I; current?: string; proposed?: string; cost?: string };

const rates = (value: unknown) => {
  const r = record(value);
  return r && Object.values(r).every((price) => typeof price === "string") ? (r as Record<string, string>) : undefined;
};

// priceRows is each item whose price the publication changes, in the order of items: one priced now and not after,
// or the other way, changes too. Nothing is shown when the summary has no proposed prices (an older node).
export function priceRows<I extends string>(summary: unknown, items: readonly I[]): PriceRow<I>[] | undefined {
  const s = record(summary);
  const proposed = rates(s?.proposed);
  if (!proposed) {
    return undefined;
  }
  const current = rates(s?.current) ?? {};
  const cost = rates(s?.cost) ?? {};
  return items
    .filter((item) => (item in proposed || item in current) && proposed[item] !== current[item])
    .map((item) => ({ item, current: current[item], proposed: proposed[item], cost: cost[item] }));
}

// minutesLeft is how many whole minutes an open approval has before it expires, at least 0.
export function minutesLeft(approval: Approval, now: number) {
  return Math.max(0, Math.floor((Date.parse(approval.expires_at) - now) / 60_000));
}
