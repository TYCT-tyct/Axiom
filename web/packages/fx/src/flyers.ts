// Ephemeral transition layers created inside .tr for the duration of one tiles run and removed at its end
// (Site.head.html:488–504, trans.js.txt:55–57, 110, 1032–1033):
// - canvas.ui at z-index 3 (above .ho z 1 and .lg z 2, under .fly z 4)
// - .fly at z-index 4 holding clones of the brand (.fb), headline (.fh) and lede (.fs), styled through CSSOM because
//   the product's stylesheets do not ship the prototype's .fly/.ui rules; pressing .fb reverses a forward run.
import { brandFlight, pitchFlight } from "./fly";

export type SharedRefs = {
  brandFrom: HTMLElement | null;
  brandTo: HTMLElement | null;
  h1: HTMLElement | null;
  lede: HTMLElement | null;
  pitchH: HTMLElement | null;
  pitchS: HTMLElement | null;
};

export type Flyers = {
  readonly ui: HTMLCanvasElement;
  readonly ctx: CanvasRenderingContext2D | null;
  readonly dpr: number;
  readonly brand: ReturnType<typeof brandFlight> | null;
  readonly pitch: ReturnType<typeof pitchFlight> | null;
  destroy(): void;
};

const baseFlyer = {
  position: "absolute",
  left: "0",
  top: "0",
  margin: "0",
  visibility: "hidden",
  boxSizing: "border-box",
} as const;

const typeProps = [
  "font-family",
  "font-style",
  "font-weight",
  "letter-spacing",
  "color",
  "text-decoration-line",
] as const;

const inlineProps = [
  "font-family",
  "font-style",
  "font-weight",
  "font-size",
  "letter-spacing",
  "padding-right",
  "margin-right",
  "display",
  "white-space",
] as const;

function copyComputed(from: Element, to: HTMLElement, props: readonly string[]) {
  const cs = getComputedStyle(from);
  for (const prop of props) {
    const val = cs.getPropertyValue(prop);
    if (val) to.style.setProperty(prop, val);
  }
}

// Clones children of source without duplicating IDs or [data-dx] markers that other scripts query.
function cloneContents(source: Element, target: HTMLElement) {
  for (const child of source.childNodes) {
    const clone = child.cloneNode(true);
    if (clone instanceof Element) {
      clone.removeAttribute("id");
      clone.removeAttribute("data-dx");
      for (const node of clone.querySelectorAll("[id], [data-dx]")) {
        node.removeAttribute("id");
        node.removeAttribute("data-dx");
      }
    }
    target.append(clone);
  }
}

// Headline spans (.k, .kern, .ph) inherit styles scoped under .ho in the product's CSS Modules; mirror their computed
// typography onto the cloned spans inside .fh so the flyer keeps the italic key, kerning and phrase breaks.
function mirrorHeadlineSpans(from: Element, clone: HTMLElement) {
  const src = from.querySelectorAll(".k, .kern, .ph");
  const dst = clone.querySelectorAll<HTMLElement>(".k, .kern, .ph");
  for (let i = 0; i < src.length && i < dst.length; i++) {
    copyComputed(src[i] as Element, dst[i] as HTMLElement, inlineProps);
  }
}

function makeBrandCopy(from: HTMLElement, onTurn: () => void): HTMLSpanElement {
  const fb = document.createElement("span");
  fb.className = "fb";
  cloneContents(from, fb);
  Object.assign(fb.style, baseFlyer, {
    display: "flex",
    alignItems: "center",
    gap: "10px",
    pointerEvents: "auto",
    cursor: "pointer",
  });
  copyComputed(from, fb, [...typeProps, "font-size", "line-height"]);
  fb.addEventListener("click", (e) => {
    e.preventDefault();
    onTurn();
  });
  return fb;
}

function makeTypeCopy(from: HTMLElement, className: string, mirrorSpans: boolean): HTMLParagraphElement {
  const el = document.createElement("p");
  el.className = className;
  cloneContents(from, el);
  Object.assign(el.style, baseFlyer, {
    zIndex: "0",
    whiteSpace: "nowrap",
    textAlign: "left",
  });
  copyComputed(from, el, typeProps);
  if (mirrorSpans) mirrorHeadlineSpans(from, el);
  return el;
}

function makeCanvas(W: number, H: number) {
  const ui = document.createElement("canvas");
  ui.className = "ui";
  ui.setAttribute("aria-hidden", "true");
  Object.assign(ui.style, {
    position: "absolute",
    inset: "0",
    zIndex: "3",
    width: "100%",
    height: "100%",
    display: "block",
    pointerEvents: "none",
  });
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  ui.width = Math.max(1, Math.round(W * dpr));
  ui.height = Math.max(1, Math.round(H * dpr));
  return { ui, ctx: ui.getContext("2d"), dpr };
}

function mountPitch(fly: HTMLElement, refs: SharedRefs, lower: boolean) {
  if (lower || !refs.h1 || !refs.lede || !refs.pitchH || !refs.pitchS) return null;
  const fh = makeTypeCopy(refs.h1, "fh lens", true);
  const fs = makeTypeCopy(refs.lede, "fs lens", false);
  fly.append(fh, fs);
  return { fh, fs, from: { h: refs.h1, s: refs.lede }, to: { h: refs.pitchH, s: refs.pitchS } };
}

export function mountFlyers(
  root: HTMLElement,
  refs: SharedRefs,
  W: number,
  H: number,
  lower: boolean,
  onBrandPress: () => void,
): Flyers {
  const { ui, ctx, dpr } = makeCanvas(W, H);
  const fly = document.createElement("div");
  fly.className = "fly";
  fly.setAttribute("aria-hidden", "true");
  Object.assign(fly.style, {
    position: "absolute",
    inset: "0",
    zIndex: "4",
    pointerEvents: "none",
  });

  const pitchSpec = mountPitch(fly, refs, lower);
  const fb = refs.brandFrom && refs.brandTo ? makeBrandCopy(refs.brandFrom, onBrandPress) : null;
  if (fb) fly.append(fb);
  root.append(ui, fly);

  const brand = fb && refs.brandFrom && refs.brandTo ? brandFlight(fb, refs.brandFrom, refs.brandTo) : null;
  const pitch = pitchSpec ? pitchFlight({ h: pitchSpec.fh, s: pitchSpec.fs }, pitchSpec.from, pitchSpec.to) : null;

  return {
    ui,
    ctx,
    dpr,
    brand,
    pitch,
    destroy() {
      brand?.restore();
      pitch?.restore();
      fly.remove();
      ui.remove();
    },
  };
}
