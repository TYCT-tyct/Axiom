import { expect, type Page, type Route, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";
import { goodResetToken, type MockState, otherResetToken } from "./console-data.ts";
import { knownEmail } from "./mock-auth.ts";
import { hold, signIn } from "./sign-in.ts";

// Resetting a forgotten password against the mocked API, whose links behave as the server's: each is for one person,
// works once, and is spent by a newer one for the same address (console-api.ts requestReset, completeReset).

test.use({ timezoneId: "Asia/Shanghai" });

// The password a reset sets, long enough for the contract.
const newPassword = "a long new password nobody guesses";

const heading = (page: Page, name: string) => page.getByRole("heading", { level: 1, name });

async function openLink(page: Page, token: string) {
  await page.goto(`/console/reset-password#token=${token}`);
  await expect(heading(page, "设置新密码")).toBeVisible();
}

async function setNewPassword(page: Page, password = newPassword) {
  await page.getByLabel("新密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "重设密码" }).click();
}

// The link the mocked server mailed to an address last.
const mailedTo = (state: MockState, email: string) =>
  [...state.resetLinks].findLast(([, owner]) => owner === email)?.[0] ?? "";

test("the password step leads to the reset page and back, keeping where the visitor was going", async ({ page }) => {
  const state = await mockConsole(page);
  await page.goto("/console/keys");
  await expect(heading(page, "登录")).toBeVisible();
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByRole("link", { name: "忘记密码？" }).click();
  await expect(heading(page, "找回密码")).toBeVisible();
  // the address the account uses now, which is the one the server looks up
  await expect(page.getByText("账户现在使用的邮箱")).toBeVisible();
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "发送重设邮件" }).click();
  // the answer takes the focus the submit had; it says what the server does, not that mail is sure to come
  await expect(heading(page, "查收邮件")).toBeFocused();
  await expect(page.getByText("如果有可用的账户使用这个邮箱，它会收到")).toBeVisible();
  expect(state.log.filter((line) => line === "POST /auth/password-reset")).toHaveLength(1);
  // one step back is the form, with the address, to send again: the address takes the focus, to be edited at once
  await page.getByRole("button", { name: "重新发送或换个邮箱" }).click();
  await expect(heading(page, "找回密码")).toBeVisible();
  await expect(page.getByLabel("邮箱")).toHaveValue(knownEmail);
  await expect(page.getByLabel("邮箱")).toBeFocused();
  await page.getByRole("link", { name: "返回登录" }).click();
  await expect(heading(page, "登录")).toBeVisible();
  await signIn(page);
  await expect(heading(page, "API Keys")).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/console/keys");
});

test("a link sets the new password once, which then signs in; the same link is then refused", async ({ page }) => {
  const state = await mockConsole(page);
  // where the link's token goes: only into the body of the reset itself, never an address or a header
  const carried: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes(goodResetToken)) carried.push(`url ${request.url()}`);
    if (Object.values(request.headers()).some((value) => value.includes(goodResetToken))) {
      carried.push(`header ${new URL(request.url()).pathname}`);
    }
    if (request.postData()?.includes(goodResetToken)) carried.push(`body ${new URL(request.url()).pathname}`);
  });
  await openLink(page, goodResetToken);
  expect(new URL(page.url()).hash).toBe("");
  await setNewPassword(page);
  await expect(heading(page, "密码已重设")).toBeFocused();
  await page.getByRole("link", { name: "去登录" }).click();
  await expect(heading(page, "登录")).toBeVisible();
  await signIn(page, knownEmail, newPassword);
  await expect(heading(page, "概览")).toBeVisible();
  expect(carried).toEqual(["body /api/console/v1/auth/password-reset/complete"]);

  // signed in now, the same link is spent, and the way on is to sign out first, not to open this console
  await openLink(page, goodResetToken);
  await setNewPassword(page);
  await expect(heading(page, "链接不能用了")).toBeVisible();
  await expect(page.getByText(`仍以 ${knownEmail} 登录`)).toBeVisible();
  await expect(page.getByRole("link", { name: "用新密码登录" })).toHaveCount(0);
  await page.getByRole("button", { name: "退出这个账户" }).click();
  await expect(page.getByRole("link", { name: "用新密码登录" })).toBeVisible();
  expect(state.level).toBe("none");
  await page.getByRole("button", { name: "重新申请" }).click();
  await expect(heading(page, "找回密码")).toBeVisible();
});

