import { describe, expect, it } from "vitest";
import { scaled, scaledLcp } from "./vitals.ts";

describe("scaled", () => {
  it.each([
    // At the reference host's speed a pair is the page's own LCP, the budget the document states.
    { name: "the reference host", pair: { base: 948, page: 1200 }, reference: 948, want: 1200 },
    // A host twice as slow stretches the reference page and the page under test alike: the budget stays the page's.
    { name: "a host twice as slow", pair: { base: 1896, page: 2400 }, reference: 948, want: 1200 },
    // A faster host is not charged for the reference page being fast: the page keeps its full budget.
    { name: "a host twice as fast", pair: { base: 474, page: 1200 }, reference: 948, want: 1200 },
  ])("$name", ({ pair, reference, want }) => {
    expect(scaled(pair, reference)).toBe(want);
  });
});

describe("scaledLcp", () => {
  // A burst of another job's work is one slow pair; the median of three drops it.
  it("drops a burst that landed in one pair", () => {
    const pairs = [
      { base: 948, page: 1000 },
      { base: 948, page: 1800 },
      { base: 948, page: 1000 },
    ];
    expect(scaledLcp(pairs, 948)).toBe(1000);
  });

  // A page that got slower raises all three pairs, so the median does not drop it.
  it("keeps a page that got slower in every pair", () => {
    const pairs = [
      { base: 948, page: 1750 },
      { base: 948, page: 1900 },
      { base: 948, page: 1800 },
    ];
    expect(scaledLcp(pairs, 948)).toBe(1800);
  });

  // A slower host is compensated in every pair, so a page over the ceiling on a fast host passes at the same ratio.
  it("scales a slow host's pairs to the reference", () => {
    const pairs = [
      { base: 1896, page: 2400 },
      { base: 1896, page: 2400 },
      { base: 1896, page: 2200 },
    ];
    expect(scaledLcp(pairs, 948)).toBe(1200);
  });
});
