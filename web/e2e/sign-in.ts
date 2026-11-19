import type { Locator, Page } from "@playwright/test";
import { knownEmail, knownPassword } from "./mock-auth.ts";

// The page asks for the email, then the password, and sends both in one sign-in.
export async function signIn(page: Page, email = knownEmail, password = knownPassword) {
  await page.getByLabel("邮箱").fill(email);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "登录", exact: true }).click();
}

// A response the test lets through when it says: the route waits for release, then answers.
export function hold() {
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  return { held, release };
}

// Whether the page is locked while a request is out (LoginFrame's useLock): the form, the sea with its links and the
// language switch are inert.
export const locked = (page: Page) =>
  page.evaluate(() =>
    ["form", ".lg-sea", ".lg-top"].every((selector) => (document.querySelector(selector) as HTMLElement | null)?.inert),
  );

// A real press where the visitor would press, which an inert control does not take (a click Playwright dispatched on
// the element itself would reach it).
export async function pressAt(locator: Locator) {
  const box = await locator.boundingBox();
  if (!box) throw new Error("pressAt: the element is not on screen");
  await locator.page().mouse.click(box.x + box.width / 2, box.y + box.height / 2);
}
