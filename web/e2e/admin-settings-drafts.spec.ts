import { expect, type Page, test } from "@playwright/test";
import { dialog, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { words, zhText } from "./words.ts";

// A settings page edited in place and submitted through one dialog (admin/src/settings-drafts.tsx), on registration.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const h1 = (page: Page, name: string | RegExp) => page.getByRole("heading", { level: 1, name });

test("registration is changed in its rows and submitted in one dialog with one reason; leaving with a change asks first", async ({
  page,
}) => {
  const state = await open(page, "/settings/registration");
  await page.getByLabel(words("setting.registration.burst")).fill("30");
  await expect(page.getByText(words("drafts.pending_other"))).toBeVisible();
  await page
    .getByRole("link", { name: words("shell.nav.site") })
    .first()
    .click();
  await expect(dialog(page).getByText(words("drafts.leaveBody"))).toBeVisible();
  await dialog(page)
    .getByRole("button", { name: words("drafts.stay") })
    .click();
  await expect(h1(page, words("shell.nav.registration"))).toBeVisible();
  await page.getByRole("button", { name: new RegExp(zhText("drafts.review")) }).click();
  await expect(dialog(page).locator("del")).toHaveText("20");
  await expect(dialog(page).locator("ins")).toHaveText("30");
  await send(page);
  await expect(page.getByText(words("drafts.submitted_other"))).toBeVisible();
  await expect(page.getByLabel(words("setting.registration.burst"))).toHaveValue("30");
  expect(state.registration).toMatchObject({
    domain_burst: 30,
    domain_per_hour: 200,
    domain_deny: ["mailinator.com"],
  });
});

test("⌘K finds a setting by its name and leads to its row", async ({ page }) => {
  await open(page, "/models");
  // wait for the page itself: the palette opens only on a page that has started
  await expect(h1(page, words("nav.models"))).toBeVisible();
  await page.keyboard.press("Control+k");
  const palette = page.getByRole("dialog", { name: zhText("shell.palette.label") });
  await palette.getByRole("combobox").fill(zhText("setting.registration.burst"));
  await expect(palette.getByRole("option")).toHaveCount(1);
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/settings\/registration\?setting=registration\.burst$/);
  await expect(page.getByLabel(words("setting.registration.burst"))).toBeVisible();
});

test("the old settings addresses land on the pages that took their place", async ({ page }) => {
  await open(page, "/settings");
  await expect(page).toHaveURL(/\/settings\/site$/);
  for (const { from, to } of [
    { from: "/retention", to: "/settings/gateway" },
    { from: "/operations-credentials", to: "/settings/agent" },
    { from: "/operations-limits", to: "/settings/agent" },
  ]) {
    await page.goto(from);
    await expect(page).toHaveURL(new RegExp(`${to}$`));
  }
});
