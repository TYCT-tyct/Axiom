import type { AdminSchemas } from "@axiom/api";
import { parseMoney } from "@axiom/ui";
import type { Problem } from "./change";

// The dashboard's figures that take a target. Calls, paid users, revenue, monthly revenue, gross margin and retention
// are floors; errors and cost are ceilings, so the same positive gap is good for one and bad for the other.
export type TargetMetric =
  | "calls"
  | "errors"
  | "revenue"
  | "cost"
  | "paid_users"
  | "monthly_revenue"
  | "gross_margin"
  | "retention";

export const allTargetMetrics: readonly TargetMetric[] = [
  "calls",
  "errors",
  "revenue",
  "cost",
  "paid_users",
  "monthly_revenue",
  "gross_margin",
  "retention",
];

export const legacyTargetMetrics: readonly TargetMetric[] = ["calls", "errors", "revenue", "cost"];

export type TargetComparison = AdminSchemas["OperationsTargetComparison"];
export type CountComparison = AdminSchemas["OperationsCountTarget"];
export type MoneyComparison = AdminSchemas["OperationsMoneyTarget"];
export type RateComparison = AdminSchemas["OperationsRateTarget"];
export type TargetsInput = Pick<
  AdminSchemas["OperationsTargetsUpdate"],
  "calls" | "errors" | "revenue" | "cost" | "paid_users" | "monthly_revenue" | "gross_margin" | "retention"
>;

// The month a new target is for: the visit's UTC month, the same calendar the dashboard's window uses.
export function targetMonth(at: Date): string {
  return at.toISOString().slice(0, 7);
}

export function targetsEmpty(targets: TargetComparison): boolean {
  return (
    targets.calls === undefined &&
    targets.errors === undefined &&
    targets.revenue === undefined &&
    targets.cost === undefined &&
    targets.paid_users === undefined &&
    targets.monthly_revenue === undefined &&
    targets.gross_margin === undefined &&
    targets.retention === undefined
  );
}

export type GapSign = -1 | 0 | 1;

export type TargetTone = "ahead" | "behind" | "over" | "under" | "at";

// targetTone says how to read a gap: above a floor is ahead, above a ceiling is over, and the reverse below. At the
// target is neutral for both.
export function targetTone(metric: TargetMetric, sign: GapSign): TargetTone {
  if (sign === 0) {
    return "at";
  }
  if (metric === "errors" || metric === "cost") {
    return sign > 0 ? "over" : "under";
  }
  return sign > 0 ? "ahead" : "behind";
}

const maxCount = 1_000_000_000_000_000;

// A target's amount is stored in numeric(38, 0), wider than a ledger entry's int64 that parseMoney bounds by default.
const maxTargetDigits = 38;
export function parseTargetMoney(text: string): string | null {
  return parseMoney(text, maxTargetDigits);
}

// A rate target is stored in basis points, hundredths of a percent, and the editor asks for it as a percentage. The
// text is parsed as digits, not through a float, so 35.05% is exactly 3505 and 100.01% is refused rather than rounded
// into the valid range.
export function parseTargetRate(text: string): number | null {
  const typed = text.trim();
  if (!/^\d+(\.\d{1,2})?$/.test(typed)) {
    return null;
  }
  const [whole, fraction = ""] = typed.split(".");
  const basisPoints = Number(whole) * 100 + Number(`${fraction}00`.slice(0, 2));
  return Number.isSafeInteger(basisPoints) && basisPoints <= 10_000 ? basisPoints : null;
}

// formatTargetRate shows basis points as the percentage the editor takes, without trailing zeros.
export function formatTargetRate(basisPoints: number): string {
  const sign = basisPoints < 0 ? "-" : "";
  const absolute = Math.abs(basisPoints);
  const whole = Math.trunc(absolute / 100);
  const fraction = absolute % 100;
  if (fraction === 0) {
    return `${sign}${whole}%`;
  }
  if (fraction % 10 === 0) {
    return `${sign}${whole}.${fraction / 10}%`;
  }
  return `${sign}${whole}.${String(fraction).padStart(2, "0")}%`;
}

// readTargets turns the form into the request's targets: an empty field is a target to clear, a count must be a whole
// number the contract accepts, money is parsed exactly in the site's currency, and a rate is a percentage of at most
// 100. One malformed field refuses the whole form rather than saving the others. `metrics` is the set the node stores:
// a page in front of a node before the commercial metrics passes the legacy four, so the request never carries a field
// that node would refuse.
export function readTargets(
  values: Record<string, string>,
  currency: string,
  problem: { count: string; money: string; rate: string },
  metrics: readonly TargetMetric[] = allTargetMetrics,
): TargetsInput | Problem {
  const supported = new Set(metrics);
  const read: TargetsInput = {};
  for (const part of [
    readTargetCounts(values, supported, maxCount, problem.count),
    readTargetMoney(values, supported, currency, problem.money),
    readTargetRates(values, supported, problem.rate),
  ]) {
    if ("problem" in part) {
      return part;
    }
    Object.assign(read, part);
  }
  return read;
}

function readTargetCounts(
  values: Record<string, string>,
  supported: Set<TargetMetric>,
  max: number,
  problem: string,
): TargetsInput | Problem {
  const read: TargetsInput = {};
  for (const name of ["calls", "errors", "paid_users"] as const) {
    if (!supported.has(name)) {
      continue;
    }
    const count = readCount(values[name], max);
    if (count === "problem") {
      return { problem };
    }
    if (count !== null) {
      read[name] = count;
    }
  }
  return read;
}

function readTargetMoney(
  values: Record<string, string>,
  supported: Set<TargetMetric>,
  currency: string,
  problem: string,
): TargetsInput | Problem {
  const read: TargetsInput = {};
  for (const name of ["revenue", "cost", "monthly_revenue"] as const) {
    if (!supported.has(name)) {
      continue;
    }
    const typed = (values[name] ?? "").trim();
    if (typed === "") {
      continue;
    }
    const amount = parseTargetMoney(typed);
    if (amount === null) {
      return { problem };
    }
    read[name] = { amount, currency };
  }
  return read;
}

function readTargetRates(
  values: Record<string, string>,
  supported: Set<TargetMetric>,
  problem: string,
): TargetsInput | Problem {
  const read: TargetsInput = {};
  for (const name of ["gross_margin", "retention"] as const) {
    if (!supported.has(name)) {
      continue;
    }
    const typed = (values[name] ?? "").trim();
    if (typed === "") {
      continue;
    }
    const rate = parseTargetRate(typed);
    if (rate === null) {
      return { problem };
    }
    read[name] = rate;
  }
  return read;
}

// readCount is a whole non-negative count within the contract's bound, null when the field is empty, and "problem"
// when it is malformed. The sentinel keeps the three count fields from repeating the same parse and bound.
function readCount(text: string | undefined, max: number): number | null | "problem" {
  const typed = (text ?? "").trim();
  if (typed === "") {
    return null;
  }
  const count = Number(typed);
  if (!/^\d+$/.test(typed) || !Number.isSafeInteger(count) || count > max) {
    return "problem";
  }
  return count;
}
