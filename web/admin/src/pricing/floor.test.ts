import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { belowFloor, lower, raises } from "./floor";

type Schedule = AdminSchemas["PriceSchedule"];
const list = (rates: Schedule["rates"]): Schedule => ({ v: 1, currency: "USD", rates });

it.each([
  ["0.000003", "0.000004", true],
  ["0.000004", "0.000004", false],
  ["0.000005", "0.000004", false],
  ["2", "10", true],
  ["0.1", "0.10", false],
  ["0.000000000000001", "0.000000000000002", true],
  ["1000", "999.999999999999999", false],
] as const)("%s is lower than %s: %s", (price, than, want) => {
  expect(lower(price, than)).toBe(want);
});

it("calls a rule's price below the floor when any item it prices is under the floor's", () => {
  const floor = list({ input: "0.000003", output: "0.000015" });
  expect(belowFloor({ requested: list({ input: "0.000003", output: "0.000014" }), floor })).toBe(true);
  expect(belowFloor({ requested: list({ input: "0.000004", output: "0.000015" }), floor })).toBe(false);
});

it("has no floor for an item the floor does not price, and none without a floor", () => {
  expect(
    belowFloor({ requested: list({ input: "0.000001", cache_read: "0.1" }), floor: list({ input: "0.000001" }) }),
  ).toBe(false);
  expect(belowFloor({ requested: list({ input: "0.000001" }) })).toBe(false);
});

it("weighs each endpoint of a proposal against its own floor", () => {
  const requested_endpoints = { a: list({ input: "0.000002" }), b: list({ input: "0.000002" }) };
  expect(
    belowFloor({ requested_endpoints, floors: { a: list({ input: "0.000002" }), b: list({ input: "0.000003" }) } }),
  ).toBe(true);
  expect(
    belowFloor({ requested_endpoints, floors: { a: list({ input: "0.000002" }), b: list({ input: "0.000002" }) } }),
  ).toBe(false);
});

it("says a price raises the current one when an item both price goes up", () => {
  const current = list({ input: "0.000003", output: "0.000015" });
  expect(raises(list({ input: "0.000004", output: "0.000015" }), current)).toBe(true);
  expect(raises(list({ input: "0.000002", output: "0.000015" }), current)).toBe(false);
  expect(raises(list({ input: "0.000004" }), undefined)).toBe(false);
});
