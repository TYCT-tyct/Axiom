import { expect, type Page, test } from "@playwright/test";
import { open } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { blockStorage, collectErrors } from "./collect-errors.ts";
import { words } from "./words.ts";

// The admin tells an open page that the program was replaced, and recovers a page whose code left with the old build
// (docs/frontend.md「工程要求」); the console's twin tests are in console.spec.ts.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

// The version `just web` stamped the build with; a test that compares against it fails when it is missing.
const built = process.env.AXIOM_VERSION ?? "";
// A program this build does not ship in.
const deployed = "axiom v0.0.0-20991231000000-000000000000";

// The notice holds its message and the reload button, so the message alone is not the whole text.
const newVersionNotice = (page: Page) =>
  page.getByRole("status").filter({ hasText: /Axiom (已经更新|has been updated)/ });
const h1 = (page: Page, name: RegExp) => page.getByRole("heading", { level: 1, name });

for (const language of ["zh", "en"] as const) {
  test(`the admin offers a reload after a deployment (${language})`, async ({ page }) => {
    const errors = collectErrors(page);
    await page.addInitScript((value) => localStorage.setItem("axiom.language", value), language);
    await open(page, "/", "usual", { version: deployed });
    await expect(h1(page, words("home.title"))).toBeVisible();
    const notice = newVersionNotice(page);
    await expect(notice).toBeVisible();
    await expect(notice).toHaveText(language === "zh" ? /Axiom 已经更新/ : /Axiom has been updated/);
    expect(errors.filter((line) => !line.startsWith("Failed to load resource"))).toEqual([]);
  });
}

test("the admin offers the reload when its site data is blocked", async ({ page }) => {
  await blockStorage(page, "localStorage");
  await open(page, "/", "usual", { version: deployed });
  await expect(newVersionNotice(page)).toBeVisible();
});

test("the admin offers no reload while the program answering is the one it was built for", async ({ page }) => {
  expect(built, "run the end-to-end tests with `just web`, which stamps the build and passes its version").not.toBe("");
  const asked = page.waitForResponse("**/api/admin/v1/version");
  await open(page, "/", "usual", { version: built });
  await expect(h1(page, words("home.title"))).toBeVisible();
  await asked;
  // The answer is compared on the next frames; a notice would be on screen by the second.
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await expect(newVersionNotice(page)).toHaveCount(0);
});

for (const storage of ["available", "blocked"] as const) {
  test(`a page whose chunk is gone reloads at most once, and only when opened (session storage ${storage})`, async ({
    page,
  }) => {
    if (storage === "blocked") {
      await blockStorage(page, "sessionStorage");
    }
    let loads = 0;
    page.on("load", () => {
      loads += 1;
    });
    await open(page, "/", "usual", { version: built });
    await expect(h1(page, words("home.title"))).toBeVisible();

    // A deployment removed the chunk of a page this build knows.
    await page.route(/\/assets\/calls-[\w-]+\.js$/, (route) => route.fulfill({ status: 404 }));
    const calls = page.getByRole("navigation").getByRole("link", { name: words("nav.calls") });
    const preloaded = page.waitForResponse(/\/assets\/calls-/);
    await calls.hover();
    await preloaded;
    await calls.focus();
    // Pointing at a link only preloads its page; the current page stays.
    await expect(h1(page, words("home.title"))).toBeVisible();
    await expect.poll(() => loads).toBe(1);

    await calls.click();
    await expect(h1(page, words("state.pageFailed"))).toBeVisible();
    // One reload fetches the new build; a chunk still missing after it shows the error instead of reloading again.
    // Without session storage the reload cannot be remembered, so there is none.
    await expect.poll(() => loads).toBe(storage === "available" ? 2 : 1);
  });
}
