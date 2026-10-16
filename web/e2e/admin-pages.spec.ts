import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { uuid } from "./admin-data.ts";
import { acct } from "./admin-scenes.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The admin's pages against the mocked API (admin-api.ts): the main flows of M1, each as an operator would walk it. The
// mock checks what the server would: the session's level, the permission, the CSRF token, the Origin, and for a change
// the Idempotency-Key and the reason, so a page that forgets one fails here.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

async function open(page: Page, path: string, data: "usual" | "empty" = "usual") {
  const state = await mockAdmin(page, data);
  signedInAs(state, "mfa");
  await page.goto(path);
  return state;
}

const dialog = (page: Page) => page.getByRole("dialog");
const h1 = (page: Page, name: string | RegExp) => page.getByRole("heading", { level: 1, name });
const press = (page: Page, key: string, nth = 0) => page.getByRole("button", { name: words(key) }).nth(nth);

// The reason every change asks for, and the button that sends it.
async function send(page: Page, reason = "because the owner asked") {
  await dialog(page).getByLabel(words("change.reason")).fill(reason);
  await dialog(page).locator("button[type=submit]").click();
}

// The dialog has an icon button for closing and the result's own: the result's comes last.
const close = (page: Page) =>
  dialog(page)
    .getByRole("button", { name: zhText("change.close") })
    .last()
    .click();
const done = (page: Page) => dialog(page).getByText(zhText("change.done"));

test.describe("users and credit", () => {
  test("users are found by the start of the email and paged by cursor", async ({ page }) => {
    await open(page, "/users");
    await expect(page.getByRole("row")).toHaveCount(51);
    await page.getByRole("button", { name: "加载更多" }).click();
    await expect(page.getByRole("row")).toHaveCount(61);
    await page.getByLabel(zhText("users.searchLabel")).fill("USER1");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    expect(new URL(page.url()).searchParams.get("q")).toBe("USER1");
    await expect(page.getByRole("row")).toHaveCount(12);
    await page.getByLabel(zhText("users.searchLabel")).fill("nobody");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    await expect(page.getByRole("heading", { name: zhText("users.noMatch.title") })).toBeVisible();
  });

  test("credit is added with a reason and a key, and the same key again does it once", async ({ page }) => {
    const state = await open(page, `/accounts/${acct(1)}`);
    await expect(h1(page, "user1@example.com")).toBeVisible();
    await expect(page.getByText("$12.00").first()).toBeVisible();

    await press(page, "users.credit.open").click();
    // a missing reason and a bad amount are told before anything is sent
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByRole("alert")).toHaveText(/金额/);
    await dialog(page).getByLabel(words("users.credit.amount")).fill("2.5");
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.reasonMissing"));
    expect(state.log.filter((line) => line.startsWith("POST"))).toEqual([]);

    // the answer is lost on the way, after the server did it
    state.dropReplies = 1;
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(/连不上/);
    expect(state.stored.size).toBe(1);
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    await expect(dialog(page).getByText(zhText("change.replayed"))).toBeVisible();
    await expect(dialog(page).getByText("$14.50")).toBeVisible();
    // the same key reached the server twice and the credit was added once
    const posts = state.log.filter((line) => line.startsWith("POST /accounts"));
    expect(posts).toHaveLength(2);
    expect(state.stored.size).toBe(1);
    await close(page);
    await expect(page.getByText("$14.50").first()).toBeVisible();
  });

  test("a grant the server refuses says so and can be changed and sent again", async ({ page }) => {
    await open(page, `/accounts/${acct(2)}`);
    await press(page, "users.credit.open").click();
    await dialog(page).getByLabel(words("users.credit.amount")).fill("2000000");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(/没能处理/);
    await dialog(page).getByLabel(words("users.credit.amount")).fill("2");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    await expect(dialog(page).getByText(zhText("change.replayed"))).toHaveCount(0);
  });

  test("an operator without the permission to grant sees the account but no way to add credit", async ({ page }) => {
    const state = await mockAdmin(page);
    state.permissions = ["users.view"];
    signedInAs(state, "mfa");
    await page.goto(`/accounts/${acct(1)}`);
    await expect(h1(page, "user1@example.com")).toBeVisible();
    await expect(press(page, "users.credit.open")).toHaveCount(0);
  });

  test("an account that is not there says so", async ({ page }) => {
    await open(page, `/accounts/${acct(999)}`);
    await expect(page.getByRole("heading", { name: "没能加载" })).toBeVisible();
  });
});

