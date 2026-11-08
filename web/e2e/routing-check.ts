import type { Schemas } from "./console-data.ts";

// What the mocked console and admin answer of a preset's rules: the summary a list shows and the check an editor asks
// (internal/routing/check.go). A stand-in, not the server's logic: every mocked endpoint may train on data and none is
// scored, so a policy list without may_train, or a score, keeps no endpoint, which is all the scenes need.

type Rule = Schemas["RoutingRule"];
type Rules = { default?: Rule | null; rules?: Rule[] | null };

function summaryOf(stored: Schemas["RoutingRules"]): Schemas["RoutingRulesSummary"] {
  const base = (stored as Rules).default ?? {};
  const rules = (stored as Rules).rules ?? [];
  return {
    sort: base.sort || "balanced",
    classes: base.classes?.includes("*") ? undefined : (base.classes ?? undefined),
    verified_only: base.verified_only === true,
    priced: [base, ...rules].some((rule) => Object.values(rule.max_price ?? {}).some((price) => price != null)),
    overrides: rules.length,
  };
}

export function withSummary<Preset extends { rules: Schemas["RoutingRules"] }>(preset: Preset) {
  return { ...preset, summary: summaryOf(preset.rules) };
}

function unmatchedBy(rule: Rule): Schemas["RoutingRuleUnmatched"]["fields"] {
  const fields: Schemas["RoutingRuleUnmatched"]["fields"] = [];
  if (rule.policies && !rule.policies.includes("may_train") && !rule.policies.includes("*")) {
    fields.push("policies");
  }
  if (rule.min_score != null) {
    fields.push("min_score");
  }
  return fields;
}

export function checkOf(stored: Schemas["RoutingRules"]): Schemas["RoutingRulesCheck"] {
  const base = (stored as Rules).default ?? {};
  const rules = (stored as Rules).rules ?? [];
  const unmatched = [
    { index: undefined, fields: unmatchedBy(base) },
    ...rules.map((rule, index) => ({ index, fields: unmatchedBy(rule) })),
  ].filter((rule) => rule.fields.length > 0);
  return { summary: summaryOf(stored), unmatched };
}
