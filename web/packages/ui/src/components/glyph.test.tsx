import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { Glyph } from "./glyph";
import { moduleCss, reduced } from "./module-css";

const css = moduleCss("glyph");

// Under reduced motion the hover motion does not play: the variable every motion is a function of is never anything
// but 0, nothing transitions, and every moved part is moved only through it, so it stays in place on hover.
it("keeps every icon still under reduced motion", () => {
  const still = reduced(css);
  expect([...still.matchAll(/--icon-go:\s*([^;]+);/g)].map((m) => m[1])).toEqual(["0"]);
  expect(still).not.toMatch(/transition|animation/);
  const moves = [...css.matchAll(/(?:^|\s)(transform|stroke-dashoffset):\s*([^;]+);/g)];
  expect(moves.length).toBeGreaterThan(27);
  for (const [, , value] of moves) {
    expect(value).toContain("var(--icon-go)");
  }
});

it("fills a selected icon", () => {
  expect(renderToStaticMarkup(<Glyph name="overview" selected />)).toMatch(/class="[^"]*_selected_/);
  expect(renderToStaticMarkup(<Glyph name="overview" />)).not.toContain("_selected_");
});
