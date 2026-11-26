import { expect, test } from "@playwright/test";
import { open } from "./admin-page.ts";
import { acct } from "./admin-scenes.ts";
import { apps } from "./apps.ts";
import { words } from "./words.ts";

// The users page against the mocked API: an account is reached through its owner, as no operation lists accounts.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

test.describe("accounts", () => {
  test("an account is reached through its owner, and a row opens its drawer with the three limits", async ({
    page,
  }) => {
    await open(page, "/users/accounts");
    await page.getByRole("row", { name: /user1@example\.com/ }).click();
    await expect(page.getByRole("dialog").first().getByRole("heading", { name: "user1@example.com" })).toBeVisible();
    expect(new URL(page.url()).searchParams.get("selected")).toBe(acct(1));
    for (const limit of [
      "settings.keyLimits.field.max_keys",
      "settings.keyLimits.field.creations_per_hour",
      "settings.workspaceLimit.field",
    ]) {
      await expect(page.getByRole("dialog").getByText(words(limit))).toBeVisible();
    }
    // J steps to the next account without closing the drawer
    await page.keyboard.press("j");
    await expect.poll(() => new URL(page.url()).searchParams.get("selected")).not.toBe(acct(1));
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });
});
