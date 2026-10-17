import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";
import { knownPassword } from "./console-data.ts";

// The security page against the mocked API (console-api.ts): two-step verification and the codes it hands out once.

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

// What a page does when its window comes back to the front, which is when it asks again for the session.
const returnToWindow = (page: Page) =>
  page.evaluate(() => document.dispatchEvent(new Event("visibilitychange", { bubbles: true })));

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

  test("the codes are shown even when the session cannot be read again, and it is read once the server can", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/security");
    await expect(page.getByText("未开启")).toBeVisible();
    let reads = 0;
    const session = "**/api/console/v1/session";
    await page.route(session, (route) => {
      reads += 1;
      return route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } });
    });

    await page.getByRole("button", { name: "开启两步验证" }).click();
    const dialog = page.getByRole("dialog", { name: "开启两步验证" });
    await dialog.getByLabel("密码").fill(knownPassword);
    await dialog.getByLabel("验证器里的 6 位验证码").fill("123456");
    await dialog.getByRole("button", { name: "开启", exact: true }).click();

    // The server has changed the factor and will not say the codes again. The session is asked for three times (the
    // query's retries) and fails each time; the codes stay on screen throughout.
    const codes = page.getByRole("dialog", { name: "保存你的恢复码" });
    await expect(codes.getByRole("listitem")).toHaveCount(10);
    await expect.poll(() => reads, { timeout: 10_000 }).toBe(3);
    await expect(codes.getByRole("listitem")).toHaveCount(10);
    await codes.getByRole("button", { name: "我已保存" }).click();

    await page.unroute(session);
    await returnToWindow(page);
    await expect(page.getByText("已开启")).toBeVisible();
  });

  for (const flow of [
    {
      name: "turning it on",
      start: async (page: Page, state: Awaited<ReturnType<typeof mockConsole>>) => {
        signedInAs(state);
        await page.goto("/console/security");
        await page.getByRole("button", { name: "开启两步验证" }).click();
        const dialog = page.getByRole("dialog", { name: "开启两步验证" });
        await dialog.getByLabel("密码").fill(knownPassword);
        return {
          dialog,
          submit: dialog.getByRole("button", { name: "开启", exact: true }),
          url: "**/auth/totp/confirm",
        };
      },
    },
    {
      name: "making new recovery codes",
      start: async (page: Page, state: Awaited<ReturnType<typeof mockConsole>>) => {
        state.mfaEnabled = true;
        signedInAs(state, "mfa");
        await page.goto("/console/security");
        await page.getByRole("button", { name: "重新生成恢复码" }).click();
        const dialog = page.getByRole("dialog", { name: "重新生成恢复码" });
        return { dialog, submit: dialog.getByRole("button", { name: "重新生成" }), url: "**/auth/recovery-codes" };
      },
    },
  ]) {
    test(`${flow.name} cannot be dismissed while the request is out, so the codes it answers with are shown`, async ({
      page,
    }) => {
      const state = await mockConsole(page);
      const { dialog, submit, url } = await flow.start(page, state);
      // The request is held until the checks below are done, not for a fixed time: on a loaded runner they can take
      // longer than any fixed delay, and the answer would replace the dialog before they look at it.
      let answer!: () => void;
      const held = new Promise<void>((resolve) => (answer = resolve));
      await page.route(url, async (route) => {
        await held;
        await route.fallback();
      });
      await dialog.getByLabel("验证器里的 6 位验证码").fill("123456");
      await submit.click();
      await expect(submit).toBeDisabled();

      await page.keyboard.press("Escape");
      await page.mouse.click(5, 5);
      await expect(dialog.getByRole("button", { name: "取消" })).toBeDisabled();
      await expect(dialog.getByRole("button", { name: "关闭" })).toHaveCount(0);
      await expect(dialog).toBeVisible();
      answer();

      const codes = page.getByRole("dialog", { name: "保存你的恢复码" });
      await expect(codes.getByRole("listitem")).toHaveCount(10);
    });
  }
});
