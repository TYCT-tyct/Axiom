import { expect, type Page, test } from "@playwright/test";
import { close, dialog, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { words, zhText } from "./words.ts";

// The target editor and the dashboard's target lines against the mocked API (admin-api.ts, admin-fixtures.ts).

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const press = (page: Page, key: string) => page.getByRole("button", { name: words(key) }).click();

test.describe("business targets", () => {
  test("the dashboard compares each target and the editor replaces the month", async ({ page }) => {
    const state = await open(page, "");
    await expect(page.getByText(zhText("overview.targets.description"))).toBeVisible();
    await expect(page.getByText(words("overview.targets.target")).first()).toBeVisible();

    await press(page, "overview.targets.edit");
    const editor = dialog(page);
    await expect(editor.getByLabel(words("overview.targets.calls"))).toHaveValue("45");
    await editor.getByLabel(words("overview.targets.calls")).fill("50");
    await editor.getByLabel(words("overview.targets.errors")).fill("5");
    await send(page);
    await expect(editor.getByText(zhText("overview.targets.saved"))).toBeVisible();

    const month = new Date().toISOString().slice(0, 7);
    expect(state.targets.get(month)?.calls).toBe(50);
    expect(state.targets.get(month)?.errors).toBe(5);

    await close(page);
    await expect(page.getByText(words("overview.targets.ahead")).first()).toBeVisible();
  });

  test("a month with no target says so and still offers the editor", async ({ page }) => {
    await open(page, "", "empty");
    await expect(page.getByText(zhText("overview.targets.empty"))).toBeVisible();
    await expect(page.getByRole("button", { name: words("overview.targets.edit") })).toBeVisible();
  });

  test("the dashboard and the editor fit a 360px screen", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 900 });
    await open(page, "", "worst");
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);

    await press(page, "overview.targets.edit");
    const box = await dialog(page).boundingBox();
    expect(box?.x ?? -1).toBeGreaterThanOrEqual(0);
    expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(360);
  });
});
