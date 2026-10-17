import type { AdminSchemas } from "@axiom/api";

type Model = AdminSchemas["Model"];

export type Check = { key: "declares" | "public" | "priced" | "covers"; ok: boolean };

// What the review gate asks of a draft before it goes on sale, read off the model as the page has it: it declares what
// an upstream may report, its standard ID is a public name, it has a reference price, and every current price prices
// everything it declares. The server decides again when the model is put on sale; this tells the person what is missing
// first.
export function readiness(model: Model): Check[] {
  const { items, tiers } = model.spec;
  const current = model.prices.filter((price) => price.schedule !== undefined);
  return [
    { key: "declares", ok: Array.isArray(items) && Array.isArray(tiers) },
    { key: "public", ok: model.names.some((name) => name.name === model.slug && name.visibility === "public") },
    { key: "priced", ok: current.length > 0 },
    {
      key: "covers",
      ok:
        current.length > 0 &&
        current.every((price) => (items ?? []).every((item) => price.schedule?.rates[item] !== undefined)),
    },
  ];
}
