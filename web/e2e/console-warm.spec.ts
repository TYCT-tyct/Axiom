import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";

// A console page comes with its data (console/src/warm.ts): the link's intent asks for the page's code and its first
// screen's answers together, so the page a press opens is drawn whole, never its loading state first.
const loadingSeen = (page: Page) =>
  page.evaluate(() => {
    const seen = { loading: false };
    Object.assign(window, { loadingSeen: seen });
    const main = document.querySelector("main") ?? document.body;
    new MutationObserver(() => {
      if (main.querySelector('[role="status"]')?.textContent?.includes("正在加载")) seen.loading = true;
    }).observe(document.body, { subtree: true, childList: true });
  });

test("a page pressed after its link's intent is drawn whole, not loading first", async ({ page }) => {
  const state = await mockConsole(page);
  signedInAs(state);
  await page.goto("/console/");
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  // the usage answers the way a real node does, after a moment, so a page that asks only once it is drawn shows loading
  const usage = (url: URL) => url.pathname === "/api/console/v1/usage/statistics" && url.searchParams.has("compare");
  await page.route(
    (url) => usage(url),
    async (route) => {
      await new Promise((done) => setTimeout(done, 300));
      await route.fallback();
    },
  );
  await loadingSeen(page);
  const link = page.locator('a[href="/console/usage"]:visible').first();
  const answered = page.waitForResponse((r) => usage(new URL(r.url())));
  await link.hover();
  await answered;
  await link.click();
  await expect(page).toHaveURL(/\/console\/usage$/);
  await expect(page.getByRole("main").getByRole("heading", { level: 1 })).toBeVisible();
  expect(
    await page.evaluate(() => (window as unknown as { loadingSeen: { loading: boolean } }).loadingSeen.loading),
  ).toBe(false);
});

// What a page asks for once it is pressed, after its link's intent warmed it: nothing of its first screen. A page whose
// loader asked for other options than the page reads would ask again here. The usage page's comparison by group waits
// on its main answer, so it is the one request a warmed page may still make.
// Each starts from a page that has not read what the next one reads (every page reads the keys).
const warmed = [
  { from: "/console/keys", path: "/console/" },
  { from: "/console/", path: "/console/calls" },
  { from: "/console/", path: "/console/usage" },
  { from: "/console/calls", path: "/console/balance" },
];
for (const { from, path } of warmed)
  test(`${path} asks nothing of its first screen once its link's intent warmed it`, async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto(from);
    await expect(page.getByRole("main")).toBeVisible();
    await page.waitForLoadState("networkidle");
    const nav = page.locator(`a[href="${path}"]:visible`).first();
    await nav.hover();
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(300);
    const asked: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (!url.pathname.startsWith("/api/console/v1/") || request.method() !== "GET") return;
      if (url.pathname.endsWith("/usage/statistics") && !url.searchParams.has("compare")) return;
      asked.push(`${url.pathname}${url.search}`);
    });
    await nav.click();
    await expect(page).toHaveURL(new RegExp(`${path}$`));
    await page.waitForTimeout(600);
    expect(asked).toEqual([]);
  });
