// The LCP of a page on a host that other jobs share, from the three pairs of loads vitals.spec.ts takes: each pair is one
// load of the reference page and one of the page under test, next to each other (docs/frontend.md「实测」).

export const median = (values: number[]) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)] ?? 0;

// A pair's page LCP, scaled to the reference page's LCP of the run the ceiling came from. The factor is capped at one:
// a host slower than that run is compensated, a faster one does not shrink the page's budget below the ceiling.
export const scaled = ({ base, page }: { base: number; page: number }, reference: number) =>
  page * Math.min(1, reference / base);

// scaledLcp takes the median of the three scaled pairs, so a burst of another job's work that landed in one pair does not
// decide the gate; a page that got slower raises all three.
export const scaledLcp = (pairs: { base: number; page: number }[], reference: number) =>
  median(pairs.map((pair) => scaled(pair, reference)));
