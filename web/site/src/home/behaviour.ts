import { onMotion, reducedMotion } from "@axiom/fx";
import type { Departure } from "@axiom/fx/depart";
import type { Sea } from "@axiom/fx/sea";
import { type RefObject, useEffect, useState } from "react";
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
// section (native smooth scrolling, not the board's eased frames; a jump under reduced motion), and the tour, which
// glides through the sections a few seconds apart and back to the top. The page moving the reader on its own (the tour's
// next glide, a glide to a section followed while the screens above it grow, a glide under way) is the press's that
// started it: a glide to the top or to a section, and stop, end it; stop also halts a glide where it is.
// The screens are measured by their places (screens.tsx), which are there before their code and carry their section.
const places = (scroller: HTMLElement) => [...scroller.querySelectorAll<HTMLElement>(".under [data-place]")];
const measured = (scroller: HTMLElement): Offset[] => [
  { sec: 0, top: 0 },
  ...places(scroller).map((s) => ({ sec: Number(s.dataset.place), top: s.offsetTop })),
];
function scrolling(scroller: HTMLElement, depth: HTMLElement | null, show: (scroll: Scroll) => void) {
  let offsets: Offset[] = [{ sec: 0, top: 0 }];
  let timer = 0;
  // a glide the page started is under way (until the browser says the scroll has ended)
  let gliding = false;
  // the section a glide is taking the reader to, and where it was when the glide set out
  let following: { sec: number; top: number } | undefined;
  const glide = (top: number) => {
    gliding = true;
    scroller.scrollTo({ top, behavior: reducedMotion() ? "auto" : "smooth" });
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
      if (depth) depth.style.opacity = dim.toFixed(3);
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
      if (reducedMotion()) return;
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

// The reader taking over: a wheel, a touch, a press or a key anywhere on the frame. A press that starts a glide (the tour,
// the island's mark, a section of the menu) comes after it, so it starts afresh.
const takeOver = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

// useFrameBehaviour runs the board's own behaviours on the frame root points at: the header and the island while
// scrolling, the island opening under the pointer or the keyboard (wide layout) and the menu sheet. The page stops
// moving the reader once the reader takes over, the page is hidden or reduced motion is asked for.
export function useFrameBehaviour(root: RefObject<HTMLElement | null>, wide: boolean) {
  const [scroll, setScroll] = useState(atTop);
  const [island, setIsland] = useState(false);
  const [menu, setMenu] = useState(false);
  useEffect(() => {
    const frame = root.current;
    const scroller = frame?.querySelector<HTMLElement>(".hs");
    if (!frame || !scroller) return;
    const page = scrolling(scroller, frame.querySelector(".ho > .depth"), (next) =>
      setScroll((prev) => (same(prev, next) ? prev : next)),
    );
    const clicks = clickActions(page, setMenu);
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
    frame.addEventListener("click", onClick);
    for (const type of takeOver) frame.addEventListener(type, page.stop, { capture: true, passive: true });
    const onHidden = () => document.hidden && page.stop();
    document.addEventListener("visibilitychange", onHidden);
    const stopMotion = onMotion((allowed) => allowed || page.stop());
    for (const [type, listener] of hovers) isl?.addEventListener(type, listener);
    return () => {
      resized.disconnect();
      page.stop();
      stopMotion();
      document.removeEventListener("visibilitychange", onHidden);
      for (const type of takeOver) frame.removeEventListener(type, page.stop, { capture: true });
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
// press. Under reduced motion, and for a press that asks for another tab or window, the link goes as a link would.
export function useDeparture(root: RefObject<HTMLElement | null>, sea: RefObject<Sea | null>) {
  useEffect(() => {
    const frame = root.current;
    if (!frame) return;
    const onClick = (e: MouseEvent) => {
      const link = e.target instanceof Element ? e.target.closest<HTMLAnchorElement>("a[data-go]") : null;
      if (!link || !frame.contains(link) || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      if (reducedMotion()) return;
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
