import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { confirmation, readBase, takesBases, targetOf } from "./base-price";

const invalid = { currency: "currency", rate: "rate", none: "none", evidence: "evidence" };
const model = { group_id: "g", upstream_name: "claude-x", model_id: "m" };

it.each<{ name: string; values: Record<string, string>; body: unknown }>([
  {
    name: "the official price sends no price, so the server takes the catalog's; each names the model shown",
    values: { source: "official", rate_input: "9" },
    body: { items: [model], reason: "r" },
  },
  {
    name: "entered prices are sent per unit, in the settlement currency, only for the items typed",
    values: { source: "entered", currency: "EUR", rate_input: "3", rate_output: "15", rate_web_search: "0.01" },
    body: {
      items: [
        {
          ...model,
          price: { v: 1, currency: "USD", rates: { input: "0.000003", output: "0.000015", web_search: "0.01" } },
        },
      ],
      reason: "r",
    },
  },
  {
    name: "a price per use of exactly 1000 is the contract's highest and is sent",
    values: { source: "entered", rate_web_search: "1000" },
    body: { items: [{ ...model, price: { v: 1, currency: "USD", rates: { web_search: "1000" } } }], reason: "r" },
  },
])("$name", ({ values, body }) => {
  const choice = readBase(values, "USD", invalid);
  if ("problem" in choice) throw new Error(choice.problem);
  expect(confirmation(model, choice, "r")).toEqual(body);
});

it.each<[Record<string, string>, string]>([
  [{ source: "entered" }, "none"],
  [{ source: "entered", rate_input: "3,5" }, "rate"],
  [{ source: "entered", rate_web_search: "1000.5" }, "rate"],
])("an entered price that cannot be sent says why: %o", (values, problem) => {
  expect(readBase(values, "USD", invalid)).toEqual({ problem });
});

const endpoint = (over: Partial<AdminSchemas["SupplierEndpoint"]> = {}): AdminSchemas["SupplierEndpoint"] => ({
  id: "e",
  group_id: "g",
  model_id: "a",
  upstream_name: "chat",
  formats: ["chat"],
  cost_state: "unknown",
  cost_reason: "unpriced",
  enabled: true,
  ...over,
});
const row = (
  model_id: string | undefined,
  cost_reason: AdminSchemas["SupplierCostReason"] | undefined = "unpriced",
): AdminSchemas["SupplierModel"] => ({
  group_id: "g",
  upstream_name: "chat",
  formats: ["chat"],
  pile: "listed",
  first_seen_at: "2026-11-20T08:00:00Z",
  model_id,
  model_slug: model_id ? `vendor/${model_id}` : undefined,
  cost_reason,
});

it.each([
  { name: "the name maps to the endpoint's model still", at: endpoint(), now: row("a"), offered: true },
  { name: "the name maps to another model since", at: endpoint(), now: row("b"), offered: false },
  { name: "the listing has no such name (or not loaded yet)", at: endpoint(), now: undefined, offered: false },
  {
    name: "the cost is known",
    at: endpoint({ cost_state: "known", cost_reason: undefined }),
    now: row("a", undefined),
    offered: false,
  },
  ...(["unexpressible", "over_limit", "under_estimated"] as const).map((reason) => ({
    name: `the cost is unknown as ${reason}`,
    at: endpoint({ cost_reason: reason }),
    now: row("a", reason),
    offered: false,
  })),
])(
  "an endpoint offers a base only while unpriced, for the model its name maps to now: $name",
  ({ at, now, offered }) => {
    const target = targetOf(at, () => now);
    expect(target).toEqual(
      offered ? { group_id: "g", upstream_name: "chat", model_id: "a", model_slug: "vendor/a" } : undefined,
    );
  },
);

it.each([
  ["sub2api", "relay", true],
  ["cpa", "relay", true],
  // new-api may publish a price the table cannot express: cost_reason tells such a name from an unpriced one.
  ["newapi", "relay", true],
  ["openai", "direct", false],
] as const)("%s (%s) is offered base prices: %s", (kind, mode, offered) => {
  expect(takesBases({ kind, mode } as AdminSchemas["Supplier"])).toBe(offered);
});
