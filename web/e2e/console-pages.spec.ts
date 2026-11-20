import { expect, type Page, test } from "@playwright/test";
import { collectErrors } from "./collect-errors.ts";
import { mockConsole, signedInAs } from "./console-api.ts";
import { lastMonth } from "./console-calls-data.ts";
import { demoDocuments, goodInvite } from "./console-data.ts";
import { knownEmail } from "./mock-auth.ts";
import { advanceUntil } from "./page-clock.ts";

// The console's pages against the mocked API (console-api.ts): the main flows of M1, each as a user would walk it.

test.use({ timezoneId: "Asia/Shanghai" });

const main = (page: Page) => page.getByRole("main");

test.describe("accepting updated documents", () => {
  test("a signed-in member accepts before the shell opens", async ({ page }) => {
    const state = await mockConsole(page);
    state.legal = demoDocuments();
    signedInAs(state);
    await page.goto("/console/keys");
    await expect(page.getByRole("heading", { level: 1, name: "更新后的条款" })).toBeVisible();
    await expect(page.getByRole("region", { name: "服务条款" })).toBeVisible();
    const submit = page.getByRole("button", { name: "同意并继续" });
    await expect(submit).toBeDisabled();
    await page.getByLabel("我已阅读并同意上述服务条款和隐私政策。").check();
    await expect(submit).toBeEnabled();
    await submit.click();
    await expect(page.getByRole("heading", { level: 1, name: "API Keys" })).toBeVisible();
  });

  test("an old node's missing locale stays missing in the acceptance", async ({ page }) => {
    const state = await mockConsole(page);
    const documents = demoDocuments();
    state.legal = documents;
    signedInAs(state);
    const legacy = documents.map(({ locale: _locale, ...document }) => document);
    await page.route("**/api/console/v1/legal*", (route) =>
      route.request().method() === "GET" ? route.fulfill({ json: { documents: legacy } }) : route.fallback(),
    );
    await page.goto("/console/keys");
    await expect(page.getByRole("heading", { level: 1, name: "更新后的条款" })).toBeVisible();
    await page.getByLabel("我已阅读并同意上述服务条款和隐私政策。").check();
    const accepted = page.waitForRequest(
      (request) => request.method() === "POST" && request.url().endsWith("/api/console/v1/legal/accept"),
    );
    await page.getByRole("button", { name: "同意并继续" }).click();
    expect((await accepted).postDataJSON()).toEqual({
      consent: [
        { kind: "terms", version: 3 },
        { kind: "privacy", version: 2 },
      ],
    });
    await expect(page.getByRole("heading", { level: 1, name: "API Keys" })).toBeVisible();
  });

  test("English accepts the newest shared version, not a lower Chinese one", async ({ page }) => {
    const state = await mockConsole(page);
    state.legal = demoDocuments().map((document) =>
      document.kind === "terms" ? { ...document, version: document.locale === "en" ? 2 : 1 } : document,
    );
    signedInAs(state);
    await page.addInitScript(() => localStorage.setItem("axiom.language", "en"));
    await page.goto("/console/keys");
    await expect(page.getByRole("heading", { level: 1, name: "Updated terms" })).toBeVisible();
    await page.getByLabel("I have read and agree to the terms of service and privacy policy above.").check();
    const accepted = page.waitForRequest(
      (request) => request.method() === "POST" && request.url().endsWith("/api/console/v1/legal/accept"),
    );
    await page.getByRole("button", { name: "Accept and continue" }).click();
    expect((await accepted).postDataJSON()).toEqual({
      consent: [
        { kind: "terms", locale: "en", version: 2 },
        { kind: "privacy", locale: "zh", version: 2 },
      ],
    });
    await expect(page.getByRole("heading", { level: 1, name: "API Keys" })).toBeVisible();
  });

  // At 360 a long address in the page's words breaks rather than push the page wider than the screen.
  test("a long email in the consent page's words stays inside a phone's width", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 780 });
    const state = await mockConsole(page);
    state.legal = demoDocuments();
    state.user.email = "averyveryveryverylonglocalpartwithnoplacetobreakatallreally@exampledomainthatisalsolong.com";
    signedInAs(state);
    await page.goto("/console/keys");
    const words = page.getByText(state.user.email, { exact: false });
    await expect(words).toBeVisible();
    // the text's own lines, which can run past the paragraph's box
    const right = await words.evaluate((el) => {
      const text = document.createRange();
      text.selectNodeContents(el);
      return Math.max(...[...text.getClientRects()].map((r) => r.right));
    });
    expect(right).toBeLessThanOrEqual(360);
  });

  test("a member can leave the consent page for another account without accepting", async ({ page }) => {
    const state = await mockConsole(page);
    state.legal = demoDocuments();
    signedInAs(state);
    await page.goto("/console/keys");
    await expect(page.getByRole("heading", { level: 1, name: "更新后的条款" })).toBeVisible();
    await expect(page.getByRole("button", { name: "同意并继续" })).toBeDisabled();
    await page.getByRole("button", { name: "换一个账户登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
  });

  test("a member can leave the consent page when the documents fail to load", async ({ page }) => {
    const state = await mockConsole(page);
    state.legal = demoDocuments();
    signedInAs(state);
    await page.route("**/api/console/v1/legal*", (route) =>
      route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } }),
    );
    await page.goto("/console/keys");
    await expect(page.getByRole("heading", { level: 1, name: "更新后的条款" })).toBeVisible();
    await expect(page.getByRole("alert")).toBeVisible();
    await page.getByRole("button", { name: "换一个账户登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
  });
});

