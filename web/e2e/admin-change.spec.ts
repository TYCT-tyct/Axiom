import { expect, type Page, type Request, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { uuid } from "./admin-data.ts";
import { acct } from "./admin-scenes.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// What a change dialog may send after an answer that did not settle it (change.tsx, attempt.ts): after a lost answer
// only the same request with the same key, never a second change; after a stale revision nothing, until the form is
// filled again from what is current. The mock refuses a key sent again with another body, as the server does.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

async function open(page: Page, path: string, over: Parameters<typeof mockAdmin>[2] = {}) {
  const state = await mockAdmin(page, "usual", over);
  signedInAs(state, "mfa");
  await page.goto(path);
  return state;
}

const dialog = (page: Page) => page.getByRole("dialog");
const alert = (page: Page) => dialog(page).getByRole("alert");
const submit = (page: Page) => dialog(page).locator("button[type=submit]");
const press = (page: Page, key: string) => page.getByRole("button", { name: words(key) }).first();
const done = (page: Page) => dialog(page).getByText(zhText("change.done"));

async function send(page: Page, reason = "because the owner asked") {
  await dialog(page).getByLabel(words("change.reason")).fill(reason);
  await submit(page).click();
}

// Every grant the page sends: its key and its body.
function grants(page: Page) {
  const sent: { key: string | undefined; body: unknown }[] = [];
  page.on("request", (request: Request) => {
    if (request.method() === "POST" && request.url().endsWith("/credit-grants")) {
      sent.push({ key: request.headers()["idempotency-key"], body: request.postDataJSON() });
    }
  });
  return sent;
}

test.describe("a grant whose answer is lost", () => {
  const amount = words("users.credit.amount");

  test("is locked and sent again as it was, so the credit is added once", async ({ page }) => {
    const state = await open(page, `/accounts/${acct(1)}`);
    const sent = grants(page);
    await press(page, "users.credit.open").click();
    await dialog(page).getByLabel(amount).fill("2.5");
    // the server adds the credit, and the answer is lost on the way
    state.dropReplies = 1;
    await send(page);
    await expect(alert(page)).toContainText(zhText("change.unknown"));
    // nothing the request is made of can be changed before it is settled
    await expect(dialog(page).getByLabel(amount)).toBeDisabled();
    await expect(dialog(page).getByLabel(words("change.reason"))).toBeDisabled();
    await expect(submit(page)).toHaveText(zhText("change.resend"));

    await submit(page).click();
    await expect(done(page)).toBeVisible();
    await expect(dialog(page).getByText(zhText("change.replayed"))).toBeVisible();
    await expect(dialog(page).getByText("$14.50")).toBeVisible();
    expect(sent).toHaveLength(2);
    expect(sent[1]).toEqual(sent[0]);
    expect(state.stored.size).toBe(1);
  });

  test("cannot be changed while it is on its way, so the locked form shows what is sent again", async ({ page }) => {
    const state = await open(page, `/accounts/${acct(1)}`);
    const sent = grants(page);
    // the first send is held, then its answer is lost
    let lose = () => {};
    const lost = new Promise<void>((resolve) => {
      lose = resolve;
    });
    await page.route(
      "**/credit-grants",
      async (route) => {
        await lost;
        await route.abort("connectionreset");
      },
      { times: 1 },
    );
    await press(page, "users.credit.open").click();
    await dialog(page).getByLabel(amount).fill("25");
    await send(page);
    await expect(dialog(page).getByLabel(amount)).toBeDisabled();
    await expect(dialog(page).getByLabel(words("change.reason"))).toBeDisabled();
    lose();
    await expect(alert(page)).toContainText(zhText("change.unknown"));
    await expect(dialog(page).getByLabel(amount)).toHaveValue("25");

    await submit(page).click();
    await expect(done(page)).toBeVisible();
    await expect(dialog(page).getByText("$37.00")).toBeVisible();
    expect(sent).toHaveLength(2);
    expect(sent[1]).toEqual(sent[0]);
    expect(state.stored.size).toBe(1);
  });

  test("that cannot be read, as a proxy's page in its place, is sent again with its key", async ({ page }) => {
    const state = await open(page, `/accounts/${acct(1)}`);
    const sent = grants(page);
    await page.route(
      "**/credit-grants",
      (route) => route.fulfill({ status: 201, contentType: "text/html", body: "<html>bad gateway</html>" }),
      { times: 1 },
    );
    await press(page, "users.credit.open").click();
    await dialog(page).getByLabel(amount).fill("2.5");
    await send(page);
    await expect(alert(page)).toContainText(zhText("change.unknown"));
    await submit(page).click();
    await expect(done(page)).toBeVisible();
    expect(sent).toHaveLength(2);
    expect(sent[1]).toEqual(sent[0]);
    expect(state.stored.size).toBe(1);
  });

  test("whose key the server holds for another request stops: nothing more is sent, and the page shows what is true", async ({
    page,
  }) => {
    const state = await open(page, `/accounts/${acct(1)}`);
    const sent = grants(page);
    await expect(page.getByText("$12.00").first()).toBeVisible();
    await press(page, "users.credit.open").click();
    await dialog(page).getByLabel(amount).fill("2.5");
    state.dropReplies = 1;
    await send(page);
    await expect(alert(page)).toContainText(zhText("change.unknown"));
    // the key is now held for another body, as when the first try was made from content that has since changed
    for (const stored of state.stored.values()) {
      stored.fingerprint = "another request";
    }
    await submit(page).click();
    await expect(alert(page)).toHaveText(zhText("change.reused"));
    await expect(submit(page)).toHaveCount(0);
    expect(sent).toHaveLength(2);
    expect(new Set(sent.map((grant) => grant.key)).size).toBe(1);

    // the dialog's own button is the last of the two named Close
    await dialog(page)
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
    await expect(dialog(page)).toHaveCount(0);
    // the page fetched the account again, which here shows the one grant (it cannot prove a try was not done)
    await expect(page.getByText("$14.50").first()).toBeVisible();
    expect(sent).toHaveLength(2);
  });
});

test("a spec saved by another operator meanwhile is not overwritten: the save is refused until the form is reloaded", async ({
  page,
}) => {
  const path = `/models/${uuid(5, 1)}`;
  await page.clock.install();
  const state = await open(page, path);
  const other = await page.context().newPage();
  // the second operator works on the same models
  await open(other, path, { models: state.models });
  const tier = (p: Page, name: string) => dialog(p).getByLabel(words(`models.tier.${name}`));

  await press(page, "models.spec.edit").click();
  await tier(page, "priority").check();

  await press(other, "models.spec.edit").click();
  await tier(other, "flex").check();
  await send(other, "the vendor added flex");
  await expect(done(other)).toBeVisible();
  const model = state.models[0];
  expect(model?.spec.tiers).toEqual(["standard", "flex"]);

  // The first operator's page fetches the model again underneath the open dialog, as when its tab regains focus: the
  // form keeps what was typed, and the change is still made against the revision it was filled from.
  await page.clock.fastForward(60_000);
  await page.evaluate(() => window.dispatchEvent(new Event("visibilitychange")));
  await expect(page.locator("dl").getByText(zhText("models.tier.flex"), { exact: true })).toBeVisible();
  await send(page, "the vendor added priority");
  await expect(alert(page)).toHaveText(zhText("change.stale"));
  await expect(submit(page)).toHaveCount(0);
  await expect(tier(page, "priority")).toBeDisabled();
  expect(model?.spec.tiers).toEqual(["standard", "flex"]);
  expect(model?.rev).toBe(4);

  // reloading fills the form from the other operator's spec; what this one typed is gone
  await dialog(page)
    .getByRole("button", { name: zhText("change.reload") })
    .click();
  await expect(tier(page, "flex")).toBeChecked();
  await expect(tier(page, "priority")).not.toBeChecked();
  await expect(alert(page)).toHaveCount(0);
  await tier(page, "priority").check();
  await send(page, "the vendor added priority");
  await expect(done(page)).toBeVisible();
  expect(model?.spec.tiers).toEqual(["standard", "flex", "priority"]);
  expect(model?.rev).toBe(5);
});
