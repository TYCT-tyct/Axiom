import type { AdminSchemas } from "@axiom/api";

// The closed lists of the catalog's contract, in the order the pages show them.

export const modelStatuses = ["draft", "listed", "retired"] as const satisfies readonly AdminSchemas["ModelStatus"][];

export const billingItems = [
  "input",
  "cache_read",
  "cache_write",
  "cache_write_1h",
  "output",
  "reasoning",
  "audio_in",
  "audio_out",
  "image_in",
  "image_out",
  "web_search",
  "code_execution",
  "file_search",
] as const satisfies readonly AdminSchemas["BillingItem"][];

export const serviceTiers = [
  "standard",
  "flex",
  "priority",
  "fast",
] as const satisfies readonly AdminSchemas["ServiceTier"][];

export const versionBases = ["dated", "floating"] as const satisfies readonly AdminSchemas["VersionBasis"][];

export const priceSources = [
  "litellm",
  "openrouter",
  "official",
  "manual",
] as const satisfies readonly AdminSchemas["PriceSource"][];

// The sources a person can publish prices for in M1; the other two are filled by the sync.
export const handSources = ["official", "manual"] as const satisfies readonly AdminSchemas["PriceSource"][];

export function modelStatus(value: unknown) {
  return modelStatuses.find((status) => status === value);
}

// Exact decimal text with its point moved right: a unit price is per token, and people read prices per million
// tokens, which is the same digits six places on; a ratio is read as a percentage, two places on. No number is ever
// rounded on the way.
function shiftRight(value: string, places: number) {
  const negative = value.startsWith("-");
  const [whole = "0", fraction = ""] = (negative ? value.slice(1) : value).split(".");
  const padded = fraction.padEnd(places, "0");
  const integer = `${whole}${padded.slice(0, places)}`.replace(/^0+(?=\d)/, "");
  const rest = padded.slice(places).replace(/0+$/, "");
  return `${negative ? "-" : ""}${rest ? `${integer}.${rest}` : integer}`;
}

export const perMillion = (price: string) => shiftRight(price, 6);
export const percent = (ratio: string) => shiftRight(ratio, 2);

// The inverse: what a person typed per million tokens, as the contract's unit price (at most 15 decimals, so 9 are
// allowed here); null when it is not a decimal.
export function perUnit(million: string) {
  const match = /^(\d+)(?:\.(\d{1,9}))?$/.exec(million.trim());
  if (!match) {
    return null;
  }
  const whole = (match[1] ?? "").padStart(6, "0");
  const integer = whole.slice(0, -6).replace(/^0+(?=\d)/, "") || "0";
  const fraction = `${whole.slice(-6)}${match[2] ?? ""}`.replace(/0+$/, "");
  return fraction ? `${integer}.${fraction}` : integer;
}

// The items a price list can carry: every reportable item, plus file_parse, which the gateway records and the
// upstream never reports, so it is not in a spec's items.
export const rateItems = [...billingItems, "file_parse"] as const satisfies readonly (keyof AdminSchemas["Rates"])[];

// Four items are priced per use, not per token, and are shown and typed as the contract has them.
export function isPerUse(item: keyof AdminSchemas["Rates"]) {
  return item === "web_search" || item === "code_execution" || item === "file_search" || item === "file_parse";
}
