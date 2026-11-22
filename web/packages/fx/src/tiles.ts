// Single-document tiles transition driver (trans.js.txt STYLES.tiles + Trans): moves between the homepage (.ho) and
// the Login board (.lg) inside one .tr frame over the website's shared sea. The sea engine stays driven by the website
// loop (never stepped here); this driver only eases world.right and renderer.visRight to the panel edge.
import { type Flyers, mountFlyers, type SharedRefs } from "./flyers";
import { mono } from "./mono";
import type { OceanEngine } from "./ocean";
import {
  drawPitchDecode,
  drawTitle,
  type HeroSlot,
  type Kid,
  type LgSlot,
  measureHeroSlots,
  measureKids,
  measureLgSlots,
  measurePitchCells,
  measureTitleCells,
  type PitchCell,
  paintHeroSlots,
  paintKids,
  paintLgSlots,
  type TrackedTitleCell,
  type WindowEval,
} from "./page-write";
import {
  type Bake,
  type Box,
  type Clock,
  createBake,
  drawBakedCells,
  drawButton,
  drive,
  E,
  type Grid,
  gridOf,
  type Ink,
  inkOf,
  panel,
  planTiles,
  type TilePlan,
} from "./panel";
import { restore as restoreSlots } from "./pen";
import {
  type PageInit,
  restorePageInit,
  type StyleTracker,
  setPageHidden,
  snapshotPage,
  styleTracker,
} from "./style-tracker";
import { clamp, drawWord, lerp } from "./word-tiles";

export type TilesScene = {
  root: HTMLElement; // .tr: holds .ho and .lg as children, its sea canvas covering it
  ho: HTMLElement; // the homepage
  lg: HTMLElement; // the Login board's .lg, mounted inside root and laid out (the host mounts it before run(1))
  sea: OceanEngine; // the website's sea engine (world, renderer)
};

export type Tiles = {
  readonly dir: 1 | -1; // which way it runs or last ran (Morph reads it)
  readonly state: "home" | "run" | "login";
  run(dir: 1 | -1, trigger?: HTMLElement): void; // 1: home -> sign-in (trigger: pressed a[data-go]); -1: back
  turn(): void; // turn round midway, from where it stands (Esc, the brand, browser back/forward)
  land(): void; // straight to the sign-in page's end state, no animation
  settle(): void; // the window changed size while on the sign-in page: keep the sea's wall on the panel edge
  seek(ms: number): void; // tests: jump to ms on the current run's clock
  destroy(): void; // puts everything back as it was before any run
};

type FrameBox = { box: DOMRect; W: number; H: number; k: number };

type ActiveRun = {
  clock: Clock;
  flyers: Flyers;
  styles: StyleTracker;
  plan: TilePlan;
  bake: Bake;
  grid: Grid;
  ink: Ink;
  family: string;
  W: number;
  H: number;
  px: number | null;
  trigBox: Box;
  trigger: HTMLElement | null;
  lower: boolean;
  groundAt: number;
  lastGround: boolean | null;
  lastTrigHidden: boolean | null;
  side: HTMLElement | null;
  pitchS: HTMLElement | null;
  glow: HTMLElement | null;
  ctaEls: HTMLElement[];
  famEls: HTMLElement[];
  underEls: HTMLElement[];
  barEls: HTMLElement[];
  heroSlots: HeroSlot[];
  lgSlots: LgSlot[];
  titleCells: TrackedTitleCell[];
  titleFs: number;
  pitchCells: PitchCell[];
  kids: Kid[];
  // the page's question: the host may put it in after the run starts (the frame comes first, its page a moment
  // later); it is measured once it is there, before its first window (title 700, kids 720 + off)
  asked: boolean;
  lg: HTMLElement;
  rect: (el: Element) => Box;
};

// The question of the page (its step, the title's characters, the step's lines after the title) as it stands, or
// null while the host has not put it in yet.
function measureQuestion(lg: HTMLElement, rect: (el: Element) => Box, off: number) {
  if (!lg.querySelector(".lg-step .lg-title")) return null;
  const { cells: titleCells, fs: titleFs } = measureTitleCells(lg, rect);
  const { step, kids } = measureKids(lg, off);
  return { titleCells, titleFs, step, kids };
}

