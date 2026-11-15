import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { contrast } from "../packages/ui/src/brand.ts";

// The contrast packages/ui/src/css/tokens.css promises, read from the file itself so a changed value is checked as
// written: words at 4.5:1 on every surface they sit on and on their own tint over a resting surface (WCAG 1.4.3),
// control edges and the focus ring at 3:1 on any surface next to them (1.4.11, 2.4.13), in both schemes and on both
// neutral scales.

type Rgb = [number, number, number];
const css = readFileSync(join(import.meta.dirname, "../packages/ui/src/css/tokens.css"), "utf8");

function declarations(selector: string) {
  const block = css.split(`${selector} {`)[1]?.split("\n}")[0] ?? "";
  return Object.fromEntries([...block.matchAll(/(--[\w-]+):\s*([^;]+);/g)].map((m) => [m[1], m[2]?.trim()]));
}
const base = declarations(":root");
const slate = declarations(':root[data-neutral="slate"]');

// value resolves a token to #rrggbb in one scheme, following var() and taking light-dark()'s side.
function value(name: string, dark: boolean, neutral: Record<string, string | undefined>): Rgb {
  let text = neutral[name] ?? base[name];
  for (let depth = 0; text && depth < 8; depth++) {
    const pair = /^light-dark\((.+?),\s*(.+)\)$/.exec(text);
    if (pair) {
      text = (dark ? pair[2] : pair[1]) ?? "";
    }
    const ref = /^var\((--[\w-]+)\)$/.exec(text);
    if (ref?.[1]) {
      text = neutral[ref[1]] ?? base[ref[1]];
    } else if (/^#[0-9a-f]{6}$/i.test(text)) {
      return [1, 3, 5].map((i) => Number.parseInt(text?.slice(i, i + 2) ?? "", 16)) as Rgb;
    }
  }
  throw new Error(`tokens.css: ${name} does not resolve to #rrggbb (${text})`);
}

const tint = (surface: Rgb, solid: Rgb): Rgb => surface.map((v, i) => v * 0.88 + (solid[i] ?? 0) * 0.12) as Rgb;

const cases = ["sand", "slate"].flatMap((scale) =>
  ["light", "dark"].map((scheme) => ({
    scale,
    scheme,
    neutral: scale === "slate" ? slate : {},
    dark: scheme === "dark",
  })),
);

describe.each(cases)("tokens.css, $scale $scheme", ({ neutral, dark }) => {
  const color = (name: string) => value(name, dark, neutral);
  // A badge's tint sits on a resting surface; text alone may also sit on a hovered row.
  const resting = ["--page", "--panel", "--cardBg", "--raised", "--pop"];
  const surfaces = [...resting, "--hover"];

  it.each(["--t1", "--t2", "--t3", "--accentT", "--okT", "--warnT", "--badT"])("%s reads on every surface", (text) => {
    for (const surface of surfaces) {
      expect(contrast(color(text), color(surface)), `${text} on ${surface}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it.each([
    ["--okT", "--ok"],
    ["--warnT", "--warn"],
    ["--badT", "--bad"],
  ])("%s reads on a 12 percent tint of %s", (text, solid) => {
    for (const surface of resting) {
      expect(contrast(color(text), tint(color(surface), color(solid)))).toBeGreaterThanOrEqual(4.5);
    }
  });

  it.each(["--ctl-line", "--focus"])("%s stands out from every surface beside it", (edge) => {
    for (const surface of ["--n1", "--n2", "--n3", "--n4"]) {
      expect(contrast(color(edge), color(surface)), `${edge} on ${surface}`).toBeGreaterThanOrEqual(3);
    }
  });

  it("marks the selection apart from its background", () => {
    expect(contrast(color("--sel-ind"), color("--selected"))).toBeGreaterThanOrEqual(3);
  });
});
