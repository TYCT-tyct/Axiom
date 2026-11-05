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
    if (request.method() !== "GET" && request.url().includes("/routing-presets")) {
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
