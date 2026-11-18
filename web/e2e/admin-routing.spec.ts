import { expect, type Page, test } from "@playwright/test";
import { uuid } from "./admin-data.ts";
import { close, dialog, done, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { words } from "./words.ts";

// What the routing presets page sends (api/admin.yaml, tag Routing): the shared rules editor writes back what it does
// not show, and a platform preset is priced in the currency it names.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

// Every write of a preset the page sends: its method, its path and its body.
function writes(page: Page) {
  const sent: { method: string; path: string; body: Record<string, unknown> }[] = [];
  page.on("request", (request) => {
    const url = request.url();
    if (request.method() !== "GET" && url.includes("/routing-presets") && !url.includes("/routing-presets/checks")) {
      sent.push({ method: request.method(), path: new URL(request.url()).pathname, body: request.postDataJSON() });
    }
  });
  return sent;
}

test("an edit of a platform preset keeps the run-time settings the editor does not show", async ({ page }) => {
  await open(page, "/routing-presets");
  const sent = writes(page);
  await page
    .getByRole("button", { name: words("routingPresets.editNamed") })
    .first()
    .click();
  await dialog(page).getByLabel(words("routingPresets.form.name")).fill("Official sources only");
  await send(page);
  await expect(done(page)).toBeVisible();
  expect(sent).toEqual([
    {
      method: "PUT",
      path: `/api/admin/v1/routing-presets/${uuid(16, 1)}`,
      body: {
        name: "Official sources only",
        rules: { default: { classes: ["official"] }, advanced: { order: ["g1"] } },
        revision: 1,
        reason: "because the owner asked",
      },
    },
  ]);
});

test("a new platform preset that limits a price names its currency, and one that does not names none", async ({
  page,
}) => {
  await open(page, "/routing-presets");
  const sent = writes(page);
  for (const [slug, price] of [
    ["cheap-eur", "2.5"],
    ["any", ""],
  ] as const) {
    await page
      .getByRole("button", { name: words("routingPresets.create") })
      .first()
      .click();
    await dialog(page).getByLabel(words("routingPresets.form.slug")).fill(slug);
    await dialog(page).getByLabel(words("routingPresets.form.name")).fill(slug);
    await dialog(page).getByLabel(words("routingPresets.form.currency")).fill("eur");
    await dialog(page).getByLabel(words("presetRules.prices.prompt")).first().fill(price);
    await send(page);
    await expect(done(page)).toBeVisible();
    await close(page);
  }
  expect(sent.map(({ body }) => [body.slug, body.currency, body.rules])).toEqual([
    ["cheap-eur", "EUR", { default: { max_price: { prompt: "2.5" } } }],
    ["any", undefined, { default: {} }],
  ]);
});

test("one vendor rule reads in the singular, and the key count has its own column", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("axiom.language", "en"));
  await open(page, "/routing-presets");
  await page.setViewportSize({ width: 360, height: 900 });
  // The site preset has one rule by vendor or model, and the summary under the name counts it as one.
  await expect(page.getByText(words("presetRules.summary.overrides_one")).first()).toBeVisible();
  // No column is hidden on a phone: the table scrolls, and the key count sits in its own column in the page's number format.
  const counted = page.getByRole("row").filter({ hasText: "Cheapest, in dollars" });
  await expect(counted.getByRole("cell", { name: "12,345", exact: true })).toBeVisible();
  // The system preset has no count: it says what it is.
  const system = page.getByRole("row").filter({ hasText: "Allow all" });
  await expect(system.getByText(words("routingPresets.keys.system")).first()).toBeVisible();
});

test("the key count keeps the same number format in Chinese", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("axiom.language", "zh"));
  await open(page, "/routing-presets");
  await page.setViewportSize({ width: 360, height: 900 });
  const counted = page.getByRole("row").filter({ hasText: "Cheapest, in dollars" });
  await expect(counted.getByRole("cell", { name: "12,345", exact: true })).toBeVisible();
});
