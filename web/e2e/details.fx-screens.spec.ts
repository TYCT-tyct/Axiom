import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { usualState } from "./console-data.ts";
import { open, place, scrollTo, shown } from "./fx-helpers.ts";
import { pageHeading } from "./page-heading.ts";

// What the home screens say and do in a single tab, held to what the product guarantees: the questions' promises about
// charges and limits, a configuration a reader copies by hand, the one answer for the models every screen reads, the
// menu sheet to the keyboard, and the site's own name and address in the questions' pictures. The rest of the screens'
// cases are fx-screens.spec.ts.

const site = {
  name: "Axiom",
  currency: "USD",
  key_prefix: "sk-ax-v1-",
  api_base_url: "https://api.example.com",
  registration: "invite",
} as const;

// The promises are written out here, not taken from the page's messages: what settlement guarantees and no more.
const promises = {
  zh: { estimated: "整笔都是估算", paused: "暂停新的调用", over: "累计花费超过上限", not: ["封住总损失", "略超"] },
  en: {
    estimated: "estimated whole",
    paused: "takes no new calls",
    over: "total spend past it",
    not: ["cap the total loss", "slightly"],
  },
} as const;
for (const language of ["zh", "en"] as const)
  test(`the questions promise only what settlement guarantees, ${language}`, async ({ page }) => {
    const said = promises[language];
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.addInitScript((lang) => localStorage.setItem("axiom.language", lang), language);
    await page.setViewportSize({ width: 1440, height: 900 });
    await open(page);
    await scrollTo(page, 5);
    await shown(page, 5);
    const answer = (i: number) => page.locator(".hfm5 .ans").nth(i);
    await expect(answer(3)).toContainText(said.estimated);
    await expect(answer(5)).toContainText(said.paused);
    await expect(answer(6)).toContainText(said.over);
    for (const promise of said.not) await expect(answer(6)).not.toContainText(promise);
  });

test("on a phone a configuration copied by hand is the command's own characters", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 390, height: 844 });
  await open(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  // biome-ignore lint/performance/useDomNodeTextContent: a selection takes the text as laid out, which innerText is; textContent also has the key's hidden link
  const text = await page.locator(".hfm1 pre").innerText();
  expect(text).not.toMatch(/[\u00a0\u200b]/);
  expect(text).toContain("export ANTHROPIC_BASE_URL=https://api.example.com");
});

// The models come once for the page, and every screen reads the same answer: a retry from the models screen reaches the
// footer, and a screen built again shows the time of the list it shows.
const kept = () =>
  usualState().models.map(({ pricing: _, ...model }) => ({
    ...model,
    endpoints: model.endpoints?.map(({ pricing: __, ...endpoint }) => endpoint),
  }));
test("a retry from the models screen reaches the footer's prices", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockConsole(page, "usual", { site, models: kept() });
  let down = true;
  await page.route(/\/api\/console\/v1\/models(\?|$)/, (route) =>
    down ? route.fulfill({ status: 503, body: "{}" }) : route.fallback(),
  );
  await page.goto(apps[0].url);
  await pageHeading(page);
  for (const sec of [2, 6]) {
    await scrollTo(page, sec);
    await shown(page, sec);
  }
  await expect(page.locator(".hfm6 .fc > a").nth(1)).toHaveAttribute("href", "/models");
  down = false;
  await scrollTo(page, 2);
  await page.locator(".hfm2").getByRole("button").first().click();
  await expect(page.locator(".hfm2 .mr").first()).toBeVisible();
  await expect(page.locator(".hfm6 .fc > a").nth(1)).toHaveAttribute("href", "/console/models");
});

test("a screen built again shows the time its list came, not the time it was built", async ({ page }) => {
  await page.clock.install({ time: new Date("2026-11-19T10:00:00Z") });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 2);
  await shown(page, 2);
  const time = () =>
    page
      .locator(".hfm2")
      .getByText(/\d\d:\d\d/)
      .first()
      .textContent();
  await expect.poll(time).toMatch(/\d\d:\d\d/);
  const came = await time();
  await page.clock.runFor(15 * 60_000);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator(".bar .menu")).toBeVisible();
  await page.setViewportSize({ width: 1440, height: 900 });
  await scrollTo(page, 2);
  await shown(page, 2);
  await expect.poll(time).toBe(came);
});

test("the closed menu is out of the keyboard's way, and the focus goes in and comes back", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await open(page);
  const sheet = page.locator(".sheet");
  await expect(sheet).toHaveJSProperty("inert", true);
  await page.locator(".bar .menu").click();
  await expect(sheet).toHaveJSProperty("inert", false);
  await expect(page.locator(".sheet .shx")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(sheet).toHaveJSProperty("inert", true);
  await expect(page.locator(".bar .menu")).toBeFocused();
  await page.locator(".bar .menu").click();
  await page.locator(".sml[data-sec='5']").click();
  await expect(sheet).toHaveJSProperty("inert", true);
  await expect(place(page, 5)).toBeFocused();
});

const longApi = "https://api.a-rather-long-subdomain.example-relay-provider.com";
for (const width of [360, 1440])
  test(`the questions' picture of the tools shows a long address whole, at ${width}`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width, height: 900 });
    await mockConsole(page, "usual", { site: { ...site, api_base_url: longApi } });
    await page.goto(apps[0].url);
    await pageHeading(page);
    await scrollTo(page, 5);
    await shown(page, 5);
    if (width < 700) await page.locator(".hfm5 .fq .q").nth(2).click();
    else await page.locator(".hfm5 .qs .q").nth(2).hover();
    const window = page.locator(".hfm5 .w2").filter({ visible: true }).first();
    const frame = await window.evaluate((el) => {
      const r = el.getBoundingClientRect();
      return { left: r.left, right: r.right };
    });
    for (const address of await window.locator(".ac").all()) {
      const lines = await address.evaluate((el) => {
        const range = document.createRange();
        range.selectNodeContents(el);
        return [...range.getClientRects()].map((r) => ({ left: r.left, right: r.right }));
      });
      for (const line of lines) {
        expect(line.left).toBeGreaterThanOrEqual(frame.left - 0.5);
        expect(line.right).toBeLessThanOrEqual(frame.right + 0.5);
      }
    }
    await expect(window).toContainText(longApi.slice(-12));
  });

test("the first picture's request goes through the site, named as the site", async ({ page }: { page: Page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockConsole(page, "usual", { site: { ...site, name: "Acme Relay" } });
  await page.goto(apps[0].url);
  await pageHeading(page);
  await scrollTo(page, 5);
  await shown(page, 5);
  const fan = page.locator(".hfm5 .w0 .fan");
  await expect(fan).not.toContainText("Axiom");
  await expect(fan).toContainText("Acme Rel…");
});
