import { useEffect } from "react";

// The theme a site's brand colour makes (docs/frontend.md「主题与自由度」): the accent and the primary button, derived so
// that whatever the colour, text stays at 4.5:1 or more in both colour schemes (WCAG 2.2 AA) and a button never loses its
// edge. Only a colour that is not #rrggbb is refused; one that would fail is corrected, because the site owner picks a
// colour, not a contrast ratio.

type Rgb = readonly [number, number, number];
// washes are how much of the solid accent the pages mix (in oklab) into --n1 for the tints accent text sits on: tokens.css's
// --accentWash (tags, avatars, key chips) and, in light, the website's key chip under the pointer (16%, the connect
// screen's --chipHover); n1 is that step of each neutral scale, sand's then slate's.
type Scheme = {
  surfaces: readonly Rgb[];
  extreme: Rgb;
  line: string;
  softAlpha: number;
  lineAlpha: number;
  washes: readonly number[];
  n1: readonly Rgb[];
};

const black: Rgb = [0, 0, 0];
const white: Rgb = [255, 255, 255];

// The surfaces accent text and the primary button sit on: the page, and the panels and dialogs on it (--bg, --s1 and
// --s2 (--page, --cardBg and --hover of css/tokens.css); e2e/gate.spec.ts checks the
// rendered pages), and the extreme the accent moves toward when it is too faint. line is the button's edge when the
// button is too close to any of the surfaces (--t1).
export const light: Scheme = {
  surfaces: [
    [249, 249, 248],
    [253, 253, 252],
    [241, 240, 239],
  ],
  extreme: black,
  line: "#21201c",
  softAlpha: 0.09,
  lineAlpha: 0.4,
  washes: [0.12, 0.16],
  n1: [
    [253, 253, 252],
    [252, 252, 253],
  ],
};
export const dark: Scheme = {
  surfaces: [
    [17, 17, 16],
    [25, 25, 24],
    [34, 34, 33],
  ],
  extreme: white,
  line: "#eeeeec",
  softAlpha: 0.13,
  lineAlpha: 0.42,
  washes: [0.21],
  n1: [
    [17, 17, 16],
    [17, 17, 19],
  ],
};

const text = 4.5;
// WCAG 1.4.11: the edge of a control against its surroundings, and a fill that carries meaning.
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

// sRGB to oklab and back (Björn Ottosson's matrices), for the mixes tokens.css makes with color-mix(in oklab, …).
const toLinear = (v: number) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
const fromLinear = (v: number) => (v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055);
function oklab([r, g, b]: Rgb) {
  const [lr, lg, lb] = [r, g, b].map((v) => toLinear(v / 255)) as [number, number, number];
  const l = Math.cbrt(0.4122214708 * lr + 0.5363325363 * lg + 0.0514459929 * lb);
  const m = Math.cbrt(0.2119034982 * lr + 0.6806995451 * lg + 0.1073969566 * lb);
  const s = Math.cbrt(0.0883024619 * lr + 0.2817188376 * lg + 0.6299787005 * lb);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
}
function srgb([L, a, b]: number[]): Rgb {
  const l = ((L ?? 0) + 0.3963377774 * (a ?? 0) + 0.2158037573 * (b ?? 0)) ** 3;
  const m = ((L ?? 0) - 0.1055613458 * (a ?? 0) - 0.0638541728 * (b ?? 0)) ** 3;
  const s = ((L ?? 0) - 0.0894841775 * (a ?? 0) - 1.291485548 * (b ?? 0)) ** 3;
  const linear = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
  return linear.map((v) => Math.round(Math.min(1, Math.max(0, fromLinear(v))) * 255)) as unknown as Rgb;
}
// color-mix(in oklab, c share, base)
const mixOklab = (c: Rgb, base: Rgb, share: number) => {
  const [x, y] = [oklab(c), oklab(base)];
  return srgb(x.map((v, i) => v * share + (y[i] ?? 0) * (1 - share)));
};

// The brand colour, moved toward the scheme's extreme in 2% steps until it stands out from every one of surfaces by
// ratio; the extreme itself does (21:1 and 18:1), so the walk ends.
function accentFor(brand: Rgb, scheme: Scheme, ratio: number, surfaces = scheme.surfaces) {
  for (let t = 0; t < 1; t += 0.02) {
    const c = mix(brand, scheme.extreme, t);
    if (surfaces.every((s) => contrast(c, s) >= ratio)) {
      return c;
    }
  }
  return scheme.extreme;
}

// The tints the pages make from a solid accent for each neutral scale: the backgrounds of accent text too.
export const washesOf = (solid: Rgb, scheme: Scheme) =>
  scheme.washes.flatMap((share) => scheme.n1.map((n1) => mixOklab(solid, n1, share)));

// The tokens.css accent pair, which the board-drawn pages (the home page's screens among them) read, beside the older
// names: --accentT for text and --accentSolid for fills and dots; tokens.css mixes the wash and the soft tint from the
// solid. Accent text reads on the page's surfaces and on the wash too, where tags, avatars and key chips put it.
function tokensFor(brand: Rgb, scheme: Scheme) {
  const solid = accentFor(brand, scheme, edge);
  const accent = accentFor(brand, scheme, text, [...scheme.surfaces, ...washesOf(solid, scheme)]);
  return {
    "--accentT": hex(accent),
    "--accentSolid": hex(solid),
    "--accent": hex(accent),
    "--accent-ink": hex(inkOn(accent)),
    "--accent-soft": rgba(accent, scheme.softAlpha),
    "--accent-line": rgba(accent, scheme.lineAlpha),
    "--btn": hex(brand),
    "--btn-ink": hex(inkOn(brand)),
    "--btn-line": scheme.surfaces.every((s) => contrast(brand, s) >= edge) ? "transparent" : scheme.line,
  };
}

// useBrandStyle puts the brand's custom properties on the document root, the one ancestor of both the page and the
// dialogs the app renders beside it (RecoveryCodesHost). A page-level wrapper would leave those dialogs on the default
// palette. Removing the properties restores tokens.css when the colour goes away.
export function useBrandStyle(color: string | undefined) {
  useEffect(() => {
    const tokens = color ? brandStyle(color) : undefined;
    if (!tokens) {
      return;
    }
    const root = document.documentElement;
    for (const [name, value] of Object.entries(tokens)) {
      root.style.setProperty(name, value);
    }
    return () => {
      for (const name of Object.keys(tokens)) {
        root.style.removeProperty(name);
      }
    };
  }, [color]);
}

// brandStyle is the custom properties to set on the root for a brand colour written #rrggbb, each as the light value
// and the dark one the way tokens.css writes them; undefined for any other text.
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
