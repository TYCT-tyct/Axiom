import { expect, it } from "vitest";
import { add, move, orderOf, remove, same, withOrder } from "./line-order";

it("reads the order of a preset that has one and none from one that has not", () => {
  expect(orderOf({ default: {}, advanced: { order: ["g1", "g2"] } })).toEqual(["g1", "g2"]);
  expect(orderOf({ default: {} })).toEqual([]);
  expect(orderOf({ default: {}, advanced: { order: null } })).toEqual([]);
});

it("writes the order back over everything else the rules and their run-time settings hold", () => {
  const rules = {
    default: { classes: ["official"] },
    advanced: { order: ["g1"], no_fallbacks: true, max_attempts: 2 },
  };
  expect(withOrder(rules, ["g2", "g1"])).toEqual({
    default: { classes: ["official"] },
    advanced: { order: ["g2", "g1"], no_fallbacks: true, max_attempts: 2 },
  });
  expect(rules.advanced.order).toEqual(["g1"]);
});

it.each([
  ["moves a line up", move(["a", "b", "c"], 1, -1), ["b", "a", "c"]],
  ["moves a line down", move(["a", "b", "c"], 1, 1), ["a", "c", "b"]],
  ["leaves the first line where it is when moved up", move(["a", "b"], 0, -1), ["a", "b"]],
  ["leaves the last line where it is when moved down", move(["a", "b"], 1, 1), ["a", "b"]],
  ["removes a line", remove(["a", "b"], 0), ["b"]],
  ["keeps the last line", remove(["a"], 0), ["a"]],
  ["adds a line at the end", add(["a"], "b"), ["a", "b"]],
  ["adds a line once", add(["a", "b"], "a"), ["a", "b"]],
])("%s", (_, got, want) => {
  expect(got).toEqual(want);
});

it("tells a changed order from the saved one", () => {
  expect(same(["a", "b"], ["a", "b"])).toBe(true);
  expect(same(["a", "b"], ["b", "a"])).toBe(false);
  expect(same(["a"], ["a", "b"])).toBe(false);
});
