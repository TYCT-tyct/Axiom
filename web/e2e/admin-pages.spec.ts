import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { uuid } from "./admin-data.ts";
import { acct } from "./admin-scenes.ts";
import { apps } from "./apps.ts";
import { knownPassword, mfaEmail, signedInAs, userId } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The admin's pages against the mocked API (admin-api.ts): the main flows of M1, each as an operator would walk it. The
// mock checks what the server would: the session's level, the permission, the CSRF token, the Origin, and for a change
// the Idempotency-Key and the reason, so a page that forgets one fails here.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

async function open(page: Page, path: string, data: "usual" | "empty" | "worst" = "usual") {
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
  test("an account from a server of the release before, without key limits, still shows with the card marked unavailable", async ({
    page,
  }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    const account = state.accounts.get(acct(1));
    if (account) {
      // The response an older node gives: the field is not there.
      (account as { key_limits?: unknown }).key_limits = undefined;
    }
    await page.goto(`/accounts/${acct(1)}`);
    await expect(h1(page, "user1@example.com")).toBeVisible();
    await expect(page.getByText("$12.00").first()).toBeVisible();
    await expect(page.getByText(zhText("settings.keyLimits.unavailable"))).toBeVisible();
    await expect(press(page, "settings.keyLimits.edit")).toHaveCount(0);
  });

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

  test("an account's key limits show where they come from, and a change sets the account's own over the site's", async ({
    page,
  }) => {
    const state = await open(page, `/accounts/${acct(1)}`);
    await expect(page.getByText(words("settings.keyLimits.from.account")).first()).toBeVisible();
    await press(page, "settings.keyLimits.edit").click();
    await dialog(page).getByLabel(words("settings.keyLimits.field.max_keys")).fill("0");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(
      zhText("settings.keyLimits.invalid").replace("{{min}}", "1").replace("{{max}}", "10000"),
    );
    await dialog(page).getByLabel(words("settings.keyLimits.field.max_keys")).fill("250");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    expect(state.accounts.get(acct(1))?.key_limits).toEqual({
      limits: { max_keys: 250 },
      effective: { max_keys: 250, creations_per_hour: 50 },
    });
    await close(page);
    await expect(page.getByText("250").first()).toBeVisible();
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

  test("a write that finds the session over goes to sign in, comes back, and asks with a new key", async ({ page }) => {
    const state = await open(page, `/accounts/${acct(1)}`);
    const keys: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "POST" && request.url().endsWith(`/accounts/${acct(1)}/credit-grants`)) {
        const key = request.headers()["idempotency-key"];
        if (key !== undefined) {
          keys.push(key);
        }
      }
    });
    await press(page, "users.credit.open").click();
    await dialog(page).getByLabel(words("users.credit.amount")).fill("2.5");
    // The session ends after the page opened: the write is refused as unauthenticated, and the page goes to sign in
    // with the way back, taking the dialog and the key it made with it.
    state.level = "none";
    await send(page);
    await expect(h1(page, "登录")).toBeVisible();
    expect(new URL(page.url()).searchParams.get("next")).toBe(`/accounts/${acct(1)}`);
    expect(keys).toHaveLength(1);

    // Signing in with a second factor returns to the page the person was on.
    state.mfaEnabled = true;
    await page.getByLabel("邮箱").fill(mfaEmail);
    await page.getByLabel("密码").fill(knownPassword);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.getByLabel("验证码或恢复码").fill("123456");
    await page.getByRole("button", { name: "验证" }).click();
    await expect(h1(page, "user1@example.com")).toBeVisible();
    expect(new URL(page.url()).pathname).toBe(`/accounts/${acct(1)}`);

    // A new dialog makes a new key: the first one went with the page that held it.
    await press(page, "users.credit.open").click();
    await dialog(page).getByLabel(words("users.credit.amount")).fill("2.5");
    await send(page);
    await expect(done(page)).toBeVisible();
    expect(keys).toHaveLength(2);
    expect(keys[0]).not.toBe(keys[1]);
    expect(state.stored.size).toBe(1);
  });

  test("an operator without the permission to grant sees the account but no way to add credit", async ({ page }) => {
    const state = await mockAdmin(page);
    state.permissions = ["users.view"];
    signedInAs(state, "mfa");
    await page.goto(`/accounts/${acct(1)}`);
    await expect(h1(page, "user1@example.com")).toBeVisible();
    await expect(press(page, "users.credit.open")).toHaveCount(0);
  });
});

