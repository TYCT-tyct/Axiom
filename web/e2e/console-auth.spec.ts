import { expect, type Page, test } from "@playwright/test";
import { contrast } from "../packages/ui/src/brand.ts";
import { collectErrors } from "./collect-errors.ts";
import { failOnce, mockConsole, signedInAs } from "./console-api.ts";
import { demoDocuments } from "./console-data.ts";
import { knownEmail, knownPassword, limitedEmail, mfaEmail } from "./mock-auth.ts";

// Signing in, session changes and two-step verification against the mocked API.

test.use({ timezoneId: "Asia/Shanghai" });

// The page asks for the email, then the password, and sends both in one sign-in.
async function signIn(page: Page, email = knownEmail, password = knownPassword) {
  await page.getByLabel("邮箱").fill(email);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "登录", exact: true }).click();
}

// What a page does when its window comes back to the front, which is when it asks again for the session.
const returnToWindow = (page: Page) =>
  page.evaluate(() => document.dispatchEvent(new Event("visibilitychange", { bubbles: true })));

test.describe("signing in", () => {
  test("a wrong password says so, a right one opens the overview", async ({ page }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    await page.goto("/console/");
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await expect(page).toHaveTitle("登录 · Axiom");

    await signIn(page, knownEmail, "wrong");
    await expect(page.getByRole("alert")).toHaveText(/邮箱或密码不对/);
    await page.getByLabel("密码", { exact: true }).fill(knownPassword);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(state.log.filter((line) => line === "POST /auth/sign-in")).toHaveLength(2);
    // the browser logs the two 401s (no session yet, a wrong password) itself
    expect(errors.filter((line) => !line.startsWith("Failed to load resource"))).toEqual([]);
  });

  test("an empty form is told what is missing, and a limit says how long to wait", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText(/请填写邮箱/);
    await page.getByLabel("邮箱").fill(limitedEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText(/请填写密码/);
    await page.getByLabel("密码", { exact: true }).fill(knownPassword);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
  });

  test("pressing twice while the request is out sends it once", async ({ page }) => {
    const state = await mockConsole(page);
    // The request is held until both presses are made, not for a fixed time: on a loaded runner the presses can take
    // longer than any delay, and the page would have answered and signed in before the second one.
    let answer!: () => void;
    const held = new Promise<void>((resolve) => (answer = resolve));
    await page.route("**/api/console/v1/auth/sign-in", async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await page.getByLabel("密码", { exact: true }).fill(knownPassword);
    const submit = page.getByRole("button", { name: "登录", exact: true });
    await submit.click();
    await expect(submit).toBeDisabled();
    await submit.click({ force: true, noWaitAfter: true }).catch(() => {});
    answer();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(state.log.filter((line) => line === "POST /auth/sign-in")).toHaveLength(1);
  });

  test("a visitor who was going to a page comes back to it after signing in", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/keys?x=1");
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "API Keys" })).toBeVisible();
    expect(new URL(page.url()).pathname).toBe("/console/keys");
  });

  test("a visitor whose page is still loading when the session answers is sent to sign in once", async ({ page }) => {
    await mockConsole(page);
    // The page's code is held until the sign-in page shows, so the session answers before the router has shown its
    // first page, as on a busy machine. A gate that read the address its own redirect made would redirect again,
    // forever, and the page would never answer.
    let release!: () => void;
    const held = new Promise<void>((resolve) => (release = resolve));
    await page.route("**/console/assets/keys-*.js", async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto("/console/keys", { waitUntil: "commit" });
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    expect(new URL(page.url()).search).toBe("?next=%2Fkeys");
    release();
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "API Keys" })).toBeVisible();
  });

  test("an address to return to that leaves the app is dropped", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in?next=//evil.example/");
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(new URL(page.url()).host).toBe(new URL(test.info().project.use.baseURL ?? page.url()).host);
  });

  test("a protected page's own next is part of its address, not the way back", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/keys?x=1&next=/calls");
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "API Keys" })).toBeVisible();
    const back = new URL(page.url());
    expect(back.pathname).toBe("/console/keys");
    expect([back.searchParams.get("x"), back.searchParams.get("next")]).toEqual(["1", "/calls"]);
  });

  for (const next of [
    "%2F..%2Foutside",
    "%2F%252e%252e%2Foutside",
    "%2Fa%2F..%2F..%2Foutside",
    "%2F..%2F%2Fevil.example",
  ]) {
    test(`a way back that climbs out of the app (${next}) is dropped`, async ({ page }) => {
      await mockConsole(page);
      await page.goto(`/console/sign-in?next=${next}`);
      await signIn(page);
      await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
      expect(new URL(page.url()).pathname).toBe("/console/");
    });
  }

  test("a session that ends while a page is open sends the user to sign in and back to that page", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/keys");
    await expect(page.getByText("Laptop")).toBeVisible();

    state.level = "none";
    await page.getByRole("link", { name: "日志" }).click();
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
    // Signing out is in the account menu.
    await page.getByRole("button", { name: "账号设置" }).click();
    await page.getByRole("button", { name: "退出登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    state.keys = [];
    await signIn(page);
    await page.getByRole("link", { name: "API Keys" }).click();
    await expect(page.getByText("Laptop")).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "还没有 key" })).toBeVisible();
  });

  test("a session that passes to another account shows none of what the first one loaded", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/keys");
    await expect(page.getByText("Laptop")).toBeVisible();
    await page.getByRole("button", { name: "创建 key" }).click();
    await page.getByRole("dialog", { name: "创建 key" }).getByLabel("名称").fill("Deploy bot");
    await page.getByRole("button", { name: "创建", exact: true }).click();
    const secret = page.getByRole("dialog", { name: "复制你的 key" });
    await expect(secret).toBeVisible();

    // Another tab ends this account's session and starts another's. The cookie is the browser's, so the next time this
    // page asks for its session it gets the other account's, with its list cached for 30 seconds and the key's text
    // still open on the screen.
    state.account = 1;
    state.user.email = "other@example.com";
    state.keys = [];
    await returnToWindow(page);
    await page.getByRole("button", { name: "账号设置" }).click();
    await expect(page.getByRole("banner").getByText("other@example.com")).toBeVisible();
    await expect(secret).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "还没有 key" })).toBeVisible();
    await expect(page.getByText("Laptop")).toHaveCount(0);
    await expect(page.getByText("Deploy bot")).toHaveCount(0);
  });

  test("a session that keeps its account but loses a permission stops showing what that permission read", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/balance");
    await expect(page.getByText("US$12.00", { exact: true })).toBeVisible();

    state.permissions = state.permissions.filter((permission) => permission !== "balance.view");
    await returnToWindow(page);
    await expect(page.getByRole("heading", { name: "没有权限" })).toBeVisible();
    await expect(page.getByText("请联系工作区的管理员。")).toBeVisible();
    await expect(page.getByText("US$12.00", { exact: true })).toHaveCount(0);
    await expect(
      page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "账单与充值" }),
    ).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^余额/ })).toHaveCount(0);
  });

  test("a sign-out that fails says the user is still signed in, and trying again signs out", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await failOnce(page, "**/api/console/v1/auth/sign-out");
    await page.goto("/console/keys");
    await expect(page.getByText("Laptop")).toBeVisible();
    await page.getByRole("button", { name: "账号设置" }).click();
    await page.getByRole("button", { name: "退出登录" }).click();
    await expect(page.getByRole("alert").filter({ hasText: "没能退出登录" })).toContainText("你仍在登录状态");
    await expect(page.getByText("Laptop")).toBeVisible();
    expect(state.level).toBe("password");
    await page.getByRole("button", { name: "退出登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
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

  test("giving up a pending sign-in that fails says it is not given up, and trying again does", async ({ page }) => {
    await mockConsole(page);
    await failOnce(page, "**/api/console/v1/auth/sign-out");
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
    await page.getByRole("button", { name: "换一个账户登录" }).click();
    await expect(page.getByRole("alert").filter({ hasText: "没能退出登录" })).toContainText("你仍在登录状态");
    await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
    await page.getByRole("button", { name: "换一个账户登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
  });
});

test.describe("the sign-in steps", () => {
  test("while a provider's start is out, no other way in on the step can be taken", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, oauth_providers: ["github"], passkeys: true };
    let answer!: () => void;
    const held = new Promise<void>((resolve) => (answer = resolve));
    await page.route("**/api/console/v1/auth/oauth/start", async (route) => {
      await held;
      await route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } });
    });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "使用 GitHub 继续" }).click();
    await expect(page.getByRole("button", { name: "继续", exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "使用通行密钥" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "使用 GitHub 继续" })).toBeDisabled();
    // Enter submits the form, which the attempt keeps on this step too
    await page.getByLabel("邮箱").press("Enter");
    await expect(page.getByLabel("密码", { exact: true })).toHaveCount(0);
    answer();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("button", { name: "继续", exact: true })).toBeEnabled();
  });

  test("an address the server refused stays on the email step until it is changed", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await signIn(page, "a");
    await expect(page.getByRole("alert")).toHaveText(/不像一个邮箱地址/);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await expect(page.getByLabel("密码", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("alert")).toHaveText(/不像一个邮箱地址/);
    await page.getByLabel("邮箱").fill(knownEmail);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await expect(page.getByLabel("密码", { exact: true })).toBeVisible();
  });

  test("a passkey refused because the pending sign-in is over offers to sign in again, keeping the way back", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) =>
      route.fulfill({ status: 401, json: { error: { code: "unauthenticated", message: "unauthenticated" } } }),
    );
    await page.goto("/console/keys");
    await signIn(page, mfaEmail);
    await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByRole("alert")).toHaveText(/这次登录已经失效/);
    await expect(page.getByRole("button", { name: "使用通行密钥" })).toHaveCount(0);
    await page.getByRole("button", { name: "重新登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    expect(new URL(page.url()).searchParams.get("next")).toBe("/keys");
  });

  test("a passkey that fails otherwise can be tried again on the step", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) =>
      route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } }),
    );
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("button", { name: "使用通行密钥" })).toBeEnabled();
  });

  test("while a sign-in is out the step cannot be left, so its answer signs in the address it was for", async ({
    page,
  }) => {
    await mockConsole(page);
    let answer!: () => void;
    const held = new Promise<void>((resolve) => (answer = resolve));
    await page.route("**/api/console/v1/auth/sign-in", async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await page.getByLabel("密码", { exact: true }).fill(knownPassword);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await expect(page.getByRole("button", { name: "换个邮箱" })).toHaveCount(0);
    await expect(page.getByRole("link", { name: "忘记密码？" })).toHaveCount(0);
    answer();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("an address the contract refuses takes the visitor back to the email step", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await signIn(page, "a");
    await expect(page.getByLabel("邮箱")).toBeVisible();
    await expect(page.getByRole("alert")).toHaveText(/不像一个邮箱地址/);
  });

  test("switching the language keeps what was typed", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await page.getByLabel("密码", { exact: true }).fill(knownPassword);
    await page.getByRole("link", { name: "EN", exact: true }).click();
    await expect(page.getByRole("heading", { level: 1, name: "Enter your password" })).toBeVisible();
    await expect(page.getByLabel("Password", { exact: true })).toHaveValue(knownPassword);
  });

  test("the latest way in says why it failed, not an earlier one", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, oauth_providers: ["github"], passkeys: true };
    await page.route("**/api/console/v1/auth/oauth/start", (route) =>
      route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } }),
    );
    await page.route("**/api/console/v1/auth/passkeys/begin", (route) =>
      route.fulfill({
        status: 429,
        headers: { "Retry-After": "30" },
        json: { error: { code: "rate_limited", message: "rate_limited" } },
      }),
    );
    await page.goto("/console/sign-in?oauth=failed");
    await expect(page.getByRole("alert")).toHaveText(/这次登录没有完成/);
    await page.getByRole("button", { name: "使用 GitHub 继续" }).click();
    await expect(page.getByRole("alert")).not.toHaveText(/这次登录没有完成/);
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
  });

  test("a long address breaks inside the phone's form", async ({ page }) => {
    await mockConsole(page);
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(`${"w".repeat(64)}@example.com`);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await expect(page.getByLabel("密码", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
  });

  test("the site's brand colour is the focus ring of the sign-in fields", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, brand_color: "#335ddf" };
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").focus();
    const { ring, accent } = await page.evaluate(() => {
      const field = document.querySelector(".lg-field") as HTMLElement;
      const probe = document.createElement("i");
      probe.style.color = "var(--accentT)";
      field.append(probe);
      const accent = getComputedStyle(probe).color;
      probe.remove();
      return { ring: getComputedStyle(field).outlineColor, accent };
    });
    expect(ring).toBe(accent);
    expect(ring).not.toBe("rgb(7, 151, 185)");
  });
});

