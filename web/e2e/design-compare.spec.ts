import { expect, type Page, test } from "@playwright/test";
import { compare } from "./design/compare.ts";
import type { Ruling } from "./design/rulings.ts";
import { stage } from "./design/stage.ts";

// What a ruling of glyphs excuses in the screenshot comparison (design/compare.ts), on pages of its own so it runs
// without the design boards (design.spec.ts needs them): the text's own extent, and nothing of the control around it.

const view = {
  viewport: { width: 240, height: 80 },
  colorScheme: "light" as const,
  timezoneId: "Asia/Shanghai",
  time: new Date("2026-10-24T04:00:00Z"),
};

// A link drawn as a control: padding, a fill and a border around its text.
async function link(page: Page, text: string, style = "") {
  await stage(page, view);
  await page.goto("about:blank");
  await page.setViewportSize(view.viewport);
  await page.setContent(`<!doctype html><body style="margin:0;background:#fff">
    <a style="display:inline-block;margin:16px;padding:8px 24px;font:16px monospace;background:#e8e8ec;border:1px solid #60646c;${style}">${text}</a>`);
}

const glyphs: Ruling[] = [
  { scene: "", basis: "test", board: { selector: "a" }, page: { selector: "a" }, glyphs: true },
];

test.describe("a ruling of glyphs", () => {
  for (const { name, text, style, fails } of [
    { name: "excuses other glyphs in the same box", text: "EM", style: "", fails: false },
    { name: "fails a control whose fill changed", text: "EN", style: "background:#b00000", fails: true },
    { name: "fails a control whose border changed", text: "EN", style: "border-color:#b00000", fails: true },
    { name: "fails a box that moved", text: "EN", style: "margin-left:36px", fails: true },
  ]) {
    test(name, async ({ context }, info) => {
      const board = await context.newPage();
      const page = await context.newPage();
      await link(board, "EN");
      await link(page, text, style);
      const result = await compare(board, page, glyphs, `glyphs-${info.title.replace(/\W+/g, "-")}`, info);
      if (fails) expect(result.error).toBeDefined();
      else expect(result.error).toBeUndefined();
      await board.close();
      await page.close();
    });
  }
});
