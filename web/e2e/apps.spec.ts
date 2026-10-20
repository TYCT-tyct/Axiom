import { expect, test } from "@playwright/test";
import { contentSecurityPolicy } from "../vite.shared.ts";
import { mockAdmin } from "./admin-api.ts";
import { apps } from "./apps.ts";
import { collectErrors } from "./collect-errors.ts";
import { mockConsole } from "./console-api.ts";
import { signedInAs } from "./mock-auth.ts";
import { pageHeading } from "./page-heading.ts";

for (const app of apps) {
  test(`the ${app.name} works under the content security policy`, async ({ page }) => {
    const errors = collectErrors(page);
    // The console and the admin ask their APIs who is signed in, and show the page the answer leads to.
    signedInAs(await mockConsole(page));
    signedInAs(await mockAdmin(page), "mfa");

    const response = await page.goto(app.url);
    const policy = response?.headers()["content-security-policy"];
    expect(policy).toBe(contentSecurityPolicy);
    expect(policy).not.toContain("unsafe-");
    await pageHeading(page, app.name);
    expect(errors).toEqual([]);
  });

  // Scripts are cached long and only the entry page is not (docs/frontend.md「工程要求」), so a script under a name that
  // does not change with its content would keep serving an old version.
  test(`the ${app.name}'s entry page loads only content-hashed scripts`, async ({ request }) => {
    const html = await (await request.get(app.url)).text();
    const scripts = [...html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"/g)].map(([, src]) => src);
    expect(scripts).not.toEqual([]);
    for (const src of scripts) {
      expect(src).toMatch(/\/assets\/[\w-]+-[\w-]{8}\.js$/);
    }
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
  signedInAs(await mockConsole(page));
  await page.goto(apps[0].url);
  await page.getByRole("link", { name: "获取 API key" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
});