test("resetting one's own password while signed in ends that session, and the way on is to sign in", async ({
  page,
}) => {
  const state = await mockConsole(page);
  signedInAs(state);
  await openLink(page, goodResetToken);
  await setNewPassword(page);
  await expect(heading(page, "密码已重设")).toBeVisible();
  await page.getByRole("link", { name: "去登录" }).click();
  await expect(heading(page, "登录")).toBeVisible();
  await signIn(page, knownEmail, newPassword);
  await expect(heading(page, "概览")).toBeVisible();
});

test("a reset in a browser signed in as someone else offers to sign out before signing in", async ({ page }) => {
  const state = await mockConsole(page);
  signedInAs(state);
  await openLink(page, otherResetToken);
  await setNewPassword(page);
  await expect(heading(page, "密码已重设")).toBeVisible();
  await expect(page.getByText(`仍以 ${knownEmail} 登录`)).toBeVisible();
  await expect(page.getByRole("link", { name: "去登录" })).toHaveCount(0);
  // the other person's reset left this one's password and session as they were
  expect(state.level).toBe("password");
  await page.getByRole("button", { name: "退出这个账户" }).click();
  await page.getByRole("link", { name: "去登录" }).click();
  await expect(heading(page, "登录")).toBeVisible();
  expect(state.level).toBe("none");
});

test("a way on that cannot tell who is signed in offers to ask again, not to sign in", async ({ page }) => {
  await mockConsole(page);
  await openLink(page, goodResetToken);
  const failing = (route: Route) => (route.request().method() === "GET" ? route.abort() : route.fallback());
  await page.route("**/api/console/v1/session", failing);
  await setNewPassword(page);
  await expect(page.getByText("没能确认")).toBeVisible();
  await expect(page.getByRole("link", { name: "去登录" })).toHaveCount(0);
  await page.unroute("**/api/console/v1/session", failing);
  await page.getByRole("button", { name: "再确认一次" }).click();
  await expect(page.getByRole("link", { name: "去登录" })).toBeVisible();
  await expect(page.getByText("没能确认")).toHaveCount(0);
});

// What a reset's failure leaves: no answer and any 5xx may have gone through (the server's 500 can follow a commit
// whose reply was lost, its 503 for a request out of time says to look first); a limit did not, and is said on the
// form, which stays.
for (const [name, answer, unknown] of [
  ["no answer", null, true],
  ["a gateway's 502 without the contract's error", { status: 502, body: "Bad Gateway" }, true],
  ["the server's 500", { status: 500, json: { error: { code: "internal", message: "internal" } } }, true],
  ["the contract's 503", { status: 503, json: { error: { code: "unavailable", message: "unavailable" } } }, true],
  ["a limit's 429", { status: 429, json: { error: { code: "rate_limited", message: "rate_limited" } } }, false],
] as const) {
  test(`a reset met with ${name} ${unknown ? "offers the new password as well as a new link" : "stays on the form"}`, async ({
    page,
  }) => {
    await mockConsole(page);
    await page.route("**/api/console/v1/auth/password-reset/complete", (route) =>
      answer ? route.fulfill(answer) : route.abort(),
    );
    await openLink(page, goodResetToken);
    await setNewPassword(page);
    if (unknown) {
      await expect(heading(page, "不确定是否已重设")).toBeVisible();
      await expect(page.getByRole("link", { name: "用新密码登录" })).toBeVisible();
      await expect(page.getByRole("button", { name: "重新申请" })).toBeVisible();
      return;
    }
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(heading(page, "设置新密码")).toBeVisible();
    // the limit is not the password's: the field is not marked
    await expect(page.getByLabel("新密码", { exact: true })).not.toHaveAttribute("aria-invalid", "true");
  });
}

