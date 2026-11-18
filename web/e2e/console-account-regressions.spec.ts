import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";
import { knownEmail, knownPassword } from "./mock-auth.ts";

const returnToWindow = (page: Page) =>
  page.evaluate(() => document.dispatchEvent(new Event("visibilitychange", { bubbles: true })));

const samplePasskey = {
  id: "pk_00000000000000000000000000",
  name: "YubiKey",
  created_at: "2026-10-01T08:00:00Z",
  backup_eligible: true,
};

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((r) => (resolve = r));
  return { promise, resolve };
}

test.describe("security factor states and recovery code replacement", () => {
  test("passkey-only user sees enable TOTP flow and completes it", async ({ page }) => {
    const state = await mockConsole(page);
    state.passkeys = [samplePasskey];
    state.totpEnabled = false;
    state.mfaEnabled = true;
    signedInAs(state, "mfa");
    await page.goto("/console/security");

    await expect(page.getByText("未开启")).toBeVisible();
    await expect(page.getByRole("button", { name: "更换验证器" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "重新生成恢复码" })).toHaveCount(0);

    await page.getByRole("button", { name: "开启两步验证" }).click();
    const setup = page.getByRole("dialog", { name: "开启两步验证" });
    await setup.getByLabel("密码").fill(knownPassword);
    await setup.getByLabel("验证器里的 6 位验证码").fill("123456");
    await setup.getByRole("button", { name: "开启", exact: true }).click();

    const codes = page.getByRole("dialog", { name: "保存你的恢复码" });
    await expect(codes.getByRole("listitem")).toHaveCount(10);
    await codes.getByRole("button", { name: "我已保存" }).click();

    await expect(page.getByText("已开启")).toBeVisible();
    await expect(page.getByRole("button", { name: "更换验证器" })).toBeVisible();
    await expect(page.getByRole("button", { name: "重新生成恢复码" })).toBeVisible();
  });

  test("TOTP-only and passkey+TOTP users see replace controls and can replace with a recovery code", async ({
    page,
  }) => {
    const totpOnly = await mockConsole(page);
    totpOnly.totpEnabled = true;
    totpOnly.mfaEnabled = true;
    totpOnly.passkeys = [];
    signedInAs(totpOnly, "mfa");
    await page.goto("/console/security");
    await expect(page.getByRole("button", { name: "更换验证器" })).toBeVisible();
    await expect(page.getByRole("button", { name: "重新生成恢复码" })).toBeVisible();

    const state = await mockConsole(page);
    state.totpEnabled = true;
    state.mfaEnabled = true;
    state.passkeys = [samplePasskey];
    signedInAs(state, "mfa");
    await page.goto("/console/security");

    await page.getByRole("button", { name: "更换验证器" }).click();
    const replaceDialog = page.getByRole("dialog", { name: "更换验证器" });
    await expect(replaceDialog).toContainText("未用恢复码");
    await replaceDialog.getByLabel("新验证器中的 6 位验证码").fill("123456");
    await replaceDialog.getByRole("button", { name: "确认更换" }).click();
    await expect(replaceDialog.getByRole("alert")).toContainText("未用恢复码");

    await replaceDialog.getByLabel("当前 6 位验证码或恢复码").fill(state.user.recoveryCode);
    await replaceDialog.getByRole("button", { name: "确认更换" }).click();
    const codes = page.getByRole("dialog", { name: "保存你的恢复码" });
    await expect(codes.getByRole("listitem")).toHaveCount(10);
  });
});

