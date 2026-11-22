// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OceanEngine } from "./ocean";
import { E, planTiles } from "./panel";
import { tiles } from "./tiles";
import { lerp, type RawWordTile, type WordCast } from "./word-tiles";

// jsdom has no Web Animations, which restoring a run's styles asks for (style-tracker.ts settle)
Element.prototype.getAnimations = () => [];

type StubCtx = Record<
  | "setTransform"
  | "clearRect"
  | "fillRect"
  | "fillText"
  | "drawImage"
  | "beginPath"
  | "roundRect"
  | "rect"
  | "arc"
  | "fill",
  ReturnType<typeof vi.fn>
> & { font: string; textAlign: string; textBaseline: string; globalAlpha: number; fillStyle: string };

type RectSpec = [number, number, number, number];
type Layout = {
  W: number;
  H: number;
  barH: number;
  hoBrand: RectSpec;
  lgBrand: RectSpec;
  h1Fs: number;
  h1Lh: number;
  h1X: number;
  h1W: number;
  ledeX: number;
  trig: RectSpec;
  side: RectSpec;
  seaBox: RectSpec;
  pitchH: RectSpec;
  pitchHFs: number;
  pitchHLh: number;
  pitchS: RectSpec;
};

const desktopLayout: Layout = {
  W: 1440,
  H: 900,
  barH: 72,
  hoBrand: [120, 23, 88, 26],
  lgBrand: [48, 23, 88, 26],
  h1Fs: 80,
  h1Lh: 84,
  h1X: 320,
  h1W: 600,
  ledeX: 420,
  trig: [240, 420, 180, 44],
  side: [806, 0, 634, 900],
  seaBox: [0, 0, 806, 900],
  pitchH: [48, 760, 320, 48],
  pitchHFs: 40,
  pitchHLh: 48,
  pitchS: [48, 820, 300, 26],
};

const phoneLayout: Layout = {
  W: 390,
  H: 844,
  barH: 56,
  hoBrand: [20, 15, 88, 26],
  lgBrand: [20, 15, 88, 26],
  h1Fs: 40,
  h1Lh: 44,
  h1X: 20,
  h1W: 350,
  ledeX: 20,
  trig: [20, 400, 350, 44],
  side: [0, 208, 390, 636],
  seaBox: [0, 0, 390, 208],
  pitchH: [20, 152, 320, 36],
  pitchHFs: 28,
  pitchHLh: 36,
  pitchS: [0, 0, 0, 0],
};

const domRect = ([left, top, width, height]: RectSpec): DOMRect =>
  ({
    x: left,
    y: top,
    left,
    top,
    right: left + width,
    bottom: top + height,
    width,
    height,
    toJSON: () => ({}),
  }) as DOMRect;

const setRect = (node: Element, spec: RectSpec) => {
  node.getBoundingClientRect = () => domRect(spec);
};

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className = "", text = "") {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text) node.textContent = text;
  return node;
}

function makeBrand(box: RectSpec) {
  const a = el("a", "brand");
  a.href = "/";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  a.append(svg, el("span", "", "Axiom"));
  setRect(a, box);
  setRect(svg, [box[0], box[1] + 3, 20, 20]);
  return a;
}

function makeDx(text: string, mode: "r" | "s", box: RectSpec) {
  const span = el("span", "", text);
  span.dataset.dx = mode;
  setRect(span, box);
  return span;
}

const makeCtx = (): StubCtx => ({
  setTransform: vi.fn(),
  clearRect: vi.fn(),
  fillRect: vi.fn(),
  fillText: vi.fn(),
  drawImage: vi.fn(),
  beginPath: vi.fn(),
  roundRect: vi.fn(),
  rect: vi.fn(),
  arc: vi.fn(),
  fill: vi.fn(),
  font: "",
  textAlign: "",
  textBaseline: "",
  globalAlpha: 1,
  fillStyle: "",
});

