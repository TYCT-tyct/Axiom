import { expect, type Page, test } from "@playwright/test";
import { delivered, open, place, setHidden } from "./fx-helpers.ts";

// The tour (the connect screen's card, the last of the screens' cards) and the menu's list of sections: the page moving
// the reader, and what ends it. The rest of the screens' cases are fx-screens.spec.ts.

// The tour and the menu's list of sections (the last screen's card): the tour glides through the screens, the list goes
// to one, and under reduced motion there is no tour to offer.
const scrolled = (page: Page) => page.locator(".hs").evaluate((el) => el.scrollTop);
// where a glide to a screen's place ends: its top, or as far as the scroller goes
const placeTop = (page: Page, sec: number) =>
  place(page, sec).evaluate((el) => {
    const scroller = el.closest(".hs") as HTMLElement;
    return Math.min((el as HTMLElement).offsetTop, scroller.scrollHeight - scroller.clientHeight);
  });

test("the tour visits every screen in order, a few seconds apart, and comes back to the top", async ({ page }) => {
  test.setTimeout(90_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await page.clock.install();
  await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 2000);
  await page.locator(".tour").click();
  for (const sec of delivered) {
    await expect
      .poll(async () => Math.abs((await scrolled(page)) - (await placeTop(page, sec))), { timeout: 10_000 })
      .toBeLessThan(4);
    await page.clock.runFor(3200);
  }
  await page.clock.runFor(3000);
  await expect.poll(() => scrolled(page), { timeout: 10_000 }).toBeLessThan(4);
});

test("the menu's list of sections goes to one", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator(".hs").evaluate((el) => el.scrollTo({ top: 0 }));
  await page.locator(".bar .menu").click();
  await page.locator(".sml[data-sec='5']").click();
  await expect
    .poll(() => place(page, 5).evaluate((el) => Math.abs(el.getBoundingClientRect().top)), { timeout: 15_000 })
    .toBeLessThan(40);
});

// The tour is the page moving the reader: the reader taking over, the page hidden, or reduced motion asked for ends it
// where it is. The tour's own steps are on the page's clock, which stands still until the case runs it on; the glides
// themselves are the browser's.
const tourEnds: [string, (page: Page) => Promise<unknown>][] = [
  ["the island's mark takes the reader back to the top", (page) => page.locator(".isl .im").click()],
  [
    "the reader scrolls",
    async (page) => {
      await page.mouse.move(720, 450);
      await page.mouse.wheel(0, -40);
    },
  ],
  ["the tab is hidden", (page) => setHidden(page, true)],
  ["reduced motion is asked for", (page) => page.emulateMedia({ reducedMotion: "reduce" })],
];
for (const [how, end] of tourEnds) {
  test(`the tour stops when ${how}`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await open(page);
    await page.clock.install();
    await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 2000);
    await page.locator(".tour").click();
    // early in the first glide, while it is still under way
    await expect.poll(() => scrolled(page), { intervals: [20] }).toBeGreaterThan(20);
    await end(page);
    // the glide under way stops where it is: at once, apart from the reader's own move (a wheel's few pixels), or, for
    // the island's mark, at the top it glides to
    const ended = await scrolled(page);
    if (how.startsWith("the island")) await expect.poll(() => scrolled(page)).toBeLessThan(2);
    else {
      await page.waitForTimeout(700);
      expect(Math.abs((await scrolled(page)) - ended)).toBeLessThan(60);
    }
    const rested = await scrolled(page);
    // two of the tour's steps, were it still going
    await page.clock.runFor(7000);
    await page.waitForTimeout(1500);
    expect(Math.abs((await scrolled(page)) - rested)).toBeLessThan(2);
  });
}

test("under reduced motion the tour is not offered", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await open(page);
  await expect(page.locator(".tour")).toHaveClass(/\bgone\b/);
  // not reachable by the keyboard either: pressing Enter on it would start the tour
  await page.locator(".tour").focus();
  await expect(page.locator(".tour")).not.toBeFocused();
  await expect(page.locator(".tour")).toHaveAttribute("inert", "");
});
