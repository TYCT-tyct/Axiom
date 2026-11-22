import { afterFirstContent } from "@axiom/fx";
import type { Sea } from "@axiom/fx/sea";
import type { Tiles } from "@axiom/fx/tiles";
import type { LoginHost } from "@axiom/login";
import { type ReactNode, type RefObject, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { consoleHref, type Place, placeOf } from "./sign-in-place";

const loadHost = () => import("./sign-in-host");
const loadTiles = () => import("@axiom/fx/tiles");
const loadSolid = () => import("@axiom/fx/login");
type HostModule = Awaited<ReturnType<typeof loadHost>>;
type TilesModule = Awaited<ReturnType<typeof loadTiles>>;
type Code = { Host: HostModule["SignInHost"]; tiles: TilesModule["tiles"] };

// The code the sign-in pages need in the website's document, asked for once the first screen is drawn: the host
// (session, requests, the Login board, the pages the homepage's buttons open), the transition and the solid on the sea.
function loadCode(): Promise<Code> {
  return Promise.all([loadHost(), loadTiles(), loadSolid()]).then(async ([host, tiles]) => {
    await host.preloadPages();
    return { Host: host.SignInHost, tiles: tiles.tiles };
  });
}

// a box over the website's frame for the sign-in page, beside the homepage (trans.js's .tr > .lg), unseen until the
// transition shows it
function mountIn(root: HTMLElement) {
  const box = document.createElement("div");
  Object.assign(box.style, { position: "absolute", inset: "0", zIndex: "2", visibility: "hidden" });
  root.append(box);
  return box;
}

const typing = (e: KeyboardEvent) =>
  e.target instanceof Element && Boolean((e.target.closest("input, textarea") as HTMLInputElement | null)?.value);

// The frame stands ready, unseen, from the first screen on: a press moves at once, its page put in while the
// transition runs. place is the page on show, null while it waits.
type Shown = { place: Place | null; code: Code; mount: HTMLElement };
// A sign-in in this document: the transition once it runs, the way in pressed, the frame once laid out (ready), and
// whether a press went through. The address while the page is up was pushed by the way in (popped going home) or come
// to by forward; self is a pop the page made itself.
type Signing = {
  tiles: Tiles | null;
  trigger: HTMLElement | null;
  lg: HTMLElement | null;
  go: boolean;
  pushed: boolean;
  self: boolean;
  title: string;
};

// The website's half of the sign-in pages' address: going home pops the entry the way in pushed, or replaces the
// console's address the page was moved to; going back again pushes the console's address once more.
function addressing(s: RefObject<Signing>) {
  return {
    home: () => {
      if (s.current.pushed) {
        s.current.pushed = false;
        s.current.self = true;
        window.history.back();
      } else window.history.replaceState(window.history.state, "", "/");
    },
    to: (place: Place, replace = false) => {
      window.history[replace ? "replaceState" : "pushState"](window.history.state, "", consoleHref(place));
      if (!replace) s.current.pushed = true;
    },
  };
}
type Url = ReturnType<typeof addressing>;

// The transition starts once a press went through and the frame is laid out, whichever is last.
function startRun(frame: HTMLElement, sea: Sea | null, shown: Shown | null, s: Signing, end: () => void) {
  const ho = frame.querySelector<HTMLElement>(".ho");
  const lg = s.lg;
  if (!ho || !lg || !sea || !shown || !s.go || s.tiles) return;
  // the board's own ground would cover the website's sea, which shows beside the form panel here
  lg.style.background = "transparent";
  // the homepage's entrance, if it still plays, lands first: the transition starts from the page as it stands
  sea.settle?.();
  s.title ||= document.title;
  s.tiles = shown.code.tiles({ root: frame, ho, lg, sea: sea.engine }, { end: (state) => state === "home" && end() });
  shown.mount.style.removeProperty("visibility");
  s.tiles.run(1, s.trigger ?? undefined);
}

// useShown keeps the frame ready in the website's frame and puts a page in it (show); put away hides it again, its
// page taken out, once the way home has landed.
function useShown(
  root: RefObject<HTMLElement | null>,
  sea: RefObject<Sea | null>,
  seaUp: boolean,
  s: RefObject<Signing>,
) {
  const [shown, setShown] = useState<Shown | null>(null);
  const code = useRef<Promise<Code> | null>(null);
  const now = useRef(shown);
  now.current = shown;
  const ready = useCallback(() => {
    const frame = root.current;
    if (!frame || !sea.current) return Promise.reject(new Error("the frame has no sea"));
    code.current ??= loadCode();
    return code.current.then((loaded) =>
      setShown((was) => was ?? { place: null, code: loaded, mount: mountIn(frame) }),
    );
  }, [root, sea]);
  // the first screen drawn and its sea up, the frame gets ready
  useEffect(() => {
    if (seaUp) return afterFirstContent(() => void ready().catch(() => {}));
  }, [ready, seaUp]);
  const show = useCallback((place: Place | null) => setShown((was) => (was ? { ...was, place } : was)), []);
  const putAway = useCallback(() => {
    s.current.tiles?.destroy();
    Object.assign(s.current, { tiles: null, go: false, trigger: null });
    now.current?.mount.style.setProperty("visibility", "hidden");
    show(null);
    if (s.current.title) document.title = s.current.title;
  }, [s, show]);
  return { shown, now, ready, show, putAway };
}

// useSignIn holds the console's sign-in pages in the website's document (port design §9, trans.js): a press of a way
// to sign in or register opens its page over the same sea, the tiles transition joining the two as one scene, the
// address the console's; the brand, Esc and the browser's back go home the same way, turning round midway. A step goes
// on to another in place; a member, or a step the website does not hold, enters the console with a page load. Code
// that cannot come leaves the press to the link: the console's own page.
// onSigning hears when the pages go up (the way in starts) and when they are put away (home again).
export function useSignIn(
  root: RefObject<HTMLElement | null>,
  sea: RefObject<Sea | null>,
  seaUp: boolean,
  onSigning: (up: boolean) => void,
) {
  const s = useRef<Signing>({ tiles: null, trigger: null, lg: null, go: false, pushed: false, self: false, title: "" });
  const url = useMemo(() => addressing(s), []);
  const signing = useRef(onSigning);
  signing.current = onSigning;
  const page = useShown(root, sea, seaUp, s);
  const { shown, now } = page;
  const up = shown !== null;
  const putAway = useCallback(() => {
    page.putAway();
    signing.current(false);
  }, [page.putAway]);
  const start = useCallback(() => {
    if (!root.current || s.current.tiles) return;
    startRun(root.current, sea.current, now.current, s.current, putAway);
    if (s.current.tiles) signing.current(true);
  }, [root, sea, now, putAway]);
  const ways = useWaysIn(s, url, page, start);
  const host = useMemo<LoginHost | null>(() => {
    const frame = root.current;
    const engine = sea.current?.engine;
    if (!up || !frame || !engine) return null;
    // the solid reads which way the transition runs (Morph's tr.dir); before it starts, the way in
    const way = {
      get dir() {
        return s.current.tiles?.dir ?? 1;
      },
    };
    const ready = (lg: HTMLElement) => {
      s.current.lg = lg;
      start();
    };
    return { root: frame, sea: engine, way, ready };
  }, [up, root, sea, start]);
  // a step goes on to another in place (the gate's way on, a link between steps)
  const go = useCallback(
    (place: Place, replace: boolean) => {
      url.to(place, replace);
      page.show(place);
    },
    [url, page.show],
  );
  useSignInWays(root, { ...ways, url, s, show: page.show });
  const element: ReactNode =
    shown && host ? <shown.code.Host mount={shown.mount} host={host} place={shown.place} go={go} /> : null;
  return { enter: ways.enter, element };
}

// The ways in and the turns: a press goes (on the way home, it turns round), and back goes home from the page or
// turns a way in round.
function useWaysIn(s: RefObject<Signing>, url: Url, page: ReturnType<typeof useShown>, start: () => void) {
  const { ready, show, now } = page;
  const turn = useCallback(() => {
    const tiles = s.current.tiles;
    if (tiles?.state !== "run") return false;
    tiles.turn();
    if (tiles.dir < 0) url.home();
    else if (now.current?.place) url.to(now.current.place);
    return true;
  }, [s, url, now]);
  const back = useCallback(() => {
    const tiles = s.current.tiles;
    if (tiles?.state === "login") {
      tiles.run(-1);
      url.home();
    } else if (tiles?.dir === 1) turn();
  }, [s, turn, url]);
  // a way in pressed: its page goes in the ready frame and the transition starts at once. A frame not ready yet (its
  // code still on the way, or not come) leaves the press to the departure, which goes to the console's own page.
  const enter = useCallback(
    (link: HTMLAnchorElement) => {
      const place = placeOf(new URL(link.href));
      if (!place || !now.current) return false;
      if (s.current.tiles) return s.current.tiles.dir < 0 ? turn() || true : true;
      url.to(place);
      Object.assign(s.current, { go: true, trigger: link });
      show(place);
      start();
      return true;
    },
    [now, s, show, turn, url, start],
  );
  return { turn, back, enter, ready, start };
}

type Ways = ReturnType<typeof useWaysIn> & { url: Url; s: RefObject<Signing>; show: (place: Place | null) => void };

// The ways home and back again: the sign-in page's brand, Esc (unless something is typed in a field), and the
// browser's back and forward, which also move between steps; a press on the form panel while going home turns round.
function useSignInWays(root: RefObject<HTMLElement | null>, ways: Ways) {
  const latest = useRef(ways);
  latest.current = ways;
  useEffect(() => {
    const frame = root.current;
    if (!frame) return;
    const onClick = (e: MouseEvent) => wayHome(latest.current, e);
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || typing(e) || !latest.current.s.current.tiles) return;
      e.preventDefault();
      latest.current.back();
    };
    const onPop = () => moved(latest.current);
    frame.addEventListener("click", onClick);
    window.addEventListener("keydown", onKey);
    window.addEventListener("popstate", onPop);
    return () => {
      frame.removeEventListener("click", onClick);
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("popstate", onPop);
    };
  }, [root]);
}

