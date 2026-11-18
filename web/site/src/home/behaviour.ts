import { reducedMotion } from "@axiom/fx";
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
// down. offsets are where the sections start.
function scrolled(scroller: HTMLElement, offsets: number[]): Scroll & { dim: number } {
  const y = scroller.scrollTop;
  const view = scroller.clientHeight;
  const down = Math.min(1, y / Math.max(1, scroller.scrollHeight - view));
  const probe = y + view * 0.35;
  let i = 0;
  while (i < offsets.length - 1 && (offsets[i + 1] ?? 0) <= probe) i++;
  const start = offsets[i] ?? 0;
  const end = i < offsets.length - 1 ? (offsets[i + 1] ?? 0) : start + view;
  const k = Math.min(1, Math.max(0, (probe - start) / Math.max(1, end - start)));
  const last = sections.length - 1;
  const from = sections[Math.min(i, last)]?.depth ?? 0;
  const to = sections[Math.min(i + 1, last)]?.depth ?? from;
  return {
    solid: y > 8,
    sec: Math.min(i, last),
    depth: i >= last ? from : Math.round(from + (to - from) * k),
    bottom: down > 0.985,
    dim: 0.42 * down ** 1.3,
  };
}

// scrolling is the frame's scroller: where its sections start, what scrolling it says, and gliding to the top (native
// smooth scrolling, not the board's eased frames; a jump under reduced motion). Gliding to a section and the tour
// through them come with the sections, in S2.
function scrolling(scroller: HTMLElement, depth: HTMLElement | null, show: (scroll: Scroll) => void) {
  let offsets = [0];
  return {
    measure: () => {
      offsets = [0, ...[...scroller.querySelectorAll<HTMLElement>(".under > .hfm")].map((s) => s.offsetTop)];
    },
    onScroll: () => {
      const { dim, ...scroll } = scrolled(scroller, offsets);
      if (depth) depth.style.opacity = dim.toFixed(3);
      show(scroll);
    },
    toTop: () => scroller.scrollTo({ top: 0, behavior: reducedMotion() ? "auto" : "smooth" }),
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
    ["[data-go]", () => setMenu(false)],
  ];
}

const islandEvents = [
  ["mouseenter", true],
  ["mouseleave", false],
  ["focusin", true],
  ["focusout", false],
] as const;

// useFrameBehaviour runs the board's own behaviours on the frame root points at: the header and the island while
// scrolling, the island opening under the pointer or the keyboard (wide layout) and the menu sheet.
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
    resized.observe(scroller);
    scroller.addEventListener("scroll", page.onScroll, { passive: true });
    frame.addEventListener("click", onClick);
    for (const [type, listener] of hovers) isl?.addEventListener(type, listener);
    return () => {
      resized.disconnect();
      scroller.removeEventListener("scroll", page.onScroll);
      frame.removeEventListener("click", onClick);
      for (const [type, listener] of hovers) isl?.removeEventListener(type, listener);
    };
  }, [root, wide]);
  return { scroll, island, menu };
}

const loadDepart = () => import("@axiom/fx/depart");
// The departure's length on the wall clock: the page goes this long after the press, whether or not its code has come.
const departSpan = 500;
// A navigation not done this long after it started was stopped (the browser's stop button) or failed, and the page
// that had left is put back, so a press can try again. A page that does go is gone long before.
const stalled = 3000;

// The press under way, and the picture of it a frame draws. The press is the module's, not a frame's: a change of
// layout midway starts the frame afresh (home.tsx), which takes the picture with it but must not take the way there.
type Press = { frame?: HTMLElement; picture?: Departure };
let leaving: Press | undefined;

function settle() {
  leaving?.picture?.destroy();
  leaving = undefined;
}

function leave(frame: HTMLElement, link: HTMLAnchorElement, sea: Sea | null) {
  const press: Press = {};
  leaving = press;
  const start = performance.now();
  const href = link.href;
  window.setTimeout(() => {
    if (leaving !== press) return;
    window.location.assign(href);
    window.setTimeout(() => leaving === press && settle(), stalled);
  }, departSpan);
  // code that comes after the frame has gone, or after the page has gone back, draws nothing; code that never comes
  // only costs the picture
  loadDepart().then(
    ({ depart }) => {
      if (leaving !== press || !frame.isConnected || !link.isConnected) return;
      press.frame = frame;
      press.picture = depart(frame, link, start, departSpan, sea?.engine);
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
      if (e.persisted) settle();
    };
    frame.addEventListener("click", onClick);
    window.addEventListener("pageshow", onShow);
    return () => {
      frame.removeEventListener("click", onClick);
      window.removeEventListener("pageshow", onShow);
      if (leaving?.frame !== frame) return;
      leaving.picture?.destroy();
      leaving.picture = undefined;
    };
  }, [root, sea]);
}
