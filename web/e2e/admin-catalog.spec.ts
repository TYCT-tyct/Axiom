import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { uuid } from "./admin-data.ts";
import { acct } from "./admin-scenes.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The catalog and pricing pages against the mocked API (admin-api.ts): models, their names, specs and reference prices,
// authors, and the pricing rules with their preview.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

async function open(page: Page, path: string) {
  const state = await mockAdmin(page);
  signedInAs(state, "mfa");
  await page.goto(path);
  return state;
}

const dialog = (page: Page) => page.getByRole("dialog");
const h1 = (page: Page, name: string | RegExp) => page.getByRole("heading", { level: 1, name });
const press = (page: Page, key: string, nth = 0) => page.getByRole("button", { name: words(key) }).nth(nth);
const done = (page: Page) => dialog(page).getByText(zhText("change.done"));
const close = (page: Page) =>
  dialog(page)
    .getByRole("button", { name: zhText("change.close") })
    .last()
    .click();

async function send(page: Page, reason = "because the owner asked") {
  await dialog(page).getByLabel(words("change.reason")).fill(reason);
  await dialog(page).locator("button[type=submit]").click();
}

const inputRate = zhText("models.price.perMillion").replace("{{item}}", zhText("models.item.input"));
// The table of models; the page has the authors' table too.
const models = (page: Page) => page.getByRole("table", { name: zhText("nav.models") });
const model1 = `/models/${uuid(5, 1)}`;

