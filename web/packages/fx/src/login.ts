// The console's sign-in pages' engines (#747, port design §9): the Login board's sea, the solid on it, and the title's
// decode. The solid is login-ui.js's Morph unedited, as the final boards run it from trans.js on the sign-in page the
// website leads to (its Stage is switched off there whenever Morph is defined): the sea's sharks are called in, its
// cells form a turning solid for the step, which melts into the next step's, turns once round on a copy and shakes on a
// refusal. They run on the shared loop (loop.ts): the solid is drawn from the sea engine's raster, so nothing moves
// while the tab is hidden or the sea is out of view. The decode plays only for a visitor who arrived through the
// website's departure (arrival.ts).
import "@axiom/boards/engines/login-ui.js";
import { colour } from "./colour";
import { hash } from "./hash";
import { own } from "./loop";
import { mono } from "./mono";
import { buildSea, type OceanEngine } from "./ocean";

// The steps of login-ui.js's solids the product has (not wechat, connect).
export type Shape = "start" | "code" | "key" | "enter";
// What the page tells the solid: whether the address typed looks whole, how many digits of a code are in, a refused
// answer. len is the address's length, which the board's events carry too.
export type Heard = { type: "email"; len: number; ok: boolean } | { type: "digit"; n: number } | { type: "bad" };

// login-ui.js Morph's own members that the glue uses; the rest are its own.
type MorphTheme = { t1: string; ring: string; dark: boolean };
type Morph = { hear(detail: object): void; theme: (now: number) => MorphTheme; destroy(): void };
declare global {
  interface Window {
    AxiomLoginMorph: new (root: HTMLElement, lg: HTMLElement, engine: OceanEngine, transition?: object) => Morph;
  }
}

const GLYPHS = ":;t%SX8#@/\\<>=+*";
const glyph = (i: number, t: number) => GLYPHS[Math.floor(hash(i, t) * GLYPHS.length)] ?? "";

// startMorph puts the solid on the sea. Morph's root is the element the sea's world is measured in and whose
// data-state says the sign-in page is up (trans.js's frame, where the sea covers the frame); here the sea's canvas
// fills .lg-sea, so that is its root, and the page is always up. Its canvas (.lg-stg) is put first in .lg-sea, and
// Site.dc.html's rule for it, which the Login board lacks, is set through the CSSOM: over the sea's canvas, under the
// brand and the pitch.
function startMorph(frame: HTMLElement, sea: HTMLElement, engine: OceanEngine, shape: Shape) {
  sea.dataset.state = "login";
  const morph = new window.AxiomLoginMorph(sea, frame, engine);
  // Morph.theme reads getComputedStyle(--ring) straight into canvas fillStyle, which rejects the runtime
  // light-dark(...) values brandStyle writes onto :root; resolve the tokens through CSSOM instead.
  let ink: MorphTheme | undefined;
  let inkAt = -1e9;
  morph.theme = (now: number) => {
    if (ink && now >= inkAt && now - inkAt < 500) return ink;
    inkAt = now;
    ink = {
      t1: colour(frame, "var(--text1)"),
      ring: colour(frame, "var(--ring)"),
      dark: frame.classList.contains("dark"),
    };
    return ink;
  };
  const canvas = sea.querySelector<HTMLCanvasElement>(":scope > canvas.lg-stg");
  if (canvas) {
    const place = {
      position: "absolute",
      inset: "0",
      width: "100%",
      height: "100%",
      zIndex: "1",
      pointerEvents: "none",
    };
    Object.assign(canvas.style, place);
  }
  // A step's event carries what the form holds, as login-ui.js's Login emits it; the page's latest are kept for it.
  const held = { ok: false, n: 0 };
  const show = (step: Shape) => morph.hear({ type: "step", step, ...held });
  show(shape);
  return {
    show,
    hear: (heard: Heard) => {
      if (heard.type === "email") held.ok = heard.ok;
      if (heard.type === "digit") held.n = heard.n;
      morph.hear(heard);
    },
    recolour: () => {
      ink = undefined;
    },
    destroy: () => {
      morph.destroy();
      sea.removeAttribute("data-state");
    },
  };
}

type TitleCell = { x: number; y: number; r: number; blank: boolean };

// The title's characters where they are drawn, in the decode canvas's box.
function cellsOf(chars: HTMLElement[], box: HTMLElement): TitleCell[] {
  const frame = box.getBoundingClientRect();
  return chars.map((n) => {
    const b = n.getBoundingClientRect();
    return {
      x: b.left - frame.left + b.width / 2,
      y: b.top - frame.top + b.height / 2,
      r: b.right - frame.left,
      blank: (n.textContent ?? "").trim() === "",
    };
  });
}