// a click on the sign-in page's brand goes home; the flying mark turned the run round on its own press (tiles.ts), and
// the address goes home with it; the form panel pressed on the way home turns round
function wayHome({ s, back, turn, url }: Ways, e: MouseEvent) {
  const target = e.target instanceof Element ? e.target : null;
  const tiles = s.current.tiles;
  if (!target || !tiles || e.button !== 0) return;
  if (target.closest(".lg a.brand")) {
    e.preventDefault();
    back();
  } else if (target.closest(".fly .fb")) {
    if (tiles.dir < 0) url.home();
  } else if (tiles.dir < 0 && target.closest(".lg-side") && turn()) e.preventDefault();
}

// the browser moved: home goes back (or turns a way in round), a step of the sign-in pages shows it (or opens the page
// over the homepage, or turns a way home round)
function moved({ s, ready, show, start }: Ways) {
  if (s.current.self) {
    s.current.self = false;
    return;
  }
  const place = placeOf(new URL(window.location.href));
  const tiles = s.current.tiles;
  s.current.pushed = place !== null;
  if (!place) {
    if (tiles?.state === "login") tiles.run(-1);
    else if (tiles?.state === "run" && tiles.dir > 0) tiles.turn();
  } else if (!tiles) {
    s.current.go = true;
    ready().then(
      () => {
        show(place);
        start();
      },
      () => window.location.reload(),
    );
  } else if (tiles.state === "run" && tiles.dir < 0) tiles.turn();
  else show(place);
}
