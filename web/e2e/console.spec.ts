import { expect, type Locator, type Page, test } from "@playwright/test";

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

// The radios are visually hidden inside their labels, so a person clicks the label.
function chooseTheme(within: Locator, name: string) {
  return within.getByRole("radio", { name }).check({ force: true });
}

test("the console switches language and loads its pages", async ({ page }) => {
  const errors = collectErrors(page);

  await page.goto("/console/");
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  await expect(page).toHaveTitle("概览 · Axiom");

  await page.getByRole("group", { name: "语言" }).getByText("English").click();
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");

  // Another page loads as its own chunk, and the language choice outlives a reload.
  await page.getByRole("link", { name: "Logs" }).click();
  await expect(page.getByRole("heading", { name: "Logs" })).toBeVisible();
  await expect(page).toHaveTitle("Logs · Axiom");
  await page.reload();
  await expect(page.getByRole("heading", { name: "Logs" })).toBeVisible();

  expect(errors).toEqual([]);
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

test("an address no page answers says so in its title and heading", async ({ page }) => {
  await page.goto("/console/no-such-page");
  await expect(page.getByRole("heading", { level: 1, name: "找不到这个页面" })).toBeVisible();
  await expect(page).toHaveTitle("找不到这个页面 · Axiom");
});