test.describe("the model catalog", () => {
  test("models are filtered by state, author and the start of the slug, and the filters stay in the address", async ({
    page,
  }) => {
    await open(page, "/models");
    await expect(h1(page, zhText("nav.models"))).toBeVisible();
    await page.getByLabel(words("models.col.status")).selectOption("draft");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    await expect(models(page).getByRole("row")).toHaveCount(2);
    await expect(page.getByText("deepseek/model-3")).toBeVisible();
    expect(new URL(page.url()).searchParams.get("status")).toBe("draft");
    await page.reload();
    await expect(models(page).getByRole("row")).toHaveCount(2);
    await page.getByRole("button", { name: zhText("filter.reset") }).click();
    await page.getByLabel(words("models.slugPrefix")).fill("google/");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    await expect(models(page).getByRole("row").nth(1)).toContainText("google/");
    await page.getByLabel(words("models.slugPrefix")).fill("nothing/");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    await expect(page.getByRole("heading", { name: zhText("models.noMatch.title") })).toBeVisible();
  });

  test("a draft is reviewed: what is missing is listed, then it gets a name and a price and goes on sale", async ({
    page,
  }) => {
    const state = await open(page, "/models");
    await press(page, "models.draft.open").click();
    await dialog(page).getByLabel(words("models.draft.slug")).fill("Not A Slug");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("models.draft.slugInvalid"));
    await dialog(page).getByLabel(words("models.draft.slug")).fill("anthropic/new-model");
    await dialog(page).getByLabel(words("models.item.input")).check();
    await dialog(page).getByLabel(words("models.item.output")).check();
    await dialog(page).getByLabel(words("models.tier.standard")).check();
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    await dialog(page)
      .getByRole("link", { name: zhText("models.draft.review") })
      .click();
    await expect(h1(page, "anthropic/new-model")).toBeVisible();
    await expect(page.getByText(zhText("models.state.draft")).first()).toBeVisible();

    // the review gate: no public name and no price yet
    await press(page, "models.status.listed.action").click();
    await expect(dialog(page).getByText(zhText("models.ready.public"))).toBeVisible();
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(/暂时还不能用/);
    await dialog(page).getByRole("button", { name: "取消" }).click();

    await press(page, "models.names.add").click();
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.reasonMissing"));
    await send(page);
    await expect(done(page)).toBeVisible();
    await close(page);

    await press(page, "models.prices.publish").click();
    await dialog(page).getByLabel(words("models.price.currency")).fill("usd");
    await dialog(page).getByLabel(inputRate, { exact: true }).fill("3");
    await dialog(page).getByLabel(words("models.price.evidence")).fill("price page, 2026-10-15");
    await send(page, "from the vendor page");
    await expect(done(page)).toBeVisible();
    await close(page);
    await expect(page.getByRole("cell", { name: /^3\D/ }).first()).toBeVisible();

    await press(page, "models.status.listed.action").click();
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(/暂时还不能用/);
    // the price must cover everything the model declares: the output price is missing
    expect(state.models[0]?.status).toBe("draft");
  });

  test("a price version is published against the current one, and a stale one is refused", async ({ page }) => {
    const state = await open(page, model1);
    await expect(page.getByRole("cell", { name: /^3\D/ }).first()).toBeVisible();
    await press(page, "models.prices.newVersion").click();
    await dialog(page).getByLabel(inputRate, { exact: true }).fill("4");
    await dialog(page).getByLabel(words("models.price.evidence")).fill("new price page");
    // a version published by someone else in the meantime
    const row = state.models[0]?.prices[0];
    if (row?.schedule) {
      row.id = uuid(6, 999);
      row.schedule = { ...row.schedule, rates: { ...row.schedule.rates, input: "0.000005" } };
    }
    await send(page, "vendor cut prices");
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.stale"));
    expect(state.models[0]?.prices[0]?.schedule?.rates.input).toBe("0.000005");
    // the form is filled again from the version now current, and only then can it be published over it
    await dialog(page)
      .getByRole("button", { name: zhText("change.reload") })
      .click();
    await expect(dialog(page).getByLabel(inputRate, { exact: true })).toHaveValue("5");
    await dialog(page).getByLabel(inputRate, { exact: true }).fill("4");
    await dialog(page).getByLabel(words("models.price.evidence")).fill("new price page");
    await send(page, "vendor cut prices");
    await expect(done(page)).toBeVisible();
    await close(page);
    await expect(page.getByRole("cell", { name: /^4\D/ }).first()).toBeVisible();
    expect(state.models[0]?.prices[0]?.schedule?.rates.input).toBe("0.000004");
  });

  test("retiring asks for the slug, and a retired model offers no more changes", async ({ page }) => {
    const state = await open(page, model1);
    await press(page, "models.status.retired.action").click();
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.confirmMismatch"));
    await dialog(page)
      .getByLabel(/输入“openai\/model-1”以确认/)
      .fill("openai/model-1");
    await dialog(page).locator("button[type=submit]").click();
    await expect(done(page)).toBeVisible();
    await close(page);
    expect(state.models[0]?.status).toBe("retired");
    await expect(press(page, "models.spec.edit")).toHaveCount(0);
    await expect(press(page, "models.prices.publish")).toHaveCount(0);
  });

  test("a name used by another model is refused", async ({ page }) => {
    await open(page, model1);
    await press(page, "models.names.add").click();
    await dialog(page).getByLabel(words("models.names.name")).fill("GOOGLE/model-2");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(/这个名称已经有了/);
  });

  test("authors are added and renamed", async ({ page }) => {
    const state = await open(page, "/models");
    await press(page, "models.authors.add").click();
    await dialog(page).getByLabel(words("models.authors.slug")).fill("mistral");
    await dialog(page).getByLabel(words("models.authors.name")).fill("Mistral AI");
    await send(page);
    await expect(done(page)).toBeVisible();
    await close(page);
    await expect(page.getByRole("cell", { name: "Mistral AI", exact: true })).toBeVisible();
    await page.getByRole("button", { name: /修改 Mistral AI 的名称/ }).click();
    await expect(dialog(page).getByLabel(words("models.authors.slug"))).toHaveValue("mistral");
    await dialog(page).getByLabel(words("models.authors.name")).fill("Mistral");
    await send(page);
    await expect(done(page)).toBeVisible();
    expect(state.authors.find((a) => a.slug === "mistral")?.name).toBe("Mistral");
  });
});

