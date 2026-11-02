import { expect, test } from "@playwright/test";
import { mockConsole } from "./console-api.ts";
import { signedInAs } from "./mock-auth.ts";

test.describe("models and prices", () => {
  test("the console shows the account's models, and the filters live in the address", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/models");
    await expect(page.getByRole("heading", { level: 1, name: "模型与价格" })).toBeVisible();
    const table = page.getByRole("table", { name: "模型与价格" });
    await expect(table.getByRole("row")).toHaveCount(5);

    await page.getByLabel("模型", { exact: true }).fill("openai/gpt-5");
    await page.getByRole("button", { name: "筛选" }).click();
    await expect(page).toHaveURL(/q=openai%2Fgpt-5/);
    await expect(table.getByRole("row")).toHaveCount(2);
    await page.reload();
    await expect(page.getByLabel("模型", { exact: true })).toHaveValue("openai/gpt-5");
    await expect(table.getByRole("row")).toHaveCount(2);

    await table.getByRole("button", { name: "详情" }).first().click();
    const endpoints = page.getByRole("table", { name: "端点" });
    await expect(endpoints).toBeVisible();
    await expect(endpoints.getByRole("row")).toHaveCount(3);
    await expect(endpoints.getByText("ga2b3c4d3", { exact: true })).toBeVisible();
    await expect(endpoints.getByText("已验证", { exact: true })).toBeVisible();
    const rates = page.getByRole("table", { name: /gpt-5/ });
    await expect(rates).toBeVisible();
    await expect(rates.getByText("每百万 token").first()).toBeVisible();
  });
});
