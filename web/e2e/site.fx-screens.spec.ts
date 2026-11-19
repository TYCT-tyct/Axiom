import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { usualState } from "./console-data.ts";
import { delivered, open, scrollTo, shown } from "./fx-helpers.ts";
import { pageHeading } from "./page-heading.ts";

// The home page and its site's settings (getSite): which ways in it offers, what it promises before the site has
// answered, how often it asks, and where the footer's prices lead. The rest of the screens' cases are fx-screens.spec.ts.

// The ways in follow how the site takes new users (getSite's registration, as its register page reads it): the words
// offer what the site does, in both languages and both layouts, and a site that takes nobody new leads to signing in
// and offers no way to register. The words are written out here, not taken from the page's messages. (The three steps'
// screen, from another card, is #862's.)
const site = { name: "Axiom", currency: "USD", key_prefix: "sk-ax-v1-", api_base_url: "https://api.example.com" };
const joins = {
  open: {
    href: "/console/register",
    zh: { join: "注册，获取API key", a7: "注册不要钱。", picture: "免费" },
    en: { join: "Sign up and get an API key", a7: "Signing up is free.", picture: "Free" },
  },
  invite: {
    href: "/console/register",
    zh: { join: "凭邀请码注册，获取API key", a7: "要凭邀请码注册", picture: "凭邀请码，免费" },
    en: {
      join: "Sign up with an invite code",
      a7: "takes new accounts by invite code",
      picture: "Free, by invite code",
    },
  },
  closed: {
    href: "/console/sign-in",
    zh: { join: "登录，获取API key", a7: "本站暂不开放注册", picture: "暂不开放" },
    en: { join: "Sign in and get an API key", a7: "not taking new accounts", picture: "Not open" },
  },
} as const;
const visibleWays = (page: Page) =>
  page
    .locator('a[href="/console/register"]:not(.hfm3 *)')
    .evaluateAll((links) => links.filter((link) => link.checkVisibility({ visibilityProperty: true })).length);
for (const [registration, { href, ...words }] of Object.entries(joins))
  for (const language of ["zh", "en"] as const)
    for (const width of [1440, 390])
      test(`a site whose registration is ${registration} offers that way in, ${language} at ${width}`, async ({
        page,
      }) => {
        const said = words[language];
        await page.emulateMedia({ reducedMotion: "reduce" });
        await page.addInitScript((lang) => localStorage.setItem("axiom.language", lang), language);
        await page.setViewportSize({ width, height: 900 });
        await mockConsole(page, "usual", {
          site: { ...site, registration: registration as "open", email_verification: true },
        });
        await page.goto(apps[0].url);
        await pageHeading(page);
        const offered = registration !== "closed";
        if (width > 1000) await expect(page.locator(".bar .btn.primary")).toBeVisible({ visible: offered });
        await expect(page.locator(".hs > main .cta .primary")).toBeVisible({ visible: offered });
        for (const sec of delivered) {
          await scrollTo(page, sec);
          await shown(page, sec);
        }
        await expect(page.locator(".hfm1 .kc").first()).toHaveAttribute("href", href);
        await expect(page.locator(".hfm6 .cta .btn.primary")).toHaveText(said.join);
        await expect(page.locator(".hfm6 .cta .btn.primary")).toHaveAttribute("href", href);
        await expect(page.locator(".hfm5 .ans").last()).toContainText(said.a7);
        await expect(page.locator(".hfm5 .w7 .kv").first().locator(".t1")).toHaveText(said.picture);
        // the billing line of the last answer's picture is not by token: not every endpoint bills so
        await expect(page.locator(".hfm5 .w7")).not.toContainText("token");
        if (offered) expect(await visibleWays(page)).toBeGreaterThan(0);
        else expect(await visibleWays(page)).toBe(0);
      });

// Until the site has answered, and while its call fails, the page promises only what holds for every site: signing in.
// The call is asked again, and the site's own way in shows once it answers.
test("until the site answers the page offers only signing in, and it asks again after a failure", async ({ page }) => {
  let fail = true;
  let asked = 0;
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockConsole(page);
  await page.route(/\/api\/console\/v1\/site(\?|$)/, (route) => {
    asked++;
    return fail ? route.fulfill({ status: 500, body: "{}" }) : route.fallback();
  });
  await page.goto(apps[0].url);
  await pageHeading(page);
  await expect(page.locator(".bar .btn.primary")).toBeHidden();
  await scrollTo(page, 1);
  await shown(page, 1);
  await expect(page.locator(".hfm1 .kc").first()).toHaveAttribute("href", "/console/sign-in");
  await scrollTo(page, 5);
  await shown(page, 5);
  await expect(page.locator(".hfm5 .ans").last()).toContainText("以注册页为准");
  fail = false;
  await expect.poll(() => asked, { timeout: 10_000 }).toBeGreaterThan(1);
  // the usual site takes new users by invite code
  await expect(page.locator(".hfm5 .ans").last()).toContainText("要凭邀请码注册");
  await expect(page.locator(".hfm1 .kc").first()).toHaveAttribute("href", "/console/register");
  await page.locator(".hs").evaluate((el) => el.scrollTo({ top: 0 }));
  await expect(page.locator(".bar .btn.primary")).toBeVisible();
});

// The site's settings are asked for once, above the layout: crossing the layout's breakpoint keeps them, and a page the
// browser brings back from its cache asks afresh.
test("a change of layout keeps the site's answer, and a page brought back asks again", async ({ page }) => {
  let asked = 0;
  page.on("request", (request) => {
    if (/\/api\/console\/v1\/site(\?|$)/.test(request.url())) asked++;
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await expect.poll(() => asked).toBe(1);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator(".bar .menu")).toBeVisible();
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(page.locator(".bar .btn.primary")).toBeVisible();
  expect(asked).toBe(1);
  await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true })));
  await expect.poll(() => asked).toBe(2);
});

// The footer's prices go where the prices are: the models page for a site that publishes them, the console's models
// page, after signing in, for one that keeps them for its users (listPublicModels leaves every pricing out).
for (const kept of [false, true])
  test(`the footer's prices lead to ${kept ? "the console" : "the models page"} when the site ${kept ? "keeps" : "publishes"} them`, async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });
    // listPublicModels with every pricing left out, as for a site that keeps its prices for its users
    const models = usualState().models.map(({ pricing: _, ...model }) => ({
      ...model,
      endpoints: model.endpoints?.map(({ pricing: __, ...endpoint }) => endpoint),
    }));
    await mockConsole(page, "usual", kept ? { models } : {});
    await page.goto(apps[0].url);
    await pageHeading(page);
    await scrollTo(page, 6);
    await shown(page, 6);
    await expect(page.locator(".hfm6 .fc > a").nth(1)).toHaveAttribute("href", kept ? "/console/models" : "/models");
  });
