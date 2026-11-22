// Decode pen for [data-dx] text slots during the tiles transition (trans.js.txt:61, 143–156, 282–283):
// mode "r" (links and buttons) highlights the active tip in var(--ring) and hides the unwritten tail in transparent
// ink so layout never shifts; mode "s" (the .fam model strip) runs one ring-coloured and up to three dim cycling
// mono glyphs ahead of the settled prefix. Nodes are built once per slot and styled through CSSOM rather than
// innerHTML, keeping the page within CSP and avoiding per-frame DOM reconstruction.
import { hash } from "./hash";
import { GLYPHS } from "./word-tiles";

export type Slot = { el: HTMLElement; text: string; mode: "r" | "s"; d: number };

type Nodes = {
  head: Text;
  ring: HTMLSpanElement;
  trail: HTMLSpanElement | null;
  rest: HTMLSpanElement;
};

type SavedProp = { value: string; priority: string };
type Segments = { head: string; ring: string; trail: string; rest: string } | null;

type SlotState = {
  index: number;
  chars: string[];
  key: string;
  mounted: boolean;
  nodes: Nodes | null;
  anchor: HTMLElement | null | undefined;
  anchorHadAttr: boolean;
  anchorSaved: SavedProp | null;
  hidUnderline: boolean;
  addedAria: boolean;
};

const states = new WeakMap<Slot, SlotState>();

const glyph = (i: number, tick: number) => GLYPHS[Math.floor(hash(i, tick) * GLYPHS.length)] ?? ":";

function initState(text: string, index: number, anchor?: HTMLElement | null): SlotState {
  return {
    index,
    chars: [...text],
    key: "",
    mounted: false,
    nodes: null,
    anchor,
    anchorHadAttr: anchor?.hasAttribute("style") ?? false,
    anchorSaved: null,
    hidUnderline: false,
    addedAria: false,
  };
}

function stateOf(slot: Slot): SlotState {
  let state = states.get(slot);
  if (!state) {
    state = initState(slot.text, 0);
    states.set(slot, state);
  }
  return state;
}

function makeSpan(color: string): HTMLSpanElement {
  const span = document.createElement("span");
  span.style.color = color;
  return span;
}

function ensureNodes(slot: Slot, state: SlotState): Nodes {
  if (state.nodes) return state.nodes;
  const nodes: Nodes = {
    head: document.createTextNode(""),
    ring: makeSpan("var(--ring)"),
    trail: slot.mode === "s" ? makeSpan("var(--text2)") : null,
    rest: makeSpan("transparent"),
  };
  state.nodes = nodes;
  return nodes;
}

function underlinedAnchor(el: HTMLElement): HTMLElement | null {
  const a = el.closest("a");
  if (!a) return null;
  const cs = getComputedStyle(a);
  // Real browsers always populate textDecorationLine; jsdom may only expose shorthand textDecoration.
  return (cs.textDecorationLine || cs.textDecoration).includes("underline") ? a : null;
}

function setUnderlineHidden(slot: Slot, state: SlotState, hidden: boolean) {
  if (state.anchor === undefined) {
    state.anchor = underlinedAnchor(slot.el);
    state.anchorHadAttr = state.anchor?.hasAttribute("style") ?? false;
  }
  const a = state.anchor;
  if (!a || hidden === state.hidUnderline) return;
  if (hidden) {
    state.anchorSaved = {
      value: a.style.getPropertyValue("text-decoration-color"),
      priority: a.style.getPropertyPriority("text-decoration-color"),
    };
    a.style.setProperty("text-decoration-color", "transparent");
    state.hidUnderline = true;
    return;
  }
  const saved = state.anchorSaved;
  if (saved?.value) a.style.setProperty("text-decoration-color", saved.value, saved.priority);
  else a.style.removeProperty("text-decoration-color");
  if (!state.anchorHadAttr && a.style.length === 0) a.removeAttribute("style");
  state.anchorSaved = null;
  state.hidUnderline = false;
}

function unpen(slot: Slot, state: SlotState) {
  if (state.mounted) {
    slot.el.textContent = slot.text;
    state.mounted = false;
  }
  if (state.addedAria) {
    slot.el.removeAttribute("aria-label");
    state.addedAria = false;
  }
  if (state.hidUnderline) setUnderlineHidden(slot, state, false);
  else if (state.anchor && !state.anchorHadAttr && state.anchor.style.length === 0) {
    state.anchor.removeAttribute("style");
  }
  state.key = "";
}

