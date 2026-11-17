import type { Page } from "@playwright/test";
import { installFrameGate } from "./frames.ts";

// What makes two runs of a side draw the same pixels, applied the same way to the board and the page.
export type Stage = {
  viewport: { width: number; height: number };
  colorScheme: "light" | "dark";
  timezoneId: string;
  // The moment both sides are shot at: settle pauses the clock there and moves it on by a fixed amount.
  time: Date;
};

// A fixed seed for Math.random on both sides: the boards draw their canvases and demo numbers from it.
const seed = 0x5eed;

// How long a side may take to load: its clock starts this long before the scene's moment and runs, because the apps
// wait on timers while they load; settle then pauses it at the moment, so what is drawn after does not depend on how
// long loading took.
const loading = 30_000;

// stage prepares a page before it loads anything: Math.random replaced by a seeded generator (mulberry32) and the
// clock (Date, timers, requestAnimationFrame, performance.now) installed, running towards the scene's moment.
export async function stage(page: Page, view: Stage, frameOrigin?: number) {
  await page.addInitScript((start) => {
    const reset = () => {
      let state = start;
      Math.random = () => {
        state = (state + 0x6d2b79f5) | 0;
        let t = Math.imul(state ^ (state >>> 15), 1 | state);
        t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
        return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
      };
    };
    reset();
    // React consumes randomness for internal DOM keys before mounting. Start the ocean's stream at its own boundary
    // so the reference and React draw the same world regardless of framework initialization.
    Object.defineProperty(window, "AxiomOceanOrig", {
      configurable: true,
      set(ocean: { Engine: new (...args: never[]) => object }) {
        ocean.Engine = new Proxy(ocean.Engine, {
          construct(target, args) {
            reset();
            return Reflect.construct(target, args);
          },
        });
        Object.defineProperty(window, "AxiomOceanOrig", { configurable: true, writable: true, value: ocean });
      },
    });
  }, seed);
  await page.clock.install({ time: view.time.getTime() - loading });
  await page.addInitScript(installFrameGate, frameOrigin);
}

// Paired mechanical proofs need identical frame phases after navigation, as well as the same wall clock.
export async function stagePaused(page: Page, view: Stage) {
  await stage(page, view, view.time.getTime());
  await page.clock.pauseAt(view.time);
}

// Canvas engines resize and reschedule their first frame in native observers, outside Playwright's clock. Let those
// initial deliveries finish while the clock is paused, so they cannot interrupt one side's simulated animation time.
export async function canvasesReady(page: Page) {
  await page.evaluate(async () => {
    await Promise.all(
      [...document.querySelectorAll("canvas")].map(
        (canvas) =>
          new Promise<void>((resolve) => {
            let sized = false;
            let intersected = false;
            const ready = () => {
              if (sized && intersected) {
                resize.disconnect();
                intersection.disconnect();
                resolve();
              }
            };
            const resize = new ResizeObserver(() => {
              sized = true;
              ready();
            });
            const intersection = new IntersectionObserver(() => {
              intersected = true;
              ready();
            });
            resize.observe(canvas);
            intersection.observe(canvas);
          }),
      ),
    );
  });
}

// How long the clock runs before the shot: entrances and transitions driven by timers or animation frames reach their
// end state. CSS animations and transitions are not on this clock; freeze stops them.
const settleFor = 5_000;

// step scrolls every scrolling box one screen further, or back to the top, and says whether any moved.
function step(toTop: boolean) {
  let moved = false;
  for (const box of [document.scrollingElement, ...document.querySelectorAll("*")]) {
    if (!box || box.clientHeight === 0 || box.scrollHeight <= box.clientHeight + 1) continue;
    const before = box.scrollTop;
    box.scrollTop = toTop ? 0 : before + box.clientHeight;
    moved ||= box.scrollTop !== before;
  }
  return moved;
}

// A frame the browser renders, where intersection observers fire. The page's requestAnimationFrame belongs to the paused
// clock; the raf polling of waitForFunction does not, and the predicate is false on its first poll, true on the next.
const frame = (page: Page) =>
  page.waitForFunction(
    () => {
      const w = window as { designFrame?: boolean };
      w.designFrame = !w.designFrame;
      return !w.designFrame;
    },
    undefined,
    { polling: "raf" },
  );

// settle pauses the clock at the scene's moment, walks every scrolling box to its end a screen at a time, so lazy
// blocks and intersection observers fire, puts them back at the top, and runs the clock to the end state.
export async function settle(page: Page, time: Date) {
  await page.clock.pauseAt(time);
  while (await page.evaluate(step, false)) {
    await frame(page);
  }
  await page.evaluate(step, true);
  await frame(page);
  await page.clock.runFor(settleFor);
  await frame(page);
}

// freeze stops every CSS animation and transition where it is the same on each run: a finite one at its end, an endless
// one (a live pulse) removed, so the element is drawn as it is without it. Playwright's animations: "disabled" alone, and
// pausing an endless one at its start, both still drew the console overview's pulse a sub-pixel apart between runs.
export async function freeze(page: Page) {
  await page.evaluate(() => {
    for (const animation of document.getAnimations()) {
      if (animation.effect?.getComputedTiming().endTime === Number.POSITIVE_INFINITY) {
        animation.cancel();
      } else {
        animation.finish();
      }
    }
  });
  await frame(page);
}