test("a link opened in the same tab is taken, and starts its own form", async ({ page }) => {
  await mockConsole(page);
  const sent: string[] = [];
  page.on("request", (request) => {
    if (request.url().endsWith("/password-reset/complete")) sent.push(request.postDataJSON().token);
  });
  await page.goto("/console/reset-password");
  await expect(heading(page, "找回密码")).toBeVisible();
  await page.evaluate(() => {
    location.hash = "token=never-mailed";
  });
  await expect(heading(page, "设置新密码")).toBeVisible();
  expect(new URL(page.url()).hash).toBe("");
  await setNewPassword(page);
  await expect(heading(page, "链接不能用了")).toBeVisible();
  // a second link, the good one, replaces the first and its refusal
  await page.evaluate((token) => {
    location.hash = `token=${token}`;
  }, goodResetToken);
  await expect(heading(page, "设置新密码")).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await setNewPassword(page);
  await expect(heading(page, "密码已重设")).toBeVisible();
  expect(sent).toEqual(["never-mailed", goodResetToken]);
});

test("asking for a link again leaves only the newest one working", async ({ page }) => {
  const state = await mockConsole(page);
  await page.goto("/console/reset-password");
  await expect(heading(page, "找回密码")).toBeVisible();
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "发送重设邮件" }).click();
  await expect(heading(page, "查收邮件")).toBeVisible();
  await openLink(page, goodResetToken);
  await setNewPassword(page);
  await expect(heading(page, "链接不能用了")).toBeVisible();
  await openLink(page, mailedTo(state, knownEmail));
  await setNewPassword(page);
  await expect(heading(page, "密码已重设")).toBeVisible();
});

// one character either side of the contract's 12 to 256, counted in characters (11 emoji are 11, not 22 UTF-16 units),
// as the contract's guard refuses them (400 naming the field); and the server's own check (422 weak_password)
for (const [name, password, weak] of [
  ["11 characters", "😀".repeat(11), false],
  ["257 characters", "w".repeat(257), false],
  ["the server's weak_password", newPassword, true],
] as const) {
  test(`a new password refused for ${name} is told the range, which goes once it is edited, and the link still works`, async ({
    page,
  }) => {
    await mockConsole(page);
    const weakOnce = (route: Route) =>
      route.fulfill({ status: 422, json: { error: { code: "weak_password", message: "weak_password" } } });
    if (weak) await page.route("**/api/console/v1/auth/password-reset/complete", weakOnce, { times: 1 });
    await openLink(page, goodResetToken);
    await setNewPassword(page, password);
    await expect(page.getByRole("alert")).toHaveText("新密码要 12 到 256 个字符。");
    const field = page.getByLabel("新密码", { exact: true });
    await expect(field).toHaveAttribute("aria-invalid", "true");
    await field.press("x");
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(field).not.toHaveAttribute("aria-invalid", "true");
    await setNewPassword(page);
    await expect(heading(page, "密码已重设")).toBeVisible();
  });
}

test("an address the server does not take is said at the field, until the field is edited", async ({ page }) => {
  await mockConsole(page);
  await page.goto("/console/reset-password");
  await expect(heading(page, "找回密码")).toBeVisible();
  const field = page.getByLabel("邮箱");
  await field.fill("a@b@example.com");
  await page.getByRole("button", { name: "发送重设邮件" }).click();
  await expect(page.getByRole("alert")).toHaveText("这不像一个邮箱地址，检查一下。");
  await expect(field).toHaveAttribute("aria-invalid", "true");
  await field.fill("a@example.com");
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(field).not.toHaveAttribute("aria-invalid", "true");
});

