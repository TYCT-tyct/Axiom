import { expect, it } from "vitest";
import { regionCSS } from "./region-css.ts";
import { JSDOM } from "./template.ts";

const css =
  ".ov .card{color:red}\n.ov .iso-wire{stroke:blue}\n.ov .other{color:green}\n.ov .legend .iso-wire{fill:red}";

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
  // a rule that also needs a part the region does not have is not kept
  expect(out).not.toContain("legend");
  expect(regionCSS(css, elements('<div class="card"></div>'))).not.toContain("iso-wire");
});

// A rule that needs a state a script sets at run time (.on, data-tab) is dropped for markup that has not got it, unless
// the state is declared; kept, its original selector stays.
it("keeps rules that need a declared run-time state", () => {
  const states = ".ov .card.on{opacity:1}\n.ov[data-tab] .card{display:none}\n.ov .card.off{opacity:0}";
  const out = regionCSS(states, elements('<div class="card"></div>'), [], ["on", "data-tab"]);
  expect(out).toContain(":global(.ov .card.on):where(.scope){opacity:1}");
  expect(out).toContain(":global(.ov[data-tab] .card):where(.scope){display:none}");
  expect(out).not.toContain(".off");
  expect(regionCSS(states, elements('<div class="card"></div>'))).not.toContain(".card.on");
});
