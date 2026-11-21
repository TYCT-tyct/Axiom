import { expect, type Page, test } from "@playwright/test";
import { uuid } from "./admin-data.ts";
import { close, dialog, done, open, send } from "./admin-page.ts";
import { acct } from "./admin-scenes.ts";
import { apps } from "./apps.ts";
import { freeze } from "./design/stage.ts";
import { words } from "./words.ts";

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

async function snapshot(page: Page, name: string) {
  if (!process.env.AXIOM_ADMIN_SHOTS) return;
  await freeze(page);
  await page.screenshot({ path: `${process.env.AXIOM_ADMIN_SHOTS}/${name}.png`, fullPage: true });
}

test("alert events show delivery receipts and resolve with a reason", async ({ page }) => {
  const state = await open(page, "/alert-events");
  await page
    .getByRole("button", { name: words("alertEvents.details") })
    .first()
    .click();
  await expect(page.getByText("email-delivery")).toBeVisible();
  await snapshot(page, "alert-events");
  await page.getByRole("button", { name: words("alertEvents.resolve"), exact: true }).click();
  await send(page, "incident mitigated");
  await expect(done(page)).toBeVisible();
  await close(page);
  await expect(page.getByText(words("alertEvents.state.resolved")).first()).toBeVisible();
  expect(state.log).toContain(`POST /alerts/events/${uuid(31, 1)}/resolve`);
});

test("alert settings replace the rules, test a channel, and rotate a write-only secret", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const state = await open(page, "/alert-configuration");
  await page.getByRole("button", { name: words("alertConfig.edit") }).click();
  const rules = dialog(page).getByLabel(words("alertConfig.rules"), { exact: true });
  const original = await rules.inputValue();
  await rules.fill(original.replace('"threshold": 2', '"threshold": 4'));
  const spend = dialog(page).getByLabel(words("alertConfig.spend"), { exact: true });
  await spend.fill((await spend.inputValue()).replace("1000000000", "9007199254740993"));
  const write = page.waitForRequest(
    (request) => request.method() === "PUT" && request.url().endsWith("/alerts/configuration"),
  );
  await send(page, "increase the threshold");
  expect((await write).postData()).toMatch(/"min_spend_nano": 9007199254740993[,\n]/);
  await expect(done(page)).toBeVisible();
  await close(page);

  await page
    .getByRole("button", { name: words("alertConfig.testChannel") })
    .first()
    .click();
  await send(page, "test the channel");
  await expect(done(page)).toBeVisible();
  await close(page);

  await page.getByRole("button", { name: words("alertConfig.rotate") }).click();
  const secret = dialog(page).getByLabel(words("alertConfig.secret"));
  await expect(secret).toHaveAttribute("type", "password");
  await expect(secret).toHaveValue("");
  await snapshot(page, "alert-secret-phone");
  await secret.fill("a test signing value for this fixture");
  await send(page, "rotate the signing secret");
  await expect(done(page)).toBeVisible();
  expect(state.log).toEqual(
    expect.arrayContaining([
      "PUT /alerts/configuration",
      "POST /alerts/tests",
      "PUT /alerts/channels/operations-webhook/secret",
    ]),
  );
});

test("the announcements list follows successful publishes and accepts a withdrawal by ID", async ({ page }) => {
  const state = await open(page, "/announcements");
  await expect(page.getByText(words("announcements.empty"))).toBeVisible();
  await page.getByRole("button", { name: words("announcements.publish") }).click();
  await dialog(page).locator("[name=title]").fill("Planned maintenance");
  await dialog(page).locator("[name=body]").fill("The console stays open during maintenance.");
  await dialog(page).locator("[name=starts_at]").fill("2030-01-02T09:00");
  await dialog(page).locator("[name=ends_at]").fill("2030-01-02T10:00");
  await send(page, "notify customers");
  await expect(done(page)).toBeVisible();
  await close(page);
  await expect(page.getByText("Planned maintenance")).toBeVisible();

  await page.getByRole("link", { name: words("nav.alertEvents") }).click();
  await page.getByRole("link", { name: words("nav.announcements") }).click();
  await expect(page.getByText("Planned maintenance")).toBeVisible();
  await page.getByRole("button", { name: words("announcements.withdrawById") }).click();
  await dialog(page).locator("[name=id]").fill("ann_00000000000000000000000001");
  await send(page, "maintenance cancelled");
  await expect(done(page)).toBeVisible();
  await close(page);
  await expect(page.getByText(words("announcements.withdrawn"))).toBeVisible();
  await snapshot(page, "announcements");
  await expect(page.getByRole("button", { name: words("announcements.withdraw"), exact: true })).toHaveCount(0);
  expect(state.log).toContain("POST /announcements/ann_00000000000000000000000001/withdraw");
  expect(state.log.some((entry) => entry.startsWith("GET /announcements"))).toBe(false);
});

