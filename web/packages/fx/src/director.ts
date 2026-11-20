// The moment director of the home page's sea (the design's eggs.js, docs/frontend.md「海的彩蛋」). It owns the rules, so the
// moments never have to:
//   - auto moments (the hourly tide): at most one per visit; the highest priority queued goes first
//   - nothing automatic in the first 10 s, within 3 s of touching a control, or while the tab is hidden
//   - earned moments (a shark's visit, feeding, the floor, the whirlpool) happen when the reader does the thing, once
//     per visit each
//   - states (night, 1024) colour the visit from the end of the first 10 s; they spend no budget
//
// Invariants: an auto moment runs only from tick, while the tab is visible, at least entry after the start and control
// after the last touch of a control; at most one ever runs; an earned name runs at most once until it is allowed again.

export const entry = 10_000;
export const control = 3_000;

export type Clock = { now: () => number; hidden: () => boolean };
type Auto = { name: string; run: () => void; priority: number };

export function director(clock: Clock, states: () => void) {
  const start = clock.now();
  let touched = Number.NEGATIVE_INFINITY;
  let budget = 1;
  let coloured = false;
  let queue: Auto[] = [];
  const done = new Set<string>();
  const ran: string[] = [];
  return {
    // touched is a control used: the auto moments keep away from it for a while
    touched: () => {
      touched = clock.now();
    },
    // auto queues a moment for when the guards allow it; a moment already queued is not queued twice
    auto: (name: string, run: () => void, priority = 1) => {
      if (queue.some((q) => q.name === name)) return;
      queue.push({ name, run, priority });
      queue.sort((a, b) => b.priority - a.priority);
    },
    // earn runs an earned moment once per visit and says whether it ran
    earn: (name: string, run: () => void) => {
      if (done.has(name)) return false;
      done.add(name);
      run();
      return true;
    },
    // again lets an earned moment happen again (the way back up, once it has been taken)
    again: (name: string) => done.delete(name),
    tick: () => {
      if (clock.hidden()) return;
      const now = clock.now();
      if (now - start < entry) return;
      if (!coloured) {
        coloured = true;
        states();
      }
      if (now - touched < control) return;
      const next = queue.shift();
      if (!next) return;
      if (budget <= 0) {
        queue = [];
        return;
      }
      budget--;
      ran.push(next.name);
      next.run();
    },
    // what the end-to-end tests read: the auto moments run, the earned ones had, and what waits
    state: () => ({ ran: [...ran], earned: [...done], queued: queue.map((q) => q.name) }),
  };
}
