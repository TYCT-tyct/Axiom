// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startClawd } from "./clawd";
import { water } from "./water";

// Unit tests for Clawd's four review blockers:
// 1. White-label site names whose 5th character has no glyph tiles (e.g. "Acme Relay") skip that letter without throwing.
// 2. Sea-floor letter actions register only when the floor can run them (never for <5-char names) and settle when the
//    floor leaves the viewport so the whale's turn is never held forever.
// 3. Typing "claude", clicking a floor letter, and clicking the strip all share the whaleOut() exclusion and keep the
//    pending action until the whale leaves (interleaving test).

let intersectionCb: IntersectionObserverCallback | undefined;
const observers: { cb: IntersectionObserverCallback; targets: Set<Element> }[] = [];
let rAFs = new Map<number, FrameRequestCallback>();
let nextId = 1;
let nowMs = 1000;

function intersectAll(target: Element, isIntersecting: boolean) {
  for (const obs of observers) {
    if (obs.targets.has(target)) {
      obs.cb([{ target, isIntersecting } as IntersectionObserverEntry], {} as IntersectionObserver);
    }
  }
}

function stepFrame(ms = 16) {
  nowMs += ms;
  const fns = [...rAFs.values()];
  rAFs = new Map();
  for (const fn of fns) fn(nowMs);
}

function makeRect(top: number, height: number, left = 0, width = 1000): DOMRect {
  return {
    top,
    left,
    width,
    height,
    right: left + width,
    bottom: top + height,
    x: left,
    y: top,
    toJSON: () => ({}),
  };
}

function makeBedInfo(letters: { a: number; b: number }[], filledLetters: Set<number>) {
  const cols = 50;
  const rows = 20;
  const CW = 10;
  const CH = 10;
  const colL = new Int16Array(cols).fill(-1);
  const mask = new Float32Array(cols * rows);
  for (let li = 0; li < letters.length; li++) {
    const startCol = li * 8;
    for (let c = startCol; c < Math.min(cols, startCol + 6); c++) {
      colL[c] = li;
      if (filledLetters.has(li)) {
        for (let r = 2; r < 10; r++) mask[r * cols + c] = 0.9;
      }
    }
  }
  return {
    W: 500,
    Hh: 200,
    t: 1,
    topAt: () => 160,
    cols,
    rows,
    ox: 0,
    CW,
    CH,
    GF: 8,
    mask,
    colL,
    iGap: 4,
    letters,
    base: 150,
    tile: "#43484e",
    ink: "#b0b4ba",
  };
}

function makeFrame(artTop = 600) {
  const root = document.createElement("div");
  root.className = "tr";
  Object.defineProperty(root, "offsetWidth", { configurable: true, value: 1440 });
  Object.defineProperty(root, "offsetHeight", { configurable: true, value: 900 });
  root.getBoundingClientRect = () => makeRect(0, 900, 0, 1440);

  const cw = document.createElement("canvas");
  cw.className = "cw";
  const wh = document.createElement("canvas");
  wh.className = "wh";
  const ho = document.createElement("div");
  ho.className = "ho";
  const hs = document.createElement("div");
  hs.className = "hs";
  const main = document.createElement("main");
  main.className = "main";
  const fam = document.createElement("div");
  fam.className = "fam";
  const claude = document.createElement("span");
  claude.dataset.egg = "claude";
  fam.append(claude);
  main.append(fam);

  const art = document.createElement("canvas");
  art.className = "art";
  const pos = { top: artTop };
  art.getBoundingClientRect = () => makeRect(pos.top, 200, 200, 500);

  hs.append(main, art);
  ho.append(hs);
  root.append(cw, wh, ho);
  document.body.append(root);
  return { root, cw, main, art, pos };
}

beforeEach(() => {
  observers.length = 0;
  intersectionCb = undefined;
  rAFs = new Map();
  nextId = 1;
  nowMs = 1000;
  water.whale = null;
  water.clawd = null;
  vi.spyOn(performance, "now").mockImplementation(() => nowMs);
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    const id = nextId++;
    rAFs.set(id, cb);
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    rAFs.delete(id);
  });
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      private entry: { cb: IntersectionObserverCallback; targets: Set<Element> };
      constructor(cb: IntersectionObserverCallback) {
        intersectionCb = cb;
        this.entry = { cb, targets: new Set() };
        observers.push(this.entry);
      }
      observe(el: Element) {
        this.entry.targets.add(el);
      }
      unobserve(el: Element) {
        this.entry.targets.delete(el);
      }
      disconnect() {
        this.entry.targets.clear();
      }
    },
  );
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  const ctx = {
    setTransform: () => {},
    clearRect: () => {},
    fillRect: () => {},
    fillText: () => {},
    save: () => {},
    restore: () => {},
    beginPath: () => {},
    moveTo: () => {},
    lineTo: () => {},
    closePath: () => {},
    rect: () => {},
    clip: () => {},
    globalAlpha: 1,
    fillStyle: "",
    font: "",
    textAlign: "center",
    textBaseline: "middle",
    shadowColor: "",
    shadowBlur: 0,
  } as unknown as CanvasRenderingContext2D;
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(ctx);
});