test.describe("registering", () => {
  test("an invite code from the link registers and signs in", async ({ page }) => {
    await mockConsole(page);
    await page.goto(`/console/register?invite=${goodInvite}`);
    await expect(page.getByLabel("邀请码")).toHaveValue(goodInvite);
    await page.getByLabel("邮箱").fill("new@example.com");
    await page.getByLabel("密码").fill("a long password nobody guesses");
    await page.getByRole("button", { name: "注册", exact: true }).click();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("a bad code and a taken email say what is wrong", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/register");
    await page.getByLabel("邀请码").fill("nope");
    await page.getByLabel("邮箱").fill("new@example.com");
    await page.getByLabel("密码").fill("a long password nobody guesses");
    await page.getByRole("button", { name: "注册", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText(/邀请码无效/);
    await page.getByLabel("邀请码").fill(goodInvite);
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "注册", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText(/已经注册过/);
  });

  test("the registration form shows the published documents and needs this version accepted", async ({ page }) => {
    const state = await mockConsole(page);
    state.legal = demoDocuments();
    await page.goto(`/console/register?invite=${goodInvite}`);
    await expect(page.getByRole("region", { name: "服务条款" })).toBeVisible();
    await expect(page.getByRole("region", { name: "隐私政策" })).toBeVisible();
    await expect(page.getByRole("heading", { level: 2, name: "服务条款" })).toBeVisible();
    await expect(page.getByRole("heading", { level: 2, name: "隐私政策" })).toBeVisible();
    await expect(page.getByText("第 3 版")).toBeVisible();
    await expect(page.getByText("第 2 版")).toBeVisible();
    await expect(page.getByText("【示例文本（demo）").first()).toBeVisible();
    await expect(page.getByText("## 服务说明")).toHaveCount(0);
    const submit = page.getByRole("button", { name: "注册", exact: true });
    await expect(submit).toBeDisabled();
    await page.getByLabel("邮箱").fill("new@example.com");
    await page.getByLabel("密码").fill("a long password nobody guesses");
    await page.getByLabel("我已阅读并同意上述服务条款和隐私政策。").check();
    await expect(submit).toBeEnabled();
    await submit.click();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("a site that does not take registration says so instead of showing a form", async ({ page }) => {
    const state = await mockConsole(page);
    state.site.registration = "closed";
    await page.goto("/console/register");
    await expect(page.getByText("本站目前不开放注册")).toBeVisible();
    await expect(page.getByLabel("邀请码")).toHaveCount(0);
  });
});

test.describe("API keys", () => {
  test("a new key shows its text once, in a dialog that stays until the user is done", async ({ page }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/keys");
    await expect(page.getByText("Laptop")).toBeVisible();

    await page.getByRole("button", { name: "创建 key" }).click();
    await page.getByRole("dialog", { name: "创建 key" }).getByLabel("名称").fill("Deploy bot");
    await page.getByLabel(/花费上限/).fill("2.5");
    await page.getByLabel("可用模型").fill("openai/gpt-5\nanthropic/claude-sonnet-4");
    await page.getByRole("button", { name: "创建", exact: true }).click();

    const secret = page.getByRole("dialog", { name: "复制你的 key" });
    await expect(secret).toBeVisible();
    const text = (await secret.locator("code").textContent()) ?? "";
    expect(text).toMatch(/^sk-ax-v1-x{45}\d{4}$/);
    // The guide sends every client to the site's API domain, taken from its own site config, and carries the key while
    // this dialog is open: three request formats, then the three clients the docs name.
    const snippets = secret.locator("pre");
    await expect(snippets).toHaveCount(6);
    await expect(snippets.nth(0)).toContainText("https://api.example.com/v1/chat/completions");
    await expect(snippets.nth(1)).toContainText("https://api.example.com/v1/responses");
    await expect(snippets.nth(2)).toContainText("https://api.example.com/v1/messages");
    await expect(snippets.nth(3)).toContainText("export ANTHROPIC_BASE_URL=https://api.example.com");
    await expect(snippets.nth(3)).toContainText(`export ANTHROPIC_AUTH_TOKEN=${text}`);
    await expect(snippets.nth(4)).toContainText('base_url = "https://api.example.com/v1"');
    await expect(snippets.nth(4)).toContainText('env_key = "AXIOM_API_KEY"');
    await expect(snippets.nth(5)).toHaveText("https://api.example.com/v1");
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    const copyNames = [
      "OpenAI Chat Completions",
      "OpenAI Responses",
      "Anthropic Messages",
      "Claude Code",
      "Codex",
      "Cursor",
    ];
    for (const [index, name] of copyNames.entries()) {
      const copy = secret.getByRole("button", { name: `复制 ${name}`, exact: true });
      await copy.focus();
      await copy.press(index % 2 === 0 ? "Enter" : "Space");
      const expected = (await snippets.nth(index).textContent()) ?? "";
      await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(expected);
    }
    // Escape and a click outside do not close it; the user is the one to say the key is saved.
    await page.keyboard.press("Escape");
    await expect(secret).toBeVisible();
    await page.mouse.click(5, 5);
    await expect(secret).toBeVisible();
    await secret.getByRole("button", { name: "我已保存" }).click();
    await expect(secret).toHaveCount(0);

    await expect(page.getByText("Deploy bot")).toBeVisible();
    await expect(main(page)).not.toContainText(text);
    expect(state.keys[0]).toMatchObject({
      name: "Deploy bot",
      spend_limit: { amount: "2500000000", currency: "USD" },
      models: ["openai/gpt-5", "anthropic/claude-sonnet-4"],
    });
    expect(errors).toEqual([]);
  });

  test("a key is edited with all its settings sent back, and can be switched off", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/keys");
    // A key opens from its row in the list, as the board draws it.
    await page.getByRole("row", { name: /^Laptop / }).click();
    const dialog = page.getByRole("dialog", { name: "修改 key" });
    await expect(dialog.getByLabel(/花费上限/)).toHaveValue("20");
    await dialog.getByLabel("名称").fill("Laptop (work)");
    await dialog.getByLabel("停用这个 key").check();
    await dialog.getByRole("button", { name: "保存" }).click();
    await expect(dialog).toHaveCount(0);

    // The row itself is the control the board draws; a key that is off says so under its name.
    const row = page.getByRole("row", { name: /^Laptop \(work\)/ });
    await expect(row.getByText("已停用")).toBeVisible();
    expect(state.keys.find((key) => key.name === "Laptop (work)")).toMatchObject({
      disabled: true,
      spend_limit: { amount: "20000000000" },
      limit_reset: "monthly",
    });
  });

  test("a key without a limit can be given one, and a limit that was cleared can be set again", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/keys");
    const limitOf = (name: string) => state.keys.find((key) => key.name === name)?.spend_limit;
    async function editLimit(name: string, amount: string) {
      await page.getByRole("row", { name: new RegExp(`^${name} `) }).click();
      const dialog = page.getByRole("dialog", { name: "修改 key" });
      const limit = dialog.getByLabel("花费上限（USD）");
      await expect(limit).toBeEnabled();
      await limit.fill(amount);
      await dialog.getByRole("button", { name: "保存" }).click();
      await expect(dialog).toHaveCount(0);
    }

    await editLimit("CI runner", "5");
    expect(limitOf("CI runner")).toEqual({ amount: "5000000000", currency: "USD" });
    await editLimit("Laptop", "");
    expect(limitOf("Laptop")).toBeUndefined();
    await editLimit("Laptop", "7.5");
    expect(limitOf("Laptop")).toEqual({ amount: "7500000000", currency: "USD" });
  });

  test("a limit cannot be set while the account's currency is unknown, and the form says why", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.route("**/api/console/v1/balance", (route) => route.fulfill({ status: 403 }));
    await page.goto("/console/keys");
    await page.getByRole("row", { name: /^CI runner / }).click();
    const dialog = page.getByRole("dialog", { name: "修改 key" });
    await expect(dialog.getByText("没能取得账户的币种")).toBeVisible();
    await expect(dialog.getByLabel(/花费上限/)).toBeDisabled();
    // The rest of the key can still be changed.
    await dialog.getByLabel("名称").fill("CI runner 2");
    await dialog.getByRole("button", { name: "保存" }).click();
    await expect(dialog).toHaveCount(0);
    expect(state.keys.map((key) => key.name)).toContain("CI runner 2");
  });

  test("a key is deleted only after the user confirms", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/keys");
    await page.getByRole("row", { name: /^CI runner / }).click();
    await page.getByRole("dialog", { name: "修改 key" }).getByRole("button", { name: "删除 CI runner" }).click();
    const dialog = page.getByRole("dialog", { name: "删除 key" });
    await dialog.getByRole("button", { name: "取消" }).click();
    expect(state.keys).toHaveLength(3);

    await page.getByRole("row", { name: /^CI runner / }).click();
    await page.getByRole("dialog", { name: "修改 key" }).getByRole("button", { name: "删除 CI runner" }).click();
    await page.getByRole("dialog", { name: "删除 key" }).getByRole("button", { name: "删除" }).click();
    await expect(page.getByText("CI runner")).toHaveCount(0);
    expect(state.keys.map((key) => key.name)).toEqual(["Laptop", "Old key"]);
  });

  test("an account that may only view keys sees no way to change them", async ({ page }) => {
    const state = await mockConsole(page, "usual", { permissions: ["api_keys.view", "balance.view"] });
    signedInAs(state);
    await page.goto("/console/keys");
    await expect(page.getByText("Laptop")).toBeVisible();
    await expect(page.getByRole("button", { name: "创建 key" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^修改/ })).toHaveCount(0);
    // The sidebar leaves out what the account cannot open.
    await expect(page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "日志" })).toHaveCount(0);
  });

  test("an empty list invites creating the first key, in English too", async ({ page }) => {
    const state = await mockConsole(page, "empty");
    signedInAs(state);
    await page.addInitScript(() => localStorage.setItem("axiom.language", "en"));
    await page.goto("/console/keys");
    await expect(page.getByRole("heading", { name: "No keys yet" })).toBeVisible();
    await page.getByRole("button", { name: "Create key" }).click();
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await expect(page.getByText("Give the key a name.")).toBeVisible();
  });

  test("an account without the permission is told so, and a failure can be retried", async ({ page }) => {
    const state = await mockConsole(page, "usual", { permissions: [] });
    signedInAs(state);
    await page.goto("/console/keys");
    await expect(page.getByRole("heading", { name: "没有权限" })).toBeVisible();
    state.permissions = ["api_keys.view"];
    await page.getByRole("link", { name: "概览" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });
});

test.describe("balance", () => {
  test("the overview and the balance page show the figures exactly", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/");
    await expect(page.locator("#main").getByText("US$11.75")).toBeVisible();
    await expect(page.getByText("US$3.141592653")).toBeVisible();
    await page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "账单与充值" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "账单与充值" })).toBeVisible();
    await expect(page.getByText("US$12.00", { exact: true })).toBeVisible();
    await expect(page.getByText("US$0.25", { exact: true })).toBeVisible();
  });
});