test.describe("email change token security and offline recovery", () => {
  test("cold-open unauthenticated redirect keeps token out of ?next= and confirms via POST body", async ({ page }) => {
    const state = await mockConsole(page);
    state.pendingEmail = "verified@example.com";
    state.emailConfirmToken = "tok-email-secret";

    await page.goto("/console/settings#email-change-token=tok-email-secret");
    await expect(page).toHaveURL(/\/console\/sign-in\?next=%2Fsettings$/);
    expect(page.url()).not.toContain("email-change-token");
    expect(page.url()).not.toContain("tok-email-secret");

    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByLabel("密码").fill(knownPassword);
    await page.getByRole("button", { name: "登录", exact: true }).click();

    await expect(page.getByText("邮箱已更换并完成验证。")).toBeVisible();
    await expect(page.getByLabel("邮箱")).toHaveValue("verified@example.com");
    await expect(page).toHaveURL(/\/console\/settings$/);
  });

  test("reopening same email verification link after network failure retries confirmation", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    state.pendingEmail = "retry@example.com";
    state.emailConfirmToken = "tok-retry-1";

    let attempts = 0;
    await page.route("**/api/console/v1/profile/email/confirm", async (route) => {
      attempts += 1;
      if (attempts === 1) return route.abort("failed");
      return route.fallback();
    });

    await page.goto("/console/settings#email-change-token=tok-retry-1");
    await expect(page.getByRole("alert")).toBeVisible();
    expect(attempts).toBe(1);

    await page.goto("/console/settings#email-change-token=tok-retry-1");
    await expect(page.getByText("邮箱已更换并完成验证。")).toBeVisible();
    await expect(page.getByLabel("邮箱")).toHaveValue("retry@example.com");
    expect(attempts).toBe(2);
  });
});

test.describe("cross-account isolation on delayed mutations", () => {
  test("delayed profile name save from account A does not overwrite account B", async ({ page }) => {
    const state = await mockConsole(page);
    state.account = 1;
    state.displayName = "Alice";
    signedInAs(state);

    const inFlight = deferred();
    const release = deferred();
    await page.route("**/api/console/v1/profile/name", async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      inFlight.resolve();
      await release.promise;
      return route.fulfill({ status: 204 });
    });

    await page.goto("/console/settings");
    const nameInput = page.getByLabel("名字");
    await expect(nameInput).toHaveValue("Alice");
    await nameInput.fill("Alice Edited");
    await page.locator(".qses", { hasText: "保存名字" }).getByRole("button", { name: "保存" }).click();
    await inFlight.promise;

    state.account = 2;
    state.user.email = "bob@example.com";
    state.displayName = "Bob";
    await returnToWindow(page);
    await expect(page.getByRole("button", { name: "账号设置" })).toContainText("B");
    await expect(nameInput).toHaveValue("Bob");

    const postDone = page.waitForResponse(
      (res) => res.url().endsWith("/api/console/v1/profile/name") && res.request().method() === "POST",
    );
    release.resolve();
    await postDone;
    await expect(page.getByText("名字已保存。")).toHaveCount(0);
    await expect(nameInput).toHaveValue("Bob");
  });

  test("delayed notification save from account A does not overwrite account B", async ({ page }) => {
    const state = await mockConsole(page);
    state.account = 1;
    state.notifications = { ...state.notifications, low_balance: false };
    signedInAs(state);

    const inFlight = deferred();
    const release = deferred();
    await page.route("**/api/console/v1/notification-preferences", async (route) => {
      if (route.request().method() !== "PUT") return route.fallback();
      inFlight.resolve();
      await release.promise;
      return route.fulfill({ status: 200, json: { ...state.notifications, low_balance: false } });
    });

    await page.goto("/console/notifications");
    const lowBalanceSwitch = page.getByRole("button", { name: /余额不足/ });
    await expect(lowBalanceSwitch).toHaveAttribute("aria-label", /已关闭/);
    await page.getByRole("button", { name: "保存偏好", exact: true }).click();
    await inFlight.promise;

    state.account = 2;
    state.user.email = "bob@example.com";
    state.notifications = { ...state.notifications, low_balance: true };
    await returnToWindow(page);
    await expect(lowBalanceSwitch).toHaveAttribute("aria-label", /已开启/);

    const putDone = page.waitForResponse(
      (res) => res.url().endsWith("/api/console/v1/notification-preferences") && res.request().method() === "PUT",
    );
    release.resolve();
    await putDone;
    await expect(lowBalanceSwitch).toHaveAttribute("aria-label", /已开启/);
    await expect(page.getByText("偏好已保存。")).toHaveCount(0);
  });

  test("delayed revoke-all sessions from account A does not clear account B devices", async ({ page }) => {
    const state = await mockConsole(page);
    state.account = 1;
    signedInAs(state);

    const inFlight = deferred();
    const release = deferred();
    await page.route("**/api/console/v1/sessions", async (route) => {
      if (route.request().method() !== "DELETE") return route.fallback();
      inFlight.resolve();
      await release.promise;
      return route.fulfill({ status: 200, json: { revoked: 2 } });
    });

    await page.goto("/console/security");
    await page.getByRole("button", { name: "全部退出" }).click();
    await page.getByRole("dialog", { name: "退出其他设备" }).getByRole("button", { name: "退出其他设备" }).click();
    await inFlight.promise;

    state.account = 2;
    state.user.email = "bob@example.com";
    await returnToWindow(page);
    await expect(page.locator("#main").getByText("bob@example.com")).toBeVisible();
    await expect(page.locator(".qses", { hasText: "Safari on iPhone" })).toBeVisible();

    const delDone = page.waitForResponse(
      (res) => res.url().endsWith("/api/console/v1/sessions") && res.request().method() === "DELETE",
    );
    release.resolve();
    await delDone;
    await expect(page.locator(".qses", { hasText: "Safari on iPhone" })).toBeVisible();
  });
});

