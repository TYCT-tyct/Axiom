import { describe, expect, it } from "vitest";
import { multiplierOf, percentOf } from "./markup";

describe("a tier's markup", () => {
  it.each([
    ["20", "1.2"],
    ["0", "1"],
    ["-10", "0.9"],
    ["12.5", "1.125"],
    ["0.01", "1.0001"],
    ["-99.99", "0.0001"],
    [" 300 ", "4"],
  ])("pays %s%% as the multiplier %s, and reads it back", (typed, multiplier) => {
    expect(multiplierOf(typed)).toBe(multiplier);
    expect(percentOf(multiplier)).toBe(typed.trim());
  });

  it.each(["", "abc", "1.234", "-100", "-150", "12345", "1e2"])("refuses %j", (typed) => {
    expect(multiplierOf(typed)).toBeNull();
  });

  it("reads a multiplier with more places than a person types", () => {
    expect(percentOf("1.123456")).toBe("12.3456");
    expect(percentOf("0.5")).toBe("-50");
  });
});
