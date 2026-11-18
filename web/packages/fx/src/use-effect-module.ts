import { type RefObject, useEffect, useRef } from "react";

// What an effect module's start returns: the running effect, ended by destroy.
export type Running = { destroy: () => void };

// A frame callback can run before the browser presents any content. Wait for paint timing where available, then
// schedule the import at idle. Browsers without paint timing fall back to scheduling from the next frame.
function afterFirstContent(go: () => void) {
  let cancelIdle: (() => void) | undefined;
  const schedule = () => {
    if (typeof requestIdleCallback === "function") {
      const idle = requestIdleCallback(go, { timeout: 2000 });
      cancelIdle = () => cancelIdleCallback(idle);
    } else {
      const timer = window.setTimeout(go, 200);
      cancelIdle = () => clearTimeout(timer);
    }
  };

  if (globalThis.PerformanceObserver?.supportedEntryTypes?.includes("paint")) {
    const observer = new PerformanceObserver((entries) => {
      if (entries.getEntriesByName("first-contentful-paint").length === 0) return;
      observer.disconnect();
      schedule();
    });
    observer.observe({ type: "paint", buffered: true });
    return () => {
      observer.disconnect();
      cancelIdle?.();
    };
  }

  const frame = requestAnimationFrame(schedule);
  return () => {
    cancelAnimationFrame(frame);
    cancelIdle?.();
  };
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
