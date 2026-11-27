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
    const state = await open(page, "/settings/site");
    const color = page.getByLabel(words("setting.site.color"));
    await expect(color).toHaveValue("#2563eb");
    await expect(page.getByText(zhText("settings.brand.light")).first()).toBeVisible();
    await expect(page.getByText(zhText("settings.brand.dark")).first()).toBeVisible();

    // Text that is no colour is marked and cannot be submitted.
    await color.fill("not-a-color");
    await expect(page.getByText(words("drafts.invalid_other"))).toBeVisible();
    await expect(page.getByRole("button", { name: new RegExp(zhText("drafts.review")) })).toBeDisabled();

    // Extreme yellow triggers automatic contrast correction while keeping live WCAG contrast ratios visible.
    await color.fill("#FFFF00");
    await expect(page.getByText(/文字对比度 \d+\.\d+:1/)).toHaveCount(2);
    await expect(page.getByText(zhText("settings.brand.adjusted"))).toBeVisible();
    await page.getByRole("button", { name: new RegExp(zhText("drafts.review")) }).click();
    await expect(dialog(page).locator("ins")).toHaveText("#FFFF00");
    await send(page);
    await expect(page.getByText(words("drafts.submitted_other"))).toBeVisible();
    expect(siteExtrasOf(state).brand).toEqual({ brand_color: "#ffff00" });

    await color.fill("");
    await page.getByRole("button", { name: new RegExp(zhText("drafts.review")) }).click();
    await send(page, "return to default palette");
    await expect(page.getByText(words("drafts.submitted_other"))).toBeVisible();
    expect(siteExtrasOf(state).brand).toEqual({});
  });

  test("site workspace limit and login limits validate bounds and save with a reason", async ({ page }) => {
    const state = await open(page, "/settings/limits");
    const review = page.getByRole("button", { name: new RegExp(zhText("drafts.review")) });

    const workspaces = page.getByLabel(words("setting.limits.maxWorkspaces"));
    await workspaces.fill("2000");
    await expect(page.getByText(words("drafts.invalid_other"))).toBeVisible();
    await expect(review).toBeDisabled();
    await workspaces.fill("80");
    await review.click();
    await send(page);
    await expect(page.getByText(words("drafts.submitted_other"))).toBeVisible();
    expect(siteExtrasOf(state).workspaceLimit).toEqual({ max_workspaces: 80, effective: 80 });

    await page.goto("/settings/sessions");
    const challenge = page.getByLabel(words("setting.session.challenge"));
    await challenge.fill("200");
    await expect(page.getByText(words("drafts.invalid_other"))).toBeVisible();
    await challenge.fill("8");
    await page.getByLabel(words("setting.session.ipBurst")).fill("");
    await review.click();
    await send(page);
    await expect(page.getByText(words("drafts.submitted_other"))).toBeVisible();
    expect(siteExtrasOf(state).loginLimits.limits).toEqual({ challenge_code_attempts: 8 });
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
    await page.goto("/settings/site");
    await expect(page.getByLabel(words("setting.site.name"))).toBeVisible();
    await expect(page.getByLabel(words("setting.site.color"))).toHaveCount(0);
    await page.goto("/settings/limits");
    await expect(page.getByLabel(words("setting.limits.maxKeys"))).toBeVisible();
    await expect(page.getByLabel(words("setting.limits.maxWorkspaces"))).toHaveCount(0);
    await page.goto("/settings/sessions");
    await expect(page.getByLabel(words("setting.session.consoleIdle"))).toBeVisible();
    await expect(page.getByLabel(words("setting.session.ipBurst"))).toHaveCount(0);
  });

  test("an account's workspace limit can be overridden and cleared back to the site limit", async ({ page }) => {
    // The account's drawer has the three limits, each with its change; the workspaces' is the third.
    const state = await open(page, `/accounts/${acct(1)}`);
    const edit = page.getByRole("button", { name: zhText("users.account.editLimit") }).nth(2);
    await edit.click();
    await dialog(page).getByLabel(words("settings.workspaceLimit.field")).fill("12");
    await send(page, "raise workspace limit");
    await expect(done(page)).toBeVisible();
    expect(state.accounts.get(acct(1))?.workspace_limit).toEqual({ max_workspaces: 12, effective: 12 });
    await close(page);

    await edit.click();
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
    await expect(page.getByRole("dialog").getByText(zhText("settings.workspaceLimit.field"))).toBeVisible();
    await expect(page.getByRole("button", { name: zhText("users.account.editLimit") })).toHaveCount(0);
  });
});

