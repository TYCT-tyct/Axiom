import { describe, expect, it } from "vitest";
import { brandPreview, brandStyle, contrast, dark, light, washesOf } from "./brand";

type Rgb = [number, number, number];
const rgb = (hex: string) => [1, 3, 5].map((i) => Number.parseInt(hex.slice(i, i + 2), 16)) as Rgb;

// The colours the acceptance gate names (docs/frontend.md「验收门槛」), and a grid of the rest of the space.
const extremes = [
  "#ff0000",
  "#ffff00",
  "#050505",
  "#fafafa",
  "#39ff14",
  "#ffffff",
  "#000000",
  "#777777",
  "#888888",
  "#929292",
];
const grid = [0, 64, 128, 192, 255].flatMap((r) =>
  [0, 64, 128, 192, 255].flatMap((g) =>
    [0, 64, 128, 192, 255].map((b) => `#${[r, g, b].map((v) => v.toString(16).padStart(2, "0")).join("")}`),
  ),
);

// brandStyle writes light-dark(light, dark); the two values of a token.
function schemes(color: string) {
  const style = brandStyle(color) ?? {};
  const pick = (name: string, i: number) =>
    (style[name]?.match(/^light-dark\((.*)\)$/)?.[1] ?? "").split(", ")[i] ?? "";
  return [0, 1].map((i) => ({
    accent: pick("--accent", i),
    text: pick("--accentT", i),
    solid: pick("--accentSolid", i),
    btn: pick("--btn", i),
    ink: pick("--btn-ink", i),
    line: pick("--btn-line", i),
  }));
}

// Accent text on the wash tokens.css mixes from the solid (tags, avatars, key chips), in both neutral scales.
function onWashes(text: string, solid: string, scheme: number) {
  for (const wash of washesOf(rgb(solid), scheme === 0 ? light : dark)) {
    expect(contrast(rgb(text), wash)).toBeGreaterThanOrEqual(4.5);
  }
}

describe("brandStyle", () => {
  // the page, card and hover surfaces (--page, --cardBg and --hover in css/tokens.css), light then dark; the rendered pages are checked against the real ones by
  // e2e/gate.spec.ts. A button sits on any of them: the page, a panel, a dialog.
  const surfaces = [["#f9f9f8", "#fdfdfc", "#f1f0ef"].map(rgb), ["#111110", "#191918", "#222221"].map(rgb)];

  it.each([...extremes, ...grid])("keeps %s readable in both schemes", (color) => {
    schemes(color).forEach((s, i) => {
      for (const surfaceColor of surfaces[i] as Rgb[]) {
        expect(contrast(rgb(s.accent), surfaceColor)).toBeGreaterThanOrEqual(4.5);
        expect(contrast(rgb(s.text), surfaceColor)).toBeGreaterThanOrEqual(4.5);
        expect(contrast(rgb(s.solid), surfaceColor)).toBeGreaterThanOrEqual(3);
      }
      onWashes(s.text, s.solid, i);
      expect(contrast(rgb(s.ink), rgb(s.btn))).toBeGreaterThanOrEqual(4.5);
      if ((surfaces[i] as Rgb[]).some((surface) => contrast(rgb(s.btn), surface) < 3)) {
        for (const surface of surfaces[i] as Rgb[]) {
          expect(contrast(rgb(s.line), surface)).toBeGreaterThanOrEqual(3);
        }
      } else {
        expect(s.line).toBe("transparent");
      }
    });
  });

  it.each(["", "red", "#fff", "#12345", "#1234567", "#gggggg", "rgb(1 2 3)"])("refuses %j", (color) => {
    expect(brandStyle(color)).toBeUndefined();
    expect(brandPreview(color)).toBeUndefined();
  });

  it("matches brandStyle tokens and reports WCAG-passing contrast ratios in brandPreview", () => {
    for (const color of extremes) {
      const preview = brandPreview(color);
      expect(preview).toBeDefined();
      const [l, d] = schemes(color);
      expect(preview?.light.accentText).toBe(l?.text);
      expect(preview?.light.accentSolid).toBe(l?.solid);
      expect(preview?.dark.accentText).toBe(d?.text);
      expect(preview?.dark.accentSolid).toBe(d?.solid);
      expect(preview?.light.textContrast).toBeGreaterThanOrEqual(4.5);
      expect(preview?.light.solidContrast).toBeGreaterThanOrEqual(3);
      expect(preview?.dark.textContrast).toBeGreaterThanOrEqual(4.5);
      expect(preview?.dark.solidContrast).toBeGreaterThanOrEqual(3);
    }
  });
});