test.describe("site settings", () => {
  test("the values in force show with where they come from, and a change keeps the defaults for what is left empty", async ({
    page,
  }) => {
    const state = await open(page, "/settings");
    await expect(page.getByText(/7\s*天/)).toBeVisible();
    await expect(page.getByText(/30\s*天/).first()).toBeVisible();
    await press(page, "settings.edit").click();
    await dialog(page).getByLabel(words("settings.field.admin_idle_timeout_seconds")).fill("100");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("settings.invalidRange"));
    await dialog(page).getByLabel(words("settings.field.admin_idle_timeout_seconds")).fill("7200");
    await dialog(page).getByLabel(words("settings.field.console_idle_timeout_seconds")).fill("");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    expect(state.settings.settings).toEqual({
      admin_idle_timeout_seconds: 7200,
      new_key_daily_limit: { amount: "20000000000", currency: "USD" },
    });
    await close(page);
    await expect(page.getByText(/7200/)).toBeVisible();
  });
});

test.describe("suppliers", () => {
  test("a supplier is connected in two steps and settles into the setup checklist", async ({ page }) => {
    const state = await open(page, "/suppliers");
    await press(page, "suppliers.connect.open").click();
    await dialog(page).getByLabel(words("suppliers.connect.origin")).fill("https://shop.unknown.example");
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("suppliers.connect.unrecognised"));
    expect(state.log).toContain("POST /suppliers/detections");

    await dialog(page).getByLabel(words("suppliers.connect.origin")).fill("https://api.example.cn");
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByText(/识别为 newapi/)).toBeVisible();
    await dialog(page).getByLabel(words("suppliers.connect.name")).fill("Example CN 2");
    await dialog(page).getByLabel(words("suppliers.connect.account")).fill("ops@example.cn");
    await dialog(page).getByLabel(words("suppliers.connect.currency")).fill("cny");
    await dialog(page).getByLabel(words("suppliers.login.access_token")).fill("pat-secret-value");
    await send(page, "second source");
    await expect(done(page)).toBeVisible();
    // the login is sent once, as typed, and nothing of it comes back
    const created = state.suppliers[0]?.supplier;
    expect(created).toMatchObject({ name: "Example CN 2", settlement_currency: "CNY", status: "connecting" });
    expect(JSON.stringify(created)).not.toContain("pat-secret-value");

    await dialog(page)
      .getByRole("link", { name: zhText("suppliers.connect.view") })
      .click();
    await expect(h1(page, "Example CN 2")).toBeVisible();
    // the page asks again until the sync has settled it
    await expect(page.getByText(zhText("suppliers.steps.groups.title"))).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole("button", { name: words("suppliers.group.select.action") })).toHaveCount(2);
  });

  test("groups are chosen, a key is bound and the unit value confirmed", async ({ page }) => {
    const state = await open(page, `/suppliers/${uuid(2, 2)}`);
    await expect(h1(page, "Example US")).toBeVisible();
    await expect(page.getByText(zhText("suppliers.unit.unconfirmed")).first()).toBeVisible();

    await press(page, "suppliers.unit.open").click();
    await dialog(page).getByLabel(words("suppliers.unit.value")).fill("0");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("suppliers.unit.invalid"));
    await dialog(page).getByLabel(words("suppliers.unit.value")).fill("1");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    await close(page);
    expect(state.suppliers[1]?.supplier.unit_value).toBe("1");

    await press(page, "suppliers.group.select.action").click();
    await send(page, "use the vip group");
    await expect(dialog(page).getByText("Code vip-2")).toBeVisible();
    await close(page);

    await press(page, "suppliers.bind.action").click();
    await dialog(page).getByLabel(words("suppliers.bind.group")).selectOption({ index: 1 });
    await send(page);
    await expect(done(page)).toBeVisible();
    await close(page);
    await expect(page.getByText("key-2-b").first()).toBeVisible();
    expect(state.suppliers[1]?.credentials).toHaveLength(2);
  });

  test("pausing and resuming move the supplier, and a risky removal asks for its name", async ({ page }) => {
    const state = await open(page, `/suppliers/${uuid(2, 1)}`);
    await press(page, "suppliers.status.paused.action").click();
    await send(page);
    await expect(done(page)).toBeVisible();
    await close(page);
    await expect(page.getByText(zhText("suppliers.state.paused")).first()).toBeVisible();
    await press(page, "suppliers.status.active.action").click();
    await send(page);
    await close(page);
    expect(state.suppliers[0]?.supplier.status).toBe("active");

    await press(page, "suppliers.remove.open").click();
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.confirmMismatch"));
    await dialog(page)
      .getByLabel(/输入“Supplier 1”以确认/)
      .fill("Supplier 1");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    await close(page);
    await expect(page.getByText(zhText("suppliers.state.removed")).first()).toBeVisible({ timeout: 15_000 });
    // a removed supplier offers no more changes
    await expect(press(page, "suppliers.remove.open")).toHaveCount(0);
  });

  test("a supplier that needs attention says why and takes a new login", async ({ page }) => {
    const state = await open(page, `/suppliers/${uuid(2, 3)}`);
    await expect(page.getByText("The supplier rejected the login (401).").first()).toBeVisible();
    await press(page, "suppliers.login.open").click();
    await dialog(page).getByLabel(words("suppliers.login.access_token")).fill("fresh-token");
    await send(page, "rotated");
    await expect(done(page)).toBeVisible();
    expect(state.suppliers[2]?.supplier.status).toBe("connecting");
    await close(page);
    await expect(page.getByText(zhText("suppliers.state.active")).first()).toBeVisible({ timeout: 15_000 });
  });

  test("the list filters by state and an operator who may only view cannot connect or change", async ({ page }) => {
    const state = await mockAdmin(page);
    state.permissions = ["suppliers.view"];
    signedInAs(state, "mfa");
    await page.goto("/suppliers");
    await expect(page.getByRole("row")).toHaveCount(6);
    await expect(press(page, "suppliers.connect.open")).toHaveCount(0);
    await page.getByLabel(zhText("suppliers.col.status")).selectOption("paused");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    await expect(page.getByRole("row")).toHaveCount(2);
    await page.getByRole("link", { name: /打开 Paused Site/ }).click();
    await expect(h1(page, "Paused Site")).toBeVisible();
    await expect(press(page, "suppliers.unit.open")).toHaveCount(0);
    await expect(press(page, "suppliers.remove.open")).toHaveCount(0);
  });
});

test.describe("names waiting to be mapped", () => {
  test("a name is mapped to a model and leaves the queue", async ({ page }) => {
    const state = await open(page, "/mappings");
    const before = state.pending.length;
    await expect(page.getByText("vendor-model-1-preview")).toBeVisible();
    await press(page, "mappings.map").click();
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("mappings.dialog.modelMissing"));
    await dialog(page).getByLabel(words("mappings.dialog.model")).fill("openai/model-1");
    await expect(dialog(page).getByText(/已选中/)).toBeVisible();
    await dialog(page).getByLabel(words("mappings.dialog.scope")).selectOption("supplier");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    expect(state.pending).toHaveLength(before - 1);
    await close(page);
    await expect(page.getByText("vendor-model-1-preview")).toHaveCount(0);
  });
});
