// The operations monitor's health score, worked out in the page from what the page already shows, so every point taken
// off can be named and followed to its cause (ADMIN_HOWTO「健康分怎么算」). It starts at 100 and loses:
//   a line whose success rate is under 97%: 12, under 99%: 4 (a line is a supplier's group, over the shown window);
//   a supplier whose balance lasts under 5 days: 6;
//   each unresolved alert: 2, 10 at most;
//   any credential at its concurrency ceiling: 3, once.
// 90 or more is 运行正常, 70 to 89 需要留意, below that 有故障.

export type Cause =
  | { kind: "line"; supplier: string; group: string; failures: number; calls: number; heavy: boolean }
  | { kind: "balance"; supplier: string; days: number }
  | { kind: "alerts"; count: number }
  | { kind: "concurrency"; count: number };

export type Deduction = { points: number; cause: Cause };
type Verdict = "ok" | "watch" | "bad";
export type Health = { score: number; deductions: Deduction[]; verdict: Verdict };

export type HealthInput = {
  lines: readonly { supplier: string; group: string; calls: number; failures: number }[];
  suppliers: readonly { id: string; status: string; balanceDays?: number }[];
  unresolvedAlerts: number;
  credentials: readonly { in_flight?: number; max_concurrency: number }[];
};

const lineLoss = { heavy: 12, light: 4 } as const;
const balanceLoss = 6;
const alertLoss = 2;
const alertCap = 10;
const concurrencyLoss = 3;
const balanceDays = 5;
// Suppliers that route traffic; one paused, still connecting or on its way out has no balance to run short of.
const serving = new Set(["active", "low_balance"]);

// A rate under 97% is failures over 3% of the calls; compared as integers, so 97.0% exactly is not under it.
const failsOver = (line: { calls: number; failures: number }, percent: number) =>
  line.calls > 0 && line.failures * 100 > line.calls * percent;

// How a line's success rate reads: the colour a rate has wherever one is shown, with the lines' own thresholds.
export function rateTone(line: { calls: number; failures: number }): "ok" | "warn" | "bad" {
  return failsOver(line, 3) ? "bad" : failsOver(line, 1) ? "warn" : "ok";
}

const lineDeductions = (lines: HealthInput["lines"]): Deduction[] =>
  lines.flatMap((line) => {
    const heavy = failsOver(line, 3);
    return heavy || failsOver(line, 1)
      ? [{ points: heavy ? lineLoss.heavy : lineLoss.light, cause: { kind: "line" as const, ...line, heavy } }]
      : [];
  });

const balanceDeductions = (suppliers: HealthInput["suppliers"]): Deduction[] =>
  suppliers.flatMap((supplier) =>
    serving.has(supplier.status) && supplier.balanceDays !== undefined && supplier.balanceDays < balanceDays
      ? [
          {
            points: balanceLoss,
            cause: { kind: "balance" as const, supplier: supplier.id, days: supplier.balanceDays },
          },
        ]
      : [],
  );

const alertDeductions = (count: number): Deduction[] =>
  count > 0 ? [{ points: Math.min(alertCap, count * alertLoss), cause: { kind: "alerts", count } }] : [];

const concurrencyDeductions = (credentials: HealthInput["credentials"]): Deduction[] => {
  const full = credentials.filter((credential) => (credential.in_flight ?? 0) >= credential.max_concurrency);
  return full.length > 0 ? [{ points: concurrencyLoss, cause: { kind: "concurrency", count: full.length } }] : [];
};

export function healthOf(input: HealthInput): Health {
  const deductions = [
    ...lineDeductions(input.lines),
    ...balanceDeductions(input.suppliers),
    ...alertDeductions(input.unresolvedAlerts),
    ...concurrencyDeductions(input.credentials),
  ];
  const score = Math.max(0, 100 - deductions.reduce((sum, deduction) => sum + deduction.points, 0));
  return { score, deductions, verdict: score >= 90 ? "ok" : score >= 70 ? "watch" : "bad" };
}
