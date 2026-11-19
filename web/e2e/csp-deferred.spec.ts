import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { collectErrors } from "./collect-errors.ts";
import { mockConsole } from "./console-api.ts";
import { knownEmail, knownPassword } from "./mock-auth.ts";
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
    const place = page.locator(`.under [data-place="${sec}"]`);
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

test("the departure to the sign-in page, its arrival and both its steps, with no violation or error", async ({
  page,
}) => {
  // the policy of each document the tab shows, the website's and the console's, kept in the test
  const violations: string[] = [];
  await page.exposeFunction("reportPolicy", (line: string) => violations.push(line));
  await page.addInitScript(() =>
    document.addEventListener("securitypolicyviolation", (e) =>
      (window as unknown as { reportPolicy: (line: string) => void }).reportPolicy(
        `${location.pathname} ${e.violatedDirective} ${e.blockedURI}`,
      ),
    ),
  );
  const errors = collectErrors(page);
  const failed: string[] = [];
  page.on("response", (response) => {
    if (response.status() >= 400) failed.push(`${response.status()} ${new URL(response.url()).pathname}`);
  });
  await openHome(page, 1440);
  await page.getByRole("banner").getByRole("link", { name: "登录", exact: true }).click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await pageHeading(page);
  // the sea and the stage run, and the arrival's decode has resolved the title
  await expect
    .poll(() =>
      page.evaluate(
        () => (window as unknown as { axiomFx?: { state: { frames: number } } }).axiomFx?.state.frames ?? 0,
      ),
    )
    .toBeGreaterThan(0);
  await expect(page.locator(".lg-title .ch[style*=transparent]")).toHaveCount(0);
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill(knownPassword);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  // the browser logs the 401 of the session asked for before signing in itself, and nothing else may fail to load
  expect({ violations, errors: errors.filter((line) => !line.startsWith("Failed to load resource")) }).toEqual({
    violations: [],
    errors: [],
  });
  expect(failed.filter((line) => line !== "401 /api/console/v1/session")).toEqual([]);
});