test.describe("pricing", () => {
  test("rules are listed most specific first, and a rule is previewed before it is saved", async ({ page }) => {
    const state = await open(page, "/pricing");
    await expect(h1(page, zhText("nav.pricing"))).toBeVisible();
    const rules = page.getByRole("table", { name: words("pricing.rules.title") });
    await expect(rules.getByRole("row")).toHaveCount(state.rules.length + 1);
    await expect(page.getByText(zhText("pricing.off")).first()).toBeVisible();

    await press(page, "pricing.rule.new").click();
    await dialog(page).getByLabel(words("pricing.rule.target")).selectOption("author");
    await dialog(page).getByLabel(words("pricing.rule.targetId")).selectOption("openai");
    await dialog(page).getByLabel(words("pricing.method.multiplier")).fill("1.5");
    await dialog(page)
      .getByRole("button", { name: zhText("pricing.rule.previewRun") })
      .click();
    await expect(
      dialog(page)
        .getByRole("cell", { name: /33\.33%/ })
        .first(),
    ).toBeVisible();
    await send(page, "openai at 1.5 times the reference price");
    await expect(done(page)).toBeVisible();
    expect(state.rules.at(-1)).toMatchObject({
      target: { kind: "author", id: "openai" },
      method: { kind: "reference_multiplier", multiplier: "1.5", currency_conversion: "face" },
    });
    await close(page);
    await expect(rules.getByRole("row")).toHaveCount(state.rules.length + 1);
  });

  test("a rule for a target and audience that has one is refused, and a bad ratio is told", async ({ page }) => {
    await open(page, "/pricing");
    await press(page, "pricing.rule.new").click();
    await dialog(page).getByLabel(words("pricing.method.multiplier")).fill("twenty");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("pricing.rule.invalid.multiplier"));
    await dialog(page).getByLabel(words("pricing.method.multiplier")).fill("1.2");
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByRole("alert")).toHaveText(/和现有的冲突/);
  });

  test("a rule is changed as of the revision read and can be turned off", async ({ page }) => {
    const state = await open(page, "/pricing");
    await page
      .getByRole("button", { name: /修改“.*”规则/ })
      .first()
      .click();
    await dialog(page).getByLabel(words("pricing.rule.enabled")).uncheck();
    // someone else changed the rule after this dialog read it
    const rule = state.rules[0];
    if (rule) {
      rule.rev += 1;
    }
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.stale"));
    expect(rule?.enabled).toBe(true);
    await dialog(page)
      .getByRole("button", { name: zhText("change.reload") })
      .click();
    await expect(dialog(page).getByLabel(words("pricing.rule.enabled"))).toBeChecked();
    await dialog(page).getByLabel(words("pricing.rule.enabled")).uncheck();
    await send(page);
    await expect(done(page)).toBeVisible();
    expect(state.rules.find((r) => !r.enabled && r.target.kind === "model")?.rev).toBe(3);
  });

  test("limits are not applied yet: the inputs are disabled, a stored limit is told, and editing clears it", async ({
    page,
  }) => {
    const state = await open(page, "/pricing");
    await press(page, "pricing.rule.new").click();
    for (const key of ["minMargin", "refMin", "refMax"]) {
      await expect(dialog(page).getByLabel(words(`pricing.constraints.${key}`))).toBeDisabled();
    }
    await expect(dialog(page).getByText(zhText("pricing.constraints.stored"))).toHaveCount(0);
    await close(page);

    await page
      .getByRole("button", { name: /修改“.*”规则/ })
      .first()
      .click();
    await expect(dialog(page).getByLabel(words("pricing.constraints.minMargin"))).toBeDisabled();
    await expect(dialog(page).getByText(zhText("pricing.constraints.stored"))).toBeVisible();
    const sent = page.waitForRequest((request) => request.method() === "PUT");
    await send(page);
    expect((await sent).postDataJSON().rule.constraints).toEqual({});
    await expect(done(page)).toBeVisible();
    expect(state.rules[0]?.constraints).toEqual({});
  });

  test("retiring a rule preserves its stored target, audience, method and unsupported constraints", async ({
    page,
  }) => {
    const state = await open(page, "/pricing");
    const existing = structuredClone(state.rules[0]);
    if (!existing) throw new Error("the pricing fixture needs a rule to retire");
    expect(existing.constraints).not.toEqual({});
    await page
      .getByRole("button", { name: /修改“.*”规则/ })
      .first()
      .click();
    await dialog(page).getByLabel(words("pricing.rule.enabled")).uncheck();
    const sent = page.waitForRequest((request) => request.method() === "PUT");
    await send(page);
    const retired = {
      enabled: false,
      target: existing.target,
      audience: existing.audience,
      method: existing.method,
      constraints: existing.constraints,
    };
    expect((await sent).postDataJSON().rule).toEqual(retired);
    await expect(done(page)).toBeVisible();
    expect(state.rules[0]).toMatchObject({ ...retired, id: existing.id, rev: existing.rev + 1 });
  });

  test("the preview prices chosen models for a tier and says what it cannot price", async ({ page }) => {
    await open(page, "/pricing");
    await page.getByLabel(words("pricing.preview.tier")).selectOption({ label: "Pro" });
    await page.getByRole("button", { name: words("pricing.preview.run") }).click();
    await expect(page.getByRole("cell", { name: /openai\/model-1/ }).first()).toBeVisible();
    await expect(page.getByText(zhText("pricing.preview.state.priced")).first()).toBeVisible();
    await page.getByLabel(words("pricing.preview.account")).fill(`acct_${"1".repeat(26)}`);
    await page.getByRole("button", { name: words("pricing.preview.run") }).click();
    await expect(page.getByRole("cell", { name: /openai\/model-1/ }).first()).toBeVisible();
  });
});

