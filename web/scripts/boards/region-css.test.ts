import { expect, it } from "vitest";
import { regionCSS } from "./region-css.ts";
import { JSDOM } from "./template.ts";

const css = ".ov .card{color:red}\n.ov .iso-wire{stroke:blue}\n.ov .other{color:green}";

function elements(markup: string) {
  const { document } = new JSDOM(`<div class="ov">${markup}</div>`).window;
  return new Set([...document.querySelectorAll(".ov *")]);
}

// A rule for markup an engine inserts at run time (the connect screen's fetched figure) is not in the cut markup, so
// it is dropped unless its class is declared; kept, it applies to any element inside the region's scope.
it("keeps rules for declared run-time markup, scoped inside the region", () => {
  const out = regionCSS(css, elements('<div class="card"></div>'), ["iso-"]);
  expect(out).toContain(":global(.ov .card):where(.scope){color:red}");
  expect(out).toContain(":global(.ov .iso-wire):where(.scope *){stroke:blue}");
  expect(out).not.toContain("other");
  expect(regionCSS(css, elements('<div class="card"></div>'))).not.toContain("iso-wire");
});
