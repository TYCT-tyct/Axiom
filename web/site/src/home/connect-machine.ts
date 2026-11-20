import { figure, kit } from "./connect-figure.gen";

// The connect screen's machine (Site.dc.html M1's figure): a step selector drawn in isometric. The drawing is the SVG
// asset the screen fetches; this module is what moves it: the shaft that climbs to a vendor's level, the wiper that
// turns to a model's contact, the key, the lamp, the display and the points of light that run along its wires. The
// wiper and the shaft are redrawn at any angle and height with the board's own drawing kit.
const GEO = figure.geo;
const P = figure.P;
const rad = (degrees: number) => (degrees * Math.PI) / 180;
export type Point = readonly [number, number];
export const at = (list: readonly number[][] | undefined, i: number): Point => {
  const p = list?.[i];
  return [p?.[0] ?? 0, p?.[1] ?? 0];
};

// the wiper at any angle, drawn by the kit so it can turn smoothly
function wiperSvg(deg: number) {
  const c = Math.cos(rad(deg));
  const s = Math.sin(rad(deg));
  const p = (r: number, h: number) => [r * c - s * h, r * s + c * h];
  const blade = [p(6, 2.6), p(60, 1.6), p(GEO.R_PIN - 0.6, 1), p(GEO.R_PIN - 0.6, -1), p(60, -1.6), p(6, -2.6)];
  const [near, far] = [GEO.R_PIN - 3.2, GEO.R_PIN - 0.4];
  const tip = [p(near, 1.4), p(far, 1.4), p(far, -1.4), p(near, -1.4)];
  const solid = (ring: number[][], z: number, height: number, options: object) =>
    kit.solidSvg(kit.extrude(ring, z, height, P, options), { tone: "hi", lit: true });
  return solid(blade, GEO.WZ - 0.6, 1.2, { bevel: 0.2 }) + solid(tip, GEO.WZ - 1, 1.6, {});
}
// the shaft between the hub and the top frame, and above it, for any lift
const midSvg = (lift: number) =>
  kit.solidSvg(kit.cylinder(0, 0, GEO.SHAFT_R, GEO.hubZ + 4 + lift, GEO.TOP_Z - GEO.hubZ - 4 - lift, P, 24), {
    tone: "hi",
    crease: "none",
  });
function topSvg(lift: number) {
  const z1 = GEO.SHAFT_TOP + lift;
  const z0 = GEO.TOP_Z + GEO.TOP_T;
  const knurl = kit.lineSvg(kit.knurl(0, 0, GEO.SHAFT_R, z1 - 14, z1 - 1, 18, P, 0.4), { tone: "lo" });
  return (
    kit.solidSvg(kit.cylinder(0, 0, GEO.SHAFT_R, z0, z1 - z0, P, 24), { tone: "hi", crease: "none", inner: knurl }) +
    kit.solidSvg(kit.cylinder(0, 0, GEO.SHAFT_R + 1.2, z1 - 1, 2, P, 24, 0.3), { tone: "hi", crease: "lo" })
  );
}

const NS = "http://www.w3.org/2000/svg";
// markup of the kit's or the figure's own, as nodes of this document
function nodes(markup: string) {
  const parsed = new DOMParser().parseFromString(`<svg xmlns="${NS}">${markup}</svg>`, "image/svg+xml");
  return [...parsed.documentElement.children].map((child) => document.importNode(child, true));
}

// The opening tags of markup, with their attributes: the kit writes every attribute value in double quotes.
const tags = (markup: string) =>
  [...markup.matchAll(/<([a-z]+)((?:\s+[\w:-]+="[^"]*")*)\s*\/?>/gi)].map(([, name, attributes]) => ({
    name: (name as string).toLowerCase(),
    attributes: [...(attributes as string).matchAll(/([\w:-]+)="([^"]*)"/g)].map(([, key, value]) => [key, value]),
  }));

// redraw puts the kit's markup for a part at another angle or height into group: drawn the same way it has the same
// elements, so only their attributes (the geometry) change and the nodes stay; anything else is drawn afresh. It runs
// every frame of a move, where parsing and replacing the nodes would not do.
function redraw(group: Element, markup: string) {
  const next = tags(markup);
  const present = [...group.querySelectorAll("*")];
  if (next.length !== present.length || next.some((tag, i) => tag.name !== present[i]?.localName)) {
    group.replaceChildren(...nodes(markup));
    return;
  }
  next.forEach((tag, i) => {
    const element = present[i] as Element;
    for (const [key, value] of tag.attributes) {
      if (element.getAttribute(key as string) !== value) element.setAttribute(key as string, value as string);
    }
  });
}

// a list of the drawing's points
export const points = (list: readonly number[][] | undefined) => (list ?? []).map((_, i) => at(list, i));

const list = (root: Element, selector: string) => [...root.querySelectorAll<SVGElement>(selector)];

export class Machine {
  readonly root: SVGGElement;
  readonly pulses: SVGGElement[];
  readonly lamp: Element | null;
  readonly mods: Element[];
  readonly dnames: Element[];
  readonly display: { m: Element | null; v: Element | null };
  private readonly parts = new Map<string, SVGElement>();
  private readonly variants = new Map<string, SVGElement>();
  private readonly pins = new Map<string, Element>();
  private wiper: number | undefined;