// takeQuestion measures the question the first frame it is there (a query each frame until then, one layout read once)
// and holds its lines on their windows from that frame on, so none is seen before it is written.
function takeQuestion(run: ActiveRun) {
  if (run.asked) return;
  const q = measureQuestion(run.lg, run.rect, run.plan.off);
  if (!q) return;
  run.asked = true;
  Object.assign(run, { titleCells: q.titleCells, titleFs: q.titleFs, kids: q.kids });
  if (q.step) run.styles.set(q.step, { animation: "none" });
}

function measureFrame(root: HTMLElement): FrameBox {
  const box = root.getBoundingClientRect();
  const W = root.offsetWidth || box.width || window.innerWidth;
  const H = root.offsetHeight || box.height || window.innerHeight;
  const k = root.offsetWidth > 0 && box.width > 0 ? box.width / root.offsetWidth : 1;
  return { box, W, H, k };
}

function rectIn(el: Element, frame: FrameBox): Box {
  const b = el.getBoundingClientRect();
  return {
    x: (b.left - frame.box.left) / frame.k,
    y: (b.top - frame.box.top) / frame.k,
    w: b.width / frame.k,
    h: b.height / frame.k,
  };
}

function panelEdge(lg: HTMLElement, frame: FrameBox): number | null {
  const side = lg.querySelector<HTMLElement>(".lg-side");
  if (!side) return null;
  const q = rectIn(side, frame);
  return q.x > 1 ? q.x : null;
}

// Moves world.right between the full sea width and the panel's left edge (trans.js.txt:160–162, zoomK = 1).
function seaTo(sea: OceanEngine, px: number | null, u: number, wall?: number | null) {
  const w = sea.world;
  if (!w?.width) return;
  w.right = wall === undefined ? (u <= 0 || px == null ? null : lerp(w.width, px, u)) : wall;
}

function pickTrigger(ho: HTMLElement, saved: HTMLElement | null): HTMLElement | null {
  if (saved?.isConnected && ho.contains(saved)) return saved;
  const all = [...ho.querySelectorAll<HTMLElement>("[data-go]")];
  const visible = all.find((el) => {
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  });
  return visible ?? all[0] ?? null;
}

function fallbackTriggerBox(W: number): Box {
  return W < 768 ? { x: 20, y: 15, w: 88, h: 32 } : { x: Math.max(24, W - 180), y: 18, w: 68, h: 36 };
}

function firstField(lg: HTMLElement): HTMLElement | null {
  return (
    lg.querySelector<HTMLElement>(".lg-form #lg-id") ??
    lg.querySelector<HTMLElement>('.lg-form input:not([hidden]):not([type="hidden"]):not([disabled])') ??
    lg.querySelector<HTMLElement>(".lg-form textarea:not([hidden]):not([disabled])") ??
    lg.querySelector<HTMLElement>(".lg-form button:not([disabled]), .lg-form a[href]")
  );
}

function focusSafely(el: HTMLElement | null) {
  if (!el || typeof el.focus !== "function") return;
  try {
    el.focus({ preventScroll: true });
  } catch {
    // focus refused by detached or sandboxed host
  }
}

function findShared(ho: HTMLElement, lg: HTMLElement): SharedRefs {
  return {
    brandFrom: ho.querySelector<HTMLElement>(".bar .brand") ?? ho.querySelector<HTMLElement>(".brand"),
    brandTo: lg.querySelector<HTMLElement>(".brand"),
    h1: ho.querySelector<HTMLElement>("h1"),
    lede: ho.querySelector<HTMLElement>(".lede"),
    pitchH: lg.querySelector<HTMLElement>(".lg-pitch .h"),
    pitchS: lg.querySelector<HTMLElement>(".lg-pitch .s"),
  };
}

function isLower(refs: SharedRefs, ho: HTMLElement, frame: FrameBox): boolean {
  if (!refs.h1 || !refs.lede || !refs.pitchH || !refs.pitchS) return true;
  const hb = rectIn(refs.h1, frame);
  const bar = ho.querySelector(".bar");
  const top = bar ? rectIn(bar, frame).h : 0;
  const hcy = hb.y + hb.h / 2;
  return hcy < top || hcy > frame.H;
}

function visiblePanel(side: HTMLElement | null, frame: FrameBox): Box {
  if (!side) return panel(frame.W, frame.H);
  const p0 = rectIn(side, frame);
  const py0 = Math.max(0, p0.y);
  return { x: p0.x, y: py0, w: p0.w, h: Math.max(1, Math.min(frame.H, p0.y + p0.h) - py0) };
}

