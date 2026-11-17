import { expect, type Page, type Route, test } from "@playwright/test";
import { collectErrors } from "./collect-errors.ts";
import { mockConsole, signedInAs } from "./console-api.ts";
import { advanceUntil } from "./page-clock.ts";

// The version `just web` stamped the build with; a test that compares against it fails when it is missing.
const built = process.env.AXIOM_VERSION ?? "";
// A program this build does not ship in.
const deployed = "axiom v0.0.0-20991231000000-000000000000";

// A signed-in account, on a program running the given version.
async function openConsole(page: Page, version: string) {
  const state = await mockConsole(page, "usual", { version });
  signedInAs(state);
  return state;
}

const balancePath = "**/api/console/v1/balance";

// Some private modes, and blocked site data, make even reading the storage throw.
function blockStorage(page: Page, storage: "localStorage" | "sessionStorage") {
  return page.addInitScript((name) => {
    Object.defineProperty(window, name, {
      get: () => {
        throw new DOMException("The site's data is blocked", "SecurityError");
      },
    });
  }, storage);
}

function newVersionNotice(page: Page) {
  return page.getByRole("status").filter({ hasText: "Axiom 已经更新" });
}

// The theme and the language are chosen in the account menu, which the avatar opens.
async function choose(page: Page, name: string) {
  await page.getByRole("button", { name: "账号设置" }).click();
  await page.getByRole("button", { name }).click();
}

test("the console offers a reload after a deployment, in either language", async ({ page }) => {
  const errors = collectErrors(page);
  // The program answering is not the one this build ships in.
  await openConsole(page, deployed);
  await page.setViewportSize({ width: 1440, height: 900 });

  await page.goto("/console/");
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  await expect(page).toHaveTitle("概览 · Axiom");
  await expect(newVersionNotice(page)).toBeVisible();
  // The notice floats over the page and takes no room from the frame: the bar and the sidebar stay where they are.
  await expect(page.getByRole("banner")).toHaveJSProperty("offsetTop", 0);
  await expect(page.getByRole("button", { name: "账号设置" })).toBeInViewport({ ratio: 1 });

  // The language is switched in the account menu.
  await choose(page, "English");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Axiom has been updated" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");

  // Another page loads as its own chunk, and the language choice outlives a reload.
  await page.getByRole("link", { name: "Logs" }).click();
  await expect(page.getByRole("heading", { name: "Calls" })).toBeVisible();
  await expect(page).toHaveTitle("Calls · Axiom");
  await page.reload();
  await expect(page.getByRole("heading", { name: "Calls" })).toBeVisible();

  expect(errors).toEqual([]);
});

test("the console offers no reload while the program answering is the one it was built for", async ({ page }) => {
  expect(built, "run the end-to-end tests with `just web`, which stamps the build and passes its version").not.toBe("");
  await openConsole(page, built);
  const asked = page.waitForResponse("**/api/console/v1/version");
  await page.goto("/console/");
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  await asked;
  // The answer is compared on the next frames; a notice would be on screen by the second.
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await expect(newVersionNotice(page)).toHaveCount(0);
});

test("the console offers the reload and takes a language for the visit when its site data is blocked", async ({
  page,
}) => {
  const errors = collectErrors(page);
  await blockStorage(page, "localStorage");
  await openConsole(page, deployed);
  await page.goto("/console/");
  await expect(newVersionNotice(page)).toBeVisible();
  await choose(page, "English");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  expect(errors).toEqual([]);
});

// The overview's balance shows each way a call can fail.

test("a refused call says the account may not see it, without asking again", async ({ page }) => {
  await openConsole(page, built);
  let calls = 0;
  await page.route(balancePath, (route) => {
    calls += 1;
    return route.fulfill({ status: 403 });
  });
  await page.goto("/console/");
  await expect(page.getByRole("heading", { name: "没有权限" })).toBeVisible();
  expect(calls).toBe(1);
});

test("a server failure is retried, then shows its request ID, and retrying asks again", async ({ page }) => {
  // The query waits between retries on the page's clock, which advanceUntil moves: the three attempts and the moment
  // the error shows then cost no wall-clock time, so a loaded runner cannot make them arrive after the assertion gave
  // up.
  await page.clock.install();
  await openConsole(page, built);
  let calls = 0;
  await page.route(balancePath, (route) => {
    calls += 1;
    return calls <= 3 ? route.fulfill({ status: 500, headers: { "X-Request-ID": "req-500" } }) : route.fallback();
  });
  await page.goto("/console/");
  const failed = page.getByRole("heading", { name: "没能加载" });
  await advanceUntil(page, () => expect(failed).toBeVisible({ timeout: 100 }));
  await expect(page.getByText("请求 ID req-500")).toBeVisible();
  expect(calls).toBe(3);

  await page.getByRole("button", { name: "重试" }).click();
  await advanceUntil(page, () => expect(page.locator("#main").getByText("US$11.75")).toBeVisible({ timeout: 100 }));
  expect(calls).toBe(4);
});

for (const { cause, answer } of [
  { cause: "the connection fails", answer: (route: Route) => route.abort() },
  // A half-open connection: the request is sent and nothing ever comes back.
  { cause: "nothing comes back", answer: () => {} },
]) {
  test(`a call that gets no response because ${cause} ends in the network error`, async ({ page }) => {
    // The page's clock runs the timeouts and the pauses between retries in moments.
    await page.clock.install();
    await openConsole(page, built);
    await page.route(balancePath, answer);
    await page.goto("/console/");
    const failed = page.getByText("连不上 Axiom，请检查网络后重试。");
    await advanceUntil(page, () => expect(failed).toBeVisible({ timeout: 100 }));
  });
}

