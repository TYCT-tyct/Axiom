import { expect, test } from "@playwright/test";
import { contentSecurityPolicy } from "../vite.shared.ts";
import { apps } from "./apps.ts";

for (const app of apps) {
  test(`the ${app.name} works under the content security policy`, async ({ page }) => {
    const errors: string[] = [];
    page.on("console", (message) => {
      if (message.type() === "error") {
        errors.push(message.text());
      }
    });
    page.on("pageerror", (error) => errors.push(error.message));
    // The console asks for the running version; any answer will do here.
    await page.route("**/api/console/v1/version", (route) => route.fulfill({ json: { version: "" } }));

    const response = await page.goto(app.url);
    const policy = response?.headers()["content-security-policy"];
    expect(policy).toBe(contentSecurityPolicy);
    expect(policy).not.toContain("unsafe-");
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    expect(errors).toEqual([]);
  });

  test(`the ${app.name} shows the saved theme before its scripts run`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: "dark" });
    await page.addInitScript(() => localStorage.setItem("axiom.theme", "light"));
    // Holding back the app's bundle shows what the first paint gets.
    await page.route(/\/assets\/index-[\w-]+\.js$/, (route) => route.abort());
    await page.goto(app.url);
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  });
}

test("the website's buttons open the console", async ({ page }) => {
  await page.route("**/api/console/v1/version", (route) => route.fulfill({ json: { version: "" } }));
  await page.goto(apps[0].url);
  await page.getByRole("link", { name: "获取 API key" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
});
