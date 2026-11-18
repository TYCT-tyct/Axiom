import { own } from "./loop";

// A screen's script on the shared frame loop: the boards write their scripts as sleeps and per-frame steps, and a
// timeline gives them a clock of the screen's own. Its time runs only on the loop's frames, so a script stands still
// while its element is out of view or the page hidden, and under reduced motion it never moves at all: the screen stays
// in the state it was drawn in.
//
// Invariants: time only moves forward, by the loop's frame steps; once end is called, every pending and later sleep
// resolves false, no step runs again, and the element is no longer the loop's.
export type Timeline = {
  // now is the screen's time in milliseconds.
  now: () => number;
  // sleep resolves true after ms of the screen's time, or false once the timeline has ended.
  sleep: (ms: number) => Promise<boolean>;
  // each runs step on every frame, with the screen's time, until it returns false or the timeline ends.
  each: (step: (now: number) => boolean) => void;
  end: () => void;
};

export function timeline(element: Element): Timeline {
  let time = 0;
  let live = true;
  const sleeps = new Set<{ at: number; done: (ok: boolean) => void }>();
  const steps = new Set<(now: number) => boolean>();
  const release = own(element, (dt) => {
    time += dt * 1000;
    for (const sleep of [...sleeps]) {
      if (sleep.at <= time) {
        sleeps.delete(sleep);
        sleep.done(true);
      }
    }
    for (const step of [...steps]) {
      if (!step(time)) steps.delete(step);
    }
  });
  return {
    now: () => time,
    sleep: (ms) =>
      new Promise((done) => {
        if (live) sleeps.add({ at: time + ms, done });
        else done(false);
      }),
    each: (step) => {
      if (live) steps.add(step);
    },
    end: () => {
      live = false;
      release();
      for (const sleep of sleeps) sleep.done(false);
      sleeps.clear();
      steps.clear();
    },
  };
}