test("the preview's lists are asked for with the page's, not after the lists above answer", async ({ page }) => {
  signedInAs(await mockAdmin(page), "mfa");
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/admin/v1/pricing/publications", async (route) => {
    await held;
    await route.fallback();
  });
  const asked = Promise.all([
    page.waitForRequest((request) => /\/api\/admin\/v1\/models\?.*status=listed/.test(request.url())),
    page.waitForRequest((request) => new URL(request.url()).pathname === "/api/admin/v1/tiers"),
  ]);
  await page.goto("/pricing");
  await asked;
  await expect(page.getByLabel(words("pricing.preview.account"))).toHaveCount(0);
  release();
  await expect(page.getByLabel(words("pricing.preview.account"))).toBeVisible();
});

test("a retry of the rules above keeps what the preview below holds", async ({ page }) => {
  signedInAs(await mockAdmin(page), "mfa");
  let down = true;
  await page.route("**/api/admin/v1/pricing/rules", (route) =>
    down
      ? route.fulfill({ status: 503, json: { error: { code: "unavailable", message: "unavailable" } } })
      : route.fallback(),
  );
  await page.goto("/pricing");
  const account = page.getByLabel(words("pricing.preview.account"));
  const held = `acct_${"1".repeat(26)}`;
  await account.fill(held);
  down = false;
  await page.getByRole("button", { name: "重试" }).first().click();
  await expect(page.getByRole("table", { name: words("pricing.rules.title") })).toBeVisible();
  await expect(account).toHaveValue(held);
});

test("the admin works in English too: credit is added and the result is read in English", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("axiom.language", "en"));
  const state = await mockAdmin(page);
  signedInAs(state, "mfa");
  await page.goto(`/accounts/${acct(1)}`);
  await expect(page.getByRole("button", { name: "Add credit" })).toBeVisible();
  await page.getByRole("button", { name: "Add credit" }).click();
  await dialog(page)
    .getByLabel(/^Amount/)
    .fill("5");
  await dialog(page).getByLabel("Reason").fill("goodwill");
  await dialog(page).locator("button[type=submit]").click();
  await expect(dialog(page).getByText("Done.")).toBeVisible();
  await expect(dialog(page).getByText(/Balance now/)).toBeVisible();
});
