import type { Page } from "@playwright/test";

declare global {
  interface Window {
    designFrames?: { pause(): void; resume(): void };
  }
}

// Installed after Playwright's clock, before the boards. Native observers may schedule frames even while time is
// paused. Keep their callbacks queued during capture, then resume them for the interaction assertions.
export function installFrameGate(origin?: number) {
  // Clock installation replays RPC/loading time into performance.now after navigation. Anchor paired proofs to their
  // fixed Date instead, including the clock's 16 ms frame grid, so SVG tweens reach exactly the same point.
  const now = () => Date.now() - (origin ?? 0);
  if (origin !== undefined) Object.defineProperty(performance, "now", { configurable: true, value: now });
  const request =
    origin === undefined
      ? window.requestAnimationFrame.bind(window)
      : (callback: FrameRequestCallback) => window.setTimeout(() => callback(now()), 16 - (now() % 16));
  const cancel = origin === undefined ? window.cancelAnimationFrame.bind(window) : window.clearTimeout.bind(window);
  type Frame = { callback: FrameRequestCallback; native?: number };
  const pending = new Map<number, Frame>();
  let next = 0;
  let paused = false;
  const schedule = (id: number, frame: Frame) => {
    frame.native = request((time) => {
      pending.delete(id);
      frame.callback(time);
    });
  };
  window.requestAnimationFrame = (callback) => {
    const id = ++next;
    const frame = { callback };
    pending.set(id, frame);
    if (!paused) schedule(id, frame);
    return id;
  };
  window.cancelAnimationFrame = (id) => {
    const frame = pending.get(id);
    if (frame?.native !== undefined) cancel(frame.native);
    pending.delete(id);
  };
  window.designFrames = {
    pause() {
      paused = true;
      for (const frame of pending.values()) {
        if (frame.native !== undefined) cancel(frame.native);
        delete frame.native;
      }
    },
    resume() {
      paused = false;
      for (const [id, frame] of pending) schedule(id, frame);
    },
  };
}

export const pauseFrames = (page: Page) => page.evaluate(() => window.designFrames?.pause());
export const resumeFrames = (page: Page) => page.evaluate(() => window.designFrames?.resume());