// A character d ms before it resolves: a dot while it waits, a cycling glyph as it comes close, the ring colour next.
function waiting(
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

// decode resolves the title's characters one after another, as the website's hero does, and blinks the caret twice
// after the last. The characters' colour is set through the CSSOM while they wait, and given back as each resolves.
function decode(title: HTMLElement, canvas: HTMLCanvasElement): () => void {
  const chars = [...title.querySelectorAll<HTMLElement>(".ch")];
  const box = canvas.parentElement;
  if (!chars.length || !box) return () => {};
  const cells = cellsOf(chars, box);
  const ink = {
    ring: colour(box, "var(--ring)"),
    dim: colour(box, "var(--text2)"),
    fs: Number.parseFloat(getComputedStyle(chars[0] as HTMLElement).fontSize) || 40,
  };
  const family = mono();
  const at = (i: number) => 80 + i * 60;
  const end = at(cells.length - 1);
  const last = cells[cells.length - 1] as TitleCell;
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  const W = box.offsetWidth;
  const H = box.offsetHeight;
  canvas.width = Math.round(W * dpr);
  canvas.height = Math.round(H * dpr);
  const x = canvas.getContext("2d") as CanvasRenderingContext2D;
  let ms = 0;
  for (const c of chars) c.style.color = "transparent";
  const release = own(canvas, (dt) => {
    ms += dt * 1000;
    const tick = Math.floor(ms / 50);
    x.setTransform(dpr, 0, 0, dpr, 0, 0);
    x.clearRect(0, 0, W, H);
    x.textAlign = "center";
    x.textBaseline = "middle";
    x.font = `500 ${Math.round(ink.fs * 0.62)}px ${family}`;
    cells.forEach((c, i) => {
      const d = at(i) - ms;
      if (d <= 0) (chars[i] as HTMLElement).style.removeProperty("color");
      else if (!c.blank) waiting(x, c, d, glyph(i, tick), ink);
    });
    const after = ms - end;
    if (after >= 0 && (after < 260 || (after >= 400 && after < 660))) caret(x, last, ink);
    x.globalAlpha = 1;
    if (after >= 700) stop();
  });
  const stop = () => {
    release();
    for (const c of chars) c.style.removeProperty("color");
    x.clearRect(0, 0, canvas.width, canvas.height);
  };
  return stop;
}

function caret(x: CanvasRenderingContext2D, last: TitleCell, ink: { ring: string; fs: number }) {
  x.globalAlpha = 1;
  x.fillStyle = ink.ring;
  const w = Math.max(2, Math.round(ink.fs * 0.05));
  x.fillRect(Math.round(last.r + ink.fs * 0.06), Math.round(last.y - ink.fs * 0.42), w, Math.round(ink.fs * 0.84));
}

// The Login board's sea (its ROSTER and the options its boot passes the engine).
const roster = [
  { kind: "hammerhead", size: 1 },
  { kind: "classic", size: 0.72 },
  { kind: "classic", size: 0.52 },
];

export type Login = {
  show: (shape: Shape) => void;
  hear: (heard: Heard) => void;
  // recolour takes the page's colours again (the scheme, light or dark, or the site's brand) and draws in them
  recolour: (light: boolean) => void;
  // decode plays the title's decode once, for an arrival; the page passes its title and the form's decode canvas.
  decode: (title: HTMLElement, canvas: HTMLCanvasElement) => void;
  // endDecode ends a decode still playing, its title shown and its canvas cleared: the title has changed under it
  endDecode: () => void;
  destroy: () => void;
};

// The options the Login board's boot passes its sea, sized to the frame.
function seaOptions(frame: HTMLElement, canvas: HTMLCanvasElement, light: boolean) {
  const side = Math.min(frame.clientWidth || innerWidth, frame.clientHeight || innerHeight);
  return {
    font: mono(),
    light,
    whale: null,
    bgLight: "#030302",
    bgDark: "#111113",
    sizeK: (0.64 * side) / Math.max(1, Math.min(canvas.clientWidth || innerWidth, canvas.clientHeight || innerHeight)),
    maxLen: 640,
    warm: 2,
    virtualScale: 1,
    cell: Math.min(13, Math.max(8, Math.round((frame.clientWidth || innerWidth) / 124))),
  };
}

// startLogin starts the sea and the solid on frame, the Login board's .lg, showing shape's.
export function startLogin(frame: HTMLElement, light: boolean, shape: Shape): Login {
  const canvas = frame.querySelector<HTMLCanvasElement>("canvas.sea");
  const seaBox = frame.querySelector<HTMLElement>(".lg-sea");
  if (!canvas || !seaBox) throw new Error("startLogin: the frame has no .lg-sea with a canvas.sea");
  const engine = buildSea(canvas, roster, seaOptions(frame, canvas, light));
  const morph = startMorph(frame, seaBox, engine, shape);
  let stopDecode = () => {};
  // the solid draws from the engine's raster (Morph's onRaster), so this one tick drives both
  const release = own(canvas, (dt) => engine.tick(dt));
  return {
    show: morph.show,
    hear: morph.hear,
    // recolour updates the sea and clears the solid's cached resolved tokens so scheme and brand changes take effect on the next frame
    recolour: (light) => {
      engine.setLight(light);
      morph.recolour();
    },
    decode: (title, decodeCanvas) => {
      stopDecode = decode(title, decodeCanvas);
    },
    endDecode: () => stopDecode(),
    destroy: () => {
      stopDecode();
      release();
      morph.destroy();
      engine.destroy();
    },
  };
}
