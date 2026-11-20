import { markArrival, own } from "@axiom/fx";
import type { Departure } from "@axiom/fx/depart";
import type { Sea } from "@axiom/fx/sea";
import { type RefObject, useEffect, useLayoutEffect, useRef, useState } from "react";
import { sections } from "./words";

export type Scroll = { solid: boolean; sec: number; depth: number; bottom: boolean };
const atTop: Scroll = { solid: false, sec: 0, depth: 0, bottom: false };
const same = (a: Scroll, b: Scroll) =>
  a.solid === b.solid && a.sec === b.sec && a.depth === b.depth && a.bottom === b.bottom;

// scrolled reads where the page is (Site.dc.html onScroll): the header gives way to the island once the page moves,
// the island names the section a third of the way down the view and its depth, and the sea dims as the page goes
// down. offsets are where the sections on the page start, each with its index in sections; a screen still to come is
// not among them.
type Offset = { sec: number; top: number };
function scrolled(scroller: HTMLElement, offsets: Offset[]): Scroll & { dim: number } {
  const y = scroller.scrollTop;
  const view = scroller.clientHeight;
  const down = Math.min(1, y / Math.max(1, scroller.scrollHeight - view));
  const probe = y + view * 0.35;
  let i = 0;
  while (i < offsets.length - 1 && (offsets[i + 1]?.top ?? 0) <= probe) i++;
  const start = offsets[i]?.top ?? 0;
  const end = offsets[i + 1]?.top ?? start + view;
  const k = Math.min(1, Math.max(0, (probe - start) / Math.max(1, end - start)));
  const last = sections.length - 1;
  const sec = Math.min(offsets[i]?.sec ?? 0, last);
  const from = sections[sec]?.depth ?? 0;
  // toward the next section on the page, which need not be the next in sections; the last one holds its depth
  const next = offsets[i + 1];
  const to = next ? (sections[Math.min(next.sec, last)]?.depth ?? from) : from;
  return {
    solid: y > 8,
    sec,
    depth: Math.round(from + (to - from) * k),
    bottom: down > 0.985,
    dim: 0.42 * down ** 1.3,
  };
}

// scrolling is the frame's scroller: where its sections start, what scrolling it says, and gliding to the top or to a
// section (native smooth scrolling, not the board's eased frames), and the tour, which
// glides through the sections a few seconds apart and back to the top. The page moving the reader on its own (the tour's
// next glide, a glide to a section followed while the screens above it grow, a glide under way) is the press's that
// started it: a glide to the top or to a section, and stop, end it; stop also halts a glide where it is.
// The screens are measured by their places (screens.tsx), which are there before their code and carry their section.
const places = (scroller: HTMLElement) => [...scroller.querySelectorAll<HTMLElement>(".under [data-place]")];
const measured = (scroller: HTMLElement): Offset[] => [
  { sec: 0, top: 0 },
  ...places(scroller).map((s) => ({ sec: Number(s.dataset.place), top: s.offsetTop })),
];
// applyDepth updates .depth opacity from scroll depth and (dark theme only) the eggs' night floor.
function applyDepth(depth: HTMLElement | null, dim: number) {
  if (!depth) return;
  depth.dataset.dim = dim.toFixed(3);
  const floor = depth.closest(".ho.dark") ? Number(depth.dataset.floor ?? 0) : 0;
  depth.style.opacity = Math.max(dim, floor).toFixed(3);
}

const wheelDelta = (e: WheelEvent, view: number) => e.deltaY * (e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? view : 1);