  // asset is the fetched SVG as text; it is put into fig, its parts found, and the two light groups hidden (the board
  // draws them with a style attribute, which the page's content security policy blocks)
  constructor(
    readonly fig: HTMLElement,
    asset: string,
  ) {
    const parsed = new DOMParser().parseFromString(asset, "image/svg+xml").documentElement;
    const svg = document.importNode(parsed, true) as unknown as SVGSVGElement;
    this.root = document.createElementNS(NS, "g");
    this.root.id = "axfig-d";
    this.root.append(...svg.childNodes);
    svg.append(this.root);
    fig.replaceChildren(svg);
    this.pulses = list(this.root, ".pulse") as SVGGElement[];
    for (const group of this.pulses) {
      group.style.opacity = "0";
      // a short fading trail behind the head
      for (let i = 4; i >= 0; i--) {
        const dot = document.createElementNS(NS, "circle");
        dot.setAttribute("class", "t");
        dot.setAttribute("r", String(1.5 - i * 0.18));
        dot.style.opacity = String(0.65 - i * 0.12);
        group.insertBefore(dot, group.firstChild);
      }
    }
    for (const el of list(this.root, "[data-m]")) this.parts.set(el.dataset.m as string, el);
    for (const el of list(this.root, "[data-v]")) this.variants.set(el.dataset.v as string, el);
    for (const el of list(this.root, ".pin")) this.pins.set(el.dataset.pin as string, el);
    this.lamp = this.root.querySelector(".lamp");
    this.mods = list(this.root, ".mod");
    this.dnames = list(this.root, ".dname");
    this.display = { m: this.root.querySelector('[data-disp="m"]'), v: this.root.querySelector('[data-disp="v"]') };
  }

  pin(key: string) {
    return this.pins.get(key);
  }
  hover(name: "plug" | "key", on: boolean) {
    (name === "plug" ? this.parts.get("plug") : this.variants.get("key"))?.classList.toggle("hl", on);
  }
  move(name: string, [dx, dy]: Point) {
    this.parts.get(name)?.setAttribute("transform", `translate(${dx.toFixed(2)} ${dy.toFixed(2)})`);
  }
  // the key's two drawings: out of the lock and in it
  swap(name: string, i: number) {
    const group = this.variants.get(name);
    const markup = (figure.variants as Record<string, string[]>)[name]?.[i];
    if (!group || !markup || group.dataset.i === String(i)) return;
    group.replaceChildren(...nodes(markup));
    group.dataset.i = String(i);
  }
  setWiper(deg: number) {
    const group = this.variants.get("wiper");
    if (!group || this.wiper === deg) return;
    redraw(group, wiperSvg(deg));
    this.wiper = deg;
  }
  // the shaft stands z high: the parts on it move up the drawing's vertical, and the shaft itself is drawn again
  setLift(z: number) {
    const [vx, vy] = figure.zv as [number, number];
    for (const name of ["lower", "hub", "wiper"]) this.move(name, [vx * z, vy * z]);
    const [mid, top] = [this.variants.get("mid"), this.variants.get("top")];
    if (mid) redraw(mid, midSvg(z));
    if (top) redraw(top, topSvg(z));
  }
  text(m: string, v: string) {
    if (this.display.m) this.display.m.textContent = m;
    if (this.display.v) this.display.v.textContent = v;
  }
}

// trailAt puts the head of a point of light at at(s) and its trail behind it, a step apart along the wire
export function trailAt(group: Element, position: (s: number) => Point, s: number, step: number) {
  const [hx, hy] = position(s);
  for (const el of group.querySelectorAll(".glow,.core")) {
    el.setAttribute("cx", hx.toFixed(1));
    el.setAttribute("cy", hy.toFixed(1));
  }
  const trail = [...group.querySelectorAll(".t")];
  trail.forEach((dot, i) => {
    const [x, y] = position(Math.max(0, s - (trail.length - i) * step));
    dot.setAttribute("cx", x.toFixed(1));
    dot.setAttribute("cy", y.toFixed(1));
  });
}

// the point a distance s along a polyline, the speed even along its length
export function along(path: readonly Point[]) {
  const lengths = [0];
  for (let i = 1; i < path.length; i++) {
    const [x, y] = path[i] as Point;
    const [px, py] = path[i - 1] as Point;
    lengths.push((lengths[i - 1] as number) + Math.hypot(x - px, y - py));
  }
  const total = lengths[lengths.length - 1] as number;
  const position = (s: number): Point => {
    let i = 1;
    while (i < lengths.length - 1 && (lengths[i] as number) < s) i++;
    const f = (s - (lengths[i - 1] as number)) / Math.max(1e-6, (lengths[i] as number) - (lengths[i - 1] as number));
    const [x0, y0] = path[i - 1] as Point;
    const [x1, y1] = path[i] as Point;
    return [x0 + (x1 - x0) * f, y0 + (y1 - y0) * f];
  };
  return { total, position };
}

export { figure };