function hideShared(styles: StyleTracker, refs: SharedRefs, lower: boolean) {
  if (refs.brandFrom) styles.set(refs.brandFrom, { visibility: "hidden" });
  if (refs.brandTo) styles.set(refs.brandTo, { visibility: "hidden" });
  if (lower) return;
  for (const el of [refs.h1, refs.lede, refs.pitchH, refs.pitchS]) {
    if (el) styles.set(el, { visibility: "hidden" });
  }
}

function makeWindowEval(mt: number, dir: 1 | -1): WindowEval {
  return (d, D, ease) => {
    if (D <= 0) return mt >= d ? 1 : 0;
    if (dir > 0) return ease(clamp((mt - d) / D, 0, 1));
    return 1 - ease(clamp((d + D - mt) / D, 0, 1));
  };
}

function paintFade(els: HTMLElement[], opacity: string, styles: StyleTracker) {
  for (const el of els) styles.set(el, { opacity });
}

function paintLeaving(run: ActiveRun, mt: number, u: WindowEval, tick: number) {
  const { styles } = run;
  const trigHidden = mt >= 60;
  if (run.trigger && trigHidden !== run.lastTrigHidden) {
    run.lastTrigHidden = trigHidden;
    styles.set(run.trigger, { visibility: trigHidden ? "hidden" : "visible" });
  }
  paintHeroSlots(run.heroSlots, u, tick, styles);
  paintFade(run.famEls, String(1 - u(160, 200, E.out)), styles);
  paintFade(run.ctaEls, String(1 - u(220, 200, E.out)), styles);
  paintFade(run.underEls, String(1 - u(60, 360, E.out)), styles);
  paintFade(run.barEls, String(1 - u(60, 360, E.out)), styles);
  if (run.glow) styles.set(run.glow, { opacity: String(1 - u(0, 460, E.out)) });
}

function paintCanvas(run: ActiveRun, mt: number, ground: boolean) {
  const { flyers, plan, ink, family } = run;
  const x = flyers.ctx;
  if (!x) return;
  x.setTransform(flyers.dpr, 0, 0, flyers.dpr, 0, 0);
  x.clearRect(0, 0, run.W, run.H);
  const view = { W: run.W, uiWidth: flyers.ui.width, uiHeight: flyers.ui.height, dpr: flyers.dpr };
  drawBakedCells(x, run.bake, plan.cells, plan.gh, mt, ground, view, ink, family);
  drawButton(x, plan.bits, run.trigBox, mt, ink, run.grid, true);
  if (plan.word.length) drawWord(x, plan.word, plan.wm, mt, ink.tile, E.emph, family);
  x.globalAlpha = 1;
}

function paintRun(run: ActiveRun, sea: OceanEngine, mt: number, dir: 1 | -1, landing: boolean) {
  const { plan, styles, flyers, ink, family } = run;
  const off = plan.off;
  const tRun = dir > 0 ? mt : plan.T - mt;
  const tick = Math.floor(tRun / 50);
  const u = makeWindowEval(mt, dir);

  paintLeaving(run, mt, u, tick);
  flyers.brand?.go(u(40, 560 + off, E.std));
  if (!run.lower) {
    flyers.pitch?.go(
      u(120, 700 + off, E.std),
      u(120, 700 + off, E.drop),
      u(120, 700 + off, E.std),
      u(150, 700 + off, E.std),
    );
  }
  seaTo(sea, run.px, u(120 + off, 640, E.std));

  const ground = mt >= run.groundAt;
  if (run.side && ground !== run.lastGround) {
    run.lastGround = ground;
    styles.set(run.side, { "background-color": ground ? ink.bg : "transparent" });
  }
  sea.renderer.visRight = ground && run.px != null ? run.px : null;

  paintCanvas(run, mt, ground);
  takeQuestion(run);
  paintLgSlots(run.lgSlots, u, tick);
  drawTitle(flyers.ctx, run.titleCells, run.titleFs, mt - 700 - off, dir, tick, ink, family, styles);
  paintKids(run.kids, u, styles);
  if (run.lower) {
    drawPitchDecode(flyers.ctx, run.pitchCells, mt - 620 - off, dir, landing, tick, ink, family, styles);
    if (run.pitchS) styles.set(run.pitchS, { opacity: String(u(700 + off, 300, E.out)) });
  }
  if (flyers.ctx) flyers.ctx.globalAlpha = 1;
}