// Inertial wheel scrolling on the wide layout (Site.logic.html onWheel / ease): the wheel moves a target and the
// shared frame loop eases scrollTop toward it, releasing its owner once settled or interrupted.
function wheelScroller(scroller: HTMLElement) {
  let target: number | null = null;
  let pos = 0;
  let applied: number | null = null;
  let release: (() => void) | undefined;
  const stop = () => {
    target = null;
    applied = null;
    release?.();
    release = undefined;
  };
  const step = (dt: number) => {
    if (target === null) return stop();
    if (applied !== null && Math.abs(scroller.scrollTop - applied) > 2) return stop();
    const d = target - pos;
    if (Math.abs(d) < 0.5) {
      scroller.scrollTop = target;
      return stop();
    }
    const k = dt > 0 ? 1 - 0.88 ** (dt * 60) : 0.12;
    pos += d * k;
    scroller.scrollTop = pos;
    applied = scroller.scrollTop;
  };
  const onWheel = (e: WheelEvent) => {
    const max = scroller.scrollHeight - scroller.clientHeight;
    if (e.ctrlKey || e.deltaY === 0 || max <= 0) return;
    e.preventDefault();
    const cur = scroller.scrollTop;
    const next = Math.max(0, Math.min(max, (target ?? cur) + wheelDelta(e, scroller.clientHeight)));
    if (target === null && Math.abs(next - cur) < 0.5) return;
    if (target === null) pos = cur;
    target = next;
    applied = cur;
    release ??= own(scroller, step);
  };
  return { onWheel, stop };
}

function scrolling(
  scroller: HTMLElement,
  depth: HTMLElement | null,
  show: (scroll: Scroll) => void,
  onGlide?: () => void,
) {
  let offsets: Offset[] = [{ sec: 0, top: 0 }];
  let timer = 0;
  // a glide the page started is under way (until the browser says the scroll has ended)
  let gliding = false;
  // the section a glide is taking the reader to, and where it was when the glide set out
  let following: { sec: number; top: number } | undefined;
  const glide = (top: number) => {
    onGlide?.();
    gliding = true;
    scroller.scrollTo({ top, behavior: "smooth" });
  };
  const topOf = (sec: number) => offsets.find((offset) => offset.sec === sec)?.top ?? 0;
  // The screens above a section grow as their code arrives (a place is a view's height until then), which moves the
  // section on while the page glides to it: each time the places are measured again, it glides on to where the section
  // is now, for as long as the glide is the reader's latest wish (a few seconds, or until the reader takes over).
  const section = (sec: number) => {
    window.clearTimeout(timer);
    following = { sec, top: topOf(sec) };
    glide(following.top);
    timer = window.setTimeout(stop, 6000);
  };
  // the tour goes on from the section the reader is in the top of, or from the first when at the surface
  const tour = (i: number) => {
    const top = offsets[i]?.top;
    timer = window.setTimeout(() => (top === undefined ? glide(0) : tour(i + 1)), top === undefined ? 3000 : 3200);
    if (top !== undefined) glide(top);
  };
  // ends what the page set going: the next step, the following, and a glide under way, which stops where it is
  const stop = () => {
    window.clearTimeout(timer);
    following = undefined;
    if (!gliding) return;
    gliding = false;
    scroller.scrollTo({ top: scroller.scrollTop, behavior: "instant" });
  };
  return {
    measure: () => {
      offsets = measured(scroller);
      if (!following) return;
      const now = topOf(following.sec);
      if (Math.abs(now - following.top) <= 2) return;
      following.top = now;
      glide(now);
    },
    onScroll: () => {
      const { dim, ...scroll } = scrolled(scroller, offsets);
      applyDepth(depth, dim);
      show(scroll);
    },
    onScrollEnd: () => {
      gliding = false;
    },
    toTop: () => {
      stop();
      glide(0);
    },
    toSection: (sec: number) => {
      stop();
      section(sec);
    },
    tour: () => {
      stop();
      tour(scroller.scrollTop > 10 ? 0 : 1);
    },
    stop,
  };
}

// What the board does on a click, by the control clicked (Site.dc.html onClick).
function clickActions(
  page: ReturnType<typeof scrolling>,
  setMenu: (open: boolean) => void,
  back: RefObject<HTMLElement | null>,
): [string, (control: HTMLElement, e: MouseEvent) => void][] {
  return [
    [
      ".isl .im, .sheet .brand",
      (_, e) => {
        e.preventDefault();
        setMenu(false);
        page.toTop();
      },
    ],
    [
      ".menu",
      (_, e) => {
        e.preventDefault();
        setMenu(true);
      },
    ],
    [".shx", () => setMenu(false)],
    [
      ".sml",
      (control) => {
        // the menu closes onto the section chosen, where the keyboard goes on from
        back.current =
          control.closest(".tr")?.querySelector<HTMLElement>(`.under [data-place="${control.dataset.sec}"]`) ?? null;
        setMenu(false);
        page.toSection(Number(control.dataset.sec));
      },
    ],
    [".tour", () => page.tour()],
    ["[data-go]", () => setMenu(false)],
  ];
}