// Signed in, the visitor can sign out during the wait: that request is the page's too, and the limit outlasts it.
for (const signedIn of [false, true]) {
  test(`a limit keeps the request's button disabled for as long as the answer says, and then its words go${signedIn ? ", through a sign-out" : ""}`, async ({
    page,
  }) => {
    // no sea running, so moving the clock thirty seconds on does not draw thirty seconds of frames
    await page.emulateMedia({ reducedMotion: "reduce" });
    const state = await mockConsole(page);
    if (signedIn) signedInAs(state);
    await page.clock.install();
    await page.route("**/api/console/v1/auth/password-reset", (route) =>
      route.fulfill({
        status: 429,
        headers: { "Retry-After": "30" },
        json: { error: { code: "rate_limited", message: "rate_limited" } },
      }),
    );
    await page.goto("/console/reset-password");
    await expect(heading(page, "找回密码")).toBeVisible();
    await page.getByLabel("邮箱").fill(knownEmail);
    const submit = page.getByRole("button", { name: "发送重设邮件" });
    await submit.click();
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
    await expect(submit).toBeDisabled();
    // the limit is the form's to say, not the address's
    await expect(page.getByLabel("邮箱")).not.toHaveAttribute("aria-invalid", "true");
    if (signedIn) {
      await page.getByRole("button", { name: "退出这个账户" }).click();
      await expect(page.getByRole("link", { name: "返回登录" })).toBeVisible();
      await expect(submit).toBeDisabled();
    }
    // editing the address does not lift the wait: the limit is not about what was typed
    await page.getByLabel("邮箱").fill(`x${knownEmail}`);
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
    await page.clock.runFor(29_000);
    await expect(submit).toBeDisabled();
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
    await page.clock.runFor(2_000);
    await expect(submit).toBeEnabled();
    await expect(page.getByRole("alert")).toHaveCount(0);
  });
}

test("a link whose page fails to load leaves no token in the address, and says to open the link again", async ({
  page,
}) => {
  await mockConsole(page);
  const failing = (route: Route) => route.abort();
  await page.route("**/reset-password-*.js", failing);
  await page.goto(`/console/reset-password#token=${goodResetToken}`);
  await expect(heading(page, "页面没能加载")).toBeVisible();
  expect(page.url()).not.toContain(goodResetToken);
  expect(await page.evaluate(() => location.hash)).toBe("");
  // a reload would not bring the token back: the way back is the email's link, which still works
  await expect(page.getByRole("alert")).toHaveText(/回到邮件重新打开那个链接/);
  await page.unroute("**/reset-password-*.js", failing);
  await openLink(page, goodResetToken);
  await setNewPassword(page);
  await expect(heading(page, "密码已重设")).toBeVisible();
});

test("a sign-in waiting for its second step is named as that, and can be finished or ended", async ({ page }) => {
  const state = await mockConsole(page);
  state.level = "pending_mfa";
  state.csrf = "csrf-0";
  await openLink(page, "never-mailed");
  await setNewPassword(page);
  await expect(heading(page, "链接不能用了")).toBeVisible();
  await expect(page.getByText(`以 ${knownEmail} 进行的登录还在等两步验证`)).toBeVisible();
  await expect(page.getByText("仍以")).toHaveCount(0);
  await expect(page.getByRole("link", { name: "用新密码登录" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "继续那次登录" })).toHaveAttribute("href", "/console/mfa");
  await page.getByRole("button", { name: "结束那次登录" }).click();
  await expect(page.getByRole("link", { name: "用新密码登录" })).toBeVisible();
  expect(state.level).toBe("none");
});

