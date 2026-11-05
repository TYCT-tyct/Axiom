import type { Schemas } from "@axiom/api";

// A routing preset's rules as the editor holds them, and back (docs/domains/routing.md「规则内容」). The editor writes
// what an account's preset may hold: the sort, the source classes and data policies allowed, the verified threshold
// and the highest unit prices. Every member it does not edit (a member a newer node added, the platform's
// `advanced` on a copy's source) is kept as it was, so saving never drops a limit the page cannot show; the server
// checks the whole.

export const sorts = ["balanced", "price", "throughput", "latency", "quality"] as const;
export type Sort = (typeof sorts)[number];
export const policies = ["no_retention", "retained", "may_train"] as const;
export const priceItems = ["prompt", "completion", "request"] as const;
type PriceItem = (typeof priceItems)[number];

type Json = Record<string, unknown>;

// One rule. An empty text or a null list writes nothing: on the default rule that is no limit (and the balanced sort),
// on an override it is what the rule inherits.
export type RuleDraft = {
  sort: string;
  classes: string[] | null;
  policies: string[] | null;
  verifiedOnly: boolean;
  // The rule asks for a product score. Scoring is not live (docs/domains/routing.md「产品质量与来源类别」), so no
  // endpoint has one and such a rule matches nothing: the editor offers no score, only removing one it read.
  scored: boolean;
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

// The editor's fields a client check can refuse: the rule (`default` or the override's index) and the field. Besides a
// value it cannot read, it refuses a condition that excludes every endpoint, since such a preset fails every call.
export type Invalid = { rule: "default" | number; field: "name" | "classes" | "policies" | "score" | PriceItem };

const ruleMembers = ["sort", "classes", "policies", "verified_only", "min_score", "min_score_confidence", "max_price"];
// The shape of a price the server reads (priceNumber in internal/routing/price.go): digits, an optional fraction and an
// optional exponent of at most three digits. The value is sent as it was typed, as text, never through a float; the
// server alone checks the bounds and precision, so a price it once accepted always passes here.
const decimal = /^[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]{1,3})?$/;

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
    scored: false,
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
    // The server reads null as no threshold (a nil pointer in internal/routing/rules.go), so only a value asks for one.
    scored: rule.min_score != null || rule.min_score_confidence != null,
    maxPrice: { prompt: text(price.prompt), completion: text(price.completion), request: text(price.request) },
    rest: without(rule, [...ruleMembers, "author", "model"]),
  };
}

// draftOf reads stored rules. Without a copy's run-time settings: `advanced` is the platform's and an account's preset
// may not hold it, so a copy of a platform preset leaves it out (docs/domains/routing.md「默认链与 key 落章」).
export function draftOf(rules: Schemas["RoutingRules"] | undefined): RulesDraft {
  const source = object(rules);
  const overrides = Array.isArray(source.rules) ? source.rules : [];
  return {
    base: ruleDraft(source.default),
    overrides: overrides.map((value) => {
      const rule = object(value);
      const target = typeof rule.model === "string" ? "model" : "author";
      return { ...ruleDraft(rule), key: keys++, target, name: text(rule[target]) };
    }),
    rest: without(source, ["default", "rules", "advanced"]),
  };
}

type Field = Invalid["field"];

// excludedBy names the rule's condition that no endpoint can meet today, if any. An empty list allows nothing. Every
// endpoint is recorded as "may train" until suppliers record their data policies (「产品质量与来源类别」), so a
// policy list without it matches nothing; this check goes when they do.
function excludedBy(draft: RuleDraft): Field | null {
  if (draft.classes?.length === 0) {
    return "classes";
  }
  if (draft.policies && !draft.policies.includes("may_train")) {
    return "policies";
  }
  return draft.scored ? "score" : null;
}

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
  return excludedBy(draft) ?? limitsOf(draft, out) ?? out;
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

// The few facts of a preset a list shows: the default rule's sort, the classes it keeps, whether any rule limits prices,
// and how many overrides it has.
export function summaryOf(rules: Schemas["RoutingRules"]) {
  const { base, overrides } = draftOf(rules);
  return {
    sort: base.sort || "balanced",
    classes: base.classes,
    priced: [base, ...overrides].some((rule) => priceItems.some((item) => rule.maxPrice[item] !== "")),
    verifiedOnly: base.verifiedOnly,
    overrides: overrides.length,
  };
}
