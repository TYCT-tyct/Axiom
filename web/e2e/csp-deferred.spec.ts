import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { collectErrors } from "./collect-errors.ts";
import { mockConsole } from "./console-api.ts";
import { pageHeading } from "./page-heading.ts";

// The content security policy after everything a page loads late (port design §8.4): apps.spec.ts stops at the first
// heading, before the screens below, the effects and the overlays have come. Each case drives a page through what it
// loads on the way and collects every policy violation and console error, which must stay empty. A card that adds
// something loaded late adds its steps here.

async function watch(page: Page) {
  await page.addInitScript(() => {
    const seen: string[] = [];
    (window as unknown as { policy: string[] }).policy = seen;
    document.addEventListener("securitypolicyviolation", (e) => seen.push(`${e.violatedDirective} ${e.blockedURI}`));
  });
  const errors = collectErrors(page);
  return async () => ({
    violations: await page.evaluate(() => (window as unknown as { policy: string[] }).policy),
    errors,
  });
}

// Scrolls the home page's scroller to each screen in turn, letting each one's code arrive and its script draw.
async function throughScreens(page: Page) {
  for (const sec of [2, 3, 4]) {
    const place = page.locator(`.under [data-sec="${sec}"]`);
    await place.evaluate((el) => el.scrollIntoView({ block: "start" }));
    // seen, and faded in (the board's .hfm.in)
    await expect.poll(() => place.locator(".hfm").evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
  }
}

async function openHome(page: Page, width: number) {
  await page.setViewportSize({ width, height: 900 });
  await mockConsole(page);
  await page.goto(apps[0].url);
  await pageHeading(page);
}

test("the website, wide: every screen, then the language and the theme, with no violation or error", async ({
  page,
}) => {
  const report = await watch(page);
  await openHome(page, 1440);
  await throughScreens(page);

  await page.locator(".hs").evaluate((el) => el.scrollTo({ top: 0 }));
  await page.getByRole("banner").getByRole("button", { name: "简体中文" }).click();
  await page.getByRole("option", { name: /English/ }).click();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
  await page.getByRole("button", { name: /Switch to (dark|light)/ }).click();
  await throughScreens(page);

  expect(await report()).toEqual({ violations: [], errors: [] });
});

test("the website, narrow: every screen, with no violation or error", async ({ page }) => {
  const report = await watch(page);
  await openHome(page, 390);
  await throughScreens(page);
  expect(await report()).toEqual({ violations: [], errors: [] });
});
