import { expect, it } from "vitest";
import { unsettled } from "./unsettled";

const request = () => Promise.resolve();

it("a late answer of an earlier try neither holds nor drops what a later try left", () => {
  const scope = "u/1/credit-grant:a";
  // try A is sent again, the dialog is closed and A given up, and try B is sent and left unsettled
  unsettled.own(scope, "A");
  unsettled.drop(scope);
  unsettled.own(scope, "B");
  unsettled.hold(scope, { key: "B", request });
  // A's answer arrives late
  unsettled.drop(scope, "A");
  unsettled.hold(scope, { key: "A", request });
  expect(unsettled.get(scope)?.key).toBe("B");
  unsettled.drop(scope, "B");
  expect(unsettled.get(scope)).toBeUndefined();
});

it("an answer after the person gave up holds nothing", () => {
  const scope = "u/1/credit-grant:b";
  unsettled.own(scope, "A");
  unsettled.drop(scope);
  unsettled.hold(scope, { key: "A", request });
  expect(unsettled.get(scope)).toBeUndefined();
});
