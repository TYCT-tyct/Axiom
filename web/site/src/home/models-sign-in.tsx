import { afterFirstContent, markArrival } from "@axiom/fx";
import type { LoginHost } from "@axiom/login";
import { type MouseEvent, type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { consoleHref, type Place, placeOf } from "./sign-in-place";

const loadHost = () => import("./sign-in-host");
const loadSolid = () => import("@axiom/fx/login");
type Host = Awaited<ReturnType<typeof loadHost>>["SignInHost"];

// How long the layer takes to come and to go.
const fadeFor = 200;
// The way in from the models page: the console's sign-in step, which a signed-in visitor's gate passes on to the console.
const signIn: Place = { path: "/sign-in", search: {} };

const plainClick = (e: MouseEvent) => e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;
const typing = (e: KeyboardEvent) =>
  e.target instanceof Element && Boolean((e.target.closest("input, textarea") as HTMLInputElement | null)?.value);

// a layer over the models page for the sign-in page, which brings its own sea; it fades in at once, the page's parts
// after it as an arrival's do (useArrivalReveal), and the page beneath is out of reach while it is up
function layer() {
  const box = document.createElement("div");
  Object.assign(box.style, {
    position: "fixed",
    inset: "0",
    zIndex: "50",
    overflow: "auto",
    opacity: "0",
    transition: `opacity ${fadeFor}ms ease-out`,
  });
  document.body.append(box);
  requestAnimationFrame(() => requestAnimationFrame(() => (box.style.opacity = "1")));
  return box;
}

// leaving: the layer fades out, its page still drawn, and is taken away once it has
type Open = { place: Place; mount: HTMLElement; host: LoginHost; Host: Host; title: string; leaving?: boolean };

// useModelsSignIn opens the console's sign-in pages over the models page, in its document: a press of the way in shows
// the page at once with the console's address, Esc and the browser's back take it away again, and a step goes on to
// another in place; the brand goes home and a member enters the console, each with a page load. Its code is asked for
// once the models are drawn; a press before it has come follows the link to the console's own page.
export function useModelsSignIn(below: () => HTMLElement | null) {
  const code = useCode();
  const [open, setOpen] = useState<Open | null>(null);
  const now = useRef(open);
  now.current = open;
  const close = useCallback(() => {
    const was = now.current;
    if (!was || was.leaving) return;
    was.mount.style.opacity = "0";
    setOpen({ ...was, leaving: true });
    window.setTimeout(() => {
      setOpen(null);
      was.mount.remove();
      document.title = was.title;
    }, fadeFor);
  }, []);
  const go = useCallback((place: Place, replace: boolean) => {
    window.history[replace ? "replaceState" : "pushState"](window.history.state, "", consoleHref(place));
    setOpen((was) => (was ? { ...was, place } : was));
  }, []);
  // the layer comes up on a step: pressed into, or come back to by the browser's forward
  const up = useCallback((place: Place, Host: Host) => {
    // the frame plays its arrival: its parts come in over its sea, its title decodes (login-frame.tsx)
    markArrival();
    const mount = layer();
    // the models page takes the ways out (useWaysOut); the frame has no transition to wait for
    const host = { root: mount, sea: null, way: { dir: 1 as const }, ready: () => {} };
    setOpen({ place, mount, host, Host, title: document.title });
  }, []);
  const enter = useCallback(
    (e: MouseEvent<HTMLAnchorElement>) => {
      const Host = code.current;
      if (!Host || !plainClick(e) || now.current) return;
      e.preventDefault();
      window.history.pushState(window.history.state, "", consoleHref(signIn));
      up(signIn, Host);
    },
    [up],
  );
  const show = useCallback(
    (place: Place) => {
      const Host = code.current;
      if (now.current) setOpen((was) => (was ? { ...was, place } : was));
      else if (Host) up(place, Host);
      // its code never came: the console's own page at this address
      else window.location.reload();
    },
    [up],
  );
  useWaysOut(now, close, show);
  useBelowOutOfReach(below, open !== null);
  const element: ReactNode = open ? <open.Host mount={open.mount} host={open.host} place={open.place} go={go} /> : null;
  return { enter, element };
}

// the sign-in pages' code, asked for once the models are drawn: none until it has come
function useCode() {
  const code = useRef<Host | null>(null);
  useEffect(
    () =>
      afterFirstContent(
        () =>
          void Promise.all([loadHost(), loadSolid()])
            .then(async ([host]) => {
              await host.preloadPages();
              code.current = host.SignInHost;
            })
            .catch(() => {}),
      ),
    [],
  );
  return code;
}

// The ways out: Esc (unless something is typed in a field) goes back, and the browser's back and forward close the
// layer or move between steps. The listeners stay for the page's life (close and show never change): the models page's
// own popstate listener renders it within the same event, and a listener swapped then would miss it.
function useWaysOut(now: { current: Open | null }, close: () => void, show: (place: Place) => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || typing(e) || !now.current || now.current.leaving) return;
      e.preventDefault();
      window.history.back();
    };
    const onPop = () => {
      const place = placeOf(new URL(window.location.href));
      if (now.current?.leaving) return;
      if (place) show(place);
      else close();
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("popstate", onPop);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("popstate", onPop);
    };
  }, [now, close, show]);
}

// While the layer is up the models page beneath can be neither reached by keyboard nor scrolled.
function useBelowOutOfReach(below: () => HTMLElement | null, up: boolean) {
  useEffect(() => {
    const page = below();
    if (!up || !page) return;
    page.inert = true;
    const overflow = document.documentElement.style.overflow;
    document.documentElement.style.overflow = "hidden";
    return () => {
      page.inert = false;
      document.documentElement.style.overflow = overflow;
    };
  }, [below, up]);
}
