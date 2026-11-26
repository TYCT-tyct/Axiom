import { expect, type Locator, type Page, test } from "@playwright/test";
import { uuid } from "./admin-data.ts";
import { open } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { words, zhText } from "./words.ts";

// The admin's own shell (AdminShell, nav.ts): the design's four groups with what waits on them, the bell, the settings'
// own navigation and the way back, the tablet's icon rail, and ⌘K. The mock has four approvals waiting and one alert
// not yet resolved.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const nav = (page: Page) => page.getByRole("navigation", { name: zhText("shell.navigation") });
const entry = (page: Page, key: string) => nav(page).getByRole("link", { name: zhText(`shell.nav.${key}`) });
const h1 = (page: Page, name: RegExp) => page.getByRole("heading", { level: 1, name });

test("the sidebar has the design's groups, and counts what waits", async ({ page }) => {
  await open(page, "/");
  for (const group of ["run", "people", "supply", "records"]) {
    await expect(nav(page).getByRole("group", { name: zhText(`shell.groups.${group}`) })).toBeVisible();
  }
  await expect(entry(page, "overview")).toHaveAttribute("aria-current", "page");
  await expect(entry(page, "approvals").getByRole("img", { name: "4 项待处理" })).toBeVisible();
  await expect(entry(page, "ops").getByRole("img", { name: "1 项待处理" })).toBeVisible();
  // No count where nothing waits.
  await expect(entry(page, "calls").getByRole("img")).toHaveCount(0);
});

test("the bell opens an unresolved alert, and the settings swap the sidebar until 返回", async ({ page }) => {
  await open(page, "/users");
  await page.getByRole("button", { name: "告警，1 条未解决" }).click();
  await page.getByRole("link", { name: /node_unhealthy/ }).click();
  await expect(page).toHaveURL(new RegExp(`/alert-events\\?event=${uuid(31, 1)}$`));
  await expect(page.getByRole("dialog", { name: words("alertEvents.details") })).toBeVisible();
  await page.keyboard.press("Escape");

  await page.getByRole("link", { name: zhText("shell.nav.settings") }).click();
  await expect(page).toHaveURL(/\/settings\/site$/);
  const settings = page.getByRole("navigation", { name: zhText("shell.settingsNavigation") });
  await expect(settings.getByRole("link", { name: zhText("shell.nav.site") })).toHaveAttribute("aria-current", "page");
  await settings.getByRole("link", { name: zhText("shell.nav.rateLimits") }).click();
  await expect(page).toHaveURL(/\/rate-limits$/);
  await settings.getByRole("link", { name: zhText("shell.back") }).click();
  await expect(page).toHaveURL(/\/alert-events$/);
  await expect(entry(page, "ops")).toHaveAttribute("aria-current", "page");
});

test("⌘K lists the recent pages first and finds a page's tab by name", async ({ page }) => {
  await open(page, "/models");
  await entry(page, "pricing").click();
  // Wait for the page itself: a navigation that lands closes the dialogs open on the way.
  await expect(h1(page, words("nav.pricing"))).toBeVisible();
  await page.keyboard.press("Control+k");
  const palette = page.getByRole("dialog", { name: zhText("shell.palette.label") });
  const query = palette.getByRole("combobox");
  await expect(query).toBeFocused();
  const options = palette.getByRole("option");
  await expect(options.first()).toHaveText(new RegExp(`^${zhText("shell.nav.models")}`));
  await expect(options.first()).toHaveAttribute("aria-selected", "true");

  await query.fill("汇率");
  await expect(options).toHaveCount(1);
  await page.keyboard.press("Enter");
  await expect(palette).toHaveCount(0);
  await expect(page).toHaveURL(/\/pricing\?tab=rates$/);
  await expect(h1(page, words("nav.pricing"))).toBeVisible();
  // The tab's route belongs to its page's entry.
  await expect(entry(page, "pricing")).toHaveAttribute("aria-current", "page");

  await page.keyboard.press("Control+k");
  await query.fill("no such page");
  await expect(palette.getByText(/没有找到「no such page」/)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(palette).toHaveCount(0);
});

test("a tablet folds the sidebar to an icon rail, with counts as dots, until it is unfolded", async ({ page }) => {
  await page.setViewportSize({ width: 820, height: 1180 });
  await open(page, "/");
  const fold = page.getByRole("button", { name: zhText("shell.expand") });
  await expect(fold).toHaveAttribute("aria-expanded", "false");
  const dot = entry(page, "approvals").getByRole("img", { name: "4 项待处理" });
  await expect.poll(async () => (await dot.boundingBox())?.width).toBe(6);
  await fold.click();
  await expect(page.getByRole("button", { name: zhText("shell.collapse") })).toHaveAttribute("aria-expanded", "true");
  await expect.poll(async () => (await dot.boundingBox())?.width).toBeGreaterThanOrEqual(20);
});

// Where a box's left and right edges sit, once it is on the page.
async function edges(box: Locator) {
  await expect(box).toBeVisible();
  const at = await box.boundingBox();
  return { left: at?.x, right: at && at.x + at.width };
}

// A page's head is the first thing in its body: its title starts at the content's left edge and its actions end at the
// content's right edge, at every width. A head drawn beside the body sits in the body's margins instead, 40px out on a
// desktop and against the screen's edge on a phone.
for (const width of [1440, 820, 390]) {
  test(`a page's head lines up with its content at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    for (const path of ["/", "/suppliers", "/users"]) {
      await open(page, path);
      const card = await edges(page.locator("main .qcard").first());
      const title = await edges(page.getByRole("heading", { level: 1 }));
      expect(title.left, path).toBe(card.left);
      if (path === "/suppliers" && width >= 768) {
        const add = await edges(page.getByRole("button", { name: zhText("upstream.add") }));
        expect(add.right, path).toBe(card.right);
      }
    }
  });
}
