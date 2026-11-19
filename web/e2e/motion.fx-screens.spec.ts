import { expect, type Page, test } from "@playwright/test";
import { open, running, scrollTo, shown } from "./fx-helpers.ts";

// The motion setting read as it changes, not kept from a script's start: the sea bed's dune, and the whale's call. The
// rest of the screens' cases are fx-screens.spec.ts.

const dune = (page: Page) =>
  page.locator(".hfm6 canvas.art").evaluate((canvas: HTMLCanvasElement) => {
    const { data } = (canvas.getContext("2d") as CanvasRenderingContext2D).getImageData(
      0,
      0,
      canvas.width,
      canvas.height,
    );
    let sum = 0;
    for (let i = 3; i < data.length; i += 4) sum = (sum * 31 + (data[i] ?? 0)) % 1_000_000_007;
    return sum;
  });

test("the dune opened under reduced motion moves once motion is allowed, and stands again when it is not", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 6);
  await shown(page, 6);
  const still = await dune(page);
  await page.waitForTimeout(500);
  expect(await dune(page)).toBe(still);
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await expect.poll(() => dune(page)).not.toBe(still);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.waitForTimeout(300);
  const stood = await dune(page);
  await page.waitForTimeout(500);
  expect(await dune(page)).toBe(stood);
});

test("a whale call whose code is still on its way is dropped when the motion setting changes", async ({ page }) => {
  let release = () => {};
  const held = new Promise<void>((done) => {
    release = done;
  });
  await page.route(/\/assets\/whale-[\w-]+\.js$/, async (route) => {
    await held;
    await route.continue();
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await running(page);
  await page.keyboard.type("deepseek");
  // asked for and allowed again while the code is on its way
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.emulateMedia({ reducedMotion: "no-preference" });
  release();
  await page.waitForTimeout(3000);
  const painted = await page
    .locator(".tr > canvas.wh")
    .evaluate((canvas: HTMLCanvasElement) =>
      (canvas.getContext("2d") as CanvasRenderingContext2D)
        .getImageData(0, 0, canvas.width, canvas.height)
        .data.some((value, i) => i % 4 === 3 && value > 0),
    );
  expect(painted).toBe(false);
});