// segmentsByDay reads the overview's daily chart: each day's bar and how many colours it is stacked in.
const segmentsByDay = (page: Page) =>
  main(page)
    .locator(".qch2 svg rect")
    .evaluateAll((rects) => {
      const days = new Map<string, Set<string>>();
      for (const rect of rects as SVGRectElement[]) {
        const x = rect.getAttribute("x") ?? "";
        days.set(x, (days.get(x) ?? new Set()).add(rect.style.fill));
      }
      return [...days].map(([x, fills]) => ({ x, fills: fills.size }));
    });

test.describe("overview", () => {
  test("compares the month's spend with the same days of last month and splits each day by model", async ({ page }) => {
    const state = await mockConsole(page);
    state.calls = [...state.calls, ...lastMonth(state.calls, 23)];
    signedInAs(state);
    await page.clock.install({ time: new Date("2026-10-24T10:00:00+08:00") });
    await page.goto("/console/");
    await expect(main(page).getByText("比 9 月同期 +23%")).toBeVisible();
    // the day the account called four models on is one bar in their colours
    await expect.poll(async () => Math.max(...(await segmentsByDay(page)).map((day) => day.fills))).toBeGreaterThan(1);
  });

  test("a failed refresh of the rollups leaves the chart in one colour and the spend without a comparison", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.calls = [...state.calls, ...lastMonth(state.calls, 23)];
    signedInAs(state);
    await page.clock.install({ time: new Date("2026-10-24T10:00:00+08:00") });
    await page.goto("/console/");
    const note = main(page).locator(".qkpis .qkpi:first-child > em");
    await expect(note).toHaveText("比 9 月同期 +23%");
    await page.route("**/api/console/v1/usage/statistics*", (route) =>
      route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } }),
    );
    // The same window, refetched once the answers are stale: the old answers are kept beside the failure.
    await page.clock.fastForward(60_000);
    await page.evaluate(() => window.dispatchEvent(new Event("visibilitychange")));
    await advanceUntil(page, async () => {
      await expect(note).toHaveText("2026年10月", { timeout: 100 });
    });
    const days = await segmentsByDay(page);
    expect(days.length).toBeGreaterThan(0);
    expect(days.every((day) => day.fills === 1)).toBe(true);
  });

  test("an error's time turns into its date at local midnight, with nothing fetched again", async ({ page }) => {
    const state = await mockConsole(page);
    const failed = state.calls.find((call) => call.error_kind);
    if (!failed) throw new Error("the usual account has no failed call");
    state.calls = [{ ...failed, at: "2026-10-24T15:50:00Z" }];
    signedInAs(state);
    await page.clock.install({ time: new Date("2026-10-24T23:58:00+08:00") });
    await page.goto("/console/");
    const row = main(page).locator(".qrq:not(.qrqh)").first();
    await expect(row).toContainText("23:50");
    const requests: string[] = [];
    page.on("request", (request) => requests.push(request.url()));
    await page.clock.runFor(3 * 60_000);
    await expect(row).toContainText("10/24");
    await expect(row).not.toContainText("23:50");
    expect(requests.filter((url) => url.includes("/api/"))).toEqual([]);
  });
});

test("the site's brand colour is applied to the page", async ({ page }) => {
  const state = await mockConsole(page);
  state.site = { ...state.site, brand_color: "#335ddf" };
  await page.goto("/console/sign-in");
  await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
  const button = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--btn").trim());
  expect(button).toBe("light-dark(#335ddf, #335ddf)");
});
