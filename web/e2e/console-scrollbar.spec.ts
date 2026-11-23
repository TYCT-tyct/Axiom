import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";

// With a scrollbar that takes room, as on Windows (headless Chromium hides it unless asked), the console's frame stays
// put: from a page that scrolls to one that does not, and while a dialog holds the page still.
test.use({ launchOptions: { ignoreDefaultArgs: ["--hide-scrollbars"] } });

const edges = (page: Page) =>
  page.evaluate(() => ({
    heading: document.querySelector("h1")?.getBoundingClientRect().left,
    bar: document.querySelector("header")?.getBoundingClientRect().right,
  }));

test("the frame keeps its place whether the page scrolls or a dialog is open", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 600 });
  const state = await mockConsole(page);
  signedInAs(state);
  await page.goto("/console/calls");
  await expect(page.getByRole("heading", { level: 1, name: "调用记录" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollHeight > innerHeight)).toBe(true);
  const scrolling = await edges(page);

  await page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "API Keys" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "API Keys" })).toBeVisible();
  await page.setViewportSize({ width: 1280, height: 2000 });
  expect(await page.evaluate(() => document.documentElement.scrollHeight > innerHeight)).toBe(false);
  const still = await edges(page);
  expect(still.bar).toBe(scrolling.bar);

  await page.setViewportSize({ width: 1280, height: 400 });
  const before = await edges(page);
  await page.getByRole("button", { name: "创建 key" }).click();
  await expect(page.getByRole("dialog", { name: "创建 key" })).toBeVisible();
  expect(await edges(page)).toEqual(before);
});