test.describe("site settings", () => {
  test("a server of the release before has no key limits operation, and the settings still work", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    await page.route("**/api/admin/v1/sites/*/key-limits", (route) =>
      route.fulfill({ status: 404, json: { error: { code: "not_found", message: "no such resource" } } }),
    );
    await page.goto("/settings");
    await expect(page.getByText(/7\s*天/)).toBeVisible();
    await expect(page.getByText(zhText("settings.keyLimits.unavailable"))).toBeVisible();
    await expect(press(page, "settings.edit")).toBeVisible();
  });

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
    await expect(page.getByText(/7,200/)).toBeVisible();
  });

  test("the site's key limits are changed apart from the settings, and a settings change leaves them", async ({
    page,
  }) => {
    const state = await open(page, "/settings");
    await expect(page.getByText(words("settings.keyLimits.own")).first()).toBeVisible();
    await press(page, "settings.keyLimits.edit").click();
    await dialog(page).getByLabel(words("settings.keyLimits.field.creations_per_hour")).fill("30");
    await send(page);
    await expect(done(page)).toBeVisible();
    expect(state.keyLimits).toEqual({
      limits: { max_keys: 20, creations_per_hour: 30 },
      effective: { max_keys: 20, creations_per_hour: 30 },
    });
    await close(page);
    await press(page, "settings.edit").click();
    await send(page);
    await expect(done(page)).toBeVisible();
    expect(state.keyLimits.limits).toEqual({ max_keys: 20, creations_per_hour: 30 });
  });
});

test.describe("suppliers", () => {
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
    await expect(page.getByText("gpt-test-mini").first()).toBeVisible();
    await expect(page.getByRole("button", { name: words("suppliers.test.title") })).toHaveCount(0);
  });
});

