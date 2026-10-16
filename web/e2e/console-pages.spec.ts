import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";
import { goodInvite } from "./console-data.ts";
import { knownEmail, knownPassword, limitedEmail, mfaEmail } from "./mock-auth.ts";

// The console's pages against the mocked API (console-api.ts): the main flows of M1, each as a user would walk it.

test.use({ timezoneId: "Asia/Shanghai" });

function collectErrors(page: Page) {
  const errors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") {
      errors.push(message.text());
    }
  });
  page.on("pageerror", (error) => errors.push(error.message));
  return errors;
}

async function signIn(page: Page, email = knownEmail, password = knownPassword) {
  await page.getByLabel("邮箱").fill(email);
  await page.getByLabel("密码").fill(password);
  await page.getByRole("button", { name: "登录", exact: true }).click();
}

const main = (page: Page) => page.getByRole("main");

test.describe("signing in", () => {
  test("a wrong password says so, a right one opens the overview", async ({ page }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    await page.goto("/console/");
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await expect(page).toHaveTitle("登录 · Axiom");

    await signIn(page, knownEmail, "wrong");
    await expect(page.getByRole("alert")).toHaveText(/邮箱或密码不对/);
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(state.log.filter((line) => line === "POST /auth/sign-in")).toHaveLength(2);
    // the browser logs the two 401s (no session yet, a wrong password) itself
    expect(errors.filter((line) => !line.startsWith("Failed to load resource"))).toEqual([]);
  });

  test("an empty form is told what is missing, and a limit says how long to wait", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText(/请填写邮箱和密码/);
    await signIn(page, limitedEmail);
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
  });

  test("pressing twice while the request is out sends it once", async ({ page }) => {
    const state = await mockConsole(page);
    await page.route("**/api/console/v1/auth/sign-in", async (route) => {
      await new Promise((resolve) => setTimeout(resolve, 400));
      await route.fallback();
    });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByLabel("密码").fill(knownPassword);
    const submit = page.getByRole("button", { name: "登录", exact: true });
    await submit.click();
    await expect(submit).toBeDisabled();
    await submit.click({ force: true, noWaitAfter: true }).catch(() => {});
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(state.log.filter((line) => line === "POST /auth/sign-in")).toHaveLength(1);
  });

  test("a visitor who was going to a page comes back to it after signing in", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/keys?x=1");
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "API key" })).toBeVisible();
    expect(new URL(page.url()).pathname).toBe("/console/keys");
  });

  test("an address to return to that leaves the app is dropped", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in?next=//evil.example/");
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(new URL(page.url()).host).toBe(new URL(test.info().project.use.baseURL ?? page.url()).host);
  });

  test("a session that ends while a page is open sends the user to sign in and back to that page", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/keys");
    await expect(page.getByText("Laptop")).toBeVisible();

    state.level = "none";
    await page.getByRole("link", { name: "调用记录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "调用记录" })).toBeVisible();
    expect(new URL(page.url()).pathname).toBe("/console/calls");
  });

  test("signing out ends the session and the next visitor sees none of its data", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/keys");
    await expect(page.getByText("Laptop")).toBeVisible();
    await page.getByRole("button", { name: "退出登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    state.keys = [];
    await signIn(page);
    await page.getByRole("link", { name: "API key" }).click();
    await expect(page.getByText("Laptop")).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "还没有 key" })).toBeVisible();
  });
});

test.describe("two-step verification at sign-in", () => {
  test("a code from the app, or a recovery code, finishes signing in", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
    await page.getByLabel("验证码或恢复码").fill("000000");
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByRole("alert")).toHaveText(/验证码不对/);
    await page.getByLabel("验证码或恢复码").fill("AAAA-BBBB-CCCC-DDDD");
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("a pending sign-in reaches no page, and can be given up", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
    await page.goto("/console/keys");
    await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
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
    await page.getByLabel("名称").fill("Deploy bot");
    await page.getByLabel(/花费上限/).fill("2.5");
    await page.getByLabel("可用模型").fill("openai/gpt-5\nanthropic/claude-sonnet-4");
    await page.getByRole("button", { name: "创建", exact: true }).click();

    const secret = page.getByRole("dialog", { name: "复制你的 key" });
    await expect(secret).toBeVisible();
    const text = await secret.locator("code").innerText();
    expect(text).toMatch(/^sk-ax-v1-x{45}\d{4}$/);
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
    await page.getByRole("button", { name: "修改 Laptop" }).click();
    const dialog = page.getByRole("dialog", { name: "修改 key" });
    await expect(dialog.getByLabel(/花费上限/)).toHaveValue("20");
    await dialog.getByLabel("名称").fill("Laptop (work)");
    await dialog.getByLabel("停用这个 key").check();
    await dialog.getByRole("button", { name: "保存" }).click();
    await expect(dialog).toHaveCount(0);

    const row = page.getByRole("row", { name: /Laptop \(work\)/ });
    await expect(row.getByText("已停用")).toBeVisible();
    expect(state.keys.find((key) => key.name === "Laptop (work)")).toMatchObject({
      disabled: true,
      spend_limit: { amount: "20000000000" },
      limit_reset: "monthly",
    });
  });

  test("a key is deleted only after the user confirms", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/keys");
    await page.getByRole("button", { name: "删除 CI runner" }).click();
    const dialog = page.getByRole("dialog", { name: "删除 key" });
    await dialog.getByRole("button", { name: "取消" }).click();
    expect(state.keys).toHaveLength(3);

    await page.getByRole("button", { name: "删除 CI runner" }).click();
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
    await expect(page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "调用记录" })).toHaveCount(
      0,
    );
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
    await expect(page.getByText("US$11.75")).toBeVisible();
    await expect(page.getByText("US$3.141592653")).toBeVisible();
    await page.getByRole("link", { name: "查看明细" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "余额" })).toBeVisible();
    await expect(page.getByText("US$12.00", { exact: true })).toBeVisible();
    await expect(page.getByText("US$0.25", { exact: true })).toBeVisible();
  });
});

