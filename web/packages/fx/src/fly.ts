// Measured flight for the parts shared between the homepage and the sign-in page (trans.js.txt:253–339, 367):
// the brand mark (.fb) slides between the two pages' marks (<svg> or white-label <img>), and the headline (.fh) plus
// lede (.fs) travel together from the hero to .lg-pitch with glyph-level Range calibration, integer translates plus
// fractional padding so text lands on the same sub-pixel grid with no jump, and interpolated frosted-lens variables.
// Every style write goes through CSSOM and is tracked per property so restore() never touches the style attribute.
import { colour } from "./colour";
import { type StyleTracker, styleTracker } from "./style-tracker";
import { clamp, lerp } from "./word-tiles";

export type Track = (u: number) => void;

type Box = { x: number; y: number; w: number; h: number };
type Point = { x: number; y: number };
type Frame = { left: number; top: number; k: number };
type Rgba = readonly [number, number, number, number];
type VeilPair = { a: Rgba; b: Rgba } | null;

type TypeEnd = {
  box: Box;
  pos: Point;
  pad: number;
  fs: number;
  lh: number;
  ly: number;
  lx: number;
};

type TypePair = { a: TypeEnd; b: TypeEnd; gone: boolean };

const NUM = String.raw`(?:\d+\.?\d*|\.\d+)`;
const RGB_RE = new RegExp(
  `^rgba?\\(\\s*(${NUM})[,\\s]+(${NUM})[,\\s]+(${NUM})(?:\\s*[,/]\\s*(${NUM}%?))?\\s*\\)$`,
  "i",
);
const SRGB_RE = new RegExp(`^color\\(srgb\\s+(${NUM})\\s+(${NUM})\\s+(${NUM})(?:\\s*/\\s*(${NUM}%?))?\\s*\\)$`, "i");
const HEX_RE = /^#([0-9a-f]{3}|[0-9a-f]{4}|[0-9a-f]{6}|[0-9a-f]{8})$/i;

function parseAlpha(raw: string | undefined): number {
  if (!raw) return 1;
  return raw.endsWith("%") ? clamp(Number.parseFloat(raw) / 100, 0, 1) : clamp(Number.parseFloat(raw), 0, 1);
}

function parseHex(hex: string): Rgba {
  const full = hex.length <= 4 ? [...hex].map((c) => c + c).join("") : hex;
  const r = Number.parseInt(full.slice(0, 2), 16);
  const g = Number.parseInt(full.slice(2, 4), 16);
  const b = Number.parseInt(full.slice(4, 6), 16);
  const a = full.length === 8 ? Number.parseInt(full.slice(6, 8), 16) / 255 : 1;
  return [r, g, b, a];
}

function parseRgba(raw: string): Rgba | null {
  const s = raw.trim();
  if (!s) return null;
  if (s.toLowerCase() === "transparent") return [0, 0, 0, 0];
  const hex = HEX_RE.exec(s);
  if (hex?.[1]) return parseHex(hex[1]);
  const rgb = RGB_RE.exec(s);
  if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3]), parseAlpha(rgb[4])];
  const srgb = SRGB_RE.exec(s);
  if (srgb) {
    return [
      clamp(Number(srgb[1]) * 255, 0, 255),
      clamp(Number(srgb[2]) * 255, 0, 255),
      clamp(Number(srgb[3]) * 255, 0, 255),
      parseAlpha(srgb[4]),
    ];
  }
  return null;
}

// From or to a fully clear colour, only alpha moves so interpolating never passes through muddy grey (trans.js.txt:43).
function mixRgba(a: Rgba, b: Rgba, t: number): string {
  const ca: Rgba = a[3] === 0 ? [b[0], b[1], b[2], 0] : a;
  const cb: Rgba = b[3] === 0 ? [a[0], a[1], a[2], 0] : b;
  const r = Math.round(lerp(ca[0], cb[0], t));
  const g = Math.round(lerp(ca[1], cb[1], t));
  const bl = Math.round(lerp(ca[2], cb[2], t));
  return `rgba(${r},${g},${bl},${lerp(ca[3], cb[3], t).toFixed(3)})`;
}

