import { expect, test } from "@playwright/test";
import { mockConsole } from "./console-api.ts";
import { signedInAs } from "./mock-auth.ts";

test.describe("orders", () => {
  // Below sm the amount columns are hidden and their figures move under the order; the fee must move with them.
  for (const width of [360, 1280]) {
    test(`an order's paid total and fee are visible at ${width}px`, async ({ page }) => {
      const state = await mockConsole(page);
      signedInAs(state);
      await page.setViewportSize({ width, height: 900 });
      await page.goto("/console/orders");
      const row = page.getByRole("table", { name: "订单" }).getByRole("row").nth(1);
      await expect(row.getByText("¥73.73").filter({ visible: true })).toHaveCount(1);
      await expect(row.getByText("手续费 ¥0.73").filter({ visible: true })).toHaveCount(1);
    });
  }
});
