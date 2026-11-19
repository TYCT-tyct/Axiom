import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { expect, type Locator, type Page, type TestInfo } from "@playwright/test";
import { blankCanvases } from "./canvas.ts";
import { pauseFrames, resumeFrames } from "./frames.ts";
import type { Region, Ruling } from "./rulings.ts";
import { freeze, repaint } from "./stage.ts";

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
  if (!region || "canvas" in region) return [];
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

// How far an excused box may sit from the board's: the sides lay out the same text in the same face, so only rounding
// moves a box.
const placeTolerance = 1;

// moved names the boxes of a glyphs ruling that are not where the board's are: a different count, or a box whose
// corner or size is off by more than the tolerance.
function moved(ruling: Ruling, board: Rect[], page: Rect[]): string | undefined {
  const off = (a: Rect, b: Rect) =>
    (["x", "y", "width", "height"] as const).some((side) => Math.abs(a[side] - b[side]) > placeTolerance);
  if (board.length === page.length && page.every((box, i) => !off(board[i] as Rect, box))) return undefined;
  return `ruling "${ruling.basis}" excuses glyphs only, but its boxes moved: board ${JSON.stringify(board)}, page ${JSON.stringify(page)}`;
}

// Rectangle rulings cover everything in their boxes; canvas rulings change only the backing pixels under other DOM.
// Restore both after capture so subsequent interaction tests still exercise the original page.
export async function shot(page: Page, blanks: Rect[] = [], canvases: string[] = []) {
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
  await pauseFrames(page);
  await freeze(page);
  const saved = await blankCanvases(page, canvases);
  try {
    await repaint(page);
    return await page.screenshot({ animations: "disabled", caret: "hide", scale: "css" });
  } finally {
    await saved.evaluate((restore) => restore());
    await saved.dispose();
    await resumeFrames(page);
    await page.evaluate(() => {
      for (const cover of document.querySelectorAll("[data-design-blank]")) cover.remove();
    });
  }
}

// Keep the differing locations in the job log even when artifact storage is unavailable. Red is Playwright's
// default diff colour; anti-aliased pixels are yellow and excluded from these bounds, as from the assertion.
async function diffLocations(page: Page, diff: string) {
  return page.evaluate(
    async (bytes) => {
      const bitmap = await createImageBitmap(new Blob([Uint8Array.from(bytes)], { type: "image/png" }));
      const canvas = document.createElement("canvas");
      canvas.width = bitmap.width;
      canvas.height = bitmap.height;
      const ctx = canvas.getContext("2d") as CanvasRenderingContext2D;
      ctx.drawImage(bitmap, 0, 0);
      bitmap.close();
      const { data } = ctx.getImageData(0, 0, canvas.width, canvas.height);
      let left = canvas.width,
        top = canvas.height,
        right = -1,
        bottom = -1;
      const points: { x: number; y: number; elements: string[] }[] = [];
      for (let i = 0; i < data.length; i += 4) {
        if (data[i] !== 255 || data[i + 1] !== 0 || data[i + 2] !== 0) continue;
        const x = (i / 4) % canvas.width,
          y = Math.floor(i / 4 / canvas.width);
        left = Math.min(left, x);
        top = Math.min(top, y);
        right = Math.max(right, x);
        bottom = Math.max(bottom, y);
        if (points.length === 0) {
          points.push({
            x,
            y,
            elements: document
              .elementsFromPoint(x, y)
              .slice(0, 3)
              .map((el) => `${el.localName}.${[...el.classList].join(".")}`),
          });
        }
      }
      return { left, top, right, bottom, points };
    },
    [...readFileSync(diff)],
  );
}

// compare shoots both sides with the scene's ruled regions blanked, writes board.png and page.png to the scene's
// directory (the board shot is the snapshot Playwright compares with), and compares them with Playwright's own image
// comparison. On a difference it copies Playwright's diff image beside them. It never throws for a difference: the
// caller decides after recording it.
export async function compare(board: Page, page: Page, scene: Ruling[], key: string, info: TestInfo) {
  const blanks: Rect[] = [];
  const misplaced: string[] = [];
  for (const ruling of scene) {
    const sides = [
      await rects(board, ruling.board, ruling.basis, "board"),
      await rects(page, ruling.page, ruling.basis, "page"),
    ] as const;
    blanks.push(...sides[0], ...sides[1]);
    const away = ruling.glyphs && moved(ruling, ...sides);
    if (away) misplaced.push(away);
  }
  const canvases = (side: "board" | "page") =>
    scene.flatMap((ruling) => {
      const region = ruling[side];
      return region && "canvas" in region ? [region.canvas] : [];
    });
  const reference = await shot(board, blanks, canvases("board"));
  const actual = await shot(page, blanks, canvases("page"));
  const result = await compareShots(reference, actual, key, info, scene, page);
  if (misplaced.length) result.error = [result.error, ...misplaced].filter(Boolean).join("\n");
  return result;
}

export async function comparePart(board: Locator, page: Locator, key: string, info: TestInfo) {
  await Promise.all([freeze(board.page()), freeze(page.page())]);
  const options = { animations: "disabled", caret: "hide", scale: "css" } as const;
  const [reference, actual] = await Promise.all([board.screenshot(options), page.screenshot(options)]);
  return compareShots(reference, actual, key, info, []);
}

async function compareShots(
  expected: Buffer,
  actual: Buffer,
  key: string,
  info: TestInfo,
  scene: Ruling[],
  page?: Page,
) {
  const reference = info.snapshotPath(key, "board.png");
  const result: Comparison = {
    board: reference,
    page: join(dirname(reference), "page.png"),
    differentPixels: 0,
    differentRatio: 0,
    rulings: scene.map((ruling) => ruling.basis),
  };
  mkdirSync(dirname(reference), { recursive: true });
  writeFileSync(reference, expected);
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
    const diff = info.attachments.findLast((attachment) => attachment.name.endsWith("diff.png"))?.path;
    if (diff) {
      result.diff = join(dirname(reference), "diff.png");
      writeFileSync(result.diff, readFileSync(diff));
      if (page) console.info(`${key}: diff locations ${JSON.stringify(await diffLocations(page, diff))}`);
    }
  }
  return result;
}