const islandEvents = [
  ["mouseenter", true],
  ["mouseleave", false],
  ["focusin", true],
  ["focusout", false],
] as const;

// The menu sheet (narrow layout) to the keyboard: closed, it is out of the way (inert), as it is out of sight; opened,
// the focus goes into it, Escape closes it, and closed the focus comes back to what opened it, or to the section
// chosen in it.
function useMenuSheet(
  root: RefObject<HTMLElement | null>,
  menu: boolean,
  setMenu: (open: boolean) => void,
  back: RefObject<HTMLElement | null>,
) {
  const opened = useRef(false);
  useLayoutEffect(() => {
    const frame = root.current;
    const sheet = frame?.querySelector<HTMLElement>(".sheet");
    if (!frame || !sheet) return;
    sheet.inert = !menu;
    if (menu) {
      opened.current = true;
      sheet.querySelector<HTMLElement>(".shx")?.focus();
      return;
    }
    if (!opened.current) return;
    opened.current = false;
    const to = back.current ?? frame.querySelector<HTMLElement>(".bar .menu");
    back.current = null;
    if (to && !to.matches("button, a")) to.tabIndex = -1;
    to?.focus({ preventScroll: true });
  }, [root, menu, back]);
  useEffect(() => {
    const frame = root.current;
    if (!frame || !menu) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setMenu(false);
    frame.addEventListener("keydown", onKey);
    return () => frame.removeEventListener("keydown", onKey);
  }, [root, menu, setMenu]);
}

// The reader taking over: a touch, a press or a key stops both glides and inertial wheel easing; a wheel stops a
// programmatic glide without clearing the wheel's accumulated target. A press that starts a glide comes after it.
const takeOver = ["touchstart", "pointerdown", "keydown"] as const;

// useFrameBehaviour runs the board's own behaviours on the frame root points at: the header and the island while
// scrolling, desktop inertial wheel easing, the island opening under the pointer or the keyboard (wide layout) and
// the menu sheet. The page stops moving the reader once the reader takes over or the page is hidden.
export function useFrameBehaviour(root: RefObject<HTMLElement | null>, wide: boolean) {
  const [scroll, setScroll] = useState(atTop);
  const [island, setIsland] = useState(false);
  const [menu, setMenu] = useState(false);
  const back = useRef<HTMLElement | null>(null);
  useMenuSheet(root, menu, setMenu, back);
  useEffect(() => {
    const frame = root.current;
    const scroller = frame?.querySelector<HTMLElement>(".hs");
    if (!frame || !scroller) return;
    const wheel = wide ? wheelScroller(scroller) : null;
    const page = scrolling(
      scroller,
      frame.querySelector(".ho > .depth"),
      (next) => setScroll((prev) => (same(prev, next) ? prev : next)),
      wheel?.stop,
    );
    const stopAll = () => {
      page.stop();
      wheel?.stop();
    };
    const clicks = clickActions(page, setMenu, back);
    const onClick = (e: MouseEvent) => {
      const control = e.target instanceof Element ? e.target.closest<HTMLElement>("a, button") : null;
      const action = control && frame.contains(control) && clicks.find(([selector]) => control.matches(selector));
      if (control && action) action[1](control, e);
    };
    const isl = wide ? frame.querySelector(".isl") : null;
    const hovers = islandEvents.map(([type, open]) => [type, () => setIsland(open)] as const);
    const resized = new ResizeObserver(() => {
      page.measure();
      page.onScroll();
    });
    page.measure();
    page.onScroll();
    // a place grows when its screen's code arrives, and the sections after it move down
    for (const element of [scroller, ...places(scroller)]) resized.observe(element);
    scroller.addEventListener("scroll", page.onScroll, { passive: true });
    scroller.addEventListener("scrollend", page.onScrollEnd);
    if (wheel) scroller.addEventListener("wheel", wheel.onWheel, { passive: false });
    frame.addEventListener("click", onClick);
    frame.addEventListener("wheel", page.stop, { capture: true, passive: true });
    for (const type of takeOver) frame.addEventListener(type, stopAll, { capture: true, passive: true });
    const onHidden = () => document.hidden && stopAll();
    document.addEventListener("visibilitychange", onHidden);
    for (const [type, listener] of hovers) isl?.addEventListener(type, listener);
    return () => {
      resized.disconnect();
      stopAll();
      document.removeEventListener("visibilitychange", onHidden);
      frame.removeEventListener("wheel", page.stop, { capture: true });
      for (const type of takeOver) frame.removeEventListener(type, stopAll, { capture: true });
      if (wheel) scroller.removeEventListener("wheel", wheel.onWheel);
      scroller.removeEventListener("scroll", page.onScroll);
      scroller.removeEventListener("scrollend", page.onScrollEnd);
      frame.removeEventListener("click", onClick);
      for (const [type, listener] of hovers) isl?.removeEventListener(type, listener);
    };
  }, [root, wide]);
  return { scroll, island, menu };
}

