import { expect, type Locator, type Page, test } from "@playwright/test";

// The version `just web` stamped the build with; a test that compares against it fails when it is missing.
const built = process.env.AXIOM_VERSION ?? "";

function collectErrors(page: Page) {
  const errors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") {
      errors.push(message.text());
    }
  });
  page.on("pageerror", (error) => errors.push(error.message));
  return errors;
}

function answerVersion(page: Page, version: string) {
  return page.route("**/api/console/v1/version", (route) => route.fulfill({ json: { version } }));
}

// The radios are visually hidden inside their labels, so a person clicks the label.
function chooseTheme(within: Locator, name: string) {
  return within.getByRole("radio", { name }).check({ force: true });
}

test("the console offers a reload after a deployment, in either language", async ({ page }) => {
  const errors = collectErrors(page);
  // The program answering is not the one this build ships in.
  await answerVersion(page, "axiom v0.0.0-20991231000000-000000000000");
  await page.setViewportSize({ width: 1440, height: 900 });

  await page.goto("/console/");
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  await expect(page).toHaveTitle("概览 · Axiom");
  await expect(page.getByRole("status").filter({ hasText: "Axiom 已经更新" })).toBeVisible();
  // The notice takes its room inside the frame: the sidebar's controls stay on screen and the page does not scroll.
  expect(await page.evaluate(() => document.documentElement.scrollHeight)).toBeLessThanOrEqual(900);
  await expect(page.getByRole("navigation").getByRole("group", { name: "语言" })).toBeInViewport({ ratio: 1 });

  await page.getByRole("group", { name: "语言" }).getByText("English").click();
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Axiom has been updated" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");

  // Another page loads as its own chunk, and the language choice outlives a reload.
  await page.getByRole("link", { name: "Logs" }).click();
  await expect(page.getByRole("heading", { name: "Logs" })).toBeVisible();
  await expect(page).toHaveTitle("Logs · Axiom");
  await page.reload();
  await expect(page.getByRole("heading", { name: "Logs" })).toBeVisible();

  expect(errors).toEqual([]);
});

test("the console offers no reload while the program answering is the one it was built for", async ({ page }) => {
  expect(built, "run the end-to-end tests with `just web`, which stamps the build and passes its version").not.toBe("");
  await answerVersion(page, built);
  await page.goto("/console/");
  // The overview shows the version the program answered, so the answer has been compared once it appears.
  await expect(page.getByText(built, { exact: true })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Axiom 已经更新" })).toHaveCount(0);
});

for (const storage of ["available", "blocked"] as const) {
  test(`a page whose chunk is gone reloads at most once, and only when opened (session storage ${storage})`, async ({
    page,
  }) => {
    if (storage === "blocked") {
      await page.addInitScript(() => {
        Object.defineProperty(window, "sessionStorage", {
          get: () => {
            throw new DOMException("The site's data is blocked", "SecurityError");
          },
        });
      });
    }
    await answerVersion(page, built);
    let loads = 0;
    page.on("load", () => {
      loads += 1;
    });
    await page.goto("/console/");
    await expect(page.getByRole("heading", { name: "概览" })).toBeVisible();

    // A deployment removed every page chunk this build knows.
    await page.route(/\/assets\/(keys|logs|placeholder)-[\w-]+\.js$/, (route) => route.fulfill({ status: 404 }));
    const logs = page.getByRole("link", { name: "调用日志" });
    const preloaded = page.waitForResponse(/\/assets\/logs-/);
    await logs.hover();
    await preloaded;
    await logs.focus();
    // Pointing at a link only preloads its page; the current page stays, with whatever was typed into it.
    await expect(page.getByRole("heading", { name: "概览" })).toBeVisible();
    expect(loads).toBe(1);

    await logs.click();
    await expect(page.getByRole("heading", { level: 1, name: "页面没能加载" })).toBeVisible();
    // One reload fetches the new build; a chunk still missing after it shows the error instead of reloading again.
    // Without session storage the reload cannot be remembered, so there is none.
    expect(loads).toBe(storage === "available" ? 2 : 1);
  });
}

test("the theme chosen in the drawer is the one the sidebar shows once the window widens", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await answerVersion(page, built);
  await page.setViewportSize({ width: 800, height: 800 });
  await page.goto("/console/");

  await page.getByRole("button", { name: "打开菜单" }).click();
  const drawer = page.getByRole("dialog");
  await chooseTheme(drawer, "浅色");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

  // Following a link closes the drawer, names the new page and moves focus to it, so a screen reader announces it.
  await drawer.getByRole("link", { name: "调用日志" }).click();
  await expect(page).toHaveTitle("调用日志 · Axiom");
  await expect(page.locator("#main")).toBeFocused();

  await page.getByRole("button", { name: "打开菜单" }).click();
  await page.setViewportSize({ width: 1300, height: 800 });
  // Past the breakpoint the sidebar is back, so the drawer closes.
  await expect(drawer).toBeHidden();
  const sidebar = page.getByRole("navigation", { name: "主导航" });
  await expect(sidebar.getByRole("radio", { name: "浅色" })).toBeChecked();
  await chooseTheme(sidebar, "跟随系统");
  await expect(page.locator("html")).not.toHaveAttribute("data-theme");
});

test("an address no page answers says so and leads to the console's home", async ({ page }) => {
  await answerVersion(page, built);
  await page.goto("/console/no-such-page");
  await expect(page.getByRole("heading", { level: 1, name: "找不到这个页面" })).toBeVisible();
  await expect(page).toHaveTitle("找不到这个页面 · Axiom");
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible();

  // The console's own home, not the website's: the preview server would redirect "/" to the console and hide it.
  const home = page.getByRole("link", { name: "回到首页" });
  await expect(home).toHaveAttribute("href", "/console/");
  await home.click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
});
