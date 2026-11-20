import { expect, type Page, test } from "@playwright/test";
import { open, pauseAhead, running, scrollTo, shown } from "./fx-helpers.ts";

// Clawd (docs/frontend.md「Clawd（彩蛋）」): its code comes after the first content, as the sea's does; "claude" typed or
// pressed in the first screen's strip brings the jellyfish and its team down the right edge, and at the sea floor they
// jump into the sand and the letters of the wordmark turn into Clawds one after another.
const clawd = /\/assets\/clawd-[\w-]+\.js$/;
const watchClawd = (page: Page) => {
  const asked: string[] = [];
  page.on("request", (request) => {
    if (clawd.test(request.url())) asked.push(request.url());
  });
  return asked;
};

// the dive's layer has something on it along the right edge, where the depth line runs
const onTheEdge = (page: Page) =>
  page.locator(".tr > canvas.cw").evaluate((canvas: HTMLCanvasElement) => {
    const edge = Math.round(canvas.width * 0.06);
    const { data } = (canvas.getContext("2d") as CanvasRenderingContext2D).getImageData(
      canvas.width - edge,
      0,
      edge,
      canvas.height,
    );
    return data.some((value, i) => i % 4 === 3 && value > 0);
  });

// the letters the dune leaves out, because they are being drawn as Clawds (the hooks clawd-cast.js puts on the dune)
const turned = (page: Page) =>
  page
    .locator(".hfm6 canvas.art")
    .evaluate((canvas) => (canvas as HTMLCanvasElement & { __fx?: { skip: Set<number> } }).__fx?.skip.size ?? -1);

test("typing claude brings the jellyfish to the right edge", async ({ page }) => {
  const asked = watchClawd(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await running(page);
  await expect.poll(() => asked).toHaveLength(1);
  expect(await onTheEdge(page)).toBe(false);
  await page.keyboard.type("claude");
  await expect.poll(() => onTheEdge(page)).toBe(true);
});

for (const { name, width, height } of [
  { name: "wide", width: 1440, height: 900 },
  { name: "narrow", width: 390, height: 844 },
]) {
  test(`pressing claude sends the team down; at the sea floor the letters turn (${name})`, async ({ page }) => {
    const asked = watchClawd(page);
    await page.clock.install();
    await page.setViewportSize({ width, height });
    await open(page);
    await running(page);
    await expect.poll(() => asked).toHaveLength(1);
    await page.locator(".hs > .main .fam [data-egg=claude]").click();
    await expect.poll(() => onTheEdge(page)).toBe(true);
    await scrollTo(page, 6);
    await shown(page, 6);
    // the floor itself in view (on a phone the closing words and the footer are above and below it)
    await page.locator(".hfm6 canvas.art").evaluate((art) => art.scrollIntoView({ block: "center" }));
    // from here the page's clock is stepped: the jellyfish catches up with the floor, the team jumps into the sand,
    // waves, goes home, and the letters answer one after another
    await pauseAhead(page, 300);
    let seen = 0;
    for (let step = 0; step < 40 && seen === 0; step++) {
      await page.clock.runFor(250);
      seen = await turned(page);
    }
    expect(seen).toBeGreaterThan(0);
    // the jellyfish has let go and drifted away: the dive's layer is clear again
    await page.clock.runFor(3000);
    expect(await onTheEdge(page)).toBe(false);
  });
}