// Resolve --veil in el's cascade: parse literal colours directly, and fall back to a CSSOM colour probe for runtime
// light-dark(...) / color-mix(...) tokens without inheriting el's text colour when --veil is unset.
function veilOf(el: Element): Rgba | null {
  const raw = getComputedStyle(el).getPropertyValue("--veil").trim();
  if (!raw) return null;
  const direct = parseRgba(raw);
  if (direct) return direct;
  return el instanceof HTMLElement ? parseRgba(colour(el, "var(--veil)")) : null;
}

// Containing coordinate frame of a travelling copy (.fly inside .tr), accounting for any ancestor scale factor k.
// Walks past an unlaid-out wrapper (e.g. a freshly appended .fly in jsdom) to the sized .tr root above it.
function frameOf(copy: HTMLElement): Frame {
  let parent = (copy.offsetParent as HTMLElement | null) ?? copy.parentElement;
  while (parent) {
    const b = parent.getBoundingClientRect();
    if (b.width > 0 || b.height > 0 || !parent.parentElement) {
      const k = parent.offsetWidth > 0 && b.width > 0 ? b.width / parent.offsetWidth : 1;
      return { left: b.left, top: b.top, k };
    }
    parent = parent.parentElement;
  }
  return { left: 0, top: 0, k: 1 };
}

function toBox(b: DOMRect, frame: Frame): Box {
  return {
    x: (b.left - frame.left) / frame.k,
    y: (b.top - frame.top) / frame.k,
    w: b.width / frame.k,
    h: b.height / frame.k,
  };
}

const rectOf = (el: Element, frame: Frame): Box => toBox(el.getBoundingClientRect(), frame);

function unionRect(els: Element[], frame: Frame): Box {
  let x0 = Number.POSITIVE_INFINITY;
  let y0 = Number.POSITIVE_INFINITY;
  let x1 = Number.NEGATIVE_INFINITY;
  let y1 = Number.NEGATIVE_INFINITY;
  for (const el of els) {
    const q = rectOf(el, frame);
    x0 = Math.min(x0, q.x);
    y0 = Math.min(y0, q.y);
    x1 = Math.max(x1, q.x + q.w);
    y1 = Math.max(y1, q.y + q.h);
  }
  return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
}

// Text extent inside a headline or lede box: matches trans.js.txt:260–263 (.t on .lg-pitch, [data-dx] on .lede,
// union of .ch spans on h1), falling back to a full-contents Range rect or the element's own box.
function textRect(el: Element, frame: Frame): Box {
  const inner = el.querySelector(".t, [data-dx]");
  if (inner) return rectOf(inner, frame);
  const chars = [...el.querySelectorAll(".ch")];
  if (chars.length > 0) return unionRect(chars, frame);
  const range = document.createRange();
  range.selectNodeContents(el);
  if (typeof range.getBoundingClientRect === "function") {
    const b = range.getBoundingClientRect();
    if (b.width > 0 || b.height > 0) return toBox(b, frame);
  }
  return rectOf(el, frame);
}

// First non-whitespace glyph's top-left corner via Range so calibration aligns ink rather than line boxes
// (trans.js.txt:292–297).
function charRect(el: Element, frame: Frame): Point {
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT, {
    acceptNode: (n) => ((n.nodeValue ?? "").trim() ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_SKIP),
  });
  const node = walker.nextNode();
  if (!node?.nodeValue) return rectOf(el, frame);
  const i = node.nodeValue.search(/\S/);
  const len = (node.nodeValue.codePointAt(i) ?? 0) > 0xffff ? 2 : 1;
  const range = document.createRange();
  range.setStart(node, i);
  range.setEnd(node, i + len);
  if (typeof range.getBoundingClientRect !== "function") return rectOf(el, frame);
  const b = range.getBoundingClientRect();
  if (b.width === 0 && b.height === 0 && b.left === 0 && b.top === 0) return rectOf(el, frame);
  return { x: (b.left - frame.left) / frame.k, y: (b.top - frame.top) / frame.k };
}