function buildRun(scene: TilesScene, trigger: HTMLElement | null, onBrandPress: () => void): ActiveRun {
  const { root, ho, lg, sea } = scene;
  const frame = measureFrame(root);
  const rect = (el: Element) => rectIn(el, frame);
  const side = lg.querySelector<HTMLElement>(".lg-side");
  const lgSea = lg.querySelector<HTMLElement>(".lg-sea");
  const px = panelEdge(lg, frame);
  const P = visiblePanel(side, frame);
  const trigBox = trigger ? rect(trigger) : fallbackTriggerBox(frame.W);
  const grid = gridOf(sea);
  const plan = planTiles(trigBox, P, grid, root);
  const ink = inkOf(root, trigger);
  const family = mono();
  const refs = findShared(ho, lg);
  const lower = isLower(refs, ho, frame);
  const flyers = mountFlyers(root, refs, frame.W, frame.H, lower, onBrandPress);
  const heroSlots = measureHeroSlots(ho, trigger, refs.lede);
  const lgSlots = measureLgSlots(lg, plan.off);
  const question = measureQuestion(lg, rect, plan.off);
  const pitchCells = lower ? measurePitchCells(lg, rect) : [];
  const styles = styleTracker();
  hideShared(styles, refs, lower);
  if (side) styles.set(side, { "background-color": "transparent" });
  if (lgSea) styles.set(lgSea, { "box-shadow": "none" });
  if (question?.step) styles.set(question.step, { animation: "none" });

  return {
    clock: null as unknown as Clock,
    flyers,
    styles,
    plan,
    bake: createBake(),
    grid,
    ink,
    family,
    W: frame.W,
    H: frame.H,
    px,
    trigBox,
    trigger,
    lower,
    groundAt: Math.max(760 + plan.off, plan.wordLanded),
    lastGround: null,
    lastTrigHidden: null,
    side,
    pitchS: refs.pitchS,
    glow: ho.querySelector<HTMLElement>(".glow"),
    ctaEls: [...ho.querySelectorAll<HTMLElement>(".cta")],
    famEls: [...ho.querySelectorAll<HTMLElement>(".fam")],
    underEls: [...ho.querySelectorAll<HTMLElement>(".hs > .under, .isl")],
    // the header's controls that are not words (the language and theme switches, the menu) go with the page; its links
    // un-write (heroSlots)
    barEls: [...ho.querySelectorAll<HTMLElement>(".bar .nav > :not(a)")],
    heroSlots,
    lgSlots,
    titleCells: question?.titleCells ?? [],
    titleFs: question?.titleFs ?? 0,
    pitchCells,
    kids: question?.kids ?? [],
    asked: question !== null,
    lg,
    rect,
  };
}

function cleanRun(run: ActiveRun) {
  // Unwind in LIFO order: styles first, then pen slots, so any shared <a> drops an empty style attribute cleanly.
  run.styles.restore();
  restoreSlots([...run.heroSlots.map((h) => h.slot), ...run.lgSlots.map((s) => s.slot)]);
  run.flyers.destroy();
  run.plan.wm?.show();
}

type Driver = {
  scene: TilesScene;
  on: { end: (state: "home" | "login") => void };
  initState: string | null;
  initHo: PageInit;
  initLg: PageInit;
  initRight: number | null;
  initVisRight: number | null;
  state: "home" | "run" | "login";
  dir: 1 | -1;
  dead: boolean;
  lastTrigger: HTMLElement | null;
  active: ActiveRun | null;
  lastSize: FrameBox;
  ro: ResizeObserver | null;
};

function applyPageState(d: Driver, next: "home" | "run" | "login") {
  d.state = next;
  d.scene.root.dataset.state = next;
  setPageHidden(d.scene.ho, d.initHo, next === "login");
  setPageHidden(d.scene.lg, d.initLg, next === "home");
}

function lockSea(sea: OceanEngine, toLogin: boolean, px: number | null) {
  sea.renderer.visRight = toLogin && px != null ? px : null;
  seaTo(sea, px, toLogin ? 1 : 0, toLogin ? px : null);
}

function moveFocus(d: Driver, toLogin: boolean) {
  if (toLogin) return focusSafely(firstField(d.scene.lg));
  const fa = document.activeElement;
  if (!fa || fa === document.body || d.scene.lg.contains(fa)) focusSafely(d.lastTrigger);
}

function finishDriver(d: Driver, end: 1 | -1) {
  if (!d.active) return;
  const ended = d.active;
  d.active = null;
  cleanRun(ended);
  const next = end > 0 ? "login" : "home";
  d.dir = end;
  applyPageState(d, next);
  lockSea(d.scene.sea, end > 0, ended.px);
  moveFocus(d, end > 0);
  d.on.end(next);
}

