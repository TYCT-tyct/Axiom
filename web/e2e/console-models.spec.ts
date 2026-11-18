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
    const endpoints = page.getByRole("table", { name: "端点", exact: true });
    await expect(endpoints).toBeVisible();
    await expect(endpoints.getByRole("row")).toHaveCount(5);
    // The codename also appears in the closed details summary; scope the identity check to the endpoint row.
    const firstEndpoint = endpoints.getByRole("row").nth(1);
    await expect(firstEndpoint.getByText("ga2b3c4d3", { exact: true })).toBeVisible();
    await expect(firstEndpoint.getByText("已验证", { exact: true })).toBeVisible();
    await expect(firstEndpoint.getByText("流式输出 是", { exact: true })).toBeVisible();
    await expect(endpoints.getByText("流式输出 否", { exact: true })).toBeVisible();

    await endpoints.getByText("完整价目 ga2b3c4d3", { exact: true }).click();
    const endpointRates = page.getByRole("table", { name: "gpt-5 ga2b3c4d3 端点价格" });
    await expect(endpointRates).toBeVisible();
    await expect(endpointRates.getByText("分档口径", { exact: true })).toBeVisible();
    const bandRate = endpointRates.getByRole("row").filter({ hasText: "超过 128,000" });
    await expect(bandRate).toHaveCount(1);
    await expect(bandRate).toContainText("10.00");
    await expect(endpointRates.getByText("优先", { exact: true })).toBeVisible();
    const endpointSearch = page.getByRole("table", { name: "gpt-5 ga2b3c4d3 联网搜索价格" });
    await expect(endpointSearch).toBeVisible();
    await expect(endpointSearch.getByRole("cell", { name: /exa/ })).toHaveCount(1);

    const rates = page.getByRole("table", { name: /模型价格/ });
    await expect(rates).toBeVisible();
    await expect(rates.getByText("每百万 token").first()).toBeVisible();
  });

  test("endpoint attributes stay visible from phone to desktop", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/models");
    await page.getByRole("button", { name: "详情" }).first().click();
    const endpoints = page.getByRole("table", { name: "端点", exact: true });
    for (const width of [390, 800, 1280, 1600]) {
      await page.setViewportSize({ width, height: 900 });
      await expect(endpoints.getByText("200,000").filter({ visible: true })).toHaveCount(1);
      await expect(endpoints.getByText("64,000").filter({ visible: true })).toHaveCount(1);
      await expect(endpoints.getByText("工具", { exact: true }).filter({ visible: true }).first()).toBeVisible();
      await expect(endpoints.getByText("流式输出 否", { exact: true })).toBeVisible();
    }
  });

  test("announcements open on demand even when the model list fails", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.route("**/api/console/v1/models/account", (route) =>
      route.fulfill({ status: 500, json: { error: { code: "internal", message: "Model list failed" } } }),
    );
    let reads = 0;
    await page.route("**/api/console/v1/announcements", (route) => {
      reads += 1;
      return route.fulfill({
        json: {
          data: [
            { id: "first", title: "Maintenance", body: "The first announcement." },
            { id: "second", title: "Service update", body: "The second announcement." },
          ],
        },
      });
    });

    await page.goto("/console/models");
    await expect(page.getByRole("heading", { name: "没能加载" })).toBeVisible();
    await expect(page.getByRole("button", { name: "重试" })).toBeVisible();
    const announcements = page.getByText("站内公告", { exact: true });
    await expect(announcements).toBeVisible();
    expect(reads).toBe(0);
    await announcements.press("Enter");
    await expect(page.getByText("The first announcement.")).toBeVisible();
    await expect(page.getByText("The second announcement.")).toBeVisible();
    expect(reads).toBe(1);
    await expect(page.getByRole("heading", { name: "没能加载" })).toBeVisible();
  });
});
