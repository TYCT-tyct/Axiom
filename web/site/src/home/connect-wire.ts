import { at, figure, type Point } from "./connect-machine";
import { addressLine } from "./connect-tools";

// The cable and the lead of the connect screen's card (Site.dc.html M1's drawWire and placeNotes): an SVG laid over the
// screen, in the frame's own pixels, from a socket on the card's edge level with the address line to the tail of the
// plug in the machine, and a lead from the card of the call to the lamp of the vendor's module that took it.
type Box = { x: number; y: number; w: number; h: number };

export class Wire {
  private readonly svg: SVGElement;
  private readonly card: HTMLElement;
  private readonly pre: HTMLElement;
  private readonly note: HTMLElement;
  // how far the plug is drawn out of its socket along the drawing's y axis, while a tool is changed
  plug = 0;
  path: SVGPathElement | null = null;

  constructor(
    private readonly stage: HTMLElement,
    private readonly fig: HTMLElement,
    private readonly narrow: boolean,
  ) {
    this.svg = stage.querySelector(".wire") as SVGElement;
    this.card = stage.querySelector(".code") as HTMLElement;
    this.pre = stage.querySelector("pre") as HTMLElement;
    this.note = stage.querySelector(".rcard") as HTMLElement;
  }

  // a rectangle in the frame's own pixels: the frame may be shown scaled, and its coordinates are the page's
  private local(rect: DOMRect): Box {
    const frame = this.stage.getBoundingClientRect();
    const k = this.stage.offsetWidth ? frame.width / this.stage.offsetWidth : 1;
    return { x: (rect.left - frame.left) / k, y: (rect.top - frame.top) / k, w: rect.width / k, h: rect.height / k };
  }
  // a point of the drawing, in the frame's pixels, and the drawing's scale
  private figPt(p: Point): [number, number, number] {
    const f = this.local(this.fig.getBoundingClientRect());
    const s = f.w / figure.W;
    return [f.x + p[0] * s, f.y + p[1] * s, s];
  }
  private part(selector: string) {
    return this.svg.querySelector(selector) as SVGElement;
  }
  private stroke(el: SVGElement, width: number, transform?: string) {
    el.style.strokeWidth = `${width.toFixed(2)}px`;
    if (transform) el.setAttribute("transform", transform);
  }

  // the cable: from a socket on the card's edge, level with the address line (on the phone, on the card's top edge), to
  // the tail of the plug in the machine
  draw(tool: number) {
    const card = this.local(this.card.getBoundingClientRect());
    const row = this.pre.children[addressLine[tool] as number];
    const line = row ? this.local(row.getBoundingClientRect()) : { x: 0, y: card.y + 80, w: 0, h: 22 };
    const tail = at([figure.plugTail as number[]], 0);
    const y = figure.yv as [number, number];
    const [tx, ty, s] = this.figPt([tail[0] + y[0] * this.plug, tail[1] + y[1] * this.plug]);
    const yl = Math.hypot(y[0], y[1]);
    const w = Math.max(3, 2 * figure.cableR * figure.k * Math.sqrt(1.6) * s);
    const sx = this.narrow ? Math.min(card.x + card.w - 40, Math.max(card.x + 40, tx + 24)) : card.x + card.w + 12;
    const sy = this.narrow ? card.y - 12 : line.y + line.h / 2;
    const c1: Point = this.narrow ? [sx, sy - 60] : [sx + Math.max(60, (tx - sx) * 0.5), sy];
    const reach = Math.max(50, Math.hypot(tx - sx, ty - sy) * 0.42);
    const c2: Point = [tx + (y[0] / yl) * reach, ty + (y[1] / yl) * reach];
    const f = (n: number) => n.toFixed(1);
    this.cable(`M${f(sx)} ${f(sy)}C${f(c1[0])} ${f(c1[1])} ${f(c2[0])} ${f(c2[1])} ${f(tx)} ${f(ty)}`, w);
    this.socket(card, [sx, sy], w);
    this.path = this.part(".cab-o") as unknown as SVGPathElement;
  }
  private cable(d: string, w: number) {
    const [o, i, sh, dk] = [".cab-o", ".cab-i", ".cab-s", ".cab-d"].map((c) => this.part(c)) as [
      SVGElement,
      SVGElement,
      SVGElement,
      SVGElement,
    ];
    for (const el of [dk, o, i, sh]) el.setAttribute("d", d);
    this.stroke(dk, w * 0.26, `translate(0 ${(w * 0.24).toFixed(2)})`);
    this.stroke(o, w + 1);
    this.stroke(i, w);
    this.stroke(sh, w * 0.28, `translate(0 ${(-w * 0.18).toFixed(2)})`);
  }
  // the socket on the card's edge and the plug in it
  private socket(card: Box, [sx, sy]: Point, w: number) {
    const ph = w + 5;
    const set = (el: SVGElement, attrs: Record<string, number>) => {
      for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
    };
    const [socket, plug] = [this.part(".sock-b"), this.part(".plg")];
    if (this.narrow) {
      set(socket, { x: sx - ph / 2 - 3, y: card.y - 4, width: ph + 6, height: 8, rx: 2 });
      set(plug, { x: sx - ph / 2, y: card.y - 13, width: ph, height: 12, rx: 2 });
    } else {
      set(socket, { x: card.x + card.w - 4, y: sy - ph / 2 - 3, width: 8, height: ph + 6, rx: 2 });
      set(plug, { x: card.x + card.w + 1, y: sy - ph / 2, width: 12, height: ph, rx: 2 });
    }
  }

  // a lead from a to b with a dot at each end, lit or not
  private lead(a: Point, b: Point, on: boolean) {
    const lead = this.part(".ld0");
    lead.setAttribute("d", `M${a[0].toFixed(1)} ${a[1].toFixed(1)}L${b[0].toFixed(1)} ${b[1].toFixed(1)}`);
    const [far, near] = [this.part(".dt0"), this.part(".dt1")];
    far.setAttribute("cx", b[0].toFixed(1));
    far.setAttribute("cy", b[1].toFixed(1));
    near.setAttribute("cx", a[0].toFixed(1));
    near.setAttribute("cy", a[1].toFixed(1));
    for (const el of [lead, far]) el.classList.toggle("on", on);
  }
  // notes: from the card of the call to the lamp of the module at the vendor's level
  note0(vendor: number, on: boolean) {
    const c = this.local(this.note.getBoundingClientRect());
    const rack = figure.rack[vendor] as number[][];
    const [lx, ly] = this.figPt(at(rack, rack.length - 1));
    this.lead([c.x + c.w - 28, c.y], [lx, ly], on);
  }
}
