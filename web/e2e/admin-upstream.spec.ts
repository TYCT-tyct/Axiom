import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { aSupplier, uuid } from "./admin-data.ts";
import { close, dialog, done, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// 上游 and a channel's page against the mocked API (admin-api.ts): connecting, the groups, keys and settings of a
// channel, and the queue of what wants a decision, each as an operator would walk it.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const h1 = (page: Page, name: string | RegExp) => page.getByRole("heading", { level: 1, name });
const press = (page: Page, key: string, nth = 0) => page.getByRole("button", { name: words(key) }).nth(nth);
const tab = (page: Page, key: string) => page.getByRole("tab", { name: zhText(key) }).click();

test.describe("suppliers", () => {
  test("adding keys offers every direct API, not only the first page's, from any tab", async ({ page }) => {
    // A direct API listed after the first page of suppliers (50) is still one to add keys to, and the queue's tab, which
    // does not page the suppliers itself, opens the same dialog.
    const suppliers = Array.from({ length: 55 }, (_, i) => ({
      supplier: aSupplier(i + 1, i === 54 ? { name: "Far Direct", mode: "direct", kind: "openai" } : {}),
      groups: [],
      credentials: [],
      unbound_keys: [],
      endpoints: [],
    }));
    await open(page, "/suppliers?tab=inbox", "usual", { suppliers });
    await press(page, "upstream.add").click();
    // the search finds the one type by its kind, as the families in other types' lines also say OpenAI
    await dialog(page).getByLabel(words("upstream.types.search")).fill("openai");
    await dialog(page)
      .getByRole("button", { name: /OpenAI/ })
      .click();
    await expect(
      dialog(page).getByLabel(words("upstream.addDialog.to")).locator("option", { hasText: "Far Direct" }),
    ).toHaveCount(1);
  });

  test("the longest status reason the contract allows takes two lines in the list and is whole on the title", async ({
    page,
  }) => {
    await open(page, "/suppliers", "worst");
    const reason = page.locator("p[title]").filter({ hasText: "The supplier rejected the login." }).first();
    await expect(reason).toBeVisible();
    const title = (await reason.getAttribute("title")) ?? "";
    expect(title).toHaveLength(200);
    const lines = await reason.evaluate(
      (p) => p.getBoundingClientRect().height / Number.parseFloat(getComputedStyle(p).lineHeight),
    );
    expect(lines).toBeLessThanOrEqual(2);
  });

  test("a supplier is connected in two steps and settles into the setup checklist", async ({ page }) => {
    const state = await open(page, "/suppliers");
    await press(page, "upstream.add").click();
    await dialog(page)
      .getByRole("button", { name: /New API/ })
      .click();
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
    await expect(page.getByText(zhText("upstream.steps.groups.title"))).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole("switch", { name: words("upstream.groups.switchOn") })).toHaveCount(2);
  });

  test("groups are chosen, a key is bound and the unit value confirmed", async ({ page }) => {
    const state = await open(page, `/suppliers/${uuid(2, 2)}`);
    await expect(h1(page, "Example US")).toBeVisible();
    await expect(page.getByText(zhText("upstream.steps.unit.title"))).toBeVisible();

    await press(page, "upstream.steps.unit.action").click();
    await dialog(page).getByLabel(words("suppliers.unit.value")).fill("0");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("suppliers.unit.invalid"));
    await dialog(page).getByLabel(words("suppliers.unit.value")).fill("1");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    await close(page);
    expect(state.suppliers[1]?.supplier.unit_value).toBe("1");

    await page.getByRole("switch", { name: "接入 vip-2" }).click();
    await send(page, "use the vip group");
    await expect(page.getByRole("switch", { name: "停掉 vip-2" })).toBeChecked();
    expect(state.suppliers[1]?.groups[1]).toMatchObject({ selected: true, codename: "Code vip-2" });

    await tab(page, "upstream.detail.tabs.keys");
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
    await press(page, "upstream.detail.pause").click();
    await send(page);
    await expect(done(page)).toBeVisible();
    await close(page);
    await expect(page.getByText(zhText("suppliers.state.paused")).first()).toBeVisible();
    await press(page, "upstream.detail.resume").click();
    await send(page);
    await close(page);
    expect(state.suppliers[0]?.supplier.status).toBe("active");

    await tab(page, "upstream.detail.tabs.settings");
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
    await tab(page, "upstream.detail.tabs.settings");
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
    await expect(press(page, "upstream.add")).toHaveCount(0);
    await page.getByRole("searchbox", { name: zhText("upstream.channels.search") }).fill("paused");
    await expect(page.getByRole("row")).toHaveCount(2);
    await page.getByRole("button", { name: /Paused Site/ }).click();
    await expect(h1(page, "Paused Site")).toBeVisible();
    await expect(page.getByRole("switch").first()).toBeDisabled();
    await expect(press(page, "upstream.detail.resume")).toHaveCount(0);
    await tab(page, "upstream.detail.tabs.settings");
    await expect(press(page, "suppliers.unit.open")).toHaveCount(0);
    await expect(press(page, "suppliers.remove.open")).toHaveCount(0);
    await tab(page, "upstream.detail.tabs.models");
    await page.getByText(zhText("upstream.models.listed"), { exact: true }).click();
    await expect(page.getByText("gpt-test-mini").first()).toBeVisible();
    await expect(page.getByRole("button", { name: words("suppliers.test.title") })).toHaveCount(0);
  });
});

test.describe("names waiting to be mapped", () => {
  test("a name is mapped to a model from the queue, and leaves it", async ({ page }) => {
    // the old address of the queue lands on 上游's 待处理
    const state = await open(page, "/mappings");
    await expect(page).toHaveURL(/\/suppliers\?tab=inbox/);
    const before = state.pending.length;
    await expect(page.getByText("vendor-model-1-preview")).toBeVisible();
    await press(page, "upstream.inbox.item.unknown_model.act").click();
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("upstream.map.modelMissing"));
    await dialog(page).getByLabel(words("upstream.map.model")).fill("openai/model-1");
    await expect(dialog(page).getByText(/已选中/)).toBeVisible();
    await dialog(page).getByLabel(words("upstream.map.scope")).selectOption("supplier");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    expect(state.pending).toHaveLength(before - 1);
    await close(page);
    await expect(page.getByText("vendor-model-1-preview")).toHaveCount(0);
  });

  test("an ignored item leaves the queue until a later sync finds it again, and the count follows", async ({
    page,
  }) => {
    await open(page, "/suppliers?tab=inbox");
    await expect(page.getByText("60 件")).toBeVisible();
    await press(page, "upstream.inbox.ignore").click();
    await send(page, "known, the vendor renames it next week");
    await expect(page.getByText(zhText("upstream.inbox.ignored"))).toBeVisible();
    await expect(page.getByText("59 件")).toBeVisible();
    // the queue's own filter: a kind with nothing in it says so
    await page.getByRole("radio", { name: zhText("upstream.inbox.kinds.balance") }).check({ force: true });
    await expect(page.getByText(zhText("upstream.inbox.noneOfKind.title"))).toBeVisible();
  });
});
