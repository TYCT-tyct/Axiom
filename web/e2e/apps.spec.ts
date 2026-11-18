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
    await pageHeading(page);
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

test("the website reaches models and prices on a phone", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await mockConsole(page);
  await page.goto(apps[0].url);
  // a phone's header has no room for the pages: they are in the menu sheet
  await page.getByRole("banner").getByRole("link", { name: "菜单" }).click();
  await page.locator(".sheet").getByRole("link", { name: "模型", exact: true }).click();
  await expect(page).toHaveURL(/\/models$/);
  await expect(page.getByRole("heading", { level: 1, name: "模型与价格" })).toBeVisible();
});

test("the website lists models and prices and filters them in the address", async ({ page }) => {
  await mockConsole(page);
  await page.goto(`${apps[0].url}models`);
  await expect(page.getByRole("heading", { level: 1, name: "模型与价格" })).toBeVisible();
  const table = page.getByRole("table", { name: "模型与价格" });
  await expect(table.getByRole("row")).toHaveCount(5);

  await page.getByLabel("模型", { exact: true }).fill("openai/gpt-5");
  await page.getByRole("button", { name: "筛选" }).click();
  await expect(page).toHaveURL(/q=openai%2Fgpt-5/);
  await expect(table.getByRole("row")).toHaveCount(2);

  await page.getByRole("button", { name: "重置" }).click();
  await expect(table.getByRole("row")).toHaveCount(5);
  await table.getByRole("button", { name: "详情" }).first().click();
  await expect(page.getByRole("table", { name: /模型价格/ })).toBeVisible();
});

test("the website's sign-in and sign-up open the console's pages", async ({ page }) => {
  await mockConsole(page);
  await page.goto(apps[0].url);
  await page.getByRole("banner").getByRole("link", { name: "登录", exact: true }).click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await page.goto(apps[0].url);
  // the first screen's: the steps screen below has the same way on
  await page.locator(".hs > main").getByRole("link", { name: "注册，获取API key" }).click();
  await expect(page).toHaveURL(/\/console\/register$/);
});

// The site's own name and logo in the home page's header, as on its other pages (docs/frontend.md「主题与自由度」);
// a logo that fails to load leaves the board's mark.
const logoSVG =
  "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCI+PHBhdGggZmlsbD0iIzEyMyIgZD0iTTAgMGgyNHYyNEgweiIvPjwvc3ZnPg==";
for (const width of [1440, 390]) {
  test(`the website's home page shows the site's brand at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const state = await mockConsole(page);
    state.site = { ...state.site, name: "WebDemo", logo_url: logoSVG };
    await page.goto(apps[0].url);
    const header = page.getByRole("banner").getByRole("link", { name: "WebDemo" });
    await expect(header.locator(`img[src='${logoSVG}']`)).toBeVisible();
    await expect(page.locator(".isl .im")).toHaveAttribute("aria-label", "WebDemo");

    state.site = { ...state.site, logo_url: "/missing-logo.svg" };
    await page.reload();
    await expect(page.getByRole("banner").getByRole("link", { name: "WebDemo" }).locator("svg")).toBeVisible();
  });
}
