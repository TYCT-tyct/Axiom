import type { Page } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import type { AdminState } from "./admin-data.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The steps every admin page spec shares: a signed-in session against the mock, a dialog, and the change form it holds.

export async function open(
  page: Page,
  path: string,
  data: "usual" | "empty" | "worst" = "usual",
  over: Partial<AdminState> = {},
) {
  const state = await mockAdmin(page, data, over);
  signedInAs(state, "mfa");
  await page.goto(path);
  return state;
}

export const dialog = (page: Page) => page.getByRole("dialog");

// The reason every change asks for, and the button that sends it.
export async function send(page: Page, reason = "because the owner asked") {
  await dialog(page).getByLabel(words("change.reason")).fill(reason);
  await dialog(page).locator("button[type=submit]").click();
}

// The dialog has an icon button for closing and the result's own: the result's comes last.
export const close = (page: Page) =>
  dialog(page)
    .getByRole("button", { name: zhText("change.close") })
    .last()
    .click();

export const done = (page: Page) => dialog(page).getByText(zhText("change.done"));
