import { classes } from "../classes";
import s from "./glyph.module.css";

// The design's icons (AxIcons.dc.html): a 20 × 20 grid, 1.5 stroke, round caps, in the current text colour. Each is
// drawn twice, an outline group and a filled group; selected shows the filled one, with its signal part (`sig`) in the
// accent. `data-p` names the parts the stylesheet moves on hover (a0…a4) and how the filled group treats them: `cut` is
// drawn in the background colour (--cut) to cut a line out of the fill, `nf` stays unfilled, `ff`/`oo` exist only in
// the filled/outline group, `fo` is always filled, `gn` is a gauge needle. An icon never carries meaning alone: text
// sits beside it, or the control that holds it has an aria-label.
//
// more, warn and updown have hover motions on the board but no drawing there; they are drawn here on the same grid.
//
// Each part is [data-p, shape, geometry]: a rect as "x y width height rx", a circle as "cx cy r", a path as its d, a
// group as its parts.
type Part =
  | readonly [p: string, shape: "r" | "c" | "p", geometry: string]
  | readonly [p: string, shape: "g", parts: readonly Part[]];

const icons = {
  overview: [
    ["a1", "r", "3 3 6 8 2"],
    ["a2", "r", "11 3 6 5 2"],
    ["a3 sig", "r", "11 10 6 7 2"],
    ["a4", "r", "3 13 6 4 2"],
  ],
  usage: [
    ["a1", "r", "3.25 10 3 7 1"],
    ["a2", "r", "8.5 6 3 11 1"],
    ["a3 sig", "r", "13.75 3 3 14 1"],
  ],
  logs: [
    ["a1", "r", "3.5 3 13 14 2.75"],
    ["a2 cut", "p", "M7 7.25h6M7 10.25h6M7 13.25h3.5"],
  ],
  keys: [
    [
      "a1",
      "g",
      [
        ["", "c", "6.75 10 3.5"],
        ["", "p", "M10.25 10H17M14.5 10v2.5M17 10v2"],
      ],
    ],
  ],
  presets: [
    ["a1 sig", "p", "M10 3 17 6.75 10 10.5 3 6.75z"],
    ["a2 nf", "p", "m3 10.25 7 3.75 7-3.75"],
    ["a3 nf", "p", "m3 13.5 7 3.75 7-3.75"],
  ],
  route: [
    ["a0 sig", "c", "3.5 10 1.5"],
    ["a1 nf", "p", "M5 10h3c3 0 3-5 6-5h2.5M8 10c3 0 3 5 6 5h2.5"],
  ],
  byok: [
    ["a1 sig", "p", "M7.25 2.75v3.5M12.75 2.75v3.5"],
    ["a2", "p", "M5 6.5h10v3a5 5 0 0 1-10 0z"],
    ["a3", "p", "M10 14.5v2.75"],
  ],
  budget: [
    ["a1 oo", "p", "M5.05 15.95A7 7 0 1 1 14.95 15.95"],
    ["a1 ff", "p", "M5.05 15.95A7 7 0 1 1 14.95 15.95z"],
    ["a2 cut gn", "p", "M10 11V5.5"],
    ["a3 cut", "c", "10 11 .6"],
  ],
  privacy: [
    ["a1", "p", "M10 2.75 16 5v5c0 3.6-2.6 6.2-6 7.25C6.6 16.2 4 13.6 4 10V5z"],
    ["a2 cut", "p", "m7.5 10 1.75 1.75L12.75 8"],
  ],
  security: [
    ["a1", "r", "4 9 12 8.5 2.25"],
    ["a2 nf", "p", "M6.75 9V6.75a3.25 3.25 0 0 1 6.5 0V9"],
    ["a3 cut", "p", "M10 12.25v2"],
  ],
  wallet: [
    ["a1 nf", "p", "M3 6.5A2.5 2.5 0 0 1 5.5 4H14.5"],
    ["a2", "r", "3 6.5 14 10 2.5"],
    ["a3 sig", "c", "13.5 11.5 1.1"],
  ],
  team: [
    [
      "a1",
      "g",
      [
        ["", "c", "7.5 7 2.75"],
        ["", "p", "M2.75 16.5c0-2.9 2.1-4.6 4.75-4.6s4.75 1.7 4.75 4.6"],
      ],
    ],
    [
      "a2",
      "g",
      [
        ["", "c", "13.75 7.5 2.25"],
        ["", "p", "M13.6 11.9c2.3 0 3.9 1.5 3.9 4.1"],
      ],
    ],
  ],
  settings: [
    ["a0", "p", "M3 6.25h14M3 13.75h14"],
    ["a1 sig", "c", "7 6.25 2"],
    ["a2", "c", "13 13.75 2"],
  ],
  docs: [
    ["a1", "p", "M4 15.5V4.75A2 2 0 0 1 6 2.75h10v11.5H5.25A1.25 1.25 0 0 0 4 15.5zm0 0a1.75 1.75 0 0 0 1.75 1.75H16"],
  ],
  status: [["a1 nf", "p", "M2.5 10.25h3l2-4.75 3.5 9 2-4.25h4.5"]],
  collapse: [
    ["a1", "r", "3 3.5 14 13 2.5"],
    ["a2 cut", "p", "M8 3.5v13"],
  ],
  appearance: [
    ["a1 nf", "c", "10 10 6.75"],
    ["a2 fo", "p", "M10 3.25a6.75 6.75 0 0 1 0 13.5z"],
  ],
  search: [
    ["a1", "c", "8.75 8.75 5"],
    ["a1", "p", "m12.5 12.5 4 4"],
  ],
  models: [
    ["a1", "p", "M10 2.75 16.5 6.4v7.2L10 17.25 3.5 13.6V6.4z"],
    ["a2", "p", "M3.75 6.5 10 10l6.25-3.5M10 10v7"],
  ],
  copy: [
    ["a1", "r", "7 7 9.5 9.5 2.25"],
    ["a2", "p", "M13 4.5A1.5 1.5 0 0 0 11.5 3.5H5.25A1.75 1.75 0 0 0 3.5 5.25v6.25A1.5 1.5 0 0 0 4.5 13"],
  ],
  plus: [["a1", "p", "M10 4v12M4 10h12"]],
  export: [
    ["a1", "p", "M10 3.5v8.5M6.5 8.75 10 12.25l3.5-3.5"],
    ["a2", "p", "M3.5 13.5v1.25A1.75 1.75 0 0 0 5.25 16.5h9.5a1.75 1.75 0 0 0 1.75-1.75V13.5"],
  ],
  more: [
    [
      "a1 fo",
      "g",
      [
        ["", "c", "4.5 10 1.25"],
        ["", "c", "10 10 1.25"],
        ["", "c", "15.5 10 1.25"],
      ],
    ],
  ],
  close: [["a1", "p", "m5 5 10 10M15 5 5 15"]],
  check: [["a1", "p", "m4 10.5 4 4 8-9"]],
  warn: [
    ["a1", "p", "M8.7 3.5a1.5 1.5 0 0 1 2.6 0l6 10.5a1.5 1.5 0 0 1-1.3 2.25H4a1.5 1.5 0 0 1-1.3-2.25z"],
    ["a2 cut", "p", "M10 7.75v3.5M10 13.6v.01"],
  ],
  updown: [["a1 nf", "p", "m6.5 7.5 3.5-3.5 3.5 3.5M6.5 12.5l3.5 3.5 3.5-3.5"]],
} satisfies Record<string, readonly Part[]>;