function fontMetrics(el: Element, fallbackFs: number, fallbackLh: number): [number, number] {
  const cs = getComputedStyle(el);
  return [Number.parseFloat(cs.fontSize) || fallbackFs, Number.parseFloat(cs.lineHeight) || fallbackLh];
}

// The visual mark inside a brand container: Axiom's default <svg> or a white-label <img>, falling back to the
// container itself when neither is present (trans.js.txt:308–316).
const markOf = (el: Element): Element => el.querySelector("svg, img") ?? el;

function calibrateBrand(copy: HTMLElement, from: Element, to: Element, frame: Frame): [Point, Point] {
  const cal = styleTracker();
  const copyMark = markOf(copy);
  const measureEnd = (real: Element): Point => {
    const q = rectOf(real, frame);
    cal.set(copy, { transform: `translate(${q.x}px,${q.y}px)` });
    const f = rectOf(copyMark, frame);
    const r = rectOf(markOf(real), frame);
    return { x: q.x + r.x - f.x, y: q.y + r.y - f.y };
  };
  try {
    return [measureEnd(from), measureEnd(to)];
  } finally {
    cal.restore();
  }
}

function calibrateEnd(
  copy: HTMLElement,
  real: Element,
  frame: Frame,
  cal: StyleTracker,
  metrics: { fs: number; lh: number; ly: number; lx: number },
): TypeEnd {
  const box = rectOf(real, frame);
  const text = textRect(real, frame);
  const pad = text.x - box.x;
  const [fs, lh] = fontMetrics(real, metrics.fs, metrics.lh);
  const ix = Math.floor(box.x);
  const iy = Math.floor(box.y);
  cal.set(copy, {
    transform: `translate(${ix}px,${iy}px)`,
    width: `${box.w}px`,
    "padding-left": `${pad + box.x - ix}px`,
    "padding-top": `${box.y - iy}px`,
    "font-size": `${fs}px`,
    "line-height": `${lh}px`,
  });
  const f = charRect(copy, frame);
  const r = charRect(real, frame);
  return { box, pos: { x: box.x + r.x - f.x, y: box.y + r.y - f.y }, pad, fs, lh, ly: metrics.ly, lx: metrics.lx };
}

function calibrateHead(copy: HTMLElement, from: Element, to: Element, frame: Frame): TypePair {
  const cal = styleTracker();
  try {
    const a = calibrateEnd(copy, from, frame, cal, { fs: 80, lh: 84, ly: 16, lx: 32 });
    const b = calibrateEnd(copy, to, frame, cal, { fs: 40, lh: 48, ly: 12, lx: 24 });
    return { a, b, gone: false };
  } finally {
    cal.restore();
  }
}

function calibrateLede(copy: HTMLElement, from: Element, to: Element, head: TypePair, frame: Frame): TypePair {
  const cal = styleTracker();
  try {
    const a = calibrateEnd(copy, from, frame, cal, { fs: 16, lh: 26, ly: 16, lx: 32 });
    const gone = rectOf(to, frame).w < 1;
    // On phones .lg-pitch .s is display:none: ride beneath the headline, aligned to its text start, and fade out
    // along the way (trans.js.txt:273–277, 338).
    const b: TypeEnd = gone
      ? {
          box: { ...a.box },
          pos: { x: head.b.pos.x + head.b.pad - a.pad, y: head.b.pos.y + head.b.box.h + 8 },
          pad: a.pad,
          fs: a.fs,
          lh: a.lh,
          ly: 12,
          lx: 24,
        }
      : calibrateEnd(copy, to, frame, cal, { fs: 16, lh: 26, ly: 12, lx: 24 });
    return { a, b, gone };
  } finally {
    cal.restore();
  }
}

