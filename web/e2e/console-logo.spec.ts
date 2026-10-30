import { expect, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";

// The site's logo on the console's sign-in page, from getSite; the console reads the same site as its shell, so the
// page shows the brand before a session exists.
test.use({ baseURL: apps[1].url });

const logoSVG =
  "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCI+PHBhdGggZmlsbD0iIzEyMyIgZD0iTTAgMGgyNHYyNEgweiIvPjwvc3ZnPg==";

test("the console sign-in page shows the site's logo", async ({ page }) => {
  const state = await mockConsole(page);
  state.site = { ...state.site, name: "ConsoleDemo", logo_url: logoSVG };
  await page.goto("/sign-in");
  await expect(page.locator(`img[src='${logoSVG}']`)).toBeVisible();
  await expect(page.getByText("ConsoleDemo")).toBeVisible();
});

test("two site origins show different brands", async ({ page, browser }) => {
  const first = await mockConsole(page);
  first.site = { ...first.site, name: "WebDemo", logo_url: logoSVG };
  const secondPage = await browser.newPage();
  const second = await mockConsole(secondPage);
  second.site = { ...second.site, name: "ConsoleDemo", logo_url: logoSVG };

  const secondURL = new URL(apps[1].url);
  secondURL.hostname = "127.0.0.1";
  await Promise.all([page.goto("/sign-in"), secondPage.goto(new URL("sign-in", secondURL).href)]);

  await expect(page.getByText("WebDemo")).toBeVisible();
  await expect(secondPage.getByText("ConsoleDemo")).toBeVisible();
  await expect(page.locator(`img[src='${logoSVG}']`)).toBeVisible();
  await expect(secondPage.locator(`img[src='${logoSVG}']`)).toBeVisible();
  await secondPage.close();
});
