import { type RefObject, useEffect, useRef } from "react";

// What an effect module's start returns: the running effect, ended by destroy.
export type Running = { destroy: () => void };

// afterFirstContent calls go once the page has drawn its first content and the browser is idle, and returns a function
// that cancels it. A frame callback runs before the next paint, so the idle callback queued from it comes after that
// paint; a browser without requestIdleCallback waits a moment instead.
function afterFirstContent(go: () => void) {
  let idle = 0;
  const frame = requestAnimationFrame(() => {
    idle =
      typeof requestIdleCallback === "function"
        ? requestIdleCallback(go, { timeout: 2000 })
        : window.setTimeout(go, 200);
  });
  return () => {
    cancelAnimationFrame(frame);
    if (typeof cancelIdleCallback === "function") {
      cancelIdleCallback(idle);
    }
    clearTimeout(idle);
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
