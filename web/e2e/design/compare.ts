import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { expect, type Page, type TestInfo } from "@playwright/test";
import type { Region, Ruling } from "./rulings.ts";
import { freeze } from "./stage.ts";

type Rect = { x: number; y: number; width: number; height: number };

// The colour a ruled region is blanked with on both shots: one no board or page uses, so a region is plain to see.
const blank = "#ff00ff";

// Differences up to anti-aliasing: text rasterised in two documents differs in a few edge pixels.
const maxDiffPixelRatio = 0.002;

export type Comparison = {
  board: string;
  page: string;
  diff?: string;
  // The share of pixels that differ outside the ruled regions; 0 when within anti-aliasing.
  differentRatio: number;
  rulings: string[];
  error?: string;
};

async function rects(page: Page, region: Region | undefined, basis: string, side: string): Promise<Rect[]> {
  if (!region) return [];
  if (!("selector" in region)) return [region];
  const boxes = await page.locator(region.selector).evaluateAll((elements) =>
    elements.map((element) => {
      const { x, y, width, height } = element.getBoundingClientRect();
      return { x, y, width, height };
    }),
  );
  if (!boxes.length) {
    throw new Error(`ruling "${basis}": ${region.selector} matches nothing on the ${side}`);
  }
  return boxes;
}

// shot blanks the given rectangles over everything, captures the viewport with CSS animations frozen, and takes
// the blanks away again, so the next shot of the page starts from the page.
export async function shot(page: Page, blanks: Rect[] = []) {
  await page.evaluate(
    ({ blanks, colour }) => {
      for (const rect of blanks) {
        const cover = document.createElement("div");
        cover.dataset.designBlank = "";
        // One CSSOM property at a time: the page's policy refuses style attributes and cssText.
        Object.assign(cover.style, {
          position: "fixed",
          zIndex: "2147483647",
          left: `${rect.x}px`,
          top: `${rect.y}px`,
          width: `${rect.width}px`,
          height: `${rect.height}px`,
          background: colour,
        });
        document.body.append(cover);
      }
    },
    { blanks, colour: blank },
  );
  await freeze(page);
  const image = await page.screenshot({ animations: "disabled", caret: "hide", scale: "css" });
  await page.evaluate(() => {
    for (const cover of document.querySelectorAll("[data-design-blank]")) cover.remove();
  });
  return image;
}

// compare shoots both sides with the scene's ruled regions blanked, writes board.png and page.png to the scene's
// directory (the board shot is the snapshot Playwright compares with), and compares them with Playwright's own image
// comparison. On a difference it copies Playwright's diff image beside them. It never throws for a difference: the
// caller decides after recording it.
export async function compare(board: Page, page: Page, scene: Ruling[], key: string, info: TestInfo) {
  const blanks: Rect[] = [];
  for (const ruling of scene) {
    blanks.push(...(await rects(board, ruling.board, ruling.basis, "board")));
    blanks.push(...(await rects(page, ruling.page, ruling.basis, "page")));
  }
  const reference = info.snapshotPath(key, "board.png");
  const result: Comparison = {
    board: reference,
    page: join(dirname(reference), "page.png"),
    differentRatio: 0,
    rulings: scene.map((ruling) => ruling.basis),
  };
  mkdirSync(dirname(reference), { recursive: true });
  writeFileSync(reference, await shot(board, blanks));
  const actual = await shot(page, blanks);
  writeFileSync(result.page, actual);
  try {
    expect(actual).toMatchSnapshot([key, "board.png"], { maxDiffPixelRatio });
  } catch (error) {
    result.error = error instanceof Error ? error.message : String(error);
    // Playwright says "N pixels (ratio 0.12 of all image pixels) are different", or that the sizes differ.
    result.differentRatio = Number(result.error.match(/ratio ([\d.]+) of all image pixels/)?.[1] ?? 1);
    const diff = info.attachments.find((attachment) => attachment.name.endsWith("diff.png"))?.path;
    if (diff) {
      result.diff = join(dirname(reference), "diff.png");
      writeFileSync(result.diff, readFileSync(diff));
    }
  }
  return result;
}
