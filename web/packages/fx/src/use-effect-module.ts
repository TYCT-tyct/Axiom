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

// useEffectModule loads an effect's code when `when` says, after the first content at idle unless told otherwise
// (nearView for what lies below the first screen), and starts it on the element ref points at; it ends the effect when
// the component unmounts. A module that fails to load leaves the page as it is: the effect is decoration, and the page
// works without it. The handle, once started, is in the returned ref.
export function useEffectModule<M, R extends Running>(
  ref: RefObject<HTMLElement | null>,
  load: () => Promise<M>,
  start: (module: M, element: HTMLElement) => R,
  when: (element: HTMLElement | null, go: () => void) => () => void = (_, go) => afterFirstContent(go),
): RefObject<R | null> {
  const running = useRef<R | null>(null);
  // The latest functions, read when the module arrives: a re-render does not reload it.
  const latest = useRef({ load, start, when });
  latest.current = { load, start, when };
  useEffect(() => {
    let live = true;
    const cancel = latest.current.when(ref.current, () => {
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
