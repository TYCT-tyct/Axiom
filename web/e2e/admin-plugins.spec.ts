import { expect, type Page, test } from "@playwright/test";
import { open } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { words, zhText } from "./words.ts";

// The plugins page: two cards of how each plugin was used, the requests that used one with a drawer for each, and the
// two settings pages that moved here from the settings; and the page of one's own sign-in.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const h1 = (page: Page, name: string | RegExp) => page.getByRole("heading", { level: 1, name });

test("the overview counts each plugin, lists the requests that used one, and opens a request with its route", async ({
  page,
}) => {
  const state = await open(page, "/plugins");
  await expect(h1(page, words("shell.nav.plugins"))).toBeVisible();
  const recent = page.getByRole("table", { name: zhText("plugins.recentLabel") });
  const web = page.getByRole("region", { name: zhText("calls.plugin.web") });
  const file = page.getByRole("region", { name: zhText("calls.plugin.file-parser") });
  // The usual calls: fifteen searched the web, and five of those also read a file, a run that failed each time.
  await expect(web.getByText("15", { exact: true }).first()).toBeVisible();
  await expect(file.getByText("5", { exact: true }).first()).toBeVisible();
  await expect(file).toContainText("100%");
  await expect(recent.getByRole("row")).toHaveCount(16);
  expect(state.log.some((entry) => entry.startsWith("GET /reports/plugins"))).toBe(true);

  await page
    .getByRole("radiogroup", { name: zhText("plugins.filterLabel") })
    .getByText(zhText("calls.plugin.file-parser"))
    .click();
  await expect(recent.getByRole("row")).toHaveCount(6);

  await recent.locator("tbody tr").first().locator("td").first().getByRole("button").click();
  await expect(page.getByRole("dialog").getByRole("heading", { name: zhText("calls.drawer.route") })).toBeVisible();
  await expect(page.getByRole("dialog").getByText(zhText("calls.drawer.paid"))).toBeVisible();
});

test("the cards show no cost, since the stats give none", async ({ page }) => {
  await open(page, "/plugins");
  await expect(page.getByRole("region", { name: zhText("calls.plugin.web") })).toBeVisible();
  await expect(page.getByText(/成本|cost/i)).toHaveCount(0);
});

test("the two tabs of settings are the pages that left the settings", async ({ page }) => {
  await open(page, "/plugins");
  await page.getByRole("tab", { name: zhText("plugins.tabs.web") }).click();
  await expect(page).toHaveURL(/\/plugins\/web$/);
  await expect(page.getByText("searxng-main")).toBeVisible();
  await page.getByRole("tab", { name: zhText("plugins.tabs.file") }).click();
  await expect(page).toHaveURL(/\/plugins\/file$/);
  await expect(page.getByText("parser.example.com")).toBeVisible();
  await page.goto("/settings");
  await expect(page).toHaveURL(/\/settings\/site$/);
  await expect(h1(page, words("shell.nav.site"))).toBeVisible();
  await expect(page.getByText(words("settings.search.title"))).toHaveCount(0);
});

test("a session that may not search calls sees the cards and no list", async ({ page }) => {
  const state = await open(page, "/plugins");
  state.permissions = ["sites.manage"];
  await page.reload();
  await expect(page.getByRole("region", { name: zhText("calls.plugin.web") })).toBeVisible();
  await expect(page.getByRole("table", { name: zhText("plugins.recentLabel") })).toHaveCount(0);
});

test("my sign-in shows the session, regenerates recovery codes once, and has no way to turn two-step off", async ({
  page,
}) => {
  const state = await open(page, "/", "usual", { mfaEnabled: true });
  await page.getByRole("button", { name: words("shell.account") }).click();
  await page.getByRole("link", { name: words("shell.myLogin") }).click();
  await expect(page).toHaveURL(/\/me$/);
  await expect(h1(page, words("shell.myLogin"))).toBeVisible();
  await expect(page.getByText(words("me.twoStep.bound"))).toBeVisible();
  await expect(page.getByRole("button", { name: /关闭|停用|turn off|disable/i })).toHaveCount(0);

  await page.getByRole("button", { name: words("me.twoStep.regenerate") }).click();
  await page.getByRole("dialog").getByLabel(zhText("security.setup.code")).fill(state.user.mfaCode);
  await page.locator("dialog button[type=submit]").click();
  await expect(page.getByRole("dialog").getByText("abcd-efgh-ijkl-1009")).toBeVisible();
  expect(state.log).toContain("POST /auth/recovery-codes");
});