test.describe("accepting updated documents on the board", () => {
  test("switching the language keeps the box as the form would send it", async ({ page }) => {
    const state = await mockConsole(page);
    state.legal = demoDocuments().filter((document) => document.locale === "zh");
    signedInAs(state);
    await page.goto("/console/consent");
    const box = page.getByLabel("我已阅读并同意上述服务条款和隐私政策。");
    await box.check();
    await page.getByRole("link", { name: "EN", exact: true }).click();
    await expect(
      page.getByLabel("I have read and agree to the terms of service and privacy policy above."),
    ).toBeChecked();
    await expect(page.getByRole("button", { name: "Accept and continue" })).toBeEnabled();
  });
});

// A control's edge against its own fill, as the page computes them: WCAG 2.2's 3:1 (docs/frontend.md「对比度令牌」).
const rgb = (css: string) => (css.match(/\d+(\.\d+)?/g) ?? []).slice(0, 3).map(Number) as [number, number, number];

async function edgeContrast(page: Page, selector: string) {
  const control = page.locator(selector).first();
  await expect(control).toBeVisible();
  const { edge, fill } = await control.evaluate((element) => {
    const style = getComputedStyle(element);
    return { edge: style.boxShadow.match(/rgba?\([^)]*\)/)?.[0] ?? "", fill: style.backgroundColor };
  });
  return contrast(rgb(edge), rgb(fill));
}

for (const scheme of ["light", "dark"] as const) {
  test(`the sign-in controls' edges hold 3:1 against their fill (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    const state = await mockConsole(page);
    // a site with other ways in, so the secondary buttons are there to measure
    state.site = { ...state.site, oauth_providers: ["github"], passkeys: true };
    await page.goto("/console/sign-in");
    expect(await edgeContrast(page, ".lg-field")).toBeGreaterThanOrEqual(3);
    expect(await edgeContrast(page, ".lg-btn:not(.primary)")).toBeGreaterThanOrEqual(3);
  });

  test(`the terms box's edge holds 3:1 against its fill (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    const state = await mockConsole(page);
    state.legal = demoDocuments();
    signedInAs(state);
    await page.goto("/console/consent");
    expect(await edgeContrast(page, ".lg-consent .box")).toBeGreaterThanOrEqual(3);
  });
}
