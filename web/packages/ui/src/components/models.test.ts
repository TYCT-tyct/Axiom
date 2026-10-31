import { describe, expect, it } from "vitest";
import { baseRate, perMillionBillionths } from "./models";

// A per-token price becomes billionths per million tokens by moving the decimal point six places in text. The page
// never divides a price as a float: 0.000001 per token is exactly 1.00 per million, not 0.9999999999999999.
it.each([
  { case: "zero", amount: "0", want: "0" },
  { case: "one millionth", amount: "0.000001", want: "1000000000" },
  { case: "one billionth", amount: "0.000000001", want: "1000000" },
  { case: "fifteen places", amount: "0.000000000000001", want: "1" },
  { case: "a decimal unit", amount: "1.5", want: "1500000000000000" },
  { case: "leading zeros", amount: "00.5", want: "500000000000000" },
])("$case", ({ amount, want }) => {
  expect(perMillionBillionths(amount)).toBe(want);
});

it("refuses a price with more places than a UnitPrice can hold", () => {
  expect(perMillionBillionths("0.0000000000000001")).toBeNull();
});

describe("baseRate", () => {
  it("takes the base band, not an explicit threshold of zero", () => {
    const rates = [
      { service_tier: "standard", above: 0, item: "input", unit: "token", amount: "2" },
      { service_tier: "standard", item: "input", unit: "token", amount: "1" },
    ];
    expect(baseRate(rates, "input")?.amount).toBe("1");
  });
});
