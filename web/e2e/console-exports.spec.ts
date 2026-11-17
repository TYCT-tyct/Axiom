import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";

// A CSV export belongs to the session that asked for it: when the browser passes to another account while the file is
// still out, the page abandons the request instead of saving the other account's data.

test.use({ timezoneId: "Asia/Shanghai" });

// What a page does when its window comes back to the front, which is when it asks again for the session.
const returnToWindow = (page: Page) =>
  page.evaluate(() => document.dispatchEvent(new Event("visibilitychange", { bubbles: true })));

const exports = [
  { path: "/console/calls", request: "/calls/export" },
  { path: "/console/statement", request: "/statement.csv" },
];

for (const exported of exports) {
  test(`an export of ${exported.path} that is out when another account signs in downloads nothing`, async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto(exported.path);
    let downloads = 0;
    page.on("download", () => downloads++);

    // Hold the export until the page has read the other account's session; the page then abandons its request.
    let answer!: () => void;
    const held = new Promise<void>((resolve) => (answer = resolve));
    await page.route(`**${exported.request}*`, async (route) => {
      await held;
      await route.fallback().catch(() => {});
    });
    const sent = page.waitForRequest((request) => request.url().includes(exported.request));
    const abandoned = page.waitForEvent("requestfailed", {
      predicate: (request) => request.url().includes(exported.request),
    });
    await page.getByRole("button", { name: "导出 CSV" }).click();
    await sent;

    state.account = 2;
    state.user.email = "other@example.com";
    await returnToWindow(page);
    await page.getByRole("button", { name: "账号设置" }).click();
    await expect(page.getByText("other@example.com")).toBeVisible();
    await abandoned;
    answer();
    expect(downloads).toBe(0);
  });
}