function segments(slot: Slot, state: SlotState, written: number, tick: number): Segments {
  const { chars, index } = state;
  const n = chars.length;
  const k = !(written > 0) ? 0 : written >= 1 ? n : Math.min(n, Math.ceil(written * n));
  if (k >= n) return null;
  if (slot.mode === "s") {
    const cyc = (c: string, i: number) => (c === " " ? c : glyph(i + 97 * index, tick));
    return {
      head: chars.slice(0, k).join(""),
      ring: cyc(chars[k] ?? "", k),
      trail: chars
        .slice(k + 1, k + 4)
        .map((c, j) => cyc(c, k + 1 + j))
        .join(""),
      rest: chars.slice(k + 4).join(""),
    };
  }
  return {
    head: chars.slice(0, Math.max(0, k - 1)).join(""),
    ring: k > 0 ? (chars[k - 1] ?? "") : "",
    trail: "",
    rest: chars.slice(k).join(""),
  };
}

function syncNodes(nodes: Nodes, seg: NonNullable<Segments>) {
  if (nodes.head.data !== seg.head) nodes.head.data = seg.head;
  if (nodes.ring.textContent !== seg.ring) nodes.ring.textContent = seg.ring;
  if (nodes.trail && nodes.trail.textContent !== seg.trail) nodes.trail.textContent = seg.trail;
  if (nodes.rest.textContent !== seg.rest) nodes.rest.textContent = seg.rest;
}

// slotsOf collects every [data-dx] element under scope, optionally filtering with skip and recording each element's
// centre distance from a trigger point for wave ordering (trans.js.txt:61, 282–283). also names words the markup does
// not mark (the Login board's language links, whose own text is the word): each is a link word unless a marked one is
// inside it.
export function slotsOf(
  scope: Element,
  from?: { x: number; y: number },
  skip?: (el: HTMLElement) => boolean,
  also?: string,
): Slot[] {
  const out: Slot[] = [];
  let index = 0;
  for (const el of scope.querySelectorAll<HTMLElement>(also ? `[data-dx], ${also}` : "[data-dx]")) {
    if (skip?.(el) || (!el.dataset.dx && el.querySelector("[data-dx]"))) continue;
    const text = el.textContent ?? "";
    let d = 0;
    if (from) {
      const r = el.getBoundingClientRect();
      d = Math.hypot(r.left + r.width / 2 - from.x, r.top + r.height / 2 - from.y);
    }
    const slot: Slot = { el, text, mode: el.dataset.dx === "s" ? "s" : "r", d };
    // Resolve underlined ancestor up front at start() so frame ticks perform no getComputedStyle reads.
    states.set(slot, initState(text, index++, underlinedAnchor(el)));
    out.push(slot);
  }
  return out;
}

// pen draws slot at fraction written in [0, 1] (tick steps the cycling glyphs in mode "s"), skipping all DOM writes
// when the rendered split is unchanged and restoring plain text once written reaches 1.
export function pen(slot: Slot, written: number, tick: number): void {
  const state = stateOf(slot);
  const seg = segments(slot, state, written, tick);
  const key = seg ? `${seg.head}\0${seg.ring}\0${seg.trail}\0${seg.rest}` : "";
  if (key === state.key) return;
  if (!seg) {
    unpen(slot, state);
    return;
  }
  state.key = key;
  const nodes = ensureNodes(slot, state);
  syncNodes(nodes, seg);
  if (!state.mounted) {
    // Keep the accessible name steady while text is split across spans or replaced with cycling glyphs.
    if (!slot.el.hasAttribute("aria-label")) {
      slot.el.setAttribute("aria-label", slot.text);
      state.addedAria = true;
    }
    const children: Node[] = nodes.trail
      ? [nodes.head, nodes.ring, nodes.trail, nodes.rest]
      : [nodes.head, nodes.ring, nodes.rest];
    slot.el.replaceChildren(...children);
    state.mounted = true;
  }
  setUnderlineHidden(slot, state, true);
}

// restore puts every penned slot back as plain text and clears any underline or aria-label overrides.
export function restore(slots: Slot[]): void {
  for (const slot of slots) {
    const state = states.get(slot);
    if (state) unpen(slot, state);
  }
}
