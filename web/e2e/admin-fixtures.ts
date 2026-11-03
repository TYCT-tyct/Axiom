import type { components } from "../packages/api/src/admin.gen.ts";

// The contract's own types and the values every fixture file builds on. admin-data.ts re-exports `uuid` and `Schemas`
// for the specs that only need an ID or a type.

export type Schemas = components["schemas"];

export const pad = (n: number, width = 26) => String(n).padStart(width, "0");
export const uuid = (kind: number, n: number) => `${pad(kind, 8)}-0000-4000-8000-${pad(n, 12)}`;
export const tierId = (n: number) => `tier_${pad(n)}` as Schemas["TierId"];
export const siteId = `site_${pad(1)}` as Schemas["SiteId"];
export const usd = (units: string): Schemas["Money"] => ({ amount: `${units}000000000`, currency: "USD" });

// The targets the admin mock starts with: the four values make every direction visible against the mocked calls, so
// the screenshots show ahead, under and a missing target rather than an empty row.
export function usualTargets(): Map<string, Schemas["OperationsTargets"]> {
  const month = new Date().toISOString().slice(0, 7);
  return new Map([
    [
      month,
      {
        site_id: siteId,
        month,
        rev: 1,
        calls: 45,
        errors: 10,
        revenue: usd("2000"),
        cost: usd("1800"),
      },
    ],
  ]);
}
export const nano = (amount: string): Schemas["Money"] => ({ amount, currency: "USD" });
export const range = (count: number) => Array.from({ length: count }, (_, i) => i + 1);
export const long = (text: string, length: number) => text.repeat(Math.ceil(length / text.length)).slice(0, length);
