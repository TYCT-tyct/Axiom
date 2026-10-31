import { expect, it } from "vitest";
import { isRedeemRequest, readCode } from "./redeem";

it("reads a code as typed", () => {
  expect(readCode({ code: " AAAAA-BBBBB-CCCCC-DDDDD " })).toEqual({ code: "AAAAA-BBBBB-CCCCC-DDDDD" });
});

it.each([
  [{}, "missing"],
  [{ code: "   " }, "missing"],
  [{ code: "x".repeat(65) }, "long"],
])("refuses %o", (values, problem) => {
  expect(readCode(values)).toBe(problem);
});

it("recognises a kept request", () => {
  expect(isRedeemRequest({ code: "x" })).toBe(true);
  expect(isRedeemRequest({ code: 1 })).toBe(false);
  expect(isRedeemRequest(null)).toBe(false);
});