function buildHome(L: Layout, lower: boolean) {
  const ho = el("div", "ho light");
  const bar = el("header", "bar");
  setRect(bar, [0, 0, L.W, L.barH]);
  const navLink = el("a", "ghost");
  navLink.href = "/models";
  navLink.style.textDecorationLine = "underline";
  navLink.append(makeDx("模型", "r", [620, 26, 40, 20]));
  bar.append(makeBrand(L.hoBrand), navLink);

  const hs = el("div", "hs");
  const main = el("main", "main");
  const h1Top = lower ? -320 : 280;
  const h1 = el("h1", "lens");
  Object.assign(h1.style, { fontSize: `${L.h1Fs}px`, lineHeight: `${L.h1Lh}px` });
  setRect(h1, [L.h1X, h1Top, L.h1W, L.h1Lh]);
  const [c1, c2] = [el("span", "ch", "一"), el("span", "ch", "个")];
  setRect(c1, [L.h1X, h1Top, 60, 80]);
  setRect(c2, [L.h1X + 60, h1Top, 60, 80]);
  h1.append(c1, c2);

  const lede = el("p", "lens lede");
  Object.assign(lede.style, { fontSize: "16px", lineHeight: "26px" });
  setRect(lede, [L.ledeX, h1Top + 104, 340, 26]);
  lede.append(makeDx("每条线路实测", "r", [L.ledeX, h1Top + 104, 340, 26]));

  const cta = el("div", "cta lens");
  const trigger = el("a", "btn primary");
  trigger.href = "/console/register";
  trigger.setAttribute("data-go", "");
  setRect(trigger, L.trig);
  trigger.append(makeDx("注册", "r", [L.trig[0] + 20, L.trig[1] + 10, 80, 24]));

  const docs = el("a", "btn outline");
  docs.href = "/docs";
  setRect(docs, [L.trig[0] + 200, L.trig[1], 120, 44]);
  docs.append(makeDx("查看文档", "r", [L.trig[0] + 210, L.trig[1] + 10, 100, 24]));
  cta.append(trigger, docs);

  const fam = el("p", "fam");
  fam.append(makeDx("claude", "s", [300, 510, 60, 18]));
  main.append(el("div", "glow"), h1, lede, cta, fam);
  hs.append(main, el("div", "under"));
  ho.append(bar, hs, el("div", "isl"));
  return { ho, trigger };
}

function buildLogin(L: Layout) {
  const lg = el("div", "lg light");
  const lgSea = el("div", "lg-sea");
  setRect(lgSea, L.seaBox);

  const pitch = el("div", "lg-pitch");
  const pitchH = el("p", "h lens");
  Object.assign(pitchH.style, { fontSize: `${L.pitchHFs}px`, lineHeight: `${L.pitchHLh}px` });
  setRect(pitchH, L.pitchH);
  const pitchHT = el("span", "t");
  setRect(pitchHT, L.pitchH);
  const [pCh1, pCh2] = [el("span", "ch", "一"), el("span", "ch", "个")];
  setRect(pCh1, [48, 760, 40, 48]);
  setRect(pCh2, [88, 760, 40, 48]);
  pitchHT.append(pCh1, pCh2);
  pitchH.append(pitchHT);

  const pitchS = el("p", "s lens");
  Object.assign(pitchS.style, { fontSize: "16px", lineHeight: "26px" });
  setRect(pitchS, L.pitchS);
  const pitchST = el("span", "t", "每条线路实测");
  setRect(pitchST, L.pitchS);
  pitchS.append(pitchST);
  pitch.append(pitchH, pitchS);
  lgSea.append(makeBrand(L.lgBrand), pitch);

  const lgTop = el("nav", "lg-top");
  const langA = el("a");
  langA.href = "#";
  langA.append(makeDx("EN", "r", [L.W - 60, 20, 30, 20]));
  lgTop.append(langA);

  const lgSide = el("main", "lg-side");
  setRect(lgSide, L.side);
  const lgForm = el("div", "lg-form");
  const lgStep = el("div", "lg-step in");
  const lgTitle = el("h2", "lg-title");
  const [tCh1, tCh2] = [el("span", "ch", "登"), el("span", "ch", "录")];
  tCh1.style.fontSize = "40px";
  tCh2.style.fontSize = "40px";
  setRect(tCh1, [L.side[0] + 40, 220, 40, 48]);
  setRect(tCh2, [L.side[0] + 80, 220, 40, 48]);
  lgTitle.append(tCh1, tCh2);

  const lgSub = el("p", "lg-sub", "继续");
  const field = el("div", "lg-field");
  const input = el("input");
  input.id = "lg-id";
  input.type = "email";
  field.append(input);
  lgStep.append(lgTitle, lgSub, field);
  lgForm.append(lgStep);
  lgSide.append(lgForm);
  lg.append(lgSea, lgTop, lgSide);
  return { lg, lgSide, lgSub, input, tCh1, pCh1, pitchS };
}

