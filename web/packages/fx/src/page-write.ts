// Text writing and form reveal tracks for the tiles transition (trans.js.txt:282–288, 374–388, 1043–1047, 1110–1126),
// plus the shared title-decode canvas primitives used by login.ts so neither module duplicates glyph or caret drawing.
import { hash } from "./hash";
import { type Box, E, type Ease } from "./panel";
import { pen, type Slot, slotsOf } from "./pen";
import type { StyleTracker } from "./style-tracker";
import { clamp, GLYPHS } from "./word-tiles";

export const glyph = (i: number, tick: number) => GLYPHS[Math.floor(hash(i, tick) * GLYPHS.length)] ?? "";

export type TitleCell = { x: number; y: number; r: number; blank: boolean };
export type TrackedTitleCell = TitleCell & { el: HTMLElement; hidden: boolean };
export type PitchCell = { el: HTMLElement; x: number; y: number; size: number; blank: boolean; hidden: boolean };
export type Kid = { el: HTMLElement; delay: number };
export type HeroSlot = { slot: Slot; d: number; D: number; btn: HTMLElement | null };
export type LgSlot = { slot: Slot; start: number; dur: number };

export type WindowEval = (d: number, D: number, ease: Ease) => number;
type RectReader = (el: Element) => Box;

// A character d ms before it resolves: a dot while it waits, a cycling glyph as it comes close, the ring colour next
// (trans.js.txt:381–383, login.ts:109–125).
export function drawWaiting(
  x: CanvasRenderingContext2D,
  c: TitleCell,
  d: number,
  g: string,
  ink: { ring: string; dim: string; fs: number },
) {
  x.globalAlpha = d <= 60 ? 1 : d <= 240 ? 0.85 : 0.5;
  x.fillStyle = d <= 60 ? ink.ring : ink.dim;
  if (d <= 240) {
    x.fillText(g, c.x, c.y);
    return;
  }
  x.beginPath();
  x.arc(c.x, c.y, Math.max(1.5, ink.fs * 0.04), 0, Math.PI * 2);
  x.fill();
}

export function drawCaret(x: CanvasRenderingContext2D, last: TitleCell, ink: { ring: string; fs: number }) {
  x.globalAlpha = 1;
  x.fillStyle = ink.ring;
  const w = Math.max(2, Math.round(ink.fs * 0.05));
  x.fillRect(Math.round(last.r + ink.fs * 0.06), Math.round(last.y - ink.fs * 0.42), w, Math.round(ink.fs * 0.84));
}

function setMasked(cell: { el: HTMLElement; hidden: boolean }, hidden: boolean, styles: StyleTracker) {
  if (hidden === cell.hidden) return;
  cell.hidden = hidden;
  if (hidden) styles.setRaw(cell.el, "color", "transparent");
  else styles.restoreProp(cell.el, "color");
}

export function measureTitleCells(lg: HTMLElement, rect: RectReader): { cells: TrackedTitleCell[]; fs: number } {
  const nodes = [...lg.querySelectorAll<HTMLElement>(".lg-title .ch")];
  const cells = nodes.map((el) => {
    const q = rect(el);
    return {
      el,
      x: q.x + q.w / 2,
      y: q.y + q.h / 2,
      r: q.x + q.w,
      blank: (el.textContent ?? "").trim() === "",
      hidden: false,
    };
  });
  const first = nodes[0];
  const fs = first ? Number.parseFloat(getComputedStyle(first).fontSize) || 40 : 40;
  return { cells, fs };
}

function paintTitleCell(
  x: CanvasRenderingContext2D | null,
  c: TrackedTitleCell,
  i: number,
  ms: number,
  tick: number,
  ink: { ring: string; dim: string; fs: number },
  styles: StyleTracker,
) {
  const d = 80 + i * 60 - ms;
  setMasked(c, d > 0, styles);
  if (x && d > 0 && !c.blank && ms >= 0) drawWaiting(x, c, d, glyph(i, tick), ink);
}

// Form title decode on canvas.ui (trans.js.txt:374–388, 1112): AT(i) = 80 + 60i, forward-only 260ms cyan caret.
export function drawTitle(
  x: CanvasRenderingContext2D | null,
  cells: TrackedTitleCell[],
  fs: number,
  ms: number,
  dir: 1 | -1,
  tick: number,
  ink: { ring: string; dim: string },
  family: string,
  styles: StyleTracker,
) {
  if (!cells.length) return;
  const titleInk = { ring: ink.ring, dim: ink.dim, fs };
  if (x) {
    x.textAlign = "center";
    x.textBaseline = "middle";
    x.font = `500 ${Math.round(fs * 0.62)}px ${family}`;
  }
  for (let i = 0; i < cells.length; i++) {
    paintTitleCell(x, cells[i] as TrackedTitleCell, i, ms, tick, titleInk, styles);
  }
  const after = ms - (80 + (cells.length - 1) * 60);
  if (x && dir > 0 && after >= 0 && after < 260) drawCaret(x, cells[cells.length - 1] as TrackedTitleCell, titleInk);
  if (x) x.globalAlpha = 1;
}

