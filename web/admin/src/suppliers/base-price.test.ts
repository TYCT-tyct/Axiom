import { expect, it } from "vitest";
import { confirmation, readBase, scaled } from "./base-price";

const invalid = { currency: "currency", rate: "rate", none: "none", evidence: "evidence" };
const model = { group_id: "g", upstream_name: "claude-x" };

it.each<{ name: string; values: Record<string, string>; body: unknown }>([
  {
    name: "the official price sends no price, so the server takes the catalog's",
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
])("$name", ({ values, body }) => {
  const choice = readBase(values, "USD", invalid);
  if ("problem" in choice) throw new Error(choice.problem);
  expect(confirmation(model, choice, "r")).toEqual(body);
});

it.each<[Record<string, string>, string]>([
  [{ source: "entered" }, "none"],
  [{ source: "entered", rate_input: "3,5" }, "rate"],
])("an entered price that cannot be sent says why: %o", (values, problem) => {
  expect(readBase(values, "USD", invalid)).toEqual({ problem });
});

it("the cost in a group is the base times its ratio, exactly", () => {
  expect(scaled({ v: 1, currency: "USD", rates: { input: "0.000003", output: "0.1" } }, "1.37").rates).toEqual({
    input: "0.00000411",
    output: "0.137",
  });
});
