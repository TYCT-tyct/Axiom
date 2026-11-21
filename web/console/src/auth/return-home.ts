// Return transition from any console Login-board auth page (.lg) back to the website (/) via Escape, clicking the
// Axiom mark (.lg .brand), or browser Back, reversible midway (port design §9, trans.js.txt STYLES.tiles reverse).
import { markReturn, type OriginRect, takeOrigin } from "@axiom/fx";
import type { Departure } from "@axiom/fx/depart";
import { type RefObject, useEffect, useRef } from "react";

const loadDepart = () => import("@axiom/fx/depart");
// Matches the website's forward departure length on the wall clock.
const returnSpan = 500;
const stalled = 3000;

type Way = "link" | "escape" | "back";
type BackState = { __axiomBack?: "leave" | "stay" };
type InFlight = {
  token: object;
  frame: HTMLElement;
  via: Way;
  dir: 1 | -1;
  timer: number;
};

let claimed = false;
let savedOrigin: OriginRect | null = null;
let sentinelReady = false;
let leaving: InFlight | undefined;
let shown: { frame: HTMLElement; picture: Departure } | undefined;
let popSelf = false;
let popTimer = 0;

function clearPicture() {
  shown?.picture.destroy();
  shown = undefined;
}

function ignoreOwnPop() {
  popSelf = true;
  window.clearTimeout(popTimer);
  popTimer = window.setTimeout(() => {
    popSelf = false;
  }, 1200);
}

// When the visitor arrived from the website, keep a same-document history entry behind this page so browser Back
// fires popstate and plays the return tiles transition instead of unloading immediately.
function installSentinel() {
  if (claimed) return;
  claimed = true;
  savedOrigin = takeOrigin();
  const state = (history.state ?? {}) as BackState;
  if (state.__axiomBack === "stay") {
    sentinelReady = true;
    return;
  }
  if (!savedOrigin) return;
  try {
    history.replaceState({ ...state, __axiomBack: "leave" }, "");
    history.pushState({ ...state, __axiomBack: "stay" }, "");
    sentinelReady = true;
  } catch {
    // sandboxed history: Esc and brand click still play the transition and navigate via location.assign
  }
}

function goHome(via: Way) {
  markReturn();
  const state = (history.state ?? {}) as BackState;
  if (via === "back") {
    ignoreOwnPop();
    history.back();
    return;
  }
  if (sentinelReady && state.__axiomBack === "stay") {
    ignoreOwnPop();
    history.go(-2);
    return;
  }
  window.location.assign("/");
}

function scheduleCommit(flight: InFlight, delay: number) {
  window.clearTimeout(flight.timer);
  flight.timer = window.setTimeout(() => {
    if (leaving !== flight) return;
    leaving = undefined;
    const held = shown;
    goHome(flight.via);
    window.setTimeout(() => shown === held && clearPicture(), stalled);
  }, delay);
}

function startReturn(frame: HTMLElement, via: Way) {
  clearPicture();
  const start = performance.now();
  const flight: InFlight = { token: {}, frame, via, dir: 1, timer: 0 };
  leaving = flight;
  scheduleCommit(flight, returnSpan);
  loadDepart().then(
    ({ departLogin }) => {
      if (leaving !== flight || !frame.isConnected) return;
      shown = { frame, picture: departLogin(frame, savedOrigin, start, returnSpan) };
    },
    () => {},
  );
}

function reverseReturn(fromPop = false) {
  const flight = leaving;
  if (!flight) return;
  window.clearTimeout(flight.timer);
  const picture = shown?.picture;
  if (flight.dir === 1) {
    if (flight.via === "back" && !fromPop) {
      ignoreOwnPop();
      history.forward();
    }
    if (!picture) {
      leaving = undefined;
      return;
    }
    flight.dir = -1;
    picture.reverse((end) => {
      if (end === -1 && leaving === flight) {
        leaving = undefined;
        clearPicture();
      }
    });
    return;
  }
  if (!picture) return;
  flight.dir = 1;
  scheduleCommit(flight, Math.max(16, Math.round(picture.remaining())));
  picture.reverse();
}

const plainClick = (e: MouseEvent) => e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;

function handleClick(frame: HTMLElement | null, locked: boolean, e: MouseEvent) {
  if (!frame || locked || !plainClick(e)) return;
  const target = e.target instanceof Element ? e.target : null;
  if (!target || !frame.contains(target)) return;
  if (target.closest("a.brand")) {
    e.preventDefault();
    if (!leaving) startReturn(frame, "link");
    else reverseReturn();
    return;
  }
  // clicking back on the form panel midway turns the return transition around onto the sign-in page
  if (leaving?.dir === 1 && target.closest(".lg-side")) {
    e.preventDefault();
    reverseReturn();
  }
}

function handleKey(frame: HTMLElement | null, locked: boolean, e: KeyboardEvent) {
  if (!frame || locked || e.key !== "Escape" || e.defaultPrevented) return;
  const field =
    e.target instanceof Element ? e.target.closest<HTMLInputElement | HTMLTextAreaElement>("input, textarea") : null;
  if (field?.value) return;
  e.preventDefault();
  if (!leaving) startReturn(frame, "escape");
  else reverseReturn();
}

function handlePop(frame: HTMLElement | null, locked: boolean) {
  if (popSelf) {
    popSelf = false;
    window.clearTimeout(popTimer);
    return;
  }
  const tag = (history.state as BackState | null)?.__axiomBack;
  if (tag === "stay") {
    if (leaving?.dir === 1) reverseReturn(true);
    return;
  }
  if (tag !== "leave") return;
  if (locked || !frame) {
    ignoreOwnPop();
    history.back();
    return;
  }
  if (!leaving) startReturn(frame, "back");
  else if (leaving.dir === -1) reverseReturn(true);
}

// useReturnHome wires Escape (unless typing in a non-empty input/textarea), clicking .lg .brand, and browser Back on
// a Login-board page to play the reversible return tiles transition back to /.
export function useReturnHome(root: RefObject<HTMLElement | null>, locked: boolean) {
  const isLocked = useRef(locked);
  isLocked.current = locked;
  useEffect(() => {
    installSentinel();
  }, []);
  useEffect(() => {
    const onClick = (e: MouseEvent) => handleClick(root.current, isLocked.current, e);
    const onKey = (e: KeyboardEvent) => handleKey(root.current, isLocked.current, e);
    const onPop = () => handlePop(root.current, isLocked.current);
    window.addEventListener("click", onClick);
    window.addEventListener("keydown", onKey);
    window.addEventListener("popstate", onPop);
    return () => {
      window.removeEventListener("click", onClick);
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("popstate", onPop);
      if (leaving?.frame === root.current) {
        window.clearTimeout(leaving.timer);
        leaving = undefined;
      }
      if (shown?.frame === root.current) clearPicture();
    };
  }, [root]);
}