test.describe("profile drafts, OAuth unlink, loading/error states, passkeys, and notifications", () => {
  test("preserves early draft across slow GET, saves 100 emoji without truncation, rejects empty name", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.displayName = "Server Name";
    signedInAs(state);

    const getName = deferred();
    await page.route("**/api/console/v1/profile/name", async (route) => {
      if (route.request().method() === "GET") await getName.promise;
      await route.fallback();
    });

    const nameGot = page.waitForResponse(
      (res) => res.url().endsWith("/api/console/v1/profile/name") && res.request().method() === "GET",
    );
    await page.goto("/console/settings");
    const nameInput = page.getByLabel("名字");
    await expect(nameInput).toBeEditable();
    await nameInput.fill("Early Draft");
    getName.resolve();
    await nameGot;
    await expect(nameInput).toHaveValue("Early Draft");

    const hundredEmoji = "🎉".repeat(100);
    await nameInput.fill(hundredEmoji);
    const saveNameBtn = page.locator(".qses", { hasText: "保存名字" }).getByRole("button", { name: "保存" });
    await saveNameBtn.click();
    await expect(page.getByText("名字已保存。").first()).toBeVisible();
    expect(Array.from(state.displayName)).toHaveLength(100);
    expect(state.displayName).toBe(hundredEmoji);

    await nameInput.fill("");
    await saveNameBtn.click();
    await expect(page.getByText("名字不能为空。").first()).toBeVisible();
    expect(state.displayName).toBe(hundredEmoji);
  });

  test("unlinking OAuth invalidates active sessions list and invitations use returned link", async ({ page }) => {
    await page.addInitScript(() => {
      Object.defineProperty(navigator, "clipboard", {
        value: {
          writeText: async (text: string) => {
            (window as unknown as { __copiedInviteLink?: string }).__copiedInviteLink = text;
          },
        },
        configurable: true,
      });
    });
    const state = await mockConsole(page);
    state.site = { ...state.site, oauth_providers: ["github"] };
    state.identities = [{ provider: "github", display_name: "octocat", created_at: "2026-10-01T08:00:00Z" }];
    signedInAs(state);

    await page.route("**/api/console/v1/invitations*", async (route) => {
      await route.fulfill({
        status: 200,
        json: {
          code: state.user.inviteCode,
          link: "https://invite.example.net/r/AXIOM-7K2Q",
          invitees: state.invitees,
        },
      });
    });

    await page.goto("/console/security");
    await expect(page.getByText("已绑定：octocat")).toBeVisible();
    await expect(page.locator(".qses", { hasText: "Safari on iPhone" })).toBeVisible();
    await expect(page.getByText("https://invite.example.net/r/AXIOM-7K2Q")).toBeVisible();
    await page.getByRole("button", { name: "复制链接" }).click();
    expect(await page.evaluate(() => (window as unknown as { __copiedInviteLink?: string }).__copiedInviteLink)).toBe(
      "https://invite.example.net/r/AXIOM-7K2Q",
    );

    await page.getByRole("button", { name: "解除绑定" }).click();
    await expect(page.getByText("未绑定")).toBeVisible();
    await expect(page.locator(".qses", { hasText: "Safari on iPhone" })).toHaveCount(0);
  });

  test("distinguishes loading, error, and 404 unavailable from empty security facts", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, oauth_providers: ["github"] };
    signedInAs(state);

    const gate = deferred();
    for (const pattern of [
      "**/api/console/v1/sessions",
      "**/api/console/v1/auth/passkeys",
      "**/api/console/v1/auth/identities",
      "**/api/console/v1/invitations*",
    ]) {
      await page.route(pattern, async (route) => {
        if (route.request().method() !== "GET") return route.fallback();
        await gate.promise;
        if (route.request().url().endsWith("/sessions")) {
          return route.fulfill({ status: 404, json: { error: { code: "not_found", message: "not_found" } } });
        }
        return route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } });
      });
    }

    await page.goto("/console/security");
    await expect(page.getByText("正在加载…").first()).toBeVisible();
    await expect(page.getByText("当前没有活跃会话。")).toHaveCount(0);
    await expect(page.getByText("还没有通行密钥。")).toHaveCount(0);
    await expect(page.getByText("还没有通过你的邀请码注册的账户。")).toHaveCount(0);
    await expect(page.getByText("未绑定")).toHaveCount(0);

    gate.resolve();
    await expect(page.getByText("已登录设备列表暂不可用（当前节点尚未提供此接口）。")).toBeVisible();
    await expect(page.getByRole("button", { name: "重试" })).toHaveCount(3);
    await expect(page.getByText("当前没有活跃会话。")).toHaveCount(0);
    await expect(page.getByText("还没有通行密钥。")).toHaveCount(0);
    await expect(page.getByText("还没有通过你的邀请码注册的账户。")).toHaveCount(0);
    await expect(page.getByText("未绑定")).toHaveCount(0);
  });

  test("gates Add Passkey by site setting and login domain while keeping removals active", async ({ page }) => {
    const state = await mockConsole(page);
    state.passkeys = [samplePasskey];
    state.mfaEnabled = true;
    state.site = { ...state.site, passkeys: false };
    signedInAs(state, "mfa");

    await page.goto("/console/security");
    await expect(page.getByText("当前站点未启用通行密钥。")).toBeVisible();
    await expect(page.getByRole("button", { name: "添加通行密钥" })).toHaveCount(0);
    await expect(page.locator(".qses", { hasText: "YubiKey" })).toBeVisible();

    state.site = { ...state.site, passkeys: true, login_domain: "other.example.com" };
    await page.reload();
    await expect(page.getByText("请在站点的登录域名下添加通行密钥。")).toBeVisible();
    await expect(page.getByRole("button", { name: "添加通行密钥" })).toHaveCount(0);

    state.site = { ...state.site, passkeys: true, login_domain: new URL(page.url()).hostname };
    await page.reload();
    await expect(page.getByRole("button", { name: "添加通行密钥" })).toBeEnabled();

    // Existing passkeys remain removable even when new registrations are turned off on the site.
    state.site = { ...state.site, passkeys: false };
    await page.reload();
    await page.getByLabel("当前密码").fill(knownPassword);
    await page.locator(".qses", { hasText: "YubiKey" }).getByRole("button", { name: "移除" }).click();
    await expect(page.locator(".qses", { hasText: "YubiKey" })).toHaveCount(0);
  });

  test("notifications header covers email+webhook and disables controls while saving", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);

    const holdPut = deferred();
    const releasePut = deferred();
    await page.route("**/api/console/v1/notification-preferences", async (route) => {
      if (route.request().method() !== "PUT") return route.fallback();
      holdPut.resolve();
      await releasePut.promise;
      await route.fallback();
    });

    await page.goto("/console/notifications");
    await expect(page.locator(".qnt.hd")).toContainText("接收提醒");
    await expect(page.locator(".qnt.hd")).not.toContainText("邮件");
    await expect(page.getByText(/邮件和 webhook/).first()).toBeVisible();

    const lowSwitch = page.getByRole("button", { name: /余额不足/ });
    const threshold = page.getByLabel("阈值");
    const langBtn = page.locator(".qses", { hasText: "English" }).getByRole("button", { name: "选择" });
    await page.getByRole("button", { name: "保存偏好", exact: true }).click();
    await holdPut.promise;

    await expect(lowSwitch).toBeDisabled();
    await expect(threshold).toBeDisabled();
    await expect(langBtn).toBeDisabled();

    releasePut.resolve();
    await expect(page.getByText("偏好已保存。")).toBeVisible();
    await expect(lowSwitch).toBeEnabled();
  });
});
