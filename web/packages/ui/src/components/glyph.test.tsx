import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { Glyph } from "./glyph";

it("fills a selected icon", () => {
  expect(renderToStaticMarkup(<Glyph name="overview" selected />)).toMatch(/class="[^"]*_selected_/);
  expect(renderToStaticMarkup(<Glyph name="overview" />)).not.toContain("_selected_");
});
