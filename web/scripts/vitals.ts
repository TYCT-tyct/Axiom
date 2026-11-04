// The LCP of a page on a host that other jobs share, from the three page loads vitals.spec.ts takes, each between two
// loads of the reference page (docs/frontend.md「实测」).

export type Pair = { base: number; page: number };

export const median = (values: number[]) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)] ?? 0;

// bracketed is one page's pair from the reference loads taken just before and just after it. The mean is the reference
// beside the page, so a host whose load rises through the sample raises both sides of the ratio; taking only the load
// before the page charged every pair the rise to the next load (#624).
export const bracketed = ({ before, after, page }: { before: number; after: number; page: number }): Pair => ({
  base: (before + after) / 2,
  page,
});

// A pair's page LCP, scaled to the reference page's LCP of the run the ceiling came from. The factor is capped at one:
// a host slower than that run is compensated, a faster one does not shrink the page's budget below the ceiling.
export const scaled = ({ base, page }: Pair, reference: number) => page * Math.min(1, reference / base);

// scaledLcp takes the median of the three scaled pairs, so a burst of another job's work that landed in one pair does not
// decide the gate; a page that got slower raises all three.
export const scaledLcp = (pairs: Pair[], reference: number) => median(pairs.map((pair) => scaled(pair, reference)));
