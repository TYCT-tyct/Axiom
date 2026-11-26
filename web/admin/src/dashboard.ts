import type { AdminSchemas } from "@axiom/api";

// What the dashboard works out from the overview the API answers: comparisons with the period before, the margin, the
// model mix and the order of what needs doing. Pure, so the figures are tested without a page.

export type Overview = AdminSchemas["OperationsOverview"];
type Money = AdminSchemas["Money"];
type Tokens = AdminSchemas["OverviewTokens"];
type Day = AdminSchemas["OverviewDay"];

// An amount in whole units, for a trend line and a comparison, which are pictures and ratios; the figure itself is shown
// from the exact text.
export const units = (money: Money | undefined) => (money === undefined ? undefined : Number(money.amount) / 1e9);

// How far a figure moved against the period before it, in percent to one decimal; nothing when there was nothing before
// it to move from.
export function change(now: number | undefined, before: number | undefined): number | undefined {
  if (now === undefined || before === undefined || before === 0) {
    return undefined;
  }
  return Math.round(((now - before) / before) * 1000) / 10;
}

// The share of calls that failed, in percent; nothing for no calls.
export const failureRate = (overview: Overview | undefined) =>
  overview?.calls && overview.errors !== undefined ? (overview.errors / overview.calls) * 100 : undefined;

// The movement of a rate or a time in its own unit, to the precision it is read at.
export function shift(now: number | undefined, before: number | undefined, digits: number): number | undefined {
  if (now === undefined || before === undefined) {
    return undefined;
  }
  const scale = 10 ** digits;
  return Math.round((now - before) * scale) / scale;
}

// Revenue less cost, exactly: only when every call's cost is known, since a total that leaves some out would show a margin
// bigger than it is.
export function margin(overview: Pick<Overview, "revenue" | "cost" | "unknown_cost_calls">): Money | undefined {
  const { revenue, cost } = overview;
  if (!revenue || !cost || overview.unknown_cost_calls || revenue.currency !== cost.currency) {
    return undefined;
  }
  return { amount: (BigInt(revenue.amount) - BigInt(cost.amount)).toString(), currency: revenue.currency };
}

// Every token the calls reported. Cached reads and writes are input too, kept apart in the overview.
export const tokenTotal = (tokens: Tokens | undefined) =>
  tokens === undefined ? undefined : tokens.input + tokens.cache_read + tokens.cache_write + tokens.output;

// What the chart calls input (the tokens the model read fresh and the ones it wrote to a cache) and cache hit.
export const tokenParts = (tokens: Tokens) => ({
  output: tokens.output,
  input: tokens.input + tokens.cache_write,
  hit: tokens.cache_read,
});

// A figure of each day of the window, oldest first, for a trend line: a day that lacks it is left out so a line never
// drops to a zero the API did not give. Fewer than two days is no trend.
export function trendOf(days: readonly Day[] | undefined, pick: (day: Day) => number | undefined): number[] {
  const values = (days ?? []).flatMap((day) => pick(day) ?? []);
  return values.length < 2 ? [] : values;
}

export type MixKind = "revenue" | "calls" | "tokens";
export type MixPart = { model: string | undefined; value: number };

// The models' shares of one measure. The overview lists the busiest models only, so what the window had beyond them is
// one more part, "the others", when the window's total is known and bigger than the listed sum; a share is of that total.
// With no total the shares are of what is listed.
export function mixOf(overview: Overview, kind: MixKind): MixPart[] {
  const models = overview.top_models ?? [];
  const value = (model: AdminSchemas["OverviewModel"]) =>
    kind === "revenue" ? (units(model.revenue) ?? 0) : kind === "calls" ? (model.calls ?? 0) : (model.tokens ?? 0);
  const parts = models.map((model) => ({ model: model.model, value: value(model) })).filter((part) => part.value > 0);
  const total =
    kind === "revenue" ? units(overview.revenue) : kind === "calls" ? overview.calls : tokenTotal(overview.tokens);
  const listed = parts.reduce((sum, part) => sum + part.value, 0);
  return total !== undefined && total > listed ? [...parts, { model: undefined, value: total - listed }] : parts;
}

// What needs doing, most pressing first (adTodo): an alert that is failing, then approvals waiting, then the alerts that
// warn, then the rest. Equally pressing ones keep the order they came in.
export type Todo = { rank: 0 | 1 | 2 | 3 };

export function byUrgency<T extends Todo>(items: readonly T[]): T[] {
  return items.toSorted((a, b) => a.rank - b.rank);
}