test.describe("endpoint tests", () => {
  const verdict = (page: Page, outcome: string) =>
    dialog(page).getByText(zhText(`suppliers.test.outcome.${outcome}.title`), { exact: true });
  const testButtons = (page: Page) => page.getByRole("button", { name: words("suppliers.test.title") });

  // Every AbortSignal.timeout the page makes is held here and runs out only when the test says so: a limit is crossed
  // without waiting for it, whatever the machine's speed.
  type Held = { ms: number; controller: AbortController };
  const holdTimeouts = (page: Page) =>
    page.addInitScript(() => {
      const held: Held[] = [];
      Object.assign(window, { heldTimeouts: held });
      AbortSignal.timeout = (ms) => {
        const controller = new AbortController();
        held.push({ ms, controller });
        return controller.signal;
      };
    });
  const runOut = (page: Page, shorterThan: number) =>
    page.evaluate((limit) => {
      for (const { ms, controller } of (window as unknown as { heldTimeouts: Held[] }).heldTimeouts) {
        if (ms < limit) {
          controller.abort(new DOMException("signal timed out", "TimeoutError"));
        }
      }
    }, shorterThan);

  // The next test request is held at the server until release is called; arrived settles once it is there, and so
  // once the page has made the request's limit.
  async function holdTest(page: Page) {
    let release = () => {};
    let arrive = () => {};
    const arrived = new Promise<void>((resolve) => {
      arrive = resolve;
    });
    const released = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route(
      "**/suppliers/endpoint-tests",
      async (route) => {
        arrive();
        await released;
        await route.fallback();
      },
      { times: 1 },
    );
    return { arrived, release: () => release() };
  }

  test("a test outlasts the usual request limit, says it passed with its timings and IDs, and only callable endpoints offer one", async ({
    page,
  }) => {
    await holdTimeouts(page);
    const state = await open(page, `/suppliers/${uuid(2, 1)}`);
    await expect(page.getByText("unpriced-model")).toBeVisible();
    // six endpoints, of which the switched-off one and the one with no known cost cannot be called
    await expect(testButtons(page)).toHaveCount(4);
    await expect(page.getByText(zhText("suppliers.endpoints.state.off"))).toBeVisible();
    await expect(page.getByText(zhText("suppliers.endpoints.state.unknown"))).toBeVisible();

    await testButtons(page).nth(1).click();
    await expect(dialog(page).getByLabel(words("suppliers.test.format")).locator("option")).toHaveCount(3);
    await dialog(page).getByLabel(words("suppliers.test.format")).selectOption("chat");
    const held = await holdTest(page);
    await send(page, "check the endpoint");
    await held.arrived;
    // Every limit shorter than the server's minute for the operation runs out, the client's usual ten seconds among
    // them (packages/api): the test is still waited for, and nothing is resent.
    await runOut(page, 60_000);
    await expect(dialog(page).getByText(zhText("suppliers.test.waiting"))).toBeVisible();
    await expect(dialog(page).getByRole("alert")).toHaveCount(0);
    await expect(dialog(page).locator("button[type=submit]")).toBeDisabled();
    held.release();
    await expect(verdict(page, "passed")).toBeVisible();
    expect(state.log.filter((line) => line === "POST /suppliers/endpoint-tests")).toHaveLength(1);
    await expect(dialog(page).getByText(zhText("change.done"))).toHaveCount(0);
    await expect(dialog(page).getByText("820 毫秒")).toBeVisible();
    await expect(dialog(page).getByText("3.2 秒")).toBeVisible();
    await expect(dialog(page).getByText("chatcmpl-9fX2kQ")).toBeVisible();
    await expect(dialog(page).getByText(uuid(10, 1))).toBeVisible();
    await expect(dialog(page).getByRole("button", { name: zhText("suppliers.test.copyRequestId") })).toBeVisible();
    const [stored] = state.stored.values();
    expect(stored?.json).toMatchObject({ format: "chat", upstream_model: "gpt-test-mini" });
  });

  test("a test that outlasts its own limit is reported unknown and resent as it was", async ({ page }) => {
    await holdTimeouts(page);
    await open(page, `/suppliers/${uuid(2, 1)}`);
    await testButtons(page).nth(1).click();
    const held = await holdTest(page);
    await send(page);
    await held.arrived;
    await runOut(page, Number.POSITIVE_INFINITY);
    await expect(dialog(page).getByRole("alert")).toContainText(zhText("change.unknown"));
    await expect(dialog(page).locator("button[type=submit]")).toHaveText(zhText("change.resend"));
    held.release();
  });

  test("on a phone, two groups' endpoints of the same model are told apart before paying for one", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    const state = await open(page, `/suppliers/${uuid(2, 1)}`);
    const vip = page.getByRole("button", { name: "测试 gpt-test-mini（分组 vip-2）" });
    await expect(page.getByRole("button", { name: "测试 gpt-test-mini（分组 Maple）" })).toBeVisible();
    await expect(vip).toBeVisible();
    // the group column is hidden at this width; each row still names its group under the model
    const rows = page.getByRole("row").filter({ hasText: "gpt-test-mini" });
    await expect(rows.nth(0).getByText("Maple", { exact: true }).filter({ visible: true })).toBeVisible();
    await expect(rows.nth(1).getByText("vip-2", { exact: true }).filter({ visible: true })).toBeVisible();
    await vip.click();
    await expect(dialog(page).getByRole("heading", { name: "测试 gpt-test-mini（分组 vip-2）" })).toBeVisible();
    await send(page);
    await expect(verdict(page, "passed")).toBeVisible();
    const [detail] = state.suppliers;
    const chosen = detail?.endpoints.find(
      (e) => e.upstream_name === "gpt-test-mini" && e.group_id === detail.groups[1]?.id,
    );
    const [stored] = state.stored.values();
    expect(stored?.json).toMatchObject({ endpoint_id: chosen?.id });
  });

  test("a node of the release before endpoints were listed leaves the rest of the page working", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    // the shape an earlier node answers with: no endpoints member at all
    const detail: Partial<(typeof state.suppliers)[number]> | undefined = state.suppliers[0];
    delete detail?.endpoints;
    await page.goto(`/suppliers/${uuid(2, 1)}`);
    await expect(page.getByText(zhText("suppliers.endpoints.unavailable.empty.title"))).toBeVisible();
    await expect(page.getByRole("heading", { name: zhText("suppliers.groups.title") })).toBeVisible();
    await expect(testButtons(page)).toHaveCount(0);
  });

  test("a failed and an inconclusive test name the cause, and a lost answer is replayed, not paid twice", async ({
    page,
  }) => {
    const state = await open(page, `/suppliers/${uuid(2, 1)}`);
    await testButtons(page).nth(0).click();
    await send(page);
    await expect(verdict(page, "failed")).toBeVisible();
    await expect(dialog(page).getByText("问题出在端点：流中途断开。")).toBeVisible();
    await close(page);

    await testButtons(page).nth(3).click();
    state.dropReplies = 1;
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(/连不上/);
    await dialog(page).locator("button[type=submit]").click();
    await expect(verdict(page, "inconclusive")).toBeVisible();
    await expect(dialog(page).getByText("这次测试说明不了端点好坏：被限流（key）。")).toBeVisible();
    await expect(dialog(page).getByText(zhText("change.replayed"))).toBeVisible();
    // two tests, three requests: the resend carried the lost one's key and got its stored answer
    expect(state.log.filter((line) => line === "POST /suppliers/endpoint-tests")).toHaveLength(3);
    expect(state.stored.size).toBe(2);
  });

  test("a refusal says why nothing was paid for and what to check", async ({ page }) => {
    await open(page, `/suppliers/${uuid(2, 1)}`);
    await testButtons(page).nth(1).click();
    await page.route(
      "**/suppliers/endpoint-tests",
      (route) => route.fulfill({ status: 409, json: { error: { code: "conflict", message: "conflict" } } }),
      { times: 1 },
    );
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("suppliers.test.refused.conflict"));
    // refused before anything was done: the form is open again for another try
    await expect(dialog(page).getByLabel(words("suppliers.test.format"))).toBeEnabled();
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

test.describe("orders and refunds", () => {
  test("orders are filtered by state from the address, and the form writes the address", async ({ page }) => {
    await open(page, "/orders");
    const orders = page.getByRole("table", { name: zhText("nav.orders") });
    await expect(orders.getByRole("row")).toHaveCount(51);
    await page.getByLabel(zhText("orders.filters.state")).selectOption("pending");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    expect(new URL(page.url()).searchParams.get("state")).toBe("pending");
    // Twelve of the sixty usual orders wait for payment.
    await expect(orders.getByRole("row")).toHaveCount(13);
    await page.getByRole("button", { name: zhText("filter.reset") }).click();
    await expect(orders.getByRole("row")).toHaveCount(51);
  });

  test("a refund is approved by another person with a reason and leaves the queue", async ({ page }) => {
    const state = await open(page, "/orders");
    const queue = page.getByRole("table", { name: zhText("orders.refund.title") });
    await expect(queue.getByRole("row")).toHaveCount(4);
    await queue.getByRole("button", { name: zhText("orders.refund.approve") }).first().click();
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.reasonMissing"));
    await send(page, "checked the receipt");
    await expect(done(page)).toBeVisible();
    expect(state.refunds[0]?.state).toBe("succeeded");
    expect(state.refunds[0]?.approved_by).toBeDefined();
    await close(page);
    await expect(queue.getByRole("row")).toHaveCount(3);
  });

  test("shows what the channel took, not the quote, and says when an order is unpaid", async ({ page }) => {
    const state = await open(page, "/orders");
    const paid = state.orders[0];
    const unpaid = state.orders[1];
    const missing = state.orders[2];
    if (!paid || !unpaid || !missing) throw new Error("the usual orders are missing");
    paid.state = "paid";
    paid.total = { amount: "1000", currency: "USD" };
    paid.paid_total = { amount: "800", currency: "USD" };
    paid.paid_fee = { amount: "24", currency: "USD" };
    paid.credits = { amount: "7000000000", currency: "USD" };
    paid.credited = { amount: "6000000000", currency: "USD" };
    unpaid.state = "pending";
    delete unpaid.paid_total;
    delete unpaid.paid_fee;
    delete unpaid.credited;
    unpaid.total = { amount: "1000", currency: "USD" };
    unpaid.credits = { amount: "7000000000", currency: "USD" };
    missing.state = "paid";
    missing.total = { amount: "1000", currency: "USD" };
    missing.paid_total = { amount: "1000", currency: "USD" };
    delete missing.credited;
    missing.credits = { amount: "7000000000", currency: "USD" };
    await page.reload();

    const orders = page.getByRole("table", { name: zhText("nav.orders") });
    const paidRow = orders.getByRole("row").filter({ hasText: paid.id?.slice(-8) ?? "" });
    await expect(paidRow.getByText("US$8.00")).toBeVisible();
    await expect(paidRow.getByText("US$10.00")).toBeVisible();
    await expect(paidRow.getByText("US$0.24")).toBeVisible();
    const unpaidRow = orders.getByRole("row").filter({ hasText: unpaid.id?.slice(-8) ?? "" });
    await expect(unpaidRow.getByText(zhText("orders.notPaid"))).toBeVisible();
    await expect(unpaidRow.getByText("US$10.00")).toBeVisible();
    const missingRow = orders.getByRole("row").filter({ hasText: missing.id?.slice(-8) ?? "" });
    await expect(missingRow.getByText(zhText("orders.creditedUnavailable"))).toBeVisible();
    await expect(missingRow.getByText(zhText("orders.notCredited"))).toHaveCount(0);
  });

  test("the narrow-screen confirmation shows the refund facts", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    const state = await open(page, "/orders");
    const refund = state.refunds[0];
    if (!refund) throw new Error("the usual refunds are missing");
    const queue = page.getByRole("table", { name: zhText("orders.refund.title") });
    await queue.getByRole("button", { name: zhText("orders.refund.approve") }).first().click();
    await expect(dialog(page).getByText(zhText("orders.refund.review"))).toBeVisible();
    await expect(dialog(page).getByText(refund.account_id ?? "")).toBeVisible();
    await expect(dialog(page).getByText(refund.reason ?? "")).toBeVisible();
    await expect(dialog(page).getByText("US$5.00").first()).toBeVisible();
    await expect(dialog(page).getByText(refund.requested_by ?? "")).toBeVisible();
  });

  test("a person who asked for a refund does not approve it themselves", async ({ page }) => {
    const state = await open(page, "/orders");
    if (state.refunds[0]) {
      state.refunds[0].requested_by = userId(0);
    }
    await page.reload();
    const queue = page.getByRole("table", { name: zhText("orders.refund.title") });
    await expect(queue.getByText(zhText("orders.refund.secondPerson")).first()).toBeVisible();
    // The other two refunds still offer an approval.
    await expect(queue.getByRole("button", { name: zhText("orders.refund.approve") })).toHaveCount(2);
  });
});
