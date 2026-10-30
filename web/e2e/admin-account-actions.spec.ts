import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { acct } from "./admin-scenes.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The account's state controls against the mocked API: each change asks for its reason and carries the key the mock
// checks, and a node of the release before the sign-in switch leaves that part unavailable without taking the rest.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

async function open(page: Page, path: string) {
  const state = await mockAdmin(page, "usual");
  signedInAs(state, "mfa");
  await page.goto(path);
  return state;
}

const dialog = (page: Page) => page.getByRole("dialog");
const press = (page: Page, key: string, nth = 0) => page.getByRole("button", { name: words(key) }).nth(nth);

// The reason every change asks for, and the button that sends it.
async function send(page: Page, reason = "because the owner asked") {
  await dialog(page).getByLabel(words("change.reason")).fill(reason);
  await dialog(page).locator("button[type=submit]").click();
}

const close = (page: Page) =>
  dialog(page)
    .getByRole("button", { name: zhText("change.close") })
    .last()
    .click();
const done = (page: Page) => dialog(page).getByText(zhText("change.done"));

test.describe("account state", () => {
  test("the account page pauses calls, disables sign-in and moves the tier", async ({ page }) => {
    const state = await open(page, `/accounts/${acct(1)}`);
    await press(page, "users.account.suspend").click();
    await send(page);
    await expect(done(page)).toBeVisible();
    await close(page);
    expect(state.accounts.get(acct(1))?.calls_suspended).toBe(true);
    await expect(page.getByText(zhText("users.account.callsSuspended")).first()).toBeVisible();

    await press(page, "users.account.resume").click();
    await send(page);
    await close(page);
    expect(state.accounts.get(acct(1))?.calls_suspended).toBe(false);
    await expect(page.getByText(zhText("users.account.callsActive")).first()).toBeVisible();

    await press(page, "users.account.disableSignIn").click();
    await send(page);
    await close(page);
    expect(state.accounts.get(acct(1))?.sign_in_disabled).toBe(true);
    await expect(page.getByText(zhText("users.account.signInDisabled")).first()).toBeVisible();

    const tiers = page.waitForRequest((request) => new URL(request.url()).pathname.endsWith("/tiers"));
    await press(page, "users.account.changeTier").click();
    expect(new URL((await tiers).url()).searchParams.get("site_id")).toBe(state.accounts.get(acct(1))?.site_id);
    await dialog(page).locator("select[name=tier_id]").selectOption({ label: "Pro" });
    await send(page);
    await expect(done(page)).toBeVisible();
    await close(page);
    expect(state.accounts.get(acct(1))?.tier.name).toBe("Pro");
    await expect(page.getByText("Pro").first()).toBeVisible();
  });

  test("a node of the release before the sign-in switch shows that part as unavailable", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    const account = state.accounts.get(acct(1));
    if (account) {
      (account as { sign_in_disabled?: unknown }).sign_in_disabled = undefined;
    }
    await page.goto(`/accounts/${acct(1)}`);
    await expect(page.getByText(zhText("users.account.unavailable"))).toBeVisible();
    await expect(press(page, "users.account.disableSignIn")).toHaveCount(0);
    await expect(press(page, "users.account.suspend")).toHaveCount(1);
  });
});
