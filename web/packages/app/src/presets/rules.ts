import type { Schemas } from "@axiom/api";

// A routing preset's rules as the editor holds them, and back (docs/domains/routing.md「规则内容」). The editor writes
// what every layer's preset may hold: the sort, the source classes and data policies allowed, the verified threshold
// and the highest unit prices. Every member it does not edit (a member a newer node added, a platform preset's
// `advanced`) is kept as it was, so saving never drops a limit the page cannot show; the server checks the whole.

// The system preset: the allow-all every layer falls back to and a key without a preset routes by. Its ID is the zero
// UUID, and nobody edits, disables or chooses it.
export const systemPreset = "00000000-0000-0000-0000-000000000000";

// The values the editor offers, in its order. The contract's enums type them, so a value it drops fails the typecheck;
// one it adds is offered once it is listed here, and a stored one the page does not list is kept as it is.
export const sorts = [
  "balanced",
  "price",
  "throughput",
  "latency",
  "quality",
] as const satisfies Schemas["RoutingSort"][];
export type Sort = (typeof sorts)[number];
export const policies = ["no_retention", "retained", "may_train"] as const satisfies NonNullable<
  Schemas["RoutingRule"]["policies"]
>;
export const priceItems = ["prompt", "completion", "request"] as const satisfies (keyof Schemas["RoutingPriceLimit"])[];
type PriceItem = (typeof priceItems)[number];

type Json = Record<string, unknown>;

// One rule. An empty text or a null list writes nothing: on the default rule that is no limit (and the balanced sort),
// on an override it is what the rule inherits.
export type RuleDraft = {
  sort: string;
  classes: string[] | null;
  policies: string[] | null;
  verifiedOnly: boolean;
  maxPrice: Record<PriceItem, string>;
  rest: Json;
};

// key tells overrides apart while they are edited; it is not part of the rules.
export type OverrideDraft = RuleDraft & { key: number; target: "author" | "model"; name: string };

let keys = 0;

export function newOverride(): OverrideDraft {
  return { ...emptyRule(), key: keys++, target: "author", name: "" };
}

export type RulesDraft = { base: RuleDraft; overrides: OverrideDraft[]; rest: Json };

// The editor's fields a save is refused for: the rule (`default` or the override's index) and the field, or `rule` for
// the rule as a whole. The page refuses a value it cannot write; the server's check (checkRoutingRules) refuses a rule
// that keeps no endpoint, since such a preset fails every call.
export type Invalid = {
  rule: "default" | number;
  field: "name" | "classes" | "policies" | "score" | "verified" | "rule" | PriceItem;
};

const ruleMembers = ["sort", "classes", "policies", "verified_only", "max_price"];
// The product score a rule asks for is kept in rest, as read: scoring is not live (docs/domains/routing.md「产品质量与
// 来源类别」), so the editor offers no score, only removing one it read.
const scoreMembers = ["min_score", "min_score_confidence"];

// scored tells whether the rule asks for a product score. The server reads null as no threshold (a nil pointer in
// internal/routing/rules.go), so only a value asks for one.
export function scored(rule: RuleDraft) {
  return scoreMembers.some((member) => rule.rest[member] != null);
}

export function withoutScore<Rule extends RuleDraft>(rule: Rule): Rule {
  return { ...rule, rest: without(rule.rest, scoreMembers) };
}
// The shape of a price the server reads (priceNumber in internal/routing/price.go): digits, an optional fraction and an
// optional exponent of at most three digits. The value is sent as it was typed, as text, never through a float; the
// server alone checks the bounds and precision, so a price it once accepted always passes here.
export const decimal = /^[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]{1,3})?$/;

function object(value: unknown): Json {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? (value as Json) : {};
}

function without(source: Json, members: readonly string[]): Json {
  return Object.fromEntries(Object.entries(source).filter(([key]) => !members.includes(key)));
}

function texts(value: unknown): string[] | null {
  return Array.isArray(value) && !value.includes("*") ? value.filter((v): v is string => typeof v === "string") : null;
}

function text(value: unknown) {
  return typeof value === "string" || typeof value === "number" ? String(value) : "";
}

function emptyRule(): RuleDraft {
  return {
    sort: "",
    classes: null,
    policies: null,
    verifiedOnly: false,
    maxPrice: { prompt: "", completion: "", request: "" },
    rest: {},
  };
}

function ruleDraft(value: unknown): RuleDraft {
  const rule = object(value);
  const price = object(rule.max_price);
  return {
    sort: typeof rule.sort === "string" ? rule.sort : "",
    classes: texts(rule.classes),
    policies: texts(rule.policies),
    verifiedOnly: rule.verified_only === true,
    maxPrice: { prompt: text(price.prompt), completion: text(price.completion), request: text(price.request) },
    // A null score is no threshold to the server, so it is left out rather than kept.
    rest: without(rule, [...ruleMembers, "author", "model", ...scoreMembers.filter((member) => rule[member] == null)]),
  };
}

