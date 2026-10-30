import { expect, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The site's logo is set and cleared on the settings page, with the preview the server gives as a data URL. The API is
// the mock of admin-api.ts, which checks the session, permission, CSRF token, reason and Idempotency-Key like the server.
test.use({ baseURL: apps[2].url });

// The data URL the server gives for a preview; the page draws it under `img-src 'self' data:`.
const logoSVG =
  "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCI+PHBhdGggZmlsbD0iIzEyMyIgZD0iTTAgMGgyNHYyNEgweiIvPjwvc3ZnPg==";

test("the admin sign-in page and shell show the site's logo", async ({ page }) => {
  const state = await mockAdmin(page);
  state.logo = {
    logo: {
      content_type: "image/svg+xml",
      hash: "0".repeat(64),
      size: 100,
      width: 24,
      height: 24,
      data_url: logoSVG,
    },
  };
  await page.goto("/sign-in");
  await expect(page.locator(`img[src='${logoSVG}']`)).toBeVisible();

  signedInAs(state, "mfa");
  await page.goto("/");
  await expect(page.locator(`nav img[src='${logoSVG}']`)).toBeVisible();
});

test("a site logo is uploaded, previewed, and cleared", async ({ page }) => {
  const state = await mockAdmin(page);
  signedInAs(state, "mfa");
  await page.goto("/settings");
  const dialog = page.getByRole("dialog");
  await page.getByRole("button", { name: words("settings.logo.set") }).click();
  await dialog.locator("input[type=file]").setInputFiles({
    name: "logo.svg",
    mimeType: "image/svg+xml",
    buffer: Buffer.from(
      '<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><path d="M0 0h24v24H0z"/></svg>',
    ),
  });
  await expect(dialog.locator("img")).toBeVisible();
  await dialog.getByLabel(words("change.reason")).fill("because the owner asked");
  await dialog.locator("button[type=submit]").click();
  await expect(dialog.getByText(zhText("change.done"))).toBeVisible();
  expect(state.logo.logo?.content_type).toBe("image/svg+xml");
  await dialog
    .getByRole("button", { name: zhText("change.close") })
    .last()
    .click();
  await expect(page.locator("img[src^='data:image/svg+xml']")).toBeVisible();

  await page.getByRole("button", { name: words("settings.logo.clear") }).click();
  await dialog.getByLabel(words("change.reason")).fill("remove the brand");
  await dialog.locator("button[type=submit]").click();
  await expect(dialog.getByText(zhText("change.done"))).toBeVisible();
  expect(state.logo.logo).toBeUndefined();
  await dialog
    .getByRole("button", { name: zhText("change.close") })
    .last()
    .click();
  await expect(page.getByText(words("settings.logo.none"))).toBeVisible();
});
