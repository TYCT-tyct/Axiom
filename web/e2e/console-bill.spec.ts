import { expect, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";

test.use({ timezoneId: "Asia/Shanghai" });

test.describe("billing suite", () => {
  test("top-up retries with the same Idempotency-Key after timeout and completes payment verification", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    const beforeCount = state.orders.length;

    const keys: string[] = [];
    let timedOutOnce = false;
    await page.route("**/api/console/v1/orders", async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      keys.push(route.request().headers()["idempotency-key"] ?? "");
      if (!timedOutOnce) {
        timedOutOnce = true;
        return route.abort("timedout");
      }
      return route.fallback();
    });

    await page.goto("/console/balance");
    await page.getByRole("button", { name: "充 US$25.00" }).click();
    await expect(page.getByText("US$37.00")).toBeVisible();
    await expect(page.getByText("¥73.73（含手续费 ¥0.73）")).toBeVisible();

    await page.getByRole("button", { name: /^去支付/ }).click();
    await expect(page.getByText("重试同一请求")).toBeVisible();

    await page.getByRole("button", { name: "重试同一请求" }).click();
    await expect(page.getByRole("button", { name: "我已付款" })).toBeVisible();
    expect(keys).toHaveLength(2);
    expect(keys[0]).toBeTruthy();
    expect(keys[1]).toBe(keys[0]);
    expect(state.orders).toHaveLength(beforeCount + 1);

    await page.getByRole("button", { name: "我已付款" }).click();
    await expect(page.getByText("已支付")).toBeVisible();
  });

  test("daily statement renders 不可用 when closing_balance is null, never 0", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/statement");
    await page.getByText("按天汇总").click();
    const table = page.getByRole("table", { name: "按天汇总" });
    await expect(table).toBeVisible();
    const rowWithUnknown = table.getByRole("row").filter({ hasText: "2026-10-13" });
    await expect(rowWithUnknown).toContainText("不可用");
    await expect(rowWithUnknown.getByRole("cell").nth(2)).toHaveText("不可用");
  });

  test("redeem explains invalid/used failures while preserving typed input, then redeems a valid code", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/redeem");
    const input = page.getByRole("textbox", { name: "兑换码" });
    await input.fill("USED-CODE");
    await page.getByRole("button", { name: "兑换", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("已经被兑换过");
    await expect(input).toHaveValue("USED-CODE");

    await input.fill("VALID-2026");
    await page.getByRole("button", { name: "兑换", exact: true }).click();
    await expect(page.getByText("已向余额加入 US$5.00（尾号 2026）。")).toBeVisible();
  });

  test("plans & charge order list active windows and update billing preference", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/balance#plans");
    await expect(page.getByText("Pro Monthly")).toBeVisible();
    await expect(page.getByText("生效中")).toBeVisible();
    await expect(page.getByText("US$37.75")).toBeVisible();

    await page.getByRole("combobox", { name: "付费方式" }).selectOption("wallet_first");
    await page.getByRole("button", { name: "保存" }).click();
    await expect(page.getByText("当前为账户单独设置")).toBeVisible();
    expect(state.billingPreference.preference).toBe("wallet_first");
  });

  test("top-up modal traps Tab and Shift+Tab focus and returns focus to trigger on Escape", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/balance");
    const trigger = page.getByRole("button", { name: "充值" });
    await trigger.focus();
    await trigger.click();

    const dialog = page.getByRole("dialog", { name: "充值" });
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('input[inputmode="decimal"]')).toBeFocused();

    for (const key of ["Tab", "Shift+Tab"]) {
      for (let i = 0; i < 16; i += 1) {
        await page.keyboard.press(key);
        const inModalOrBrowser = await page.evaluate(
          () => document.activeElement === document.body || Boolean(document.activeElement?.closest("dialog")),
        );
        expect(inModalOrBrowser).toBe(true);
      }
    }

    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });

  test("printing a receipt keeps its body visible while hiding navigation and action buttons", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/orders");
    await page.getByRole("link", { name: "收据" }).first().click();
    const paidOrder = state.orders.find((o) => o.state === "paid");
    expect(paidOrder).toBeDefined();
    await expect(page.getByText(paidOrder?.id ?? "")).toBeVisible();

    await page.emulateMedia({ media: "print" });
    await expect(page.locator('nav[aria-label="账单导航"]')).toBeHidden();
    await expect(page.getByRole("link", { name: "返回订单" })).toBeHidden();
    await expect(page.getByRole("button", { name: "打印或存为 PDF" })).toBeHidden();
    await expect(page.getByText(paidOrder?.id ?? "")).toBeVisible();
  });
});
