import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { uuid } from "./admin-data.ts";
import { close, dialog, done, open, send } from "./admin-page.ts";
import { acct } from "./admin-scenes.ts";
import { siteExtrasOf } from "./admin-site-api.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const press = (page: Page, key: string, nth = 0) => page.getByRole("button", { name: words(key) }).nth(nth);

test.describe("site brand, workspace and login limits", () => {
  test("site brand previews light/dark accents with contrast, saves a new hex and clears to default", async ({
    page,
  }) => {
    const state = await open(page, "/settings");
    await expect(page.getByText("#2563eb").first()).toBeVisible();
    await expect(page.getByText(zhText("settings.brand.light")).first()).toBeVisible();
    await expect(page.getByText(zhText("settings.brand.dark")).first()).toBeVisible();

    await press(page, "settings.brand.edit").click();
    const input = dialog(page).getByLabel(words("settings.brand.color"));
    await input.fill("not-a-color");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("settings.brand.invalid"));

    // Extreme yellow triggers automatic contrast correction while keeping live WCAG contrast ratios visible.
    await input.fill("#FFFF00");
    await expect(dialog(page).getByText(/文字对比度 \d+\.\d+:1/)).toHaveCount(2);
    await expect(dialog(page).getByText(zhText("settings.brand.adjusted"))).toBeVisible();
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    expect(siteExtrasOf(state).brand).toEqual({ brand_color: "#ffff00" });
    await close(page);
    await expect(page.getByText("#ffff00").first()).toBeVisible();

    await press(page, "settings.brand.edit").click();
    await dialog(page).getByLabel(words("settings.brand.color")).fill("");
    await send(page, "return to default palette");
    await expect(done(page)).toBeVisible();
    expect(siteExtrasOf(state).brand).toEqual({});
    await close(page);
    await expect(page.getByText(zhText("settings.brand.default"))).toBeVisible();
  });

  test("site workspace limit and login limits validate bounds and save with a reason", async ({ page }) => {
    const state = await open(page, "/settings");

    await press(page, "settings.workspaceLimit.edit").click();
    await dialog(page).getByLabel(words("settings.workspaceLimit.field")).fill("2000");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(
      zhText("settings.workspaceLimit.invalid").replace("{{min}}", "1").replace("{{max}}", "1000"),
    );
    await dialog(page).getByLabel(words("settings.workspaceLimit.field")).fill("80");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    expect(siteExtrasOf(state).workspaceLimit).toEqual({ max_workspaces: 80, effective: 80 });
    await close(page);

    await press(page, "settings.loginLimits.edit").click();
    await dialog(page).getByLabel(words("settings.loginLimits.field.challenge_code_attempts")).fill("200");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("settings.loginLimits.invalid"));
    await dialog(page).getByLabel(words("settings.loginLimits.field.challenge_code_attempts")).fill("8");
    await dialog(page).getByLabel(words("settings.loginLimits.field.ip_attempt_burst")).fill("");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    expect(siteExtrasOf(state).loginLimits.limits).toEqual({ challenge_code_attempts: 8 });
    await close(page);
  });

  test("an older node without brand, workspace limit or login limits leaves the rest of settings working", async ({
    page,
  }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    for (const path of ["brand", "workspace-limit", "login-limits"]) {
      await page.route(`**/api/admin/v1/sites/*/${path}`, (route) =>
        route.fulfill({ status: 404, json: { error: { code: "not_found", message: "not found" } } }),
      );
    }
    await page.goto("/settings");
    await expect(page.getByText(zhText("settings.brand.unavailable"))).toBeVisible();
    await expect(page.getByText(zhText("settings.workspaceLimit.unavailable"))).toBeVisible();
    await expect(page.getByText(zhText("settings.loginLimits.unavailable"))).toBeVisible();
    await expect(press(page, "settings.edit")).toBeVisible();
  });

  test("an account's workspace limit can be overridden and cleared back to the site limit", async ({ page }) => {
    const state = await open(page, `/accounts/${acct(1)}`);
    await press(page, "settings.workspaceLimit.edit").click();
    await dialog(page).getByLabel(words("settings.workspaceLimit.field")).fill("12");
    await send(page, "raise workspace limit");
    await expect(done(page)).toBeVisible();
    expect(state.accounts.get(acct(1))?.workspace_limit).toEqual({ max_workspaces: 12, effective: 12 });
    await close(page);

    await press(page, "settings.workspaceLimit.edit").click();
    await dialog(page).getByLabel(words("settings.workspaceLimit.field")).fill("");
    await send(page, "inherit site limit");
    await expect(done(page)).toBeVisible();
    expect(state.accounts.get(acct(1))?.workspace_limit).toEqual({ effective: 25 });
    await close(page);
  });

  test("an operator without sites.manage sees an account's workspace limit read-only", async ({ page }) => {
    const state = await mockAdmin(page);
    state.permissions = ["users.view"];
    signedInAs(state, "mfa");
    await page.goto(`/accounts/${acct(1)}`);
    await expect(page.getByText(zhText("settings.workspaceLimit.title.account"))).toBeVisible();
    await expect(press(page, "settings.workspaceLimit.edit")).toHaveCount(0);
  });
});

test.describe("supplier endpoint toggle and credential limits", () => {
  test("disabling and enabling an endpoint updates routing state through ChangeDialog", async ({ page }) => {
    const state = await open(page, `/suppliers/${uuid(2, 1)}`);
    await page.getByRole("button", { name: "停用端点 gpt-test-mini（分组 Maple）" }).click();
    await send(page, "temporarily take endpoint out of rotation");
    await expect(done(page)).toBeVisible();
    expect(state.suppliers[0]?.endpoints[1]?.enabled).toBe(false);
    await close(page);

    await page.getByRole("button", { name: "启用端点 gpt-test-mini（分组 Maple）" }).click();
    await send(page, "return endpoint after upstream fix");
    await expect(done(page)).toBeVisible();
    expect(state.suppliers[0]?.endpoints[1]?.enabled).toBe(true);
    await close(page);
  });

  test("credential concurrency and RPM ceiling validate bounds, save and clear RPM", async ({ page }) => {
    const state = await open(page, `/suppliers/${uuid(2, 1)}`);
    await expect(page.getByText("600").first()).toBeVisible();
    await press(page, "suppliers.credentialLimits.title").click();
    await dialog(page).getByLabel(words("suppliers.credentials.concurrency")).fill("0");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("suppliers.credentialLimits.invalidConcurrency"));

    await dialog(page).getByLabel(words("suppliers.credentials.concurrency")).fill("16");
    await dialog(page).getByLabel(words("suppliers.credentials.rpm")).fill("1200");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    expect(state.suppliers[0]?.credentials[0]).toMatchObject({ max_concurrency: 16, requests_per_minute: 1200 });
    await close(page);
    await expect(page.getByText("1,200").first()).toBeVisible();

    await press(page, "suppliers.credentialLimits.title").click();
    await dialog(page).getByLabel(words("suppliers.credentials.rpm")).fill("");
    await send(page, "remove rpm ceiling");
    await expect(done(page)).toBeVisible();
    expect(state.suppliers[0]?.credentials[0]?.requests_per_minute).toBeUndefined();
    await close(page);
    await expect(page.getByText(zhText("suppliers.credentials.rpmUnlimited")).first()).toBeVisible();
  });
});
