import { describe, expect, it } from "vitest";
import { mainThreadBusyMs } from "../e2e/vitals-cpu.ts";
import { bracketed, type Pair, scaled, scaledLcp } from "./vitals.ts";

// Chrome Headless Shell 156.0.8078.4, using the Chromium build pinned for our runners.
const localTrace = [
  { ph: "M", name: "thread_name", pid: 2464516, tid: 2464516, args: { name: "CrRendererMain" } },
  { ph: "X", name: "ThreadControllerImpl::RunTask", pid: 2464516, tid: 2464516, dur: 310, tdur: 267 },
  { ph: "X", name: "ThreadControllerImpl::RunTask", pid: 2464516, tid: 2464516, dur: 3252, tdur: 1688 },
  { ph: "X", name: "ThreadControllerImpl::RunTask", pid: 2464431, tid: 2464453, dur: 87, tdur: 85 },
  { ph: "X", name: "BlinkScheduler_PerformMicrotaskCheckpoint", pid: 2464516, tid: 2464516, dur: 9, tdur: 9 },
];

it("sums the self-hosted Chrome build's renderer tasks in milliseconds", () => {
  expect(mainThreadBusyMs(localTrace)).toBe(1.955);
});

it("requires measured tasks on a renderer main thread", () => {
  expect(() =>
    mainThreadBusyMs([
      { ph: "M", name: "thread_name", pid: 4, tid: 4, args: { name: "CrRendererMain" } },
      { ph: "X", name: "ThreadControllerImpl::RunTask", pid: 4, tid: 4 },
      { ph: "X", name: "ThreadControllerImpl::RunTask", pid: 8, tid: 8, tdur: 1000 },
    ]),
  ).toThrow("the trace has no main-thread task durations");
});

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

// The three page loads of a sample, each between its own two reference loads, in the order vitals.spec.ts takes them.
// host is the host's slowdown at each load and ratio the page's LCP beside a reference on the baseline host.
function sample(host: (load: number) => number, ratio: number, reference: number): Pair[] {
  const pairs: Pair[] = [];
  for (let i = 0; i < 3; i++) {
    const before = reference * host(3 * i);
    const page = reference * ratio * host(3 * i + 1);
    const after = reference * host(3 * i + 2);
    pairs.push(bracketed({ before, after, page }));
  }
  return pairs;
}

// The same sample in the order #585 took it: each pair is one reference load and then the page.
const referenceFirst = (host: (load: number) => number, ratio: number, reference: number): Pair[] =>
  [0, 1, 2].map((i) => ({
    base: reference * host(2 * i),
    page: reference * ratio * host(2 * i + 1),
  }));

describe("bracketed sampling", () => {
  // The desktop ceiling is 1500 ms against the 948 ms reference run; the statement page's usual LCP is about 0.8 of the
  // overview beside it. A host taken over by another job gets twice as slow on every load: the reference-first order
  // charges every pair the page's next, slower load, so all three scale to 1.6 x 948 = 1517 ms, over the ceiling — run
  // 41695611815 measured 1550.895 ms this way. Bracketing the page raises the reference beside it too, so the same
  // rising host stays under the ceiling.
  it("keeps a rising host under the ceiling where the reference-first order fails", () => {
    const host = (load: number) => 2 ** load;
    expect(scaledLcp(referenceFirst(host, 0.8, 948), 948)).toBeGreaterThan(1500);
    expect(scaledLcp(sample(host, 0.8, 948), 948)).toBeLessThanOrEqual(1500);
  });

  // A host that gets slower by the same step on every load leaves the page halfway between its two reference loads, so
  // their mean is the reference beside it and every pair is the page's own LCP again.
  it("cancels a steady rise exactly", () => {
    const host = (load: number) => 1 + load / 10;
    for (const pair of sample(host, 0.8, 948)) {
      expect(scaled(pair, 948)).toBeCloseTo(0.8 * 948);
    }
  });
});
