// The theme a site's brand colour makes (docs/frontend.md「主题与自由度」): the accent and the primary button, derived so
// that whatever the colour, text stays at 4.5:1 or more in both colour schemes (WCAG 2.2 AA) and a button never loses its
// edge. Only a colour that is not #rrggbb is refused; one that would fail is corrected, because the site owner picks a
// colour, not a contrast ratio.

type Rgb = readonly [number, number, number];
type Scheme = { surfaces: readonly Rgb[]; extreme: Rgb; line: string; softAlpha: number; lineAlpha: number };

const black: Rgb = [0, 0, 0];
const white: Rgb = [255, 255, 255];

// The surfaces accent text and the primary button sit on: the page, and the panels and dialogs on it (--bg, --s1 and
// --s2 in styles.css; e2e/gate.spec.ts checks the rendered pages),
// and the extreme the accent moves toward when it is too faint. line is the button's edge when the button is too close
// to any of the surfaces (--t1).
const light: Scheme = {
  surfaces: [white, [247, 248, 249], [240, 241, 243]],
  extreme: black,
  line: "#0d0e10",
  softAlpha: 0.09,
  lineAlpha: 0.4,
};
const dark: Scheme = {
  surfaces: [
    [8, 9, 10],
    [15, 16, 18],
    [21, 22, 25],
  ],
  extreme: white,
  line: "#f7f8f8",
  softAlpha: 0.13,
  lineAlpha: 0.42,
};

const text = 4.5;
// WCAG 1.4.11: the edge of a control against its surroundings.
const edge = 3;

function luminance([r, g, b]: Rgb) {
  const [lr, lg, lb] = [r, g, b].map((v) => {
    const c = v / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * (lr ?? 0) + 0.7152 * (lg ?? 0) + 0.0722 * (lb ?? 0);
}

export function contrast(a: Rgb, b: Rgb) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return ((hi ?? 0) + 0.05) / ((lo ?? 0) + 0.05);
}

const mix = (a: Rgb, b: Rgb, t: number): Rgb =>
  [0, 1, 2].map((i) => Math.round((a[i] ?? 0) * (1 - t) + (b[i] ?? 0) * t)) as unknown as Rgb;
const hex = (c: Rgb) => `#${c.map((v) => v.toString(16).padStart(2, "0")).join("")}`;
const rgba = (c: Rgb, alpha: number) => `rgb(${c.join(" ")} / ${alpha})`;
const inkOn = (c: Rgb) => (contrast(c, black) >= contrast(c, white) ? black : white);

// The brand colour, moved toward the scheme's extreme in 2% steps until it reads on every surface; the extreme itself
// does (21:1 and 18:1), so the walk ends.
function accentFor(brand: Rgb, scheme: Scheme) {
  for (let t = 0; t < 1; t += 0.02) {
    const c = mix(brand, scheme.extreme, t);
    if (scheme.surfaces.every((s) => contrast(c, s) >= text)) {
      return c;
    }
  }
  return scheme.extreme;
}

function tokensFor(brand: Rgb, scheme: Scheme) {
  const accent = accentFor(brand, scheme);
  return {
    "--accent": hex(accent),
    "--accent-ink": hex(inkOn(accent)),
    "--accent-soft": rgba(accent, scheme.softAlpha),
    "--accent-line": rgba(accent, scheme.lineAlpha),
    "--btn": hex(brand),
    "--btn-ink": hex(inkOn(brand)),
    "--btn-line": scheme.surfaces.every((s) => contrast(brand, s) >= edge) ? "transparent" : scheme.line,
  };
}

// brandStyle is the custom properties to set on the root for a brand colour written #rrggbb, each as the light value
// and the dark one the way styles.css writes them; undefined for any other text.
export function brandStyle(color: string): Record<string, string> | undefined {
  const match = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(color);
  if (!match) {
    return undefined;
  }
  const brand = [match[1], match[2], match[3]].map((v) => Number.parseInt(v ?? "0", 16)) as unknown as Rgb;
  const [l, d] = [tokensFor(brand, light), tokensFor(brand, dark)];
  return Object.fromEntries(
    Object.entries(l).map(([name, value]) => [name, `light-dark(${value}, ${d[name as keyof typeof d]})`]),
  );
}
