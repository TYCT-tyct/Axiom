import { expect, type Locator, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { open, scrollTo, shown } from "./fx-helpers.ts";
import { pageHeading } from "./page-heading.ts";

// The home screens to the keyboard, to assistive technology and at the narrowest width: the tabs' keys, what is out of
// the way while it cannot be used, the contrast a site's colour gets on the surfaces it is drawn on, and words that
// break rather than be cut. The rest of the screens' cases are fx-screens.spec.ts.

const site = {
  name: "Axiom",
  currency: "USD",
  key_prefix: "sk-ax-v1-",
  api_base_url: "https://api.example.com",
  registration: "invite",
} as const;

test("the connect screen's tools are tabs: one stop, the arrows move along, the card is their panel", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  const tabs = page.locator(".hfm1 [role=tab]");
  await expect(page.locator(".hfm1 [role=tab][tabindex='0']")).toHaveCount(1);
  await tabs.first().focus();
  await page.keyboard.press("ArrowRight");
  await expect(tabs.nth(1)).toBeFocused();
  await expect(tabs.nth(1)).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".hfm1 pre")).toContainText("[model_providers.axiom]");
  await page.keyboard.press("End");
  await expect(tabs.nth(4)).toBeFocused();
  await page.keyboard.press("ArrowRight");
  await expect(tabs.first()).toBeFocused();
  await expect(page.locator(".hfm1 [role=tabpanel]")).toHaveAttribute("aria-labelledby", "connect-tab-0");
  await expect(tabs.first()).toHaveAttribute("aria-controls", "connect-card");
  await expect(page.locator(".hfm1 [role=tab][tabindex='0']")).toHaveCount(1);
});

test("the questions are tabs on a wide screen: the arrows choose, and each answer is its question's panel", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 5);
  await shown(page, 5);
  const tabs = page.locator(".hfm5 [role=tab]");
  await tabs.first().focus();
  await page.keyboard.press("ArrowDown");
  await expect(tabs.nth(1)).toBeFocused();
  await expect(tabs.nth(1)).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("#faq-pane-1")).toBeVisible();
  await expect(page.locator("#faq-pane-1")).toHaveAttribute("aria-labelledby", "faq-tab-1");
  await page.keyboard.press("Home");
  await expect(tabs.first()).toBeFocused();
  await expect(page.locator(".hfm5 [role=tab][tabindex='0']")).toHaveCount(1);
});

test("the key's place in a line still being written is out of the keyboard's way and of what is read", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  // the page's clock stands still, so the new tool's lines stay mid-way
  await page.clock.install();
  await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 2000);
  await page.locator(".hfm1 .tab").nth(4).click();
  const waiting = page.locator(".hfm1 .kc.wait");
  await expect(waiting).not.toHaveCount(0);
  for (const link of await waiting.all()) {
    await expect(link).toHaveAttribute("tabindex", "-1");
    await expect(link).toHaveAttribute("aria-hidden", "true");
  }
});

// The colour an element shows and the colour behind it, as sRGB bytes (a canvas resolves light-dark() and color-mix()
// the way the page does), and the WCAG ratio between them.
const ratio = (element: Locator, edge = false) =>
  element.evaluate((el, readEdge) => {
    const canvas = document.createElement("canvas").getContext("2d") as CanvasRenderingContext2D;
    const rgba = (color: string) => {
      canvas.clearRect(0, 0, 1, 1);
      canvas.fillStyle = color;
      canvas.fillRect(0, 0, 1, 1);
      return [...canvas.getImageData(0, 0, 1, 1).data];
    };
    const bytes = (color: string) => rgba(color).slice(0, 3);
    // the nearest background that is not see-through
    const behind = (from: Element | null): string => {
      for (let at = from; at; at = at.parentElement) {
        const color = getComputedStyle(at).backgroundColor;
        if ((rgba(color)[3] ?? 0) > 200) return color;
      }
      return getComputedStyle(document.body).backgroundColor;
    };
    const style = getComputedStyle(el);
    const front = readEdge ? (/(rgb|oklab|color|lab)\([^)]*\)/.exec(style.boxShadow)?.[0] ?? "") : style.color;
    const back = readEdge ? behind(el.parentElement) : behind(el);
    const lum = (c: number[]) =>
      c
        .map((v) => v / 255)
        .map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
        .reduce((sum, v, i) => sum + v * ([0.2126, 0.7152, 0.0722][i] ?? 0), 0);
    const [a, b] = [lum(bytes(front)), lum(bytes(back))].sort((x, y) => y - x) as [number, number];
    return (a + 0.05) / (b + 0.05);
  }, edge);

// the site's own colours, no brand set: the key chip under the pointer, in light, where the tint is strongest
test("the default key chip stays readable under the pointer", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light", reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockConsole(page, "usual", { site });
  await page.goto(apps[0].url);
  await pageHeading(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  const chip = page.locator(".hfm1 .kc.on").first();
  await chip.hover();
  await page.waitForTimeout(300);
  expect(await ratio(chip)).toBeGreaterThanOrEqual(4.5);
});

for (const scheme of ["light", "dark"] as const)
  test(`a grey brand keeps the key chip readable on its wash, and the footer's choice keeps its edge, ${scheme}`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
    await page.setViewportSize({ width: 390, height: 844 });
    await mockConsole(page, "usual", { site: { ...site, brand_color: "#888888" } });
    await page.goto(apps[0].url);
    await pageHeading(page);
    await scrollTo(page, 1);
    await shown(page, 1);
    const chip = page.locator(".hfm1 .kc.on").first();
    expect(await ratio(chip)).toBeGreaterThanOrEqual(4.5);
    // under the pointer too, on the tint the chip takes then
    await chip.hover();
    await page.waitForTimeout(300);
    expect(await ratio(chip)).toBeGreaterThanOrEqual(4.5);
    await scrollTo(page, 6);
    await shown(page, 6);
    expect(await ratio(page.locator(".hfm6 .sg.on").first(), true)).toBeGreaterThanOrEqual(3);
  });

// At 360 the words and the site's own data break rather than be cut by the frame: every line of them stays inside the
// view, a long site name and a long API address included.
test("at 360 the sea bed's words and the site's long name and address stay inside the view", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.addInitScript(() => localStorage.setItem("axiom.language", "en"));
  await page.setViewportSize({ width: 360, height: 780 });
  await mockConsole(page, "usual", {
    site: {
      ...site,
      name: "AcmeRelayWithAnUnbrokenVeryLongSiteName",
      api_base_url: "https://api.a-rather-long-subdomain.example-relay-provider.com",
    },
  });
  await page.goto(apps[0].url);
  await pageHeading(page);
  await scrollTo(page, 6);
  await shown(page, 6);
  const outside = await page.locator(".hfm6 h2 .ph, .hfm6 .pv, .hfm6 .ft .brand span, .hfm6 .lede").evaluateAll((els) =>
    els.flatMap((el) => {
      // the text's own lines, which can run past its element's box
      const text = document.createRange();
      text.selectNodeContents(el);
      return [...text.getClientRects()].filter((r) => r.left < -0.5 || r.right > 360.5).map(() => el.textContent);
    }),
  );
  expect(outside).toEqual([]);
});