function turnDriver(d: Driver) {
  if (d.dead || d.state !== "run" || !d.active) return;
  d.dir = d.dir > 0 ? -1 : 1;
  d.active.clock.reverse();
}

function settleDriver(d: Driver) {
  if (d.dead) return;
  if (d.state === "run" && d.active) d.active.clock.seek(d.active.plan.T);
  const frame = measureFrame(d.scene.root);
  d.lastSize = frame;
  if (d.state !== "login") return;
  lockSea(d.scene.sea, true, panelEdge(d.scene.lg, frame));
}

function startDriverRun(d: Driver, nextDir: 1 | -1, trigger?: HTMLElement) {
  if (d.dead || d.state === "run") return;
  if ((nextDir > 0 && d.state === "login") || (nextDir < 0 && d.state === "home")) return;
  if (trigger) d.lastTrigger = trigger;
  d.lastTrigger = pickTrigger(d.scene.ho, d.lastTrigger);
  d.dir = nextDir;
  applyPageState(d, "run");
  d.lastSize = measureFrame(d.scene.root);
  const run = buildRun(d.scene, d.lastTrigger, () => {
    if (d.state === "run" && d.dir > 0) turnDriver(d);
  });
  d.active = run;
  run.clock = drive({
    canvas: run.flyers.ui,
    span: run.plan.T,
    start: performance.now(),
    dir: nextDir,
    paint: (mt, curDir, landing) => paintRun(run, d.scene.sea, mt, curDir, landing),
    onEnd: (end) => finishDriver(d, end),
  });
}

function landDriver(d: Driver) {
  if (d.dead) return;
  if (d.state === "login") return settleDriver(d);
  if (d.active) {
    const prev = d.active;
    d.active = null;
    prev.clock.stop();
    cleanRun(prev);
  }
  d.dir = 1;
  applyPageState(d, "login");
  const frame = measureFrame(d.scene.root);
  d.lastSize = frame;
  lockSea(d.scene.sea, true, panelEdge(d.scene.lg, frame));
  moveFocus(d, true);
  d.on.end("login");
}

function destroyDriver(d: Driver) {
  if (d.dead) return;
  d.dead = true;
  d.ro?.disconnect();
  if (d.active) {
    const prev = d.active;
    d.active = null;
    prev.clock.stop();
    cleanRun(prev);
  }
  restorePageInit(d.scene.ho, d.initHo);
  restorePageInit(d.scene.lg, d.initLg);
  if (d.initState === null) d.scene.root.removeAttribute("data-state");
  else d.scene.root.setAttribute("data-state", d.initState);
  if (d.scene.sea.world) d.scene.sea.world.right = d.initRight;
  d.scene.sea.renderer.visRight = d.initVisRight;
}

function observeResize(d: Driver): ResizeObserver | null {
  if (typeof ResizeObserver === "undefined") return null;
  const ro = new ResizeObserver(() => {
    if (d.dead || d.state === "home") return;
    const now = measureFrame(d.scene.root);
    if (Math.abs(now.W - d.lastSize.W) < 0.5 && Math.abs(now.H - d.lastSize.H) < 0.5) return;
    settleDriver(d);
  });
  ro.observe(d.scene.root);
  return ro;
}

export function tiles(scene: TilesScene, on: { end: (state: "home" | "login") => void }): Tiles {
  const d: Driver = {
    scene,
    on,
    initState: scene.root.getAttribute("data-state"),
    initHo: snapshotPage(scene.ho),
    initLg: snapshotPage(scene.lg),
    initRight: scene.sea.world?.right ?? null,
    initVisRight: scene.sea.renderer.visRight ?? null,
    state: "home",
    dir: 1,
    dead: false,
    lastTrigger: null,
    active: null,
    lastSize: measureFrame(scene.root),
    ro: null,
  };
  d.ro = observeResize(d);
  applyPageState(d, "home");

  return {
    get dir() {
      return d.dir;
    },
    get state() {
      return d.state;
    },
    run: (nextDir, trigger) => startDriverRun(d, nextDir, trigger),
    turn: () => turnDriver(d),
    land: () => landDriver(d),
    settle: () => settleDriver(d),
    seek: (ms) => {
      if (!d.dead && d.state === "run" && d.active) d.active.clock.seek(ms);
    },
    destroy: () => destroyDriver(d),
  };
}
