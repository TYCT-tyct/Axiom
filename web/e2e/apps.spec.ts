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

  // The server's caching and compression (internal/platform/webui): the entry page is never stored, and what its name
  // pins to its content is stored for good and sent in the best encoding the client accepts.
  test(`the ${app.name}'s entry page is not stored and its scripts are stored for good, compressed`, async ({
    request,
  }) => {
    const page = await request.get(app.url);
    expect(page.headers()["cache-control"]).toBe("no-store");
    const html = await page.text();
    const scripts = [...html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"/g)].map(([, src = ""]) => src);
    for (const src of scripts) {
      const asset = await request.get(new URL(src, app.url).href, { headers: { "Accept-Encoding": "br, gzip" } });
      expect(asset.headers()["cache-control"]).toBe("public, max-age=31536000, immutable");
      expect(asset.headers()["content-encoding"]).toBe("br");
      expect(asset.headers().vary).toBe("Accept-Encoding");
    }
    const plain = await request.get(new URL(scripts[0] ?? "", app.url).href, {
      headers: { "Accept-Encoding": "identity" },
    });
    expect(plain.headers()["content-encoding"]).toBeUndefined();
  });

  // A script of a build that is gone must fail as the 404 it is: served as the entry page, it would pass for a script.
  test(`the ${app.name} answers a script it does not have with a 404, not a page`, async ({ request }) => {
    const missing = await request.get(new URL("/assets/index-ZZZZZZZZ.js", app.url).href, {
      headers: { Accept: "text/html" },
    });
    expect(missing.status()).toBe(404);
    expect(missing.headers()["content-type"]).not.toContain("html");
    expect(missing.headers()["cache-control"]).toBe("no-store");
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

// The admin's files are on the admin listener only: its own script is a 404 on the public one.
test("the public listener has none of the admin's files", async ({ request }) => {
  const admin = await (await request.get(apps[2].url)).text();
  const script = /<script\b[^>]*\bsrc="(\/assets\/index-[^"]+\.js)"/.exec(admin)?.[1] ?? "";
  expect(script).not.toBe("");
  expect((await request.get(new URL(script, apps[2].url).href)).status()).toBe(200);
  for (const app of [apps[0], apps[1]]) {
    expect((await request.get(new URL(script, app.url).href)).status()).toBe(404);
  }
});
