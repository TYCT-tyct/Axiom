import { expect, test } from "@playwright/test";
import { mockConsole } from "./console-api.ts";
import { open, running, siteSignIn } from "./fx-helpers.ts";
import { knownEmail, knownPassword } from "./mock-auth.ts";

// The way from the website's sign-in pages into the console, a page load (site/src/home/sign-in-host.tsx): the console's
// code is asked for while the sign-in page is up, and the sign-in step stays on screen until the console replaces it.
const consoleEntry = /\/console\/assets\/index-[\w-]+\.js$/;

test("a sign-in page on show asks for the console's code before anyone signs in", async ({ page }) => {
  await mockConsole(page);
  await open(page);
  await running(page);
  await expect(page.locator(".tr .lg")).toBeAttached();
  await page.evaluate(() => Object.assign(window, { sameDocument: true }));
  const ahead = page.waitForRequest(consoleEntry);
  await siteSignIn(page).click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await ahead;
  expect(await page.evaluate(() => (window as unknown as { sameDocument?: boolean }).sameDocument)).toBe(true);
});

test("the sign-in step stays on screen while its new member's console comes", async ({ page }) => {
  await mockConsole(page);
  await open(page);
  await running(page);
  await expect(page.locator(".tr .lg")).toBeAttached();
  await siteSignIn(page).click();
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill(knownPassword);
  // whether the step left the page before the page itself went, and whether its form still waited when it went, told
  // to the console's page through the tab's storage
  await page.evaluate(() => {
    let left = false;
    new MutationObserver(() => {
      if (!document.querySelector(".lg-step")) left = true;
    }).observe(document.body, { subtree: true, childList: true });
    addEventListener("pagehide", () => {
      const waiting = document.querySelector(".lg form")?.getAttribute("aria-busy") === "true";
      sessionStorage.setItem("stepLeft", JSON.stringify({ left, waiting }));
    });
  });
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  expect(JSON.parse((await page.evaluate(() => sessionStorage.getItem("stepLeft"))) ?? "null")).toEqual({
    left: false,
    waiting: true,
  });
});