// the run's plan as the driver lays it out for a scene
const box = ([x, y, w, h]: RectSpec) => ({ x, y, w, h });
const planOf = (L: { trig: RectSpec; side: RectSpec }) => planTiles(box(L.trig), box(L.side), { cw: 10, ch: 16 });

function buildScene(opts: { phone?: boolean; lower?: boolean; wordCast?: WordCast } = {}) {
  const L = opts.phone ? phoneLayout : desktopLayout;
  const root = el("div", "tr site d light");
  Object.defineProperty(root, "offsetWidth", { value: L.W, configurable: true });
  Object.defineProperty(root, "offsetHeight", { value: L.H, configurable: true });
  setRect(root, [0, 0, L.W, L.H]);
  if (opts.wordCast) {
    (root as HTMLElement & { __axCast?: { wordTiles: () => WordCast } }).__axCast = {
      wordTiles: () => opts.wordCast as WordCast,
    };
  }
  const home = buildHome(L, opts.lower ?? false);
  const login = buildLogin(L);
  root.append(home.ho, login.lg);
  document.body.append(root);

  const sea: OceanEngine & {
    world: { width: number; right: number | null; sharks: object[] };
    renderer: { cw: number; ch: number; visRight: number | null };
  } = {
    canvas: el("canvas"),
    renderer: { cw: 10, ch: 16, visRight: null },
    world: { width: L.W, right: null, sharks: [] },
    held: false,
    schedule: vi.fn(),
    tick: vi.fn(),
    setLight: vi.fn(),
    destroy: vi.fn(),
  };

  return { root, sea, L, ...home, ...login };
}