test("the legal page reads history and publishes a version in the chosen language", async ({ page }) => {
  const state = await open(page, "/legal-documents");
  await expect(page.getByText(words("legalDocs.version")).first()).toBeVisible();
  await page.getByRole("button", { name: words("legalDocs.publish") }).click();
  await dialog(page).locator("[name=kind]").selectOption("privacy");
  await dialog(page).locator("[name=locale]").selectOption("en");
  await dialog(page).locator("[name=body]").fill("## Privacy\n\nVersion reviewed for publication.");
  await send(page, "publish the reviewed translation");
  await expect(done(page)).toBeVisible();
  await close(page);
  await page
    .getByRole("button", { name: words("legalDocs.read") })
    .first()
    .click();
  await expect(page.getByText("Version reviewed for publication.", { exact: false })).toBeVisible();
  await snapshot(page, "legal-documents");
  expect(state.log.filter((entry) => entry.startsWith("POST /sites/"))).toEqual([
    `POST /sites/${state.siteId}/legal-documents`,
  ]);
});

test("retention preserves zero and rate limits distinguish zero from inheritance", async ({ page }) => {
  const state = await open(page, "/retention");
  await page.getByRole("button", { name: words("logRetention.edit") }).click();
  await dialog(page).locator("[name=hot_days]").fill("0");
  await send(page, "keep all hot partitions");
  await expect(done(page)).toBeVisible();
  await close(page);
  await expect(page.getByText(words("logRetention.keepAll"))).toBeVisible();
  await snapshot(page, "retention");

  await page.getByRole("link", { name: words("nav.rateLimits") }).click();
  await page.getByLabel(words("rateLimits.target")).selectOption("account");
  await page.getByLabel(words("rateLimits.id")).fill(acct(1));
  await page.getByRole("button", { name: words("rateLimits.lookup") }).click();
  await page.getByRole("button", { name: words("rateLimits.edit") }).click();
  const requests = dialog(page).locator("[name=requests]");
  await requests.fill("0");
  await send(page, "remove the account cap");
  await expect(done(page)).toBeVisible();
  await close(page);
  await expect(page.getByText(words("rateLimits.unlimited")).first()).toBeVisible();

  await page.getByRole("button", { name: words("rateLimits.edit") }).click();
  await expect(requests).toHaveValue("0");
  await requests.fill("");
  await send(page, "inherit the tier cap");
  await expect(done(page)).toBeVisible();
  await close(page);
  await expect(page.getByText(words("rateLimits.inherits"), { exact: true })).toBeVisible();
  await snapshot(page, "rate-limits");
  expect(state.log.filter((entry) => entry.startsWith("PUT /rate-limits/"))).toHaveLength(2);
});

test("alerts show empty, failed and forbidden reads without a resolve action", async ({ page }) => {
  const state = await open(page, "/alert-events", "empty");
  await expect(page.getByText(words("alertEvents.empty"))).toBeVisible();
  await page.route("**/api/admin/v1/alerts/events", (route) =>
    route.fulfill({
      status: 400,
      headers: { "X-Request-ID": "req-alert-events" },
      json: { error: { code: "invalid_request", message: "invalid_request" } },
    }),
  );
  await page.reload();
  await expect(page.getByText("req-alert-events")).toBeVisible();
  await page.unroute("**/api/admin/v1/alerts/events");
  state.permissions = ["users.view"];
  await page.reload();
  await expect(page.getByRole("heading", { name: words("state.forbiddenTitle") })).toBeVisible();
  await expect(page.getByRole("button", { name: words("alertEvents.resolve") })).toHaveCount(0);
});
