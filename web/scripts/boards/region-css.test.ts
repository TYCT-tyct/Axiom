import { describe, expect, it } from "vitest";
import { regionCSS } from "./region-css.ts";
import { JSDOM } from "./template.ts";

// The cut markup of a region: the elements the converter keeps rules for.
function elements(markup: string) {
  const { document } = new JSDOM(`<div class="ov">${markup}</div>`).window;
  return new Set([...document.querySelectorAll(".ov, .ov *")]);
}

// What the page renders from the generated CSS: the region's elements carry the scope class, and the markup an engine
// inserts into .fig does not. A rule is applied by dropping :global(), as CSS modules do.
function applies(css: string, markup: string, target: string) {
  const { document } = new JSDOM(`<div class="ov scope">${markup}</div>`).window;
  const element = document.querySelector(target) as Element;
  return [...css.matchAll(/^([^{]+)\{/gm)]
    .flatMap((match) => (match[1] as string).split(/,(?=:global)/))
    .filter((selector) => element.matches(selector.replace(/:global\(((?:[^()]|\([^()]*\))*)\)/, "$1")));
}

const figure = { into: ".fig", classes: ["iso-svg", "iso-wire", "iso-new", "iso-card"] };
const region = '<div class="card"></div><div class="legend"></div><div class="fig"></div>';
const rendered =
  '<div class="card scope"></div><div class="legend scope"></div><div class="fig scope"><svg class="iso-svg"><g class="hl"><path class="iso-wire"></path></g></svg></div>';

describe("inserted markup", () => {
  it("keeps the rules that can match it in its container, and applies them there", () => {
    const css = [
      ".ov .card{color:red}",
      ".ov .iso-wire{stroke:blue}",
      ".ov :is(.iso-new,.iso-wire){fill:green}",
      ".ov .fig svg{width:1px}",
      ".ov .hl .iso-wire{stroke:cyan}",
    ].join("\n");
    const out = regionCSS(css, elements(region), figure, ["hl"]);
    const wire = applies(out, rendered, ".iso-wire");
    expect(wire).toEqual(
      expect.arrayContaining([
        expect.stringContaining(".ov .iso-wire"),
        expect.stringContaining(":is(.iso-new,.iso-wire)"),
        expect.stringContaining(".ov .hl .iso-wire"),
      ]),
    );
    expect(applies(out, rendered, "svg")).toEqual([expect.stringContaining(".ov .fig svg")]);
    expect(applies(out, rendered, ".card")).toEqual([expect.stringContaining(".ov .card")]);
  });

  it("drops the rules for parts the region does not have, or that cannot be in the container", () => {
    const css = [
      ".ov .other{color:green}",
      ".ov .legend .iso-wire{fill:red}",
      ".ov .card .iso-wire{fill:red}",
      ".ov .hl .iso-wire{stroke:cyan}",
      ".ov div{margin:0}",
      ".ov .iso-wire:not(.fig *){stroke:red}",
    ].join("\n");
    const out = regionCSS(css, elements(region), figure);
    expect(out).not.toContain("other");
    expect(out).not.toContain("legend");
    expect(out).not.toContain(".card .iso-wire");
    // a rule that excludes the place the markup goes
    expect(out).not.toContain(":not(.fig *)");
    // .hl is not a declared state here
    expect(out).not.toContain(".hl");
    // a type alone, below an ancestor of the container, is the region's own div and not the figure's
    expect(out).not.toContain(":where(.scope *)");
    expect(regionCSS(".ov .iso-wire{stroke:blue}", elements(region))).toBe("");
  });

  it("styles a class the cut and the inserted markup share in both places", () => {
    const out = regionCSS(
      ".ov .iso-card{color:red}",
      elements('<div class="iso-card"></div><div class="fig"></div>'),
      figure,
    );
    const page = '<div class="iso-card scope"></div><div class="fig scope"><i class="iso-card"></i></div>';
    expect(applies(out, page, "div.iso-card")).not.toHaveLength(0);
    expect(applies(out, page, "i.iso-card")).not.toHaveLength(0);
  });
});

describe("run-time states", () => {
  // A rule that needs a state a script sets at run time (.on, data-tab) is dropped for markup that has not got it, unless
  // the state is declared; kept, its original selector stays, wherever the state sits in it.
  it.each([
    [".ov .card.on{opacity:1}", ".ov .card.on"],
    [".ov[data-tab] .card{display:none}", ".ov[data-tab] .card"],
    [".ov .on{opacity:1}", ".ov .on"],
    [".ov .card:has(.on){opacity:1}", ".ov .card:has(.on)"],
    [".ov:not(.on) .card{opacity:1}", ".ov:not(.on) .card"],
  ])("keeps %s once declared", (css, selector) => {
    const card = elements('<div class="card"></div>');
    expect(regionCSS(css, card, undefined, ["on", "data-tab"])).toContain(`:global(${selector})`);
  });

  it.each([".ov .card.on{opacity:1}", ".ov .on{opacity:1}", ".ov .card:has(.on){opacity:1}"])(
    "drops %s when the state is not declared",
    (css) => {
      expect(regionCSS(css, elements('<div class="card"></div>'))).toBe("");
    },
  );

  it("drops a state rule for a part the region does not have", () => {
    expect(regionCSS(".ov .other.on{opacity:1}", elements('<div class="card"></div>'), undefined, ["on"])).toBe("");
  });
});