describe("tiles single-document transition driver", () => {
  const contexts = new Map<HTMLCanvasElement, StubCtx>();
  const rafs = new Map<number, FrameRequestCallback>();
  let nextRaf = 1;
  let now = 1000;
  let resizeCb: ResizeObserverCallback | undefined;

  const stepRaf = (advanceMs: number) => {
    now += advanceMs;
    const batch = [...rafs.entries()];
    rafs.clear();
    for (const [, fn] of batch) fn(now);
  };

  beforeEach(() => {
    document.body.innerHTML = "";
    contexts.clear();
    rafs.clear();
    nextRaf = 1;
    now = 1000;
    resizeCb = undefined;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(function (this: HTMLCanvasElement) {
      const ctx = contexts.get(this) ?? makeCtx();
      contexts.set(this, ctx);
      return ctx as unknown as CanvasRenderingContext2D;
    } as unknown as typeof HTMLCanvasElement.prototype.getContext);
    vi.stubGlobal("requestAnimationFrame", (fn: FrameRequestCallback) => {
      const id = nextRaf++;
      rafs.set(id, fn);
      return id;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => rafs.delete(id));
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        constructor(private cb: IntersectionObserverCallback) {}
        observe(target: Element) {
          this.cb([{ target, isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);
        }
        unobserve() {}
        disconnect() {}
      },
    );
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: ResizeObserverCallback) {
          resizeCb = cb;
        }
        observe() {}
        disconnect() {}
      },
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("runs forward to login, hides and makes .ho inert, moves focus, restores styles, and never steps sea", () => {
    const { root, ho, lg, sea, trigger, input, lgSide, lgSub, tCh1 } = buildScene();
    const end = vi.fn();
    const tr = tiles({ root, ho, lg, sea }, { end });

    expect([tr.state, root.dataset.state, lg.style.visibility, lg.inert]).toEqual(["home", "home", "hidden", true]);

    trigger.focus();
    tr.run(1, trigger);
    expect([tr.state, tr.dir, root.dataset.state, ho.inert, lg.inert]).toEqual(["run", 1, "run", false, false]);

    const ui = root.querySelector<HTMLCanvasElement>("canvas.ui");
    const fly = root.querySelector<HTMLElement>(".fly");
    const fb = fly?.querySelector<HTMLElement>(".fb");
    expect([ui?.style.zIndex, ui?.style.pointerEvents, fly?.style.zIndex, fly?.style.pointerEvents]).toEqual([
      "3",
      "none",
      "4",
      "none",
    ]);
    expect(fb?.style.pointerEvents).toBe("auto");

    tr.seek(300);
    expect([document.activeElement, tCh1.style.color, lgSub.style.opacity]).toEqual([trigger, "transparent", "0"]);

    for (let i = 0; i < 80 && tr.state === "run"; i++) stepRaf(25);

    expect([tr.state, root.dataset.state, ho.style.visibility, ho.inert, lg.style.visibility, lg.inert]).toEqual([
      "login",
      "login",
      "hidden",
      true,
      "",
      false,
    ]);
    expect([document.activeElement, root.querySelector(".fly"), root.querySelector("canvas.ui")]).toEqual([
      input,
      null,
      null,
    ]);
    expect([lgSide.style.backgroundColor, lgSub.style.opacity, tCh1.style.color]).toEqual(["", "", ""]);
    expect([sea.world.right, sea.renderer.visRight]).toEqual([806, 806]);
    expect(sea.tick).not.toHaveBeenCalled();
    expect(end).toHaveBeenCalledWith("login");
    tr.destroy();
  });

  it("turns midway (via flying brand click and turn()) back to home with byte-identical .ho and .lg outerHTML", () => {
    const { root, ho, lg, sea, trigger } = buildScene();
    const end = vi.fn();
    const tr = tiles({ root, ho, lg, sea }, { end });
    const [hoBefore, lgBefore] = [ho.outerHTML, lg.outerHTML];

    trigger.focus();
    tr.run(1, trigger);
    tr.seek(350);
    root.querySelector<HTMLElement>(".fly .fb")?.click();
    expect([tr.dir, tr.state]).toEqual([-1, "run"]);

    tr.seek(2000);
    expect([tr.state, root.dataset.state, document.activeElement]).toEqual(["home", "home", trigger]);
    expect([root.querySelector(".fly"), root.querySelector("canvas.ui")]).toEqual([null, null]);
    expect([sea.world.right, sea.renderer.visRight]).toEqual([null, null]);
    expect(end).toHaveBeenLastCalledWith("home");
    expect(ho.outerHTML).toBe(hoBefore);
    expect(lg.outerHTML).toBe(lgBefore);

    // Full forward -> reverse -> double-turn -> home also leaves outerHTML byte-identical.
    tr.run(1, trigger);
    tr.seek(2000);
    tr.run(-1);
    tr.seek(400);
    tr.turn();
    expect(tr.dir).toBe(1);
    tr.turn();
    expect(tr.dir).toBe(-1);
    tr.seek(2000);
    expect(tr.state).toBe("home");
    expect(ho.outerHTML).toBe(hoBefore);
    expect(lg.outerHTML).toBe(lgBefore);
    tr.destroy();
  });

  it("matches prototype seaTo world.right and ground visRight values across seek() timestamps", () => {
    const { root, ho, lg, sea, trigger, lgSide, L } = buildScene();
    const sx = L.side[0];
    const { off } = planOf(L);
    const tr = tiles({ root, ho, lg, sea }, { end: vi.fn() });

    tr.run(1, trigger);
    tr.seek(120 + off);
    expect([sea.world.right, sea.renderer.visRight, lgSide.style.backgroundColor]).toEqual([null, null, "transparent"]);

    tr.seek(120 + off + 320);
    expect(sea.world.right).toBeCloseTo(lerp(L.W, sx, E.std(0.5)), 5);
    expect(sea.renderer.visRight).toBeNull();

    tr.seek(759 + off);
    expect([sea.renderer.visRight, lgSide.style.backgroundColor]).toEqual([null, "transparent"]);

    tr.seek(760 + off);
    expect(sea.world.right).toBeCloseTo(sx, 5);
    expect(sea.renderer.visRight).toBe(sx);
    expect(lgSide.style.backgroundColor).not.toBe("transparent");

    tr.seek(2000);
    expect([sea.world.right, sea.renderer.visRight]).toEqual([sx, sx]);
    tr.destroy();
  });

  it("keeps sea wall null on phone layouts and fades the hidden destination lede", () => {
    const { root, ho, lg, sea, trigger } = buildScene({ phone: true });
    const tr = tiles({ root, ho, lg, sea }, { end: vi.fn() });

    tr.run(1, trigger);
    tr.seek(450);
    expect([sea.world.right, sea.renderer.visRight]).toEqual([null, null]);
    expect(Number(root.querySelector<HTMLElement>(".fly .fs")?.style.opacity)).toBeLessThan(1);

    tr.seek(2000);
    expect([tr.state, sea.world.right, sea.renderer.visRight]).toEqual(["login", null, null]);
    tr.destroy();
  });

  it("lasts until the question's last line is whole, rather than cut it off at the tiles' end", () => {
    const { root, ho, lg, sea, trigger, L } = buildScene();
    const { T } = planOf(L);
    // registering's question: a line under the title, three labels and fields, the button and a note
    const last = document.createElement("p");
    lg.querySelector(".lg-step")?.append(...Array.from({ length: 5 }, () => document.createElement("p")), last);
    const tr = tiles({ root, ho, lg, sea }, { end: vi.fn() });

    tr.run(1, trigger);
    tr.seek(T);
    expect([tr.state, Number(last.style.opacity) < 1]).toEqual(["run", true]);
    tr.seek(2000);
    expect([tr.state, last.style.opacity]).toEqual(["login", ""]);
    tr.destroy();
  });

  it("handles lower scroll pitch decode, seabed wordmark tiles, and incremental offscreen baking", () => {
    const rawTiles: RawWordTile[] = Array.from({ length: 10 }, (_, i) => ({
      x: 100 + i * 12,
      y: 720,
      w: 10,
      h: 10,
      m: 0.9,
      li: i % 5,
    }));
    const wordCast: WordCast = {
      tiles: rawTiles,
      tile: "#888888",
      ink: "#111111",
      gf: 8,
      hide: vi.fn(),
      show: vi.fn(),
    };
    const { root, ho, lg, sea, trigger, pCh1, pitchS } = buildScene({ lower: true, wordCast });
    const tr = tiles({ root, ho, lg, sea }, { end: vi.fn() });

    tr.run(1, trigger);
    expect(wordCast.hide).toHaveBeenCalledTimes(1);
    expect([root.querySelector(".fly .fh"), root.querySelector(".fly .fs")]).toEqual([null, null]);

    const ui = root.querySelector<HTMLCanvasElement>("canvas.ui");
    const uiCtx = contexts.get(ui as HTMLCanvasElement);
    tr.seek(650);
    expect([pCh1.style.color, pitchS.style.opacity]).toEqual(["transparent", "0"]);
    const bakeCtx = [...contexts.entries()].find(([cv]) => cv !== ui)?.[1] as StubCtx;
    const bakedAt650 = bakeCtx.fillRect.mock.calls.length;
    expect(bakedAt650).toBeGreaterThan(0);
    expect(uiCtx?.drawImage).toHaveBeenCalled();

    tr.seek(650);
    expect(bakeCtx.fillRect.mock.calls.length).toBe(bakedAt650);

    tr.seek(2000);
    expect([tr.state, pCh1.style.color, pitchS.style.opacity]).toEqual(["login", "", ""]);
    expect(wordCast.show).toHaveBeenCalledTimes(1);
    tr.destroy();
  });

  it("supports land(), settle() on resize during login or run, and full cleanup on destroy()", () => {
    const { root, ho, lg, sea, lgSide, input } = buildScene();
    const [hoInitial, lgInitial] = [ho.outerHTML, lg.outerHTML];
    const end = vi.fn();
    const tr = tiles({ root, ho, lg, sea }, { end });

    tr.land();
    expect([tr.state, root.dataset.state, ho.style.visibility, ho.inert, document.activeElement]).toEqual([
      "login",
      "login",
      "hidden",
      true,
      input,
    ]);
    expect([sea.world.right, sea.renderer.visRight]).toEqual([806, 806]);
    expect(end).toHaveBeenCalledWith("login");

    Object.defineProperty(root, "offsetWidth", { value: 1200, configurable: true });
    setRect(root, [0, 0, 1200, 900]);
    setRect(lgSide, [640, 0, 560, 900]);
    resizeCb?.([], {} as ResizeObserver);
    expect([sea.world.right, sea.renderer.visRight]).toEqual([640, 640]);

    tr.run(-1);
    tr.seek(300);
    tr.settle();
    expect([tr.state, root.querySelector(".fly"), sea.world.right, sea.renderer.visRight]).toEqual([
      "home",
      null,
      null,
      null,
    ]);

    tr.destroy();
    expect(root.hasAttribute("data-state")).toBe(false);
    expect(ho.outerHTML).toBe(hoInitial);
    expect(lg.outerHTML).toBe(lgInitial);
  });
});
