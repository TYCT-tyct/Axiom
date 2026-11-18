// The models screen's leads (Site.dc.html M2): the line from the card naming the pick to its route's dot, and the
// hairline bridge from the selected model to the panel, in the stage's own pixels.
type Box = { x: number; y: number; w: number; h: number };
const fixed = (n: number) => n.toFixed(1);

// measurer gives an element's box in the stage's own pixels, which a scaled stage draws smaller or larger
export function measurer(stage: HTMLElement) {
  const S = stage.getBoundingClientRect();
  const k = stage.offsetWidth ? S.width / stage.offsetWidth : 1;
  return (el: Element): Box => {
    const r = el.getBoundingClientRect();
    return { x: (r.left - S.left) / k, y: (r.top - S.top) / k, w: r.width / k, h: r.height / k };
  };
}

// leadOf draws the lead from the card's corner a, over to the column mid, up or down to by and in to the dot at b
export function leadOf(leads: Element, a: readonly [number, number], b: readonly [number, number], mid: number) {
  const [ld, ldt, ldc] = [".ld", ".ldt", ".ldc"].map((s) => leads.querySelector(s));
  if (!ld || !ldt || !ldc) return undefined;
  const draw = (by: number) => {
    ld.setAttribute(
      "d",
      `M${fixed(a[0])} ${fixed(a[1])}V${fixed(a[1] - 12)}H${fixed(mid)}V${fixed(by)}H${fixed(b[0] + 6)}`,
    );
    ldt.setAttribute("cx", fixed(b[0]));
    ldt.setAttribute("cy", fixed(by));
    ldc.setAttribute("cx", fixed(a[0]));
    ldc.setAttribute("cy", fixed(a[1]));
  };
  return { draw, to: b[1], lit: [ld, ldt] };
}

// bridge joins the selected model's row to the panel's edge with a hairline; without a row (the narrow layout) it hides
export function bridge(leads: Element, row: Box | undefined, panel: Box) {
  const [br, bd] = [".br", ".bd"].map((s) => leads.querySelector(s));
  if (!(br instanceof SVGElement) || !(bd instanceof SVGElement)) return;
  br.style.opacity = row ? "1" : "0";
  bd.style.opacity = row ? "1" : "0";
  if (!row) return;
  const y = row.y + row.h / 2;
  br.setAttribute("d", `M${fixed(row.x + row.w + 4)} ${fixed(y)}H${fixed(panel.x - 4)}`);
  bd.setAttribute("cx", fixed(panel.x - 4));
  bd.setAttribute("cy", fixed(y));
}