test("the reset page neither takes nor shows the recovery codes of the session it finds", async ({ page }) => {
  const state = await mockConsole(page);
  signedInAs(state);
  let takes = 0;
  await page.route("**/api/console/v1/auth/recovery-codes/take", (route) => {
    takes += 1;
    return route.fulfill({ json: { recovery_codes: ["abcd-efgh-ijkl-0001"] } });
  });
  await openLink(page, otherResetToken);
  await setNewPassword(page);
  await expect(page.getByText(`仍以 ${knownEmail} 登录`)).toBeVisible();
  // the sign-out is sent after the session read, so a take for that session would have gone before it
  await page.getByRole("button", { name: "退出这个账户" }).click();
  await expect(page.getByRole("link", { name: "去登录" })).toBeVisible();
  expect(takes).toBe(0);
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

// Recovery codes a session has waiting (the take answers them once), shown by the console above every page but this one.
const aCode = "abcd-efgh-ijkl-0001";
const waitingCodes = [aCode, "abcd-efgh-ijkl-0002"];

async function codesWaiting(page: Page, held?: Promise<void>) {
  let takes = 0;
  await page.route("**/api/console/v1/auth/recovery-codes/take", async (route) => {
    takes += 1;
    await held;
    await route.fulfill({ json: { recovery_codes: takes === 1 ? waitingCodes : [] } });
  });
  return () => takes;
}

// The reset page opened in the same document as the console, as a link the router follows would open it.
async function toResetPage(page: Page) {
  await page.evaluate(() => {
    history.pushState(null, "", "/console/reset-password");
    dispatchEvent(new PopStateEvent("popstate"));
  });
  await expect(heading(page, "找回密码")).toBeVisible();
}

test("an open codes dialog is off the reset page, and back once the page is left", async ({ page }) => {
  const state = await mockConsole(page);
  signedInAs(state);
  await codesWaiting(page);
  await page.goto("/console/");
  await expect(page.getByRole("dialog")).toContainText(aCode);
  await toResetPage(page);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByText(aCode)).toHaveCount(0);
  await page.goBack();
  await expect(heading(page, "概览")).toBeVisible();
  await expect(page.getByRole("dialog")).toContainText(aCode);
});

test("codes that come while the reset page is open are shown once after it, and once kept never again", async ({
  page,
}) => {
  const state = await mockConsole(page);
  signedInAs(state);
  const { held, release } = hold();
  const takes = await codesWaiting(page, held);
  const asked = page.waitForRequest("**/api/console/v1/auth/recovery-codes/take");
  await page.goto("/console/");
  await asked;
  await toResetPage(page);
  const answered = page.waitForResponse("**/api/console/v1/auth/recovery-codes/take");
  release();
  await answered;
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.goBack();
  await expect(page.getByRole("dialog")).toContainText(aCode);
  await page.getByRole("button", { name: "我已保存" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  // in and out of the reset page in the same document: the codes kept are gone, and not asked for again
  await toResetPage(page);
  await page.goBack();
  await expect(heading(page, "概览")).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(takes()).toBe(1);
});

// The way back to sign in from the request and its answer is the one the session decides: a browser still signed in
// shows neither its console nor its codes until the visitor chooses.
test("a browser still signed in is named on the request and its answer, before any way to sign in", async ({
  page,
}) => {
  const state = await mockConsole(page);
  signedInAs(state);
  const takes = await codesWaiting(page);
  await page.goto("/console/reset-password");
  await expect(heading(page, "找回密码")).toBeVisible();
  await expect(page.getByText(`仍以 ${knownEmail} 登录`)).toBeVisible();
  await expect(page.getByRole("link", { name: "返回登录" })).toHaveCount(0);
  await page.getByLabel("邮箱").fill("someone.else@example.com");
  await page.getByRole("button", { name: "发送重设邮件" }).click();
  await expect(heading(page, "查收邮件")).toBeVisible();
  await expect(page.getByText(`仍以 ${knownEmail} 登录`)).toBeVisible();
  await expect(page.getByRole("link", { name: "返回登录" })).toHaveCount(0);
  await expect(heading(page, "概览")).toHaveCount(0);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(takes()).toBe(0);
  await page.getByRole("button", { name: "退出这个账户" }).click();
  await page.getByRole("link", { name: "返回登录" }).click();
  await expect(heading(page, "登录")).toBeVisible();
  expect(state.level).toBe("none");
});

test("while the session is read the step says so, and offers no way on until it is known", async ({ page }) => {
  await mockConsole(page);
  const { held, release } = hold();
  await page.route("**/api/console/v1/session", async (route) => {
    await held;
    await route.fallback();
  });
  await page.goto("/console/reset-password");
  await expect(heading(page, "找回密码")).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "正在确认登录状态" })).toBeVisible();
  await expect(page.getByRole("link", { name: "返回登录" })).toHaveCount(0);
  release();
  await expect(page.getByRole("link", { name: "返回登录" })).toBeVisible();
  await expect(page.getByText("正在确认登录状态")).toHaveCount(0);
});
