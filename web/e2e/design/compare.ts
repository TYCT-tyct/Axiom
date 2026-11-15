import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { expect, type Page, type TestInfo } from "@playwright/test";
import type { Region, Ruling } from "./rulings.ts";
import { freeze } from "./stage.ts";

type Rect = { x: number; y: number; width: number; height: number };

// The colour a ruled region is blanked with on both shots: one no board or page uses, so a region is plain to see.
const blank = "#ff00ff";

// No differing pixel is allowed outside the ruled regions, and no share of the page excuses one: a missing 40px icon is
// 0.12% of a 1440x900 shot. Anti-aliasing is left to Playwright's comparator, which skips the pixels it detects as
// anti-aliased and counts a pixel only past its per-pixel colour threshold.
const maxDiffPixels = 0;

export type Comparison = {
  board: string;
  page: string;
  diff?: string;
  // The pixels that differ outside the ruled regions, and their share of the shot; -1 when the sizes differ.
  differentPixels: number;
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
    differentPixels: 0,
    differentRatio: 0,
    rulings: scene.map((ruling) => ruling.basis),
  };
  mkdirSync(dirname(reference), { recursive: true });
  writeFileSync(reference, await shot(board, blanks));
  const actual = await shot(page, blanks);
  writeFileSync(result.page, actual);
  try {
    expect(actual).toMatchSnapshot([key, "board.png"], { maxDiffPixels });
  } catch (error) {
    result.error = error instanceof Error ? error.message : String(error);
    // Playwright says "N pixels (ratio 0.12 of all image pixels) are different", or that the sizes differ. The ratio it
    // prints is rounded to hundredths; the shot's size from its PNG header (width and height at bytes 16 and 20) gives
    // the exact one.
    const pixels = Number(result.error.match(/(\d+) pixels \(ratio/)?.[1] ?? -1);
    result.differentPixels = pixels;
    result.differentRatio = pixels < 0 ? -1 : pixels / (actual.readUInt32BE(16) * actual.readUInt32BE(20));
    const diff = info.attachments.find((attachment) => attachment.name.endsWith("diff.png"))?.path;
    if (diff) {
      result.diff = join(dirname(reference), "diff.png");
      writeFileSync(result.diff, readFileSync(diff));
    }
  }
  return result;
}
