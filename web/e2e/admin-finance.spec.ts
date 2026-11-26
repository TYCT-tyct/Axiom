import { expect, test } from "@playwright/test";
import { uuid } from "./admin-data.ts";
import { close, dialog, done, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { freeze } from "./design/stage.ts";
import { words, zhText } from "./words.ts";

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

test("a payment investigation reads exact amounts and saves its outcome without a funds operation", async ({
  page,
}) => {
  // The old address leads to the tab, and a row opens the record in a drawer.
  const state = await open(page, "/payment-discrepancies");
  await expect.poll(() => new URL(page.url()).pathname).toBe("/orders/discrepancies");
  await page.getByRole("row").nth(1).click();
  await expect(page.getByText("9007199254740993", { exact: true }).first()).toBeVisible();
  await page.getByRole("button", { name: words("discrepancies.investigate") }).click();
  await send(page, "Gateway reported a different amount; no manual credit was issued.");
  await expect(done(page)).toBeVisible();
  await close(page);
  await expect(page.getByText("Gateway reported a different amount; no manual credit was issued.")).toBeVisible();
  await expect(page.getByRole("dialog").getByText(words("discrepancies.state.resolved"))).toBeVisible();
  expect(state.log.filter((entry) => entry.startsWith("POST "))).toEqual([
    `POST /payments/discrepancies/${uuid(33, 1)}/investigate`,
  ]);
  if (process.env.AXIOM_ADMIN_SHOTS) {
    await freeze(page);
    await page.screenshot({ path: `${process.env.AXIOM_ADMIN_SHOTS}/payment-discrepancy.png`, fullPage: true });
  }
});

test("exchange rates use exact terms and the current version and expose immutable history", async ({ page }) => {
  // The page's old address lands on its tab.
  const state = await open(page, "/exchange-rates");
  await expect(page).toHaveURL(/\/pricing\?tab=rates$/);
  await page.getByRole("button", { name: words("exchangeRates.edit") }).click();
  await dialog(page).locator("[name=numerator]").fill("9007199254740993");
  await dialog(page).locator("[name=denominator]").fill("63050394783186951");
  const write = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith("/pricing/exchange-rates"));
  await send(page, "recheck the rate near the recorded value");
  expect((await write).postDataJSON()).toMatchObject({
    expected_version: 2,
    kind: "adjustment",
    numerator: "9007199254740993",
    denominator: "63050394783186951",
  });
  await expect(done(page)).toBeVisible();
  await close(page);

  await page.getByRole("button", { name: words("exchangeRates.history") }).click();
  await expect(dialog(page).getByText("Weekly review")).toBeVisible();
  await expect(dialog(page).getByText("recheck the rate near the recorded value")).toBeVisible();
  expect(state.log).toContain("GET /pricing/exchange-rates/CNY/USD");
  await dialog(page)
    .getByRole("button", { name: words("change.close") })
    .last()
    .click();

  await page.getByRole("button", { name: words("exchangeRates.add") }).click();
  await dialog(page).locator("[name=base]").selectOption("EUR");
  await dialog(page).locator("[name=quote]").selectOption("USD");
  await dialog(page).locator("[name=numerator]").fill("11");
  await dialog(page).locator("[name=denominator]").fill("10");
  await send(page, "set the first EUR conversion");
  await expect(done(page)).toBeVisible();
  await close(page);
  await expect(page.getByText("EUR / USD", { exact: true })).toBeVisible();
  if (process.env.AXIOM_ADMIN_SHOTS) {
    await freeze(page);
    await page.screenshot({ path: `${process.env.AXIOM_ADMIN_SHOTS}/exchange-rates.png` });
  }
});

test("selling policy saves an explicit zero fee and keeps read-only sessions read-only", async ({ page }) => {
  const state = await open(page, "/pricing?tab=policy");
  await page.getByRole("button", { name: words("pricePolicy.edit") }).click();
  await dialog(page).locator("[name=byok_fee_bps]").fill("0");
  await send(page, "zero fee during the launch");
  await expect(done(page)).toBeVisible();
  await close(page);
  const policy = page.getByRole("region", { name: words("pricePolicy.title") });
  await expect(policy.getByText("0%", { exact: true })).toBeVisible();
  expect(state.log).toContain("PUT /pricing/policy");

  if (process.env.AXIOM_ADMIN_SHOTS) {
    await policy.scrollIntoViewIfNeeded();
    await freeze(page);
    await page.screenshot({ path: `${process.env.AXIOM_ADMIN_SHOTS}/price-policy.png` });
  }
  state.permissions = ["pricing.view"];
  await page.reload();
  await expect(policy.getByText("0%", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: words("pricePolicy.edit") })).toHaveCount(0);
  await page.goto("/exchange-rates");
  await expect(page.getByRole("button", { name: words("exchangeRates.history") }).first()).toBeVisible();
  await expect(page.getByRole("button", { name: words("exchangeRates.edit") })).toHaveCount(0);
  await expect(page.getByRole("button", { name: words("exchangeRates.add") })).toHaveCount(0);
});

test("an authorised admin can revoke and restore an existing platform grant", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const state = await open(page, "/users/roles");
  await page.getByRole("row", { name: /user1@example\.com/ }).click();
  const revoke = page.getByRole("button", { name: `${zhText("platformGrants.revoke")}: support` });
  const restore = page.getByRole("button", { name: `${zhText("platformGrants.restore")}: support` });
  const status = (text: string) => page.getByRole("dialog").first().getByText(text, { exact: true }).first();
  await revoke.click();
  if (process.env.AXIOM_ADMIN_SHOTS) {
    await freeze(page);
    await page.screenshot({ path: `${process.env.AXIOM_ADMIN_SHOTS}/platform-grants-phone.png` });
  }
  // what the change does is shown before it is made
  await expect(dialog(page).getByText(zhText("platformGrants.active"), { exact: true })).toBeVisible();
  state.dropReplies = 1;
  await send(page, "end the support assignment");
  await expect(dialog(page).getByRole("alert")).toContainText(zhText("change.unknown"));
  await close(page);
  await expect(status(zhText("platformGrants.revoked"))).toBeVisible();

  // sending the lost request again is the same change: the toast says it was already done
  await restore.click();
  await dialog(page)
    .getByRole("button", { name: zhText("change.resend") })
    .click();
  await expect(page.getByText(zhText("change.replayedToast"))).toBeVisible();
  expect(state.stored.size).toBe(1);

  await restore.click();
  await send(page, "resume the support assignment");
  await expect(status(zhText("platformGrants.active"))).toBeVisible();
  expect(state.stored.size).toBe(2);

  // without platform_grants.manage the roles are not read at all
  state.permissions = ["users.view"];
  state.log.length = 0;
  await page.goto("/users");
  await page.getByRole("row", { name: /user1@example\.com/ }).click();
  await expect(page.getByRole("dialog").getByRole("heading", { name: "user1@example.com" })).toBeVisible();
  await expect(page.getByRole("dialog").getByText(zhText("users.tabs.roles"))).toHaveCount(0);
  expect(state.log.some((entry) => entry.includes("/platform-grants"))).toBe(false);
});
