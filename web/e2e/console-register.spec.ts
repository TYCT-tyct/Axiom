import { expect, type Page, type Route, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";
import { demoDocuments, goodInvite, type MockState } from "./console-data.ts";
import { goodVerifyToken, newEmail, takenVerifyToken } from "./mail-links.ts";
import { breaks } from "./mock.ts";
import { knownEmail } from "./mock-auth.ts";
import { signIn } from "./sign-in.ts";

// Registration and the verification of an open registration's address against the mocked API, whose registration and
// links behave as the server's (console-api.ts register, verifyEmail).

test.use({ timezoneId: "Asia/Shanghai" });

const password = "a long password nobody guesses";

const heading = (page: Page, name: string) => page.getByRole("heading", { level: 1, name });

const refused = (status: number, code: string, headers?: Record<string, string>) => ({
  status,
  headers,
  json: { error: { code, message: code } },
});

async function inviteForm(page: Page, email = "new@example.com", secret = password) {
  await page.goto(`/console/register?invite=${goodInvite}`);
  await expect(heading(page, "注册")).toBeVisible();
  await page.getByLabel("邮箱").fill(email);
  await page.getByLabel("密码", { exact: true }).fill(secret);
  await page.getByRole("button", { name: "注册", exact: true }).click();
}

const openSite = (state: MockState) => {
  state.site = { ...state.site, registration: "open", email_verification: true };
};

async function openLink(page: Page, token: string) {
  await page.goto(`/console/verify-email#token=${token}`);
  await expect(heading(page, "验证邮箱")).toBeVisible();
}

async function choosePassword(page: Page, secret = password) {
  await page.getByLabel("密码", { exact: true }).fill(secret);
  await page.getByRole("button", { name: "验证并创建账户" }).click();
}

test.describe("open registration", () => {
  test("mails a link to the address, and goes back to send again with the address kept", async ({ page }) => {
    const state = await mockConsole(page);
    openSite(state);
    await page.goto("/console/register");
    await expect(heading(page, "注册")).toBeVisible();
    await page.getByLabel("邮箱").fill(newEmail);
    await page.getByRole("button", { name: "发送验证链接" }).click();
    await expect(heading(page, "查收验证邮件")).toBeFocused();
    expect([...state.verifyLinks.values()]).toContain(newEmail);
    await page.getByRole("button", { name: "重新发送或换个邮箱" }).click();
    await expect(heading(page, "注册")).toBeVisible();
    await expect(page.getByLabel("邮箱")).toHaveValue(newEmail);
    await expect(page.getByLabel("邮箱")).toBeFocused();
  });

  test("an address the server does not take is said at the field, until the field is edited", async ({ page }) => {
    const state = await mockConsole(page);
    openSite(state);
    await page.goto("/console/register");
    await expect(heading(page, "注册")).toBeVisible();
    const field = page.getByLabel("邮箱");
    await field.fill("a@b@example.com");
    await page.getByRole("button", { name: "发送验证链接" }).click();
    await expect(page.getByRole("alert")).toHaveText("这不像一个邮箱地址，检查一下。");
    await expect(field).toHaveAttribute("aria-invalid", "true");
    await field.fill(newEmail);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(field).not.toHaveAttribute("aria-invalid", "true");
  });

  test("an answer that never came may have sent the mail, and offers to send again", async ({ page }) => {
    const state = await mockConsole(page);
    openSite(state);
    await page.route("**/api/console/v1/auth/register", (route) =>
      route.fulfill({ status: 503, json: { error: { code: "unavailable", message: "unavailable" } } }),
    );
    await page.goto("/console/register");
    await expect(heading(page, "注册")).toBeVisible();
    await page.getByLabel("邮箱").fill(newEmail);
    await page.getByRole("button", { name: "发送验证链接" }).click();
    await expect(heading(page, "不确定是否已注册")).toBeVisible();
    await page.getByRole("button", { name: "重新发送或换个邮箱" }).click();
    await expect(heading(page, "注册")).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
  });

  test("a limit is the form's to say: the address is neither marked nor pointed at it", async ({ page }) => {
    const state = await mockConsole(page);
    openSite(state);
    await page.route("**/api/console/v1/auth/register", (route) =>
      route.fulfill(refused(429, "rate_limited", { "Retry-After": "30" })),
    );
    await page.goto("/console/register");
    await expect(heading(page, "注册")).toBeVisible();
    const field = page.getByLabel("邮箱");
    await field.fill(newEmail);
    await page.getByRole("button", { name: "发送验证链接" }).click();
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
    await expect(field).not.toHaveAttribute("aria-invalid", "true");
    await expect(field).toHaveAttribute("aria-describedby", "");
  });
});

test.describe("registration with an invite code", () => {
  test("a password outside 12 to 256 characters is said at the field, and editing it clears that", async ({ page }) => {
    await mockConsole(page);
    await page.goto(`/console/register?invite=${goodInvite}`);
    await expect(heading(page, "注册")).toBeVisible();
    await page.getByLabel("邮箱").fill("new@example.com");
    const field = page.getByLabel("密码", { exact: true });
    await field.fill("😀".repeat(11));
    await page.getByRole("button", { name: "注册", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText("密码要 12 到 256 个字符。");
    await expect(field).toHaveAttribute("aria-invalid", "true");
    await field.press("x");
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(field).not.toHaveAttribute("aria-invalid", "true");
  });

  test("a limit is the form's to say, not a field's", async ({ page }) => {
    await mockConsole(page);
    await page.route("**/api/console/v1/auth/register", (route) =>
      route.fulfill({
        status: 429,
        headers: { "Retry-After": "30" },
        json: { error: { code: "rate_limited", message: "rate_limited" } },
      }),
    );
    await page.goto(`/console/register?invite=${goodInvite}`);
    await expect(heading(page, "注册")).toBeVisible();
    await page.getByLabel("邮箱").fill("new@example.com");
    await page.getByLabel("密码", { exact: true }).fill(password);
    await page.getByRole("button", { name: "注册", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
    await expect(page.getByRole("button", { name: "注册", exact: true })).toBeDisabled();
    for (const name of ["邀请码", "邮箱"]) {
      await expect(page.getByLabel(name)).not.toHaveAttribute("aria-invalid", "true");
    }
  });

  test("a refusal naming two fields is cleared field by field", async ({ page }) => {
    await mockConsole(page);
    const two = breaks("/email");
    two.json.error.fields.push({ pointer: "/password", message: "invalid" });
    await page.route("**/api/console/v1/auth/register", (route) => route.fulfill(two), { times: 1 });
    await inviteForm(page, "ab", "short");
    const email = page.getByLabel("邮箱");
    const secret = page.getByLabel("密码", { exact: true });
    await expect(email).toHaveAttribute("aria-invalid", "true");
    await expect(secret).toHaveAttribute("aria-invalid", "true");
    await email.press("x");
    await expect(email).not.toHaveAttribute("aria-invalid", "true");
    await expect(secret).toHaveAttribute("aria-invalid", "true");
    await expect(page.getByRole("alert")).toHaveText("密码要 12 到 256 个字符。");
  });

  test("a field left empty after the form's refusal is said at the field alone", async ({ page }) => {
    await mockConsole(page);
    await page.route("**/api/console/v1/auth/register", (route) => route.fulfill(refused(403, "origin_rejected")), {
      times: 1,
    });
    await inviteForm(page);
    await expect(page.getByRole("alert")).toHaveCount(1);
    await page.getByLabel("密码", { exact: true }).fill("");
    await page.getByRole("button", { name: "注册", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveCount(1);
    await expect(page.getByRole("alert")).toHaveText("请填写密码。");
  });

  test("an account made whose session does not hold says it is made, and is never registered again", async ({
    page,
  }) => {
    await mockConsole(page);
    let registers = 0;
    page.on("request", (request) => {
      if (request.method() === "POST" && request.url().endsWith("/auth/register")) registers += 1;
    });
    // the sign-in answers 200, but the browser keeps no session
    await page.route("**/api/console/v1/session", (route) => route.fulfill(refused(401, "unauthenticated")));
    await inviteForm(page);
    await expect(heading(page, "账户已创建")).toBeFocused();
    await expect(page.getByRole("button", { name: "注册", exact: true })).toHaveCount(0);
    await expect(page.getByRole("link", { name: "登录" })).toBeVisible();
    expect(registers).toBe(1);
  });

  test("an account made whose sign-in fails says so, and the way on is to sign in", async ({ page }) => {
    await mockConsole(page);
    await page.route(
      "**/api/console/v1/auth/sign-in",
      (route) => route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } }),
      { times: 1 },
    );
    await page.goto(`/console/register?invite=${goodInvite}`);
    await expect(heading(page, "注册")).toBeVisible();
    await page.getByLabel("邮箱").fill("new@example.com");
    await page.getByLabel("密码", { exact: true }).fill(password);
    await page.getByRole("button", { name: "注册", exact: true }).click();
    await expect(heading(page, "账户已创建")).toBeFocused();
    await page.getByRole("link", { name: "登录" }).click();
    await expect(heading(page, "登录")).toBeVisible();
    await signIn(page, "new@example.com", password);
    await expect(heading(page, "概览")).toBeVisible();
  });

  // any 5xx, or no answer, on the registration may have made the account
  for (const [name, answer] of [
    ["no answer", null],
    ["a 502 without the contract's error", { status: 502, body: "Bad Gateway" }],
    ["the contract's 503", { status: 503, json: { error: { code: "unavailable", message: "unavailable" } } }],
  ] as const) {
    test(`a registration met with ${name} may have made the account, and offers both ways on`, async ({ page }) => {
      await mockConsole(page);
      await page.route("**/api/console/v1/auth/register", (route: Route) =>
        answer ? route.fulfill(answer) : route.abort(),
      );
      await page.goto(`/console/register?invite=${goodInvite}`);
      await expect(heading(page, "注册")).toBeVisible();
      await page.getByLabel("邮箱").fill("new@example.com");
      await page.getByLabel("密码", { exact: true }).fill(password);
      await page.getByRole("button", { name: "注册", exact: true }).click();
      await expect(heading(page, "不确定是否已注册")).toBeVisible();
      await expect(page.getByRole("link", { name: "登录" })).toBeVisible();
      await page.getByRole("button", { name: "重新注册" }).click();
      await expect(heading(page, "注册")).toBeVisible();
      await expect(page.getByLabel("邮箱")).toHaveValue("new@example.com");
      await expect(page.getByRole("alert")).toHaveCount(0);
    });
  }
});

test.describe("verifying an open registration's address", () => {
  test("the link's password makes the account, which then signs in; the token goes only to verifyEmail", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.legal = demoDocuments();
    const carried: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes(goodVerifyToken)) carried.push(`url ${request.url()}`);
      if (Object.values(request.headers()).some((value) => value.includes(goodVerifyToken))) {
        carried.push(`header ${new URL(request.url()).pathname}`);
      }
      if (request.postData()?.includes(goodVerifyToken)) carried.push(`body ${new URL(request.url()).pathname}`);
    });
    await openLink(page, goodVerifyToken);
    expect(new URL(page.url()).hash).toBe("");
    const submit = page.getByRole("button", { name: "验证并创建账户" });
    await expect(page.getByRole("region", { name: "服务条款" })).toBeVisible();
    await expect(submit).toBeDisabled();
    await page.getByLabel("我已阅读并同意上述服务条款和隐私政策。").check();
    await choosePassword(page);
    await expect(heading(page, "邮箱已验证")).toBeFocused();
    await page.getByRole("link", { name: "去登录" }).click();
    await expect(heading(page, "登录")).toBeVisible();
    await signIn(page, newEmail, password);
    await expect(heading(page, "概览")).toBeVisible();
    expect(carried).toEqual(["body /api/console/v1/auth/verify-email"]);

    // the same link again: spent, and the way on is to sign out first, not to open this console
    await openLink(page, goodVerifyToken);
    await page.getByLabel("我已阅读并同意上述服务条款和隐私政策。").check();
    await choosePassword(page);
    await expect(heading(page, "链接不能用了")).toBeVisible();
    await expect(page.getByText(`仍以 ${newEmail} 登录`)).toBeVisible();
    await expect(page.getByRole("link", { name: "去登录" })).toHaveCount(0);
    await page.getByRole("button", { name: "退出这个账户" }).click();
    await expect(page.getByRole("link", { name: "去登录" })).toBeVisible();
    await expect(page.getByRole("link", { name: "重新注册" })).toBeVisible();
  });

  test("an address registered meanwhile says to sign in", async ({ page }) => {
    await mockConsole(page);
    await openLink(page, takenVerifyToken);
    await choosePassword(page);
    await expect(heading(page, "这个邮箱已经注册过")).toBeVisible();
    await expect(page.getByRole("link", { name: "去登录" })).toBeVisible();
  });

  for (const [name, answer] of [
    ["no answer", null],
    ["a 502 without the contract's error", { status: 502, body: "Bad Gateway" }],
    ["the contract's 503", { status: 503, json: { error: { code: "unavailable", message: "unavailable" } } }],
  ] as const) {
    test(`a verification met with ${name} may have made the account`, async ({ page }) => {
      await mockConsole(page);
      await page.route("**/api/console/v1/auth/verify-email", (route: Route) =>
        answer ? route.fulfill(answer) : route.abort(),
      );
      await openLink(page, goodVerifyToken);
      await choosePassword(page);
      await expect(heading(page, "不确定是否已完成")).toBeVisible();
      await expect(page.getByRole("link", { name: "去登录" })).toBeVisible();
      await expect(page.getByRole("link", { name: "重新注册" })).toBeVisible();
    });
  }

  test("a password outside 12 to 256 characters is said at the field, and the link still works", async ({ page }) => {
    await mockConsole(page);
    await openLink(page, goodVerifyToken);
    await choosePassword(page, "😀".repeat(11));
    await expect(page.getByRole("alert")).toHaveText("密码要 12 到 256 个字符。");
    const field = page.getByLabel("密码", { exact: true });
    await expect(field).toHaveAttribute("aria-invalid", "true");
    await field.press("x");
    await expect(page.getByRole("alert")).toHaveCount(0);
    await choosePassword(page);
    await expect(heading(page, "邮箱已验证")).toBeVisible();
  });

  test("a password left empty after a refusal is said at the field alone", async ({ page }) => {
    await mockConsole(page);
    await page.route(
      "**/api/console/v1/auth/verify-email",
      (route) =>
        route.fulfill({ status: 403, json: { error: { code: "origin_rejected", message: "origin_rejected" } } }),
      { times: 1 },
    );
    await openLink(page, goodVerifyToken);
    await choosePassword(page);
    await expect(page.getByRole("alert")).toHaveCount(1);
    await choosePassword(page, "");
    await expect(page.getByRole("alert")).toHaveCount(1);
    await expect(page.getByRole("alert")).toHaveText("请填写密码。");
    await choosePassword(page);
    await expect(heading(page, "邮箱已验证")).toBeVisible();
  });

  for (const [name, answer, title] of [
    ["a token cut short, which the contract's guard refuses", breaks("/token"), "链接不能用了"],
    ["an address whose domain the site no longer takes", refused(422, "email_domain_not_allowed"), "这个邮箱不能注册"],
  ] as const) {
    test(`a verification refused for ${name} leaves the form for the way on`, async ({ page }) => {
      await mockConsole(page);
      await page.route("**/api/console/v1/auth/verify-email", (route) => route.fulfill(answer));
      await openLink(page, goodVerifyToken);
      await choosePassword(page);
      await expect(heading(page, title)).toBeVisible();
      await expect(page.getByRole("link", { name: "去登录" })).toBeVisible();
      await expect(page.getByRole("link", { name: "重新注册" })).toBeVisible();
    });
  }

  test("a page opened without a token says to open the mail's link again", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/verify-email");
    await expect(heading(page, "验证邮箱")).toBeVisible();
    await expect(page.getByText("这个链接里没有验证令牌")).toBeVisible();
    await expect(page.getByRole("link", { name: "重新注册" })).toBeVisible();
    await expect(page.getByRole("link", { name: "去登录" })).toBeVisible();
  });

  test("a link whose page fails to load leaves no token in the address, and says to open it again", async ({
    page,
  }) => {
    await mockConsole(page);
    await page.route("**/verify-email-*.js", (route) => route.abort());
    await page.goto(`/console/verify-email#token=${goodVerifyToken}`);
    await expect(heading(page, "页面没能加载")).toBeVisible();
    expect(page.url()).not.toContain(goodVerifyToken);
    await expect(page.getByRole("alert")).toHaveText(/回到邮件重新打开那个链接/);
  });

  test("a browser signed in as someone else is named before any way to sign in, its recovery codes untouched", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    let takes = 0;
    await page.route("**/api/console/v1/auth/recovery-codes/take", (route) => {
      takes += 1;
      return route.fulfill({ json: { recovery_codes: ["abcd-efgh-ijkl-0001"] } });
    });
    await openLink(page, goodVerifyToken);
    await choosePassword(page);
    await expect(heading(page, "邮箱已验证")).toBeVisible();
    await expect(page.getByText(`仍以 ${knownEmail} 登录`)).toBeVisible();
    await expect(page.getByRole("link", { name: "去登录" })).toHaveCount(0);
    // the sign-out is sent after the session read, so a take for that session would have gone before it
    await page.getByRole("button", { name: "退出这个账户" }).click();
    await expect(page.getByRole("link", { name: "去登录" })).toBeVisible();
    expect(takes).toBe(0);
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });
});
