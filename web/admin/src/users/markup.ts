import type { AdminSchemas } from "@axiom/api";

type Rule = AdminSchemas["PricingRule"];

// A tier's markup is what the operator types, "this tier pays x% more", and what the server stores is a pricing rule
// for the tier whose default multiplier is 1 + x/100 (docs/domains/billing.md「定价」). Both directions are exact
// decimal text: a price must not move by a rounding of the page.

// A percentage above -100 with at most two decimals, the precision a person types a markup in.
const percentText = /^(-?)(\d{1,4})(?:\.(\d{1,2}))?$/;

// decimal is value / 10^places as decimal text, with no trailing zeros.
function decimal(value: bigint, places: number) {
  const negative = value < 0n;
  const digits = (negative ? -value : value).toString().padStart(places + 1, "0");
  const whole = digits.slice(0, digits.length - places);
  const fraction = digits.slice(digits.length - places).replace(/0+$/, "");
  return `${negative ? "-" : ""}${whole}${fraction ? `.${fraction}` : ""}`;
}

// multiplierOf is the rule's multiplier for a typed percentage, or null when the text is not one a tier can pay: not a
// number of that form, or a discount of 100% or more, which would sell for nothing.
export function multiplierOf(text: string): string | null {
  const match = percentText.exec(text.trim());
  if (!match) return null;
  const [, sign = "", whole = "0", fraction = ""] = match;
  const hundredths = BigInt(whole + fraction.padEnd(2, "0")) * (sign ? -1n : 1n);
  const scaled = 10_000n + hundredths; // the multiplier in ten-thousandths
  return scaled > 0n ? decimal(scaled, 4) : null;
}

// percentOf is the percentage a stored multiplier means.
export function percentOf(multiplier: string): string {
  const [whole = "0", fraction = ""] = multiplier.split(".");
  const unit = 10n ** BigInt(fraction.length);
  return decimal((BigInt(whole + fraction) - unit) * 100n, fraction.length);
}

// markupRule is the tier's own rule for every model, which the tier page edits: a default multiplier is its markup,
// and a rule of another method means the tier is priced some other way, which the pricing page edits.
export function markupRule(rules: readonly Rule[], tier: AdminSchemas["TierId"]): Rule | undefined {
  return rules.find((rule) => rule.audience.kind === "tier" && rule.audience.id === tier && rule.target.kind === "all");
}

// markupInput is the rule that makes the tier pay the multiplier, enabled.
export function markupInput(tier: AdminSchemas["TierId"], multiplier: string): AdminSchemas["PricingRuleInput"] {
  return {
    enabled: true,
    target: { kind: "all" },
    audience: { kind: "tier", id: tier },
    method: { kind: "default_multiplier", multiplier },
    constraints: {},
  };
}
