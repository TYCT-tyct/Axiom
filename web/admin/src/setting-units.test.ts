import { expect, it } from "vitest";
import { numberPatch, numberText, numberValid } from "./setting-fields";
import { countOf, inUnit, scaledOf, scaledText, secondsOf } from "./setting-units";

// A stored number is written in the unit of its row and read back to the same whole number, whatever the decimals the
// unit needs: a row that is not touched must not change the value the server holds.
it.each([
  { stored: 172_800, unit: "day", text: "2" },
  { stored: 1_800, unit: "day", text: "0.020833" },
  { stored: 300, unit: "hour", text: "0.083333" },
  { stored: 7_200, unit: "minute", text: "120" },
] as const)("$stored seconds is $text in $unit and reads back", ({ stored, unit, text }) => {
  expect(inUnit(stored, unit)).toBe(text);
  expect(secondsOf(text, unit)).toBe(stored);
});

it("reads only plain decimals", () => {
  expect([secondsOf("1.5", "hour"), secondsOf("-1", "day"), secondsOf("1e3", "day"), secondsOf("", "day")]).toEqual([
    5_400,
    undefined,
    undefined,
    undefined,
  ]);
  expect([countOf(" 12 "), countOf("1.5"), countOf("9".repeat(20))]).toEqual([12, undefined, undefined]);
});

// Basis points are edited as percentages with at most two decimals.
it("scales a percentage to basis points and back", () => {
  expect([scaledOf("12.5", 100), scaledOf("12.345", 100), scaledText(1250, 100), scaledText(1, 100)]).toEqual([
    1250,
    undefined,
    "12.5",
    "0.01",
  ]);
});

const days = { name: "console_idle_timeout_seconds", min: 300, max: 2_592_000, unit: "day" } as const;
const count = { name: "max_keys", min: 1, max: 10_000 } as const;

it("checks a row against the contract's bounds in its own unit, and lets an optional one be empty", () => {
  expect([numberValid(days, "30", true), numberValid(days, "31", true), numberValid(days, "0.001", true)]).toEqual([
    true,
    false,
    false,
  ]);
  expect([numberValid(count, "", true), numberValid(count, "", false), numberValid(count, "0", true)]).toEqual([
    true,
    false,
    false,
  ]);
  expect(numberText(days, undefined)).toBe("");
});

// Only the rows whose text changed are in the patch, in the API's units; a row emptied goes back to its default.
it("patches only what changed", () => {
  const specs = { a: days, b: count };
  const saved = { a: "7", b: "20" };
  expect(numberPatch(specs, { a: "7", b: "30" }, saved)).toEqual({ max_keys: 30 });
  expect(numberPatch(specs, { a: "", b: "20" }, saved)).toEqual({ console_idle_timeout_seconds: undefined });
  expect(numberPatch(specs, { a: "1", b: "20" }, saved)).toEqual({ console_idle_timeout_seconds: 86_400 });
});
