import type { Page } from "@playwright/test";
import { knownEmail, knownPassword } from "./mock-auth.ts";

// The page asks for the email, then the password, and sends both in one sign-in.
export async function signIn(page: Page, email = knownEmail, password = knownPassword) {
  await page.getByLabel("邮箱").fill(email);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "登录", exact: true }).click();
}
