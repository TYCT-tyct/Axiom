import { expect, it } from "vitest";
import { revenueMove } from "./summary-strip";

it.each([
  ["1500000000", "1000000000", 50],
  ["500000000", "1000000000", -50],
  ["1000000000", "3000000000", -66.6],
  ["9007199254740993", "9007199254740993", 0],
  ["1000000000", "0", undefined],
])("moves this month's revenue %s from last month's %s by %s percent", (revenue, previous, move) => {
  expect(revenueMove(revenue, previous)).toBe(move);
});
