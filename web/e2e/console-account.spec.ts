import { expect, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";
import { knownPassword } from "./mock-auth.ts";

test.describe("settings page (#747 C6)", () => {
  test("edits display name, switches theme and language immediately and keeps them on reload", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/settings");

    await expect(page.getByRole("heading", { level: 1, name: "个人资料" })).toBeVisible();
    const nameInput = page.getByLabel("名字");
    await expect(nameInput).toHaveValue("林知远");

    await nameInput.fill("陈安");
    await nameInput.press("Enter");
    await expect(page.getByText("名字已保存。").first()).toBeVisible();
    expect(state.displayName).toBe("陈安");

    // Appearance: dark theme applies immediately and persists across reload.
    const darkRow = page.locator(".qses", { hasText: "深色" });
    await darkRow.getByRole("button", { name: "切换" }).click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

    // Language: switching to English updates headings immediately and persists across reload.
    const enRow = page.locator(".qses", { hasText: "English" });
    await enRow.getByRole("button", { name: "切换" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "Profile" })).toBeVisible();

    await page.reload();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await expect(page.getByRole("heading", { level: 1, name: "Profile" })).toBeVisible();
    await expect(page.getByLabel("Name")).toHaveValue("陈安");
  });

  test("requests an email change and confirms it from URL hash", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/settings");

    await page.getByRole("button", { name: "更换邮箱" }).click();
    const dialog = page.getByRole("dialog", { name: "更换登录邮箱" });
    await dialog.getByLabel("新邮箱地址").fill("next@example.com");
    await dialog.getByLabel("当前密码").fill("wrong password");
    await dialog.getByRole("button", { name: "发送验证邮件" }).click();
    await expect(dialog.getByRole("alert")).toContainText("当前密码不对");

    await dialog.getByLabel("当前密码").fill(knownPassword);
    await dialog.getByRole("button", { name: "发送验证邮件" }).click();
    await expect(page.getByText("验证邮件已发到 next@example.com").first()).toBeVisible();

    await page.goto("/console/settings#email-change-token=tok-email-1");
    await expect(page.getByText("邮箱已更换并完成验证。")).toBeVisible();
    await expect(page).toHaveURL(/\/console\/settings$/);
    await expect(page.getByLabel("邮箱")).toHaveValue("next@example.com");
  });

  test("handles 404 display name on older nodes and saves billing order", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.route("**/api/console/v1/profile/name", (route) =>
      route.fulfill({ status: 404, json: { error: { code: "not_found", message: "not_found" } } }),
    );
    await page.goto("/console/settings");

    await expect(page.getByText("当前节点暂不支持读取或修改显示名称。")).toBeVisible();
    const walletFirst = page.locator(".qses", { hasText: "先钱包，后套餐" });
    await walletFirst.getByRole("button", { name: "选择" }).click();
    await expect(walletFirst.getByText("当前", { exact: true })).toBeVisible();
  });
});

test.describe("security page extended flows (#747 C6)", () => {
  test("lists signed-in devices, protects current device, and revokes single or all other devices", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/security");

    const currentRow = page.locator(".qses", { hasText: "Chrome on macOS" });
    await expect(currentRow.getByText("这台设备")).toBeVisible();
    await expect(currentRow.getByRole("button", { name: "退出" })).toHaveCount(0);

    const iphoneRow = page.locator(".qses", { hasText: "Safari on iPhone" });
    await iphoneRow.getByRole("button", { name: "退出" }).click();
    await expect(page.locator(".qses", { hasText: "Safari on iPhone" })).toHaveCount(0);

    await page.getByRole("button", { name: "全部退出" }).click();
    const confirm = page.getByRole("dialog", { name: "退出其他设备" });
    await confirm.getByRole("button", { name: "退出其他设备" }).click();
    await expect(page.locator(".qses", { hasText: "Firefox on Windows" })).toHaveCount(0);
    await expect(currentRow.getByText("这台设备")).toBeVisible();
  });

  test("replaces TOTP authenticator with retry on bad code and changes password", async ({ page }) => {
    const state = await mockConsole(page);
    state.mfaEnabled = true;
    signedInAs(state, "mfa");
    await page.goto("/console/security");

    await page.getByRole("button", { name: "更换验证器" }).click();
    const replaceDialog = page.getByRole("dialog", { name: "更换验证器" });
    await replaceDialog.getByLabel("当前 6 位验证码或恢复码").fill("123456");
    await replaceDialog.getByLabel("新验证器中的 6 位验证码").fill("000000");
    await replaceDialog.getByRole("button", { name: "确认更换" }).click();
    await expect(replaceDialog.getByRole("alert")).toContainText(/验证码/);

    await replaceDialog.getByLabel("新验证器中的 6 位验证码").fill("123456");
    await replaceDialog.getByRole("button", { name: "确认更换" }).click();
    const codes = page.getByRole("dialog", { name: "保存你的恢复码" });
    await expect(codes.getByRole("listitem")).toHaveCount(10);
    await codes.getByRole("button", { name: "我已保存" }).click();

    await page.getByRole("button", { name: "修改密码" }).click();
    const pwDialog = page.getByRole("dialog", { name: "修改密码" });
    await pwDialog.getByLabel("当前密码").fill(knownPassword);
    await pwDialog.getByLabel("新密码", { exact: true }).fill("updated long password 99");
    await pwDialog.getByLabel("确认新密码").fill("updated long password 99");
    await pwDialog.getByRole("button", { name: "保存新密码" }).click();
    await expect(page.getByRole("dialog", { name: "保存你的恢复码" }).getByRole("listitem")).toHaveCount(10);
    expect(state.user.password).toBe("updated long password 99");
  });

  test("shows invitations with disabled placeholders and handles 404 on older nodes", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/security");
    await expect(page.getByText("已注册")).toBeVisible();
    await expect(page.getByText("账号不可用")).toBeVisible();

    await page.route("**/api/console/v1/invitations*", (route) =>
      route.fulfill({ status: 404, json: { error: { code: "not_found", message: "not_found" } } }),
    );
    await page.goto("/console/security");
    await expect(page.getByText(/邀请记录暂不可用/)).toBeVisible();
  });
});

test.describe("notifications page (#747 C6)", () => {
  test("toggles email notifications without in-app column and saves preferences", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/notifications");

    await expect(page.getByText("站内")).toHaveCount(0);
    await page.getByRole("button", { name: /账户异常活动/ }).click();
    await page.getByLabel("阈值").fill("12.5");
    await page.getByLabel("Webhook 地址").fill("https://hooks.example.com/alerts");
    await page.locator(".qses", { hasText: "English" }).getByRole("button", { name: "选择" }).click();
    await page.getByRole("button", { name: "保存偏好", exact: true }).click();

    await expect(page.getByText("偏好已保存。")).toBeVisible();
    expect(state.notifications).toMatchObject({
      spend_anomaly: false,
      threshold: "12500000000",
      webhook_url: "https://hooks.example.com/alerts",
      language: "en",
    });
  });
});