// draw turns a table of parts into SVG elements; a part without a role has no data-p.
function draw(parts: readonly Part[]) {
  return parts.map(([p, shape, geometry], i) => {
    const key = `${i}`;
    const role = p ? p : undefined;
    if (shape === "g") {
      return (
        <g key={key} data-p={role}>
          {draw(geometry)}
        </g>
      );
    }
    if (shape === "p") {
      return <path key={key} data-p={role} d={geometry} />;
    }
    const [a, b, c, d, rx] = geometry.split(" ");
    return shape === "c" ? (
      <circle key={key} data-p={role} cx={a} cy={b} r={c} />
    ) : (
      <rect key={key} data-p={role} x={a} y={b} width={c} height={d} rx={rx} />
    );
  });
}

export type GlyphName = keyof typeof icons;

export const glyphNames = Object.keys(icons) as GlyphName[];

// Glyph is the icon of the new components, at 16 (beside text in a button, menu or note) or 20 pixels (navigation);
// selected fills it. The hover motion plays when the icon, or a control that sets --icon-hover on hover, is under the
// pointer. Icon keeps the current pages' set until they move (#747): the admin's
// first screen has no room for both (docs/frontend.md「前端性能指标」).
export function Glyph({ name, size = 16, selected = false }: { name: GlyphName; size?: 16 | 20; selected?: boolean }) {
  return (
    <svg
      viewBox="0 0 20 20"
      width={size}
      height={size}
      className={classes(s.icon, s[name], selected && s.selected)}
      aria-hidden="true"
    >
      <g className={s.o}>{draw(icons[name])}</g>
      <g className={s.f}>{draw(icons[name])}</g>
    </svg>
  );
}
