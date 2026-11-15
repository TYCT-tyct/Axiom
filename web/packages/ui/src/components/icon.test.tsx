/// <reference types="node" />
import { readFileSync } from "node:fs";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { Icon } from "./icon";

// Read as a file: vitest resolves a module stylesheet with ?raw to its class map in some runs, not to its text.
const css = readFileSync(new URL("icon.module.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// The stylesheet with every @media (prefers-reduced-motion: no-preference) block taken out: what applies under reduced
// motion.
function reduced(text: string) {
  const head = "@media (prefers-reduced-motion: no-preference)";
  let out = text;
  for (let at = out.indexOf(head); at >= 0; at = out.indexOf(head)) {
    let depth = 0;
    let end = out.indexOf("{", at);
    do {
      depth += out[end] === "{" ? 1 : out[end] === "}" ? -1 : 0;
      end++;
    } while (depth > 0);
    out = out.slice(0, at) + out.slice(end);
  }
  return out;
}

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

it.each([
  { name: "keys", want: /viewBox="0 0 20 20"/ },
  { name: "key", want: /viewBox="0 0 20 20"/ },
  { name: "pencil", want: /viewBox="0 0 16 16"/ },
] as const)("draws $name", ({ name, want }) => {
  expect(renderToStaticMarkup(<Icon name={name} />)).toMatch(want);
});

it("fills a selected icon and keeps a caller's class", () => {
  const html = renderToStaticMarkup(<Icon name="overview" selected className="extra" />);
  expect(html).toMatch(/class="[^"]*_selected_[^"]* extra"/);
});
