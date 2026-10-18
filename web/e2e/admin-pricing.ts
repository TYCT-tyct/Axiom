import type { AdminState, Schemas } from "./admin-data.ts";
import { uuid } from "./admin-data.ts";
import { base, type Ctx, changes, reads, route, safe } from "./admin-route.ts";
import { fail, type Handler, ok } from "./mock.ts";

// Pricing rules and their preview. A rule is changed as of the revision the caller read, and two rules with the same
// target and audience cannot both exist. A preview prices models from their reference price with a deliberately simple
// stand-in for the server's rules: a cost of 80% of the reference, and a sale of the cost plus the rule's markup, so a
// test sees a rule change the numbers without the mock being a pricing engine.

type Rule = Schemas["PricingRule"];

// What the server refuses of a rule in M1 (billing's RuleInput.Check): a target that is not a model, an author or all,
// cost plus, and a multiplier that is not at face value, since each would be stored and never applied.
const honoured = new Set<string>(["model", "author", "all"]);
function refused(rule: Schemas["PricingRuleInput"]) {
  const { method } = rule;
  return (
    !honoured.has(rule.target.kind) ||
    method.kind === "cost_plus" ||
    (method.kind === "reference_multiplier" && method.currency_conversion !== "face")
  );
}

const same = (a: Rule, b: Rule["target"], c: Rule["audience"]) =>
  a.target.kind === b.kind && a.target.id === b.id && a.audience.kind === c.kind && a.audience.id === c.id;

function createRule({ body, state }: Ctx<"/api/admin/v1/pricing/rules", "post">) {
  const { rule } = body;
  if (refused(rule)) {
    return fail(422, "unprocessable");
  }
  if (state.rules.some((r) => same(r, rule.target, rule.audience))) {
    return fail(409, "conflict");
  }
  const made: Rule = { id: uuid(7, state.rules.length + 10), rev: 0, updated_at: new Date().toISOString(), ...rule };
  state.rules.push(made);
  return { status: 201, json: made } as const;
}

function updateRule({ body, params, state }: Ctx<"/api/admin/v1/pricing/rules/{rule_id}", "put">) {
  const index = state.rules.findIndex((r) => r.id === params.rule_id);
  const current = state.rules[index];
  if (!current) {
    return fail(404, "not_found");
  }
  if (current.rev !== body.rev) {
    return fail(409, "stale_revision");
  }
  if (refused(body.rule)) {
    return fail(422, "unprocessable");
  }
  const changed: Rule = { ...current, ...body.rule, rev: current.rev + 1, updated_at: new Date().toISOString() };
  state.rules[index] = changed;
  return ok(changed);
}

// Decimal text times a ratio, kept exact by working in billionths of a billionth.
const scale = 10n ** 15n;
function mul(price: string, ratio: string) {
  const [whole = "0", fraction = ""] = price.split(".");
  const [rw = "0", rf = ""] = ratio.split(".");
  const a = BigInt(whole + fraction.padEnd(15, "0").slice(0, 15));
  const b = BigInt(rw + rf.padEnd(15, "0").slice(0, 15));
  const product = (a * b) / scale;
  const digits = product.toString().padStart(16, "0");
  return `${digits.slice(0, -15)}.${digits.slice(-15)}`.replace(/\.?0+$/, "");
}

function priced(model: Schemas["Model"], markup: string) {
  const reference = model.prices.find((p) => p.schedule)?.schedule;
  if (!reference) {
    return { model_id: model.id, slug: model.slug, status: "unpriced" } as const;
  }
  const rates = (ratio: string) =>
    Object.fromEntries(Object.entries(reference.rates).map(([item, price]) => [item, mul(price, ratio)]));
  return {
    model_id: model.id,
    slug: model.slug,
    endpoint_id: uuid(9, Number(model.id.slice(-4))),
    status: "priced",
    sale: { ...reference, rates: rates(`${0.8 * (1 + Number(markup))}`) },
    cost: { ...reference, rates: rates("0.8") },
    margin_ratio: markup === "0" ? "0" : (Number(markup) / (1 + Number(markup))).toFixed(4),
  } as const;
}

// The markup the preview applies: the added or replacing rule's when given, else the first enabled rule's.
function markupOf(state: AdminState, rule: Schemas["PricingRuleInput"] | undefined) {
  const method = (rule ?? state.rules.find((r) => r.enabled))?.method;
  const multiplier = method?.multiplier ? String(Number(method.multiplier) - 1) : undefined;
  return method?.markup_ratio ?? method?.margin_ratio ?? multiplier ?? "0.25";
}

function preview({ body, state }: Ctx<"/api/admin/v1/pricing/previews", "post">) {
  if (body.tier_id && body.account_id) {
    return fail(400, "invalid_request");
  }
  const models = body.model_ids.map((id) => state.models.find((m) => m.id === id));
  if (models.some((m) => !m)) {
    return fail(404, "not_found");
  }
  const markup = markupOf(state, body.rule);
  return ok({ data: models.map((m) => priced(m as Schemas["Model"], markup)) });
}

export const pricingHandlers: Handler<AdminState>[] = [
  route("get", `${base}/pricing/rules`, reads("pricing.view"), ({ state }) => ok({ data: state.rules })),
  route("post", `${base}/pricing/rules`, changes("pricing.manage"), createRule),
  route("put", `${base}/pricing/rules/{rule_id}`, changes("pricing.manage"), updateRule),
  route("post", `${base}/pricing/previews`, safe("pricing.view"), preview),
];
