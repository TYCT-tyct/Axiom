import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, it } from "vitest";
import { cut } from "./connect-figure.ts";

const web = join(import.meta.dirname, "../..");
const outputs = cut(readFileSync(join(web, "packages/boards/inputs/Site.logic.html"), "utf8"));

it("keeps the committed figure and kit current with the board (run `pnpm --dir web site-assets`)", () => {
  for (const [name, text] of Object.entries(outputs))
    expect(readFileSync(join(web, "site/src/home", name), "utf8") === text, name).toBe(true);
});

it("leaves no style attribute in the figure, which the page's content security policy would block", () => {
  expect(outputs["connect-figure.svg"]).not.toContain("style=");
});

// The board names the drawing in Chinese only; it is decoration, which the screen's words carry, so it is not read.
it("takes the figure out of what is read", () => {
  const start = outputs["connect-figure.svg"]?.slice(0, 200) ?? "";
  expect(start).toContain('aria-hidden="true"');
  expect(start).not.toMatch(/role="img"|aria-label=/);
});
