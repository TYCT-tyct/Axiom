import { describe, expect, it } from "vitest";
import { brandStyle, contrast } from "./brand";

type Rgb = [number, number, number];
const rgb = (hex: string) => [1, 3, 5].map((i) => Number.parseInt(hex.slice(i, i + 2), 16)) as Rgb;

// The colours the acceptance gate names (docs/frontend.md「验收门槛」), and a grid of the rest of the space.
const extremes = ["#ff0000", "#ffff00", "#050505", "#fafafa", "#39ff14", "#ffffff", "#000000", "#777777"];
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
    btn: pick("--btn", i),
    ink: pick("--btn-ink", i),
    line: pick("--btn-line", i),
  }));
}

describe("brandStyle", () => {
  // --bg and --s2 of styles.css, light then dark; the rendered pages are checked against the real ones by e2e/gate.spec.ts.
  const pages = [rgb("#ffffff"), rgb("#08090a")];
  const cards = [rgb("#f0f1f3"), rgb("#151619")];

  it.each([...extremes, ...grid])("keeps %s readable in both schemes", (color) => {
    schemes(color).forEach((s, i) => {
      for (const surfaceColor of [pages[i], cards[i]]) {
        expect(contrast(rgb(s.accent), surfaceColor as Rgb)).toBeGreaterThanOrEqual(4.5);
      }
      expect(contrast(rgb(s.ink), rgb(s.btn))).toBeGreaterThanOrEqual(4.5);
      if (contrast(rgb(s.btn), pages[i] as Rgb) < 3) {
        expect(contrast(rgb(s.line), pages[i] as Rgb)).toBeGreaterThanOrEqual(3);
      } else {
        expect(s.line).toBe("transparent");
      }
    });
  });

  it.each(["", "red", "#fff", "#12345", "#1234567", "#gggggg", "rgb(1 2 3)"])("refuses %j", (color) => {
    expect(brandStyle(color)).toBeUndefined();
  });
});