export function measurePitchCells(lg: HTMLElement, rect: RectReader): PitchCell[] {
  return [...lg.querySelectorAll<HTMLElement>(".lg-pitch .ch")].map((el) => {
    const q = rect(el);
    return {
      el,
      x: q.x + q.w / 2,
      y: q.y + q.h / 2,
      size: q.h,
      blank: (el.textContent ?? "").trim() === "",
      hidden: false,
    };
  });
}

function drawPitchCell(
  x: CanvasRenderingContext2D,
  c: PitchCell,
  m: number,
  i: number,
  tick: number,
  ink: { ring: string; dim: string },
  family: string,
) {
  x.font = `500 ${Math.round(c.size * 0.62)}px ${family}`;
  if (m < 80) {
    x.globalAlpha = 0.5;
    x.fillStyle = ink.dim;
    x.beginPath();
    x.arc(c.x, c.y, Math.max(1.2, c.size * 0.04), 0, Math.PI * 2);
    x.fill();
    return;
  }
  x.globalAlpha = m > 240 ? 1 : 0.85;
  x.fillStyle = m > 240 ? ink.ring : ink.dim;
  x.fillText(glyph(i * 7 + 3, tick), c.x, c.y);
}

// In-place decode of .lg-pitch .ch when the homepage has scrolled past h1 (trans.js.txt:1115–1124).
export function drawPitchDecode(
  x: CanvasRenderingContext2D | null,
  cells: PitchCell[],
  msBase: number,
  dir: 1 | -1,
  landing: boolean,
  tick: number,
  ink: { ring: string; dim: string },
  family: string,
  styles: StyleTracker,
) {
  if (!cells.length) return;
  if (x) {
    x.textAlign = "center";
    x.textBaseline = "middle";
  }
  const landed = dir > 0 ? 1e9 : -1e9;
  for (let i = 0; i < cells.length; i++) {
    const c = cells[i] as PitchCell;
    if (c.blank) continue;
    const m = landing ? landed : msBase - i * 28;
    const done = m >= 300;
    setMasked(c, !done, styles);
    if (x && !done && m >= 0) drawPitchCell(x, c, m, i, tick, ink, family);
  }
  if (x) x.globalAlpha = 1;
}

// .lg-step children after the first (i >= 1) fade and rise 8px -> 0 on staggered windows (trans.js.txt:285–286, 1113).
export function measureKids(lg: HTMLElement, off: number): { step: HTMLElement | null; kids: Kid[] } {
  const step = lg.querySelector<HTMLElement>(".lg-step");
  if (!step) return { step: null, kids: [] };
  const kids: Kid[] = [];
  for (let i = 1; i < step.children.length; i++) {
    const el = step.children[i];
    if (el instanceof HTMLElement) kids.push({ el, delay: 720 + off + 40 * i });
  }
  return { step, kids };
}

export function paintKids(kids: Kid[], u: WindowEval, styles: StyleTracker) {
  for (const k of kids) {
    const v = u(k.delay, 300, E.out);
    styles.set(k.el, { opacity: String(v), transform: `translateY(${(1 - v) * 8}px)` });
  }
}

export function measureHeroSlots(ho: HTMLElement, trigger: HTMLElement | null, lede: Element | null): HeroSlot[] {
  let origin: { x: number; y: number } | undefined;
  if (trigger) {
    const r = trigger.getBoundingClientRect();
    origin = { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  }
  const slots = slotsOf(ho, origin, (el) => Boolean(lede?.contains(el)) || el.closest("[data-go]") === trigger);
  return slots.map((slot) => ({
    slot,
    d: (slot.d / 1800) * 200,
    D: clamp([...slot.text].length * 14, 70, 240),
    btn: slot.el.closest<HTMLElement>(".btn"),
  }));
}

export function measureLgSlots(lg: HTMLElement, off: number): LgSlot[] {
  // the language switch writes itself too; its links carry no mark, their text is the word
  const slots = slotsOf(lg, undefined, (el) => Boolean(el.closest(".lg-form")), ".lg-top a");
  return slots.map((slot) => ({
    slot,
    start: (slot.el.closest(".lg-foot") ? 760 : 620) + off,
    dur: Math.max(1, [...slot.text].length * 30),
  }));
}

export function paintHeroSlots(slots: HeroSlot[], u: WindowEval, tick: number, styles: StyleTracker) {
  for (const h of slots) {
    pen(h.slot, 1 - u(h.d, h.D, E.lin), tick);
    if (h.btn) styles.set(h.btn, { opacity: String(1 - u(h.d + h.D * 0.6, 140, E.out)) });
  }
}

export function paintLgSlots(slots: LgSlot[], u: WindowEval, tick: number) {
  for (const s of slots) pen(s.slot, u(s.start, s.dur, E.lin), tick);
}
