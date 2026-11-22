import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { collectErrors } from "./collect-errors.ts";
import { mockConsole } from "./console-api.ts";

// The models page opens the console's sign-in pages over itself, in its own document (site/src/home/models-sign-in.tsx):
// the console's address, Esc and the browser's back and forward, and steps in place.
const models = `${apps[0].url}models`;
const layer = (page: Page) => page.locator(".lg");
const sameDocument = (page: Page) =>
  page.evaluate(() => (window as unknown as { sameDocument?: boolean }).sameDocument === true);

async function openModels(page: Page) {
  await mockConsole(page);
  // the sign-in pages' code is asked for once the models are drawn; the press waits for it to have come
  const pages = page.waitForResponse(/\/assets\/register-[\w-]+\.js$/);
  await page.goto(models);
  await expect(page.getByRole("heading", { level: 1, name: "模型与价格" })).toBeVisible();
  await pages;
  await page.evaluate(() => Object.assign(window, { sameDocument: true }));
}

test("the models page opens sign-in over itself, and Esc, back and forward take it away and bring it back", async ({
  page,
}) => {
  const errors = collectErrors(page);
  await openModels(page);
  const signIn = page.getByRole("banner").getByRole("link", { name: "登录" });
  await signIn.click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await expect(layer(page).locator(".lg-form input").first()).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(models);
  await expect(layer(page)).toHaveCount(0);
  // the models page is in reach again
  await expect(signIn).toBeEnabled();

  await page.goForward();
  await expect(layer(page).locator(".lg-form input").first()).toBeVisible();
  await page.goBack();
  await expect(layer(page)).toHaveCount(0);
  await expect(page).toHaveURL(models);
  expect(await sameDocument(page)).toBe(true);
  // the gate asks who is signed in: a visitor who is not is answered 401, which the browser logs
  expect(errors.filter((error) => !error.includes("401"))).toEqual([]);
});

test("a step goes on in place over the models page, and back returns to the step before", async ({ page }) => {
  await openModels(page);
  await page.getByRole("banner").getByRole("link", { name: "登录" }).click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await layer(page).getByRole("link", { name: "注册" }).click();
  await expect(page).toHaveURL(/\/console\/register$/);
  await expect(layer(page).getByRole("heading", { level: 1, name: "注册" })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await expect(layer(page)).toHaveCount(1);
  expect(await sameDocument(page)).toBe(true);
});

test("a press before the sign-in pages' code has come opens the console's own page", async ({ page }) => {
  await mockConsole(page);
  await page.route(/\/assets\/sign-in-host-[\w-]+\.js$/, () => new Promise<void>(() => {}));
  await page.goto(models);
  await page.getByRole("banner").getByRole("link", { name: "登录" }).click();
  await expect(page).toHaveURL(/\/console\/sign-in/);
  expect(await sameDocument(page)).toBe(false);
});
