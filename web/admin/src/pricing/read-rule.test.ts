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

it("retires legacy rules unchanged without needing to validate unsupported fields", () => {
  const existing = {
    id: "historical-rule",
    rev: 7,
    enabled: true,
    target: { kind: "endpoint" as const, id: "0198c0a8-0000-7000-8000-000000000001" },
    audience: { kind: "all" as const },
    method: { kind: "reference_multiplier" as const, multiplier: "2", currency_conversion: "exchange" as const },
    constraints: { min_margin_ratio: "0.2" },
  };
  expect(readRule({}, words, existing)).toEqual({
    enabled: false,
    target: existing.target,
    audience: existing.audience,
    method: existing.method,
    constraints: existing.constraints,
  });
});
