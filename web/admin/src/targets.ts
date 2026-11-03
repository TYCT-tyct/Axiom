import type { AdminSchemas } from "@axiom/api";
import { parseMoney } from "@axiom/ui";
import type { Problem } from "./change";

// The dashboard's figures that take a target. Calls and revenue are floors; errors and cost are ceilings, so the same
// positive gap is good for one and bad for the other.
export type TargetMetric = "calls" | "errors" | "revenue" | "cost";

export type TargetComparison = AdminSchemas["OperationsTargetComparison"];
export type CountComparison = AdminSchemas["OperationsCountTarget"];
export type MoneyComparison = AdminSchemas["OperationsMoneyTarget"];
export type TargetsInput = Omit<AdminSchemas["OperationsTargetsUpdate"], "reason">;

// The month a new target is for: the visit's UTC month, the same calendar the dashboard's window uses.
export function targetMonth(at: Date): string {
  return at.toISOString().slice(0, 7);
}

export function targetsEmpty(targets: TargetComparison): boolean {
  return (
    targets.calls === undefined &&
    targets.errors === undefined &&
    targets.revenue === undefined &&
    targets.cost === undefined
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

// readTargets turns the form into the request's targets: an empty field is a target to clear, a count must be a whole
// number the contract accepts, and money is parsed exactly in the site's currency. One malformed field refuses the
// whole form rather than saving the others.
export function readTargets(
  values: Record<string, string>,
  currency: string,
  problem: { count: string; money: string },
): TargetsInput | Problem {
  const read: TargetsInput = {};
  for (const name of ["calls", "errors"] as const) {
    const typed = (values[name] ?? "").trim();
    if (typed === "") {
      continue;
    }
    const count = Number(typed);
    if (!/^\d+$/.test(typed) || !Number.isSafeInteger(count) || count > maxCount) {
      return { problem: problem.count };
    }
    read[name] = count;
  }
  for (const name of ["revenue", "cost"] as const) {
    const typed = (values[name] ?? "").trim();
    if (typed === "") {
      continue;
    }
    const amount = parseMoney(typed);
    if (amount === null) {
      return { problem: problem.money };
    }
    read[name] = { amount, currency };
  }
  return read;
}