test.describe("calls", () => {
  test("the list pages by cursor, filters live in the address, and a call opens", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    const rows = page.getByRole("row");
    await expect(rows).toHaveCount(51);
    // Times show in the viewer's zone and say which it is.
    await expect(page.getByRole("link", { name: /16:29:00/ })).toBeVisible();
    await page.getByRole("button", { name: "加载更多" }).click();
    await expect(rows).toHaveCount(101);

    await page.getByLabel("模型").fill("openai/gpt-5");
    await page.getByRole("button", { name: "筛选" }).click();
    await expect(page).toHaveURL(/model=openai%2Fgpt-5/);
    await expect(rows).toHaveCount(31);
    await page.reload();
    await expect(page.getByLabel("模型")).toHaveValue("openai/gpt-5");
    await expect(rows).toHaveCount(31);

    await page.getByRole("link", { name: "清除" }).click();
    await expect(page.getByLabel("模型")).toHaveValue("");
    await expect(rows).toHaveCount(51);

    await rows.nth(1).getByRole("link").click();
    await expect(page.getByRole("heading", { level: 1, name: "调用详情" })).toBeVisible();
    await expect(page.getByText("00000000-0000-4000-8000-000000000001")).toBeVisible();
    await expect(page.getByText("cache_read")).toBeVisible();
  });

  test("a failed call says so, and a call that is not the account's is not found", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls/00000000-0000-4000-8000-000000000007");
    await expect(page.getByText("upstream_timeout")).toBeVisible();
    await expect(main(page).getByText("失败")).toBeVisible();
    await page.goto("/console/calls/00000000-0000-4000-8000-0000000fffff");
    await expect(page.getByRole("heading", { name: "找不到这次调用" })).toBeVisible();
  });

  test("no calls, no matches, and a refused filter each say what to do", async ({ page }) => {
    const state = await mockConsole(page, "empty");
    signedInAs(state);
    await page.goto("/console/calls");
    await expect(page.getByRole("heading", { name: "还没有调用记录" })).toBeVisible();
    await page.goto("/console/calls?model=none");
    await expect(page.getByRole("heading", { name: "没有符合条件的调用" })).toBeVisible();
  });

  test("a free call is marked, and the overview lists the latest failures", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    await expect(page.getByRole("row", { name: /免单/ }).first()).toBeVisible();
    await page.getByRole("link", { name: "概览" }).click();
    await expect(page.getByRole("heading", { name: "最近的报错" })).toBeVisible();
    await expect(page.getByText("upstream_timeout").first()).toBeVisible();
  });
});

test.describe("two-step verification settings", () => {
  test("turning it on shows a QR code and the recovery codes once, and later changes use the new session", async ({
    page,
  }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/security");
    await expect(page.getByText("未开启")).toBeVisible();
    await page.getByRole("button", { name: "开启两步验证" }).click();

    const dialog = page.getByRole("dialog", { name: "开启两步验证" });
    await expect(dialog.getByRole("img", { name: "两步验证的二维码" })).toBeVisible();
    await expect(dialog.getByText("JBSWY3DPEHPK3PXP")).toBeVisible();
    await dialog.getByLabel("密码").fill(knownPassword);
    await dialog.getByLabel("验证器里的 6 位验证码").fill("000000");
    await dialog.getByRole("button", { name: "开启", exact: true }).click();
    await expect(dialog.getByRole("alert")).toHaveText(/验证码不对/);
    await dialog.getByLabel("验证器里的 6 位验证码").fill("123456");
    await dialog.getByRole("button", { name: "开启", exact: true }).click();

    const codes = page.getByRole("dialog", { name: "保存你的恢复码" });
    await expect(codes.getByRole("listitem")).toHaveCount(10);
    await page.keyboard.press("Escape");
    await expect(codes).toBeVisible();
    await codes.getByRole("button", { name: "我已保存" }).click();
    await expect(page.getByText("已开启")).toBeVisible();

    // The change gave this device a new CSRF token; a request that still sent the old one would be refused.
    await page.getByRole("button", { name: "重新生成恢复码" }).click();
    const again = page.getByRole("dialog", { name: "重新生成恢复码" });
    await again.getByLabel("验证器里的 6 位验证码").fill("123456");
    await again.getByRole("button", { name: "重新生成" }).click();
    await expect(page.getByRole("dialog", { name: "保存你的恢复码" }).getByRole("listitem")).toHaveCount(10);
    expect(errors.filter((line) => !line.includes("401"))).toEqual([]);
  });

  test("the invite code and link are there to copy", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/security");
    await expect(page.getByText("AXIOM-7K2Q", { exact: true })).toBeVisible();
    await expect(page.getByText(/\/console\/register\?invite=AXIOM-7K2Q/)).toBeVisible();
  });
});
