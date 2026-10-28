import { expect, it } from "vitest";
import { isProblem } from "../change";
import { readRule } from "./read-rule";

const words = {
  target: "t",
  audience: "a",
  ratio: "r",
  multiplier: "m",
  currency: "c",
  rate: "rate",
  none: "n",
  evidence: "e",
};

it("never sends constraints, even when the form carries a limit", () => {
  const rule = readRule(
    {
      enabled: "on",
      target_kind: "all",
      audience_kind: "all",
      method_kind: "reference_multiplier",
      multiplier: "1.2",
      currency_conversion: "face",
      min_margin_ratio: "0.1",
      reference_max_ratio: "3",
    },
    words,
  );
  expect(isProblem(rule)).toBe(false);
  expect(isProblem(rule) ? null : rule.constraints).toEqual({});
});