for (const storage of ["available", "blocked"] as const) {
  test(`a page whose chunk is gone reloads at most once, and only when opened (session storage ${storage})`, async ({
    page,
  }) => {
    if (storage === "blocked") {
      await blockStorage(page, "sessionStorage");
    }
    await openConsole(page, built);
    let loads = 0;
    page.on("load", () => {
      loads += 1;
    });
    await page.goto("/console/");
    await expect(page.getByRole("heading", { name: "概览" })).toBeVisible();

    // A deployment removed every page chunk this build knows.
    await page.route(/\/assets\/(keys|calls|call|balance|security)-[\w-]+\.js$/, (route) =>
      route.fulfill({ status: 404 }),
    );
    const logs = page.getByRole("link", { name: "日志" });
    const preloaded = page.waitForResponse(/\/assets\/calls-/);
    await logs.hover();
    await preloaded;
    await logs.focus();
    // Pointing at a link only preloads its page; the current page stays, with whatever was typed into it.
    await expect(page.getByRole("heading", { name: "概览" })).toBeVisible();
    await expect.poll(() => loads).toBe(1);

    await logs.click();
    await expect(page.getByRole("heading", { level: 1, name: "页面没能加载" })).toBeVisible();
    // One reload fetches the new build; a chunk still missing after it shows the error instead of reloading again.
    // Without session storage the reload cannot be remembered, so there is none.
    await expect.poll(() => loads).toBe(storage === "available" ? 2 : 1);
  });
}

test("the theme chosen on a phone is the one the wide shell shows once the window widens", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await openConsole(page, built);
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto("/console/");

  await choose(page, "浅色");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

  // Following a link in the tabs' "more" sheet closes it, names the new page and moves focus to its heading.
  const more = page.getByRole("button", { name: "更多" });
  await more.click();
  await expect(more).toHaveAttribute("aria-expanded", "true");
  await page.getByRole("link", { name: "自带 Key" }).click();
  await expect(page).toHaveTitle("供应商 key · Axiom");
  await expect(page.getByRole("heading", { level: 1, name: "供应商 key" })).toBeFocused();
  await expect(more).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("link", { name: "自带 Key" })).toBeHidden();

  await page.setViewportSize({ width: 1300, height: 800 });
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible();
  await page.getByRole("button", { name: "账号设置" }).click();
  await expect(page.getByRole("button", { name: "浅色" })).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "跟随系统" }).click();
  await expect(page.locator("html")).not.toHaveAttribute("data-theme");
  // Escape closes the menu and hands focus back to its button.
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "账号设置" })).toBeFocused();
  await expect(page.getByRole("button", { name: "跟随系统" })).toBeHidden();
});

test("a key's secret, shown once, stays while the window crosses from the phone board to the wide one", async ({
  page,
}) => {
  await openConsole(page, built);
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto("/console/keys");
  await page.getByRole("button", { name: "创建 key" }).click();
  await page.getByRole("dialog", { name: "创建 key" }).getByLabel("名称").fill("Deploy bot");
  await page.getByRole("button", { name: "创建", exact: true }).click();
  const secret = page.getByRole("dialog", { name: "复制你的 key" });
  await expect(secret).toBeVisible();
  const text = await secret.textContent();
  for (const width of [820, 390]) {
    await page.setViewportSize({ width, height: 800 });
    await expect(secret).toBeVisible();
    expect(await secret.textContent()).toBe(text);
  }
});

test("a followed link opens the new page at its top with focus on its heading", async ({ page }) => {
  await openConsole(page, built);
  await page.setViewportSize({ width: 1280, height: 400 });
  await page.goto("/console/");
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  // The document scrolls, not a box inside the frame, and the sidebar stays in view.
  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
  await page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "日志" }).click();
  await expect(page).toHaveTitle("调用记录 · Axiom");
  await expect(page.getByRole("heading", { level: 1, name: "调用记录" })).toBeFocused();
  expect(await page.evaluate(() => window.scrollY)).toBe(0);
});

test("on paper the page stands alone, without the bar, sidebar or notices", async ({ page }) => {
  await openConsole(page, deployed);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/console/");
  await expect(newVersionNotice(page)).toBeVisible();
  await page.emulateMedia({ media: "print" });
  for (const hidden of [page.getByRole("banner"), page.getByRole("complementary"), newVersionNotice(page)]) {
    await expect(hidden).toBeHidden();
  }
  // One column: the page starts at the paper's left edge.
  const main = await page.locator("#main").boundingBox();
  expect(main?.x).toBeLessThan(16);
});

test("an address no page answers says so and leads to the console's home", async ({ page }) => {
  await openConsole(page, built);
  await page.goto("/console/no-such-page");
  await expect(page.getByRole("heading", { level: 1, name: "找不到这个页面" })).toBeVisible();
  await expect(page).toHaveTitle("找不到这个页面 · Axiom");
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible();

  // The console's own home, not the website's.
  const home = page.getByRole("link", { name: "回到首页" });
  await expect(home).toHaveAttribute("href", "/console/");
  await home.click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
});
