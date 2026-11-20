/// <reference types="node" />
import { readFileSync } from "node:fs";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { Glyph } from "./glyph";

// Read as a file: vitest resolves a module stylesheet with ?raw to its class map in some runs, not to its text.
const css = readFileSync(new URL("glyph.module.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// The hover motion is a function of one variable: every moved part is moved only through --icon-go, which is 0 at
// rest, so an icon not hovered stays in place.
it("moves every part of an icon only through --icon-go", () => {
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