const loadDepart = () => import("@axiom/fx/depart");
// The departure's length on the wall clock: the page goes this long after the press, whether or not its code has come.
const departSpan = 500;
// A page still here this long after it started to go was stopped (the browser's stop button) or failed to go: the
// picture of its departure comes off. A page that does go is gone long before.
const stalled = 3000;

// The press under way, until its page starts to go, and the picture of a departure on screen, held until the next
// page replaces this one. Both are the module's, not a frame's: a change of layout midway starts the frame afresh
// (home.tsx), which takes the picture with it but must not take the way there.
let leaving: object | undefined;
let shown: { frame: HTMLElement; picture: Departure } | undefined;

function clear() {
  shown?.picture.destroy();
  shown = undefined;
}

function leave(frame: HTMLElement, link: HTMLAnchorElement, sea: Sea | null) {
  clear();
  const press = {};
  leaving = press;
  const start = performance.now();
  const href = link.href;
  window.setTimeout(() => {
    if (leaving !== press) return;
    // from here the press is done: a press after a stopped navigation starts a departure of its own at once
    leaving = undefined;
    const held = shown;
    // the arrival is the sign-in page's: registration has no arrival of its own, and a marker left by it would play
    // the decode on a later, direct visit to sign-in
    if (new URL(href).pathname.endsWith("/sign-in")) markArrival();
    window.location.assign(href);
    window.setTimeout(() => shown === held && clear(), stalled);
  }, departSpan);
  // code that comes after the page started to go, after the frame has gone, or after the page has come back, draws
  // nothing; code that never comes only costs the picture
  loadDepart().then(
    ({ depart }) => {
      if (leaving !== press || !frame.isConnected || !link.isConnected) return;
      shown = { frame, picture: depart(frame, link, start, departSpan, sea?.engine) };
    },
    () => {},
  );
}

// useDeparture plays the tiles departure (packages/fx depart) when a [data-go] link of the frame root points at is
// pressed: signing in or up leaves the website through it, half a second after the press. Its code loads on the first
// press. A press that asks for another tab or window goes as a link would.
export function useDeparture(root: RefObject<HTMLElement | null>, sea: RefObject<Sea | null>) {
  useEffect(() => {
    const frame = root.current;
    if (!frame) return;
    const onClick = (e: MouseEvent) => {
      const link = e.target instanceof Element ? e.target.closest<HTMLAnchorElement>("a[data-go]") : null;
      if (!link || !frame.contains(link) || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      if (!leaving) leave(frame, link, sea.current);
    };
    // the browser's back button may bring this page back as it was left, mid-departure
    const onShow = (e: PageTransitionEvent) => {
      if (!e.persisted) return;
      leaving = undefined;
      clear();
    };
    frame.addEventListener("click", onClick);
    window.addEventListener("pageshow", onShow);
    return () => {
      frame.removeEventListener("click", onClick);
      window.removeEventListener("pageshow", onShow);
      if (shown?.frame === frame) clear();
    };
  }, [root, sea]);
}