// draftOf reads stored rules. A copy leaves out the run-time settings: `advanced` is the platform's and an account's
// preset may not hold it (docs/domains/routing.md「默认链与 key 落章」). An edit keeps them, so the admin saving a
// platform preset never drops what the editor cannot show.
export function draftOf(rules: Schemas["RoutingRules"] | undefined, copy = false): RulesDraft {
  const source = object(rules);
  const overrides = Array.isArray(source.rules) ? source.rules : [];
  return {
    base: ruleDraft(source.default),
    overrides: overrides.map((value) => {
      const rule = object(value);
      const target = typeof rule.model === "string" ? "model" : "author";
      return { ...ruleDraft(rule), key: keys++, target, name: text(rule[target]) };
    }),
    rest: without(source, copy ? ["default", "rules", "advanced"] : ["default", "rules"]),
  };
}

type Field = Invalid["field"];

// limitsOf writes the rule's price limits into out, or names the first field a client can tell is wrong. The server
// holds the rest: that an override only narrows, and the prices' precision.
function limitsOf(draft: RuleDraft, out: Json): Field | null {
  const price: Json = {};
  for (const item of priceItems) {
    const value = draft.maxPrice[item].trim();
    if (value !== "" && !decimal.test(value)) {
      return item;
    }
    if (value !== "") {
      price[item] = value;
    }
  }
  if (Object.keys(price).length > 0) {
    out.max_price = price;
  }
  return null;
}

// ruleOf writes one rule over the members it keeps as they were, or names its first field that is wrong.
function ruleOf(draft: RuleDraft): Json | Field {
  const out: Json = { ...draft.rest };
  const set = (key: string, value: unknown) => {
    if (value !== undefined) {
      out[key] = value;
    }
  };
  set("sort", draft.sort || undefined);
  set("classes", draft.classes ?? undefined);
  set("policies", draft.policies ?? undefined);
  set("verified_only", draft.verifiedOnly || undefined);
  // An empty list allows nothing, which the server refuses as a mistake rather than a rule.
  if (draft.classes?.length === 0) {
    return "classes";
  }
  return draft.policies?.length === 0 ? "policies" : (limitsOf(draft, out) ?? out);
}

// rulesOf writes the draft as the rules the API takes, or the first field it refuses.
export function rulesOf(draft: RulesDraft): { rules: Schemas["RoutingRules"] } | { invalid: Invalid } {
  const base = ruleOf(draft.base);
  if (typeof base === "string") {
    return { invalid: { rule: "default", field: base } };
  }
  const rules: Json[] = [];
  for (const [index, override] of draft.overrides.entries()) {
    const rule = ruleOf(override);
    if (override.name.trim() === "") {
      return { invalid: { rule: index, field: "name" } };
    }
    if (typeof rule === "string") {
      return { invalid: { rule: index, field: rule } };
    }
    rules.push({ [override.target]: override.name.trim(), ...rule });
  }
  const out: Json = { ...draft.rest, default: base };
  if (rules.length > 0) {
    out.rules = rules;
  }
  // The contract types the rules as an opaque object (x-go-type jsonx.Raw); the server reads and checks them whole.
  return { rules: out as Schemas["RoutingRules"] };
}

// priced tells whether any rule of the draft limits a price, which decides whether a platform preset names a currency.
export function priced(draft: RulesDraft) {
  return [draft.base, ...draft.overrides].some((rule) => priceItems.some((item) => rule.maxPrice[item].trim() !== ""));
}

type Unmatched = Schemas["RoutingRuleUnmatched"];

// The editor's field for each condition the server names; both score conditions are the one score notice.
const unmatchedFields: Record<Unmatched["fields"][number], Field> = {
  classes: "classes",
  policies: "policies",
  min_score: "score",
  min_score_confidence: "score",
  verified_only: "verified",
  "max_price.prompt": "prompt",
  "max_price.completion": "completion",
  "max_price.request": "request",
};

// invalidOf is the first rule the server's check says keeps no endpoint, as the editor marks it: the first condition
// it names, or the rule as a whole when it names none (only the conditions together, or what the rule inherits, keep
// none) or one this page does not know.
export function invalidOf(check: { unmatched: Unmatched[] }): Invalid | null {
  const first = check.unmatched[0];
  if (!first) {
    return null;
  }
  const named = first.fields[0];
  return { rule: first.index ?? "default", field: (named && unmatchedFields[named]) || "rule" };
}
