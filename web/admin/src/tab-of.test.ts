import { expect, it } from "vitest";
import { tabOf } from "./tab-of";

// The first tab is the page's own address and carries no `tab`, so it is not among the ones a tab names.
it.each([
  ["rules", "rules"],
  ["policy", "policy"],
  ["prices", undefined],
  ["nothing", undefined],
  ["", undefined],
  [undefined, undefined],
  [["rules"], undefined],
])("reads a tab of %j as %j", (value, want) => {
  expect(tabOf(value, ["rules", "rates", "policy"] as const)).toBe(want);
});