test.describe("supplier endpoint toggle and credential limits", () => {
  test("disabling and enabling an endpoint updates routing state through ChangeDialog", async ({ page }) => {
    const state = await open(page, `/suppliers/${uuid(2, 1)}?tab=models`);
    await page.getByText(zhText("upstream.models.listed"), { exact: true }).click();
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

  test("a relay model with no published price is put on sale at the official base price, then cleared", async ({
    page,
  }) => {
    const state = await mockAdmin(page, "usual");
    signedInAs(state, "mfa");
    const detail = state.suppliers[1];
    const endpoint = detail?.endpoints.find((one) => one.upstream_name === "unpriced-model");
    if (!detail?.groups[0] || !endpoint) throw new Error("the usual data has supplier 2's unpriced endpoint");
    detail.supplier.unit_value = "1";
    detail.groups[0].ratio = "1.5";
    endpoint.model_id = uuid(5, 1);
    endpoint.cost_reason = "unpriced";
    await page.goto(`/suppliers/${uuid(2, 2)}?tab=models`);
    await page.getByText(zhText("upstream.models.listed"), { exact: true }).click();
    const row = page.getByRole("row").filter({ hasText: "unpriced-model" });
    await expect(row).toContainText(zhText("suppliers.endpoints.state.unknown"));

    await page.getByRole("button", { name: "确认 unpriced-model 的底价（分组 Maple 2）" }).click();
    // the official price is the default (input 3 per million tokens); the cost is the server's, said as a formula
    await expect(dialog(page).getByText("3", { exact: true })).toBeVisible();
    await expect(dialog(page)).toContainText(zhText("upstream.basePrice.formula"));
    await send(page, "the supplier publishes no price");
    await expect(done(page)).toBeVisible();
    expect(endpoint.cost_state).toBe("known");
    await close(page);
    await expect(row).toContainText(zhText("suppliers.endpoints.state.ready"));

    // the price lines are the server's figure: the base times the group ratio 1.5
    await page.getByRole("button", { name: "打开 unpriced-model 的价目和检测" }).click();
    await expect(page.getByText(zhText("upstream.endpoint.sources.confirmed"))).toBeVisible();
    await expect(page.getByText("4.5 USD")).toBeVisible();
    await press(page, "upstream.basePrice.clear").click();
    await send(page, "the supplier published its price");
    await expect(done(page)).toBeVisible();
    await expect(dialog(page)).toContainText(zhText("upstream.basePrice.cleared"));
    expect(endpoint.cost_state).toBe("unknown");
    await close(page);

    // a new-api relay is offered a base where it publishes no price, not where its price cannot be expressed, and the
    // dialog names the model it showed
    const newapi = state.suppliers[0];
    const unpriced = newapi?.endpoints.find((one) => one.upstream_name === "unpriced-model");
    const unexpressible = newapi?.endpoints.find((one) => one.upstream_name === "rate-limited-model");
    if (!newapi || !unpriced || !unexpressible) throw new Error("the usual data has supplier 1's endpoints");
    newapi.supplier.unit_value = "1";
    Object.assign(unpriced, { model_id: uuid(5, 1), cost_reason: "unpriced" });
    Object.assign(unexpressible, { cost_state: "unknown", cost_reason: "unexpressible" });
    await page.goto(`/suppliers/${uuid(2, 1)}?tab=models`);
    await page.getByText(zhText("upstream.models.listed"), { exact: true }).click();
    await expect(page.getByRole("row").filter({ hasText: "rate-limited-model" })).toContainText(
      zhText("suppliers.endpoints.state.unknown"),
    );
    const offer = page.getByRole("button", { name: "确认 unpriced-model 的底价（分组 Maple）" });
    await expect(offer).toBeVisible();
    await expect(page.getByRole("button", { name: "确认 rate-limited-model 的底价（分组 Maple）" })).toHaveCount(0);
    await offer.click();
    const sent = page.waitForRequest((request) => request.url().endsWith("/base-prices"));
    await send(page, "the new-api relay hides this price");
    expect((await sent).postDataJSON().items).toEqual([
      { group_id: unpriced.group_id, upstream_name: "unpriced-model", model_id: uuid(5, 1) },
    ]);
    await expect(done(page)).toBeVisible();
    expect(unpriced.cost_state).toBe("known");
  });

  test("credential concurrency and RPM ceiling validate bounds, save and clear RPM", async ({ page }) => {
    const state = await open(page, `/suppliers/${uuid(2, 1)}?tab=keys`);
    // the key's drawer holds its limits
    await page.getByRole("button", { name: "key-1-a" }).click();
    await expect(page.getByText(/ \/ 600$/).first()).toBeVisible();
    await press(page, "upstream.credential.limits").click();
    await dialog(page).getByLabel(words("suppliers.credentials.concurrency")).fill("0");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("suppliers.credentialLimits.invalidConcurrency"));

    await dialog(page).getByLabel(words("suppliers.credentials.concurrency")).fill("16");
    await dialog(page).getByLabel(words("suppliers.credentials.rpm")).fill("1200");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    expect(state.suppliers[0]?.credentials[0]).toMatchObject({ max_concurrency: 16, requests_per_minute: 1200 });
    await close(page);
    await expect(page.getByText(/ \/ 1200$/).first()).toBeVisible();

    await press(page, "upstream.credential.limits").click();
    await dialog(page).getByLabel(words("suppliers.credentials.rpm")).fill("");
    await send(page, "remove rpm ceiling");
    await expect(done(page)).toBeVisible();
    expect(state.suppliers[0]?.credentials[0]?.requests_per_minute).toBeUndefined();
    await close(page);
    await expect(page.getByText(new RegExp(` / ${zhText("upstream.credential.noCeiling")}$`)).first()).toBeVisible();
  });
});