function paintType(
  styles: StyleTracker,
  copy: HTMLElement,
  pair: TypePair,
  ux: number,
  uy: number,
  us: number,
  veil: VeilPair,
  byAt?: number,
): number {
  const { a, b } = pair;
  const bx = lerp(a.pos.x, b.pos.x, ux);
  const bw = lerp(a.box.w, b.box.w, ux);
  const pad = lerp(a.pad, b.pad, ux);
  const lh = lerp(a.lh, b.lh, us);
  const by = byAt !== undefined ? byAt : lerp(a.pos.y, b.pos.y, uy);
  // Whole pixels in the transform, the fraction in the padding so glyphs sit on the page's sub-pixel grid
  // (trans.js.txt:322–329).
  const ix = Math.floor(bx);
  const iy = Math.floor(by);
  const props: Record<string, string> = {
    visibility: "visible",
    transform: `translate(${ix}px,${iy}px)`,
    width: `${bw}px`,
    "padding-left": `${pad + bx - ix}px`,
    "padding-top": `${by - iy}px`,
    "font-size": `${lerp(a.fs, b.fs, us)}px`,
    "line-height": `${lh}px`,
    "--ly": `${lerp(a.ly, b.ly, us)}px`,
    "--lx": `${lerp(a.lx, b.lx, us)}px`,
    "--mx": `${lerp(24, 20, us)}px`,
    "--my": `${lerp(24, 16, us)}px`,
  };
  if (veil) props["--fv"] = mixRgba(veil.a, veil.b, us);
  styles.set(copy, props);
  return by;
}

// brandFlight measures from and to against copy's mark (<svg> or <img>) and returns a Track that translates copy
// between them with no pixel jump at either end (trans.js.txt:308–316, 367).
export function brandFlight(copy: HTMLElement, from: Element, to: Element): { go: Track; restore(): void } {
  const frame = frameOf(copy);
  const [a, b] = calibrateBrand(copy, from, to, frame);
  const styles = styleTracker();
  return {
    go(u: number) {
      styles.set(copy, {
        visibility: "visible",
        transform: `translate(${lerp(a.x, b.x, u)}px,${lerp(a.y, b.y, u)}px)`,
      });
    },
    restore() {
      styles.restore();
    },
  };
}

// pitchFlight calibrates headline and lede copies against both endpoints and drives them together: ux/uy/us move the
// headline's horizontal position, vertical drop and type/lens scale, while the lede rides beneath it with horizontal
// progress uxl (fading out when the destination lede is hidden, trans.js.txt:260–339).
export function pitchFlight(
  copies: { h: HTMLElement; s: HTMLElement },
  from: { h: Element; s: Element },
  to: { h: Element; s: Element },
): { go(ux: number, uy: number, us: number, uxl?: number): void; restore(): void } {
  const head = calibrateHead(copies.h, from.h, to.h, frameOf(copies.h));
  const lede = calibrateLede(copies.s, from.s, to.s, head, frameOf(copies.s));
  const veilA = veilOf(from.h);
  const veilB = veilOf(to.h);
  const veil: VeilPair = veilA && veilB ? { a: veilA, b: veilB } : null;
  const styles = styleTracker();
  return {
    go(ux: number, uy: number, us: number, uxl = ux) {
      const hy = paintType(styles, copies.h, head, ux, uy, us, veil);
      const sy =
        uy === 0 && us === 0
          ? lede.a.pos.y
          : uy === 1 && us === 1
            ? lede.b.pos.y
            : hy + lerp(lede.a.pos.y - head.a.pos.y, lede.b.pos.y - head.b.pos.y, us);
      paintType(styles, copies.s, lede, uxl, uy, us, veil, sy);
      if (lede.gone) styles.set(copies.s, { opacity: String(1 - clamp(us * 1.6, 0, 1)) });
    },
    restore() {
      styles.restore();
    },
  };
}