afterEach(() => {
  document.body.innerHTML = "";
  water.whale = null;
  water.clawd = null;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const fiveLetters = [
  { a: 0, b: 60 },
  { a: 80, b: 140 },
  { a: 160, b: 220 },
  { a: 240, b: 300 },
  { a: 320, b: 380 },
];

describe("Clawd cast guards", () => {
  it("skips a letter with no glyph tiles (e.g. the 5th-char space in 'Acme Relay') without throwing", () => {
    const { root, art } = makeFrame(600);
    const clawd = startClawd(root, { engine: () => undefined, dark: () => true, phone: false });
    clawd.floor(art);
    const fx = (
      art as HTMLCanvasElement & {
        __fx: {
          skip: Set<number>;
          pre: (c: CanvasRenderingContext2D, i: object) => void;
          post: (c: CanvasRenderingContext2D, i: object) => void;
        };
      }
    ).__fx;
    const ctx = art.getContext("2d") as CanvasRenderingContext2D;
    // Letters 0..3 have tiles ("Acme"); letter 4 is a space (" ") with zero tiles.
    const info = makeBedInfo(fiveLetters, new Set([0, 1, 2, 3]));
    fx.pre(ctx, info);

    clawd.call();
    // Advance past every staggered ripple timer (300 + 4*1500 = 6300 ms) and draw post() each time.
    for (let i = 0; i < 30; i++) {
      nowMs += 300;
      info.t += 0.3;
      expect(() => {
        fx.pre(ctx, info);
        fx.post(ctx, info);
      }).not.toThrow();
    }
    // Letter 4 was never added to skip, while letters 0..3 morphed cleanly.
    expect(fx.skip.has(4)).toBe(false);
    clawd.destroy();
  });

  it("does not hold the whale's turn for a short site name (<5 letters) or after the floor leaves view", () => {
    const { root, art, pos } = makeFrame(600);
    const clawd = startClawd(root, { engine: () => undefined, dark: () => true, phone: false });
    clawd.floor(art);
    intersectAll(art, true);

    // Short site name ("ABC"): bed-script never calls pre(), so cast.bed.info is null.
    clawd.call();
    expect(water.clawd?.busy()).toBe(false);

    // Normal 5-letter site: floor is ready and ripple makes Clawd busy.
    const fx = (
      art as HTMLCanvasElement & {
        __fx: { skip: Set<number>; pre: (c: CanvasRenderingContext2D, i: object) => void };
      }
    ).__fx;
    const ctx = art.getContext("2d") as CanvasRenderingContext2D;
    const info = makeBedInfo(fiveLetters, new Set([0, 1, 2, 3, 4]));
    fx.pre(ctx, info);
    clawd.call();
    expect(water.clawd?.busy()).toBe(true);

    // Trigger first morph so morphs & skip are populated too.
    nowMs += 400;
    info.t += 0.4;
    fx.pre(ctx, info);
    expect(fx.skip.size).toBeGreaterThan(0);

    // Scroll away so the floor leaves the viewport: actions settle immediately and whale is unblocked.
    pos.top = 1500;
    intersectAll(art, false);
    expect(water.clawd?.busy()).toBe(false);
    expect(fx.skip.size).toBe(0);

    clawd.destroy();
  });

  it("interleaves whale and sea-floor Clawd triggers through one shared exclusion check", () => {
    const { root, cw, art } = makeFrame(600);
    const clawd = startClawd(root, { engine: () => undefined, dark: () => true, phone: false });
    clawd.floor(art);
    intersectAll(art, true);
    intersectAll(cw, true);

    const fx = (
      art as HTMLCanvasElement & {
        __fx: { skip: Set<number>; pre: (c: CanvasRenderingContext2D, i: object) => void };
      }
    ).__fx;
    const ctx = art.getContext("2d") as CanvasRenderingContext2D;
    const info = makeBedInfo(fiveLetters, new Set([0, 1, 2, 3, 4]));
    fx.pre(ctx, info);

    // Whale arrives first.
    let whaleActive = true;
    water.whale = { wh: { active: () => whaleActive }, active: () => whaleActive };

    // 1. Typing "claude" at the sea floor while the whale is out must NOT start ripple yet.
    clawd.call();
    nowMs += 1000;
    info.t += 1;
    fx.pre(ctx, info);
    expect(fx.skip.size).toBe(0);

    // 2. Clicking a floor letter while the whale is out must NOT morph immediately either (replaces pending action).
    root.dispatchEvent(new MouseEvent("click", { clientX: 290, clientY: 650, bubbles: true }));
    nowMs += 500;
    info.t += 0.5;
    fx.pre(ctx, info);
    expect(fx.skip.size).toBe(0);

    // Whale leaves -> on the next frame, the pending letter action runs.
    whaleActive = false;
    stepFrame(16);
    expect(fx.skip.has(1)).toBe(true);

    clawd.destroy();
  });

  it("clears stale floor info when reset() is called (e.g. site name shortens to <5 chars)", () => {
    const { root, art } = makeFrame(600);
    const clawd = startClawd(root, { engine: () => undefined, dark: () => true, phone: false });
    clawd.floor(art);
    const fx = (
      art as HTMLCanvasElement & {
        __fx: { pre: (c: CanvasRenderingContext2D, i: object) => void; reset?: () => void };
      }
    ).__fx;
    fx.pre(art.getContext("2d") as CanvasRenderingContext2D, makeBedInfo(fiveLetters, new Set([0, 1, 2, 3, 4])));
    expect(typeof fx.reset).toBe("function");
    fx.reset?.();
    clawd.call();
    expect(water.clawd?.busy()).toBe(false);
    expect(intersectionCb).toBeDefined();
    clawd.destroy();
  });
});
