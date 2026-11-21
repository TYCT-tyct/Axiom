import { type RefObject, useEffect, useRef } from "react";

// What an effect module's start returns: the running effect, ended by destroy.
export type Running = { destroy: () => void };

// A rendering opportunity can pass before the browser reports a contentful paint. Wait for the paint entry before
// scheduling idle work, using a frame callback where Paint Timing is unavailable.
export function afterFirstContent(go: () => void) {
  let cancelIdle: (() => void) | undefined;
  const schedule = () => {
    if (typeof requestIdleCallback === "function") {
      const idle = requestIdleCallback(go, { timeout: 2000 });
      cancelIdle = () => cancelIdleCallback(idle);
    } else {
      const timer = window.setTimeout(go, 200);
      cancelIdle = () => window.clearTimeout(timer);
    }
  };
  let cancelPaint: () => void;
  if (globalThis.PerformanceObserver?.supportedEntryTypes?.includes("paint")) {
    const observer = new PerformanceObserver((list) => {
      if (list.getEntries().some((entry) => entry.name === "first-contentful-paint")) {
        observer.disconnect();
        schedule();
      }
    });
    observer.observe({ type: "paint", buffered: true });
    cancelPaint = () => observer.disconnect();
  } else {
    const frame = requestAnimationFrame(schedule);
    cancelPaint = () => cancelAnimationFrame(frame);
  }
  return () => {
    cancelPaint();
    cancelIdle?.();
  };
}

// nearView calls go once element comes within a view's height of being seen in the box that scrolls it, and returns a
// function that cancels it. The box is the nearest scrolling ancestor: an observer on the viewport would see the element
// only once it is in view, because the box clips it until then.
export function nearView(element: HTMLElement | null, go: () => void) {
  if (!element) return () => {};
  let root = element.parentElement;
  while (root && !/auto|scroll/.test(getComputedStyle(root).overflowY)) root = root.parentElement;
  const observer = new IntersectionObserver(
    (entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        observer.disconnect();
        go();
      }
    },
    { root, rootMargin: "100% 0px" },
  );
  observer.observe(element);
  return () => observer.disconnect();
}

// useEffectModule loads an effect's code after the first content, at idle, and starts it on the element ref points at;
// it ends the effect when the component unmounts. A module that fails to load leaves the page as it is: the effect is
// decoration, and the page works without it. The handle, once started, is in the returned ref.
export function useEffectModule<M, R extends Running>(
  ref: RefObject<HTMLElement | null>,
  load: () => Promise<M>,
  start: (module: M, element: HTMLElement) => R,
): RefObject<R | null> {
  const running = useRef<R | null>(null);
  // The latest functions, read when the module arrives: a re-render does not reload it.
  const latest = useRef({ load, start });
  latest.current = { load, start };
  useEffect(() => {
    let live = true;
    const cancel = afterFirstContent(() => {
      latest.current
        .load()
        .then((module) => {
          const element = ref.current;
          if (live && element) {
            running.current = latest.current.start(module, element);
          }
        })
        .catch(() => {
          // the page stays as it is drawn; nothing else depends on the effect
        });
    });
    return () => {
      live = false;
      cancel();
      running.current?.destroy();
      running.current = null;
    };
  }, [ref]);
  return running;
}
