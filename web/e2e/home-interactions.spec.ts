import { expect, type Page, test } from "@playwright/test";
import { collectErrors } from "./collect-errors.ts";
import { loop, open, running, scrollTo, shown } from "./fx-helpers.ts";

// Home-page interactions matching the final Site / SitePhone boards (#747):
// 1) desktop inertial wheel scrolling (wide only, releases loop when settled)
// 2) random first-screen entrance between tide and decode
// 3) narrow phone/tablet steps (.hfm3) and trust (.hfm4) frames sized to their text height + 96px

async function watchPolicy(page: Page) {
  await page.addInitScript(() => {
    const seen: string[] = [];
    (window as unknown as { policy: string[] }).policy = seen;
    document.addEventListener("securitypolicyviolation", (e) => seen.push(`${e.violatedDirective} ${e.blockedURI}`));
  });
  return () => page.evaluate(() => (window as unknown as { policy: string[] }).policy);
}

test("desktop wheel scrolls with inertia on wide and settles without keeping a loop owner", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await running(page);
  await expect.poll(() => page.locator(".tr").getAttribute("data-enter")).toBe("done");

  const scroller = page.locator(".hs");
  const baselineOwners = (await loop(page)).owners;
  expect(await scroller.evaluate((el) => el.scrollTop)).toBe(0);

  // Record every scrollTop seen during the inertial glide to verify multi-frame easing.
  await scroller.evaluate((el) => {
    const samples: number[] = [];
    (window as unknown as { scrollSamples: number[] }).scrollSamples = samples;
    el.addEventListener("scroll", () => samples.push(el.scrollTop), { passive: true });
  });

  // Move pointer into the scroller and wheel down: scrollTop eases smoothly to ~400 and stops.
  await page.mouse.move(720, 450);
  await page.mouse.wheel(0, 400);

  await expect.poll(() => scroller.evaluate((el) => el.scrollTop), { timeout: 5_000 }).toBeGreaterThanOrEqual(395);
  expect(await scroller.evaluate((el) => el.scrollTop)).toBeLessThanOrEqual(405);

  const samples = await page.evaluate(() => (window as unknown as { scrollSamples: number[] }).scrollSamples);
  // Multi-frame easing produces intermediate positions strictly between start (0) and target (400),
  // rather than jumping to 400 in a single scroll event.
  expect(samples.filter((y) => y > 0 && y < 395).length).toBeGreaterThanOrEqual(2);

  // Once settled, the wheel easing releases its loop owner.
  await expect.poll(async () => (await loop(page)).owners).toBe(baselineOwners);
});

for (const { variant, random } of [
  { variant: "tide", random: 0.1 },
  { variant: "decode", random: 0.9 },
]) {
  test(`first-screen entrance plays ${variant} cleanly with zero CSP violations or errors`, async ({ page }) => {
    const errors = collectErrors(page);
    const violations = await watchPolicy(page);
    await page.addInitScript((r) => {
      Math.random = () => r;
      const states: string[] = [];
      (window as unknown as { enterStates: string[] }).enterStates = states;
      const obs = new MutationObserver((records) => {
        for (const rec of records) {
          if (rec.type === "attributes" && rec.attributeName === "data-enter" && rec.target instanceof HTMLElement) {
            const val = rec.target.dataset.enter;
            if (val) states.push(val);
          }
        }
      });
      obs.observe(document, { subtree: true, attributes: true, attributeFilter: ["data-enter"] });
    }, random);
    await page.setViewportSize({ width: 1440, height: 900 });
    await open(page);
    await running(page);

    // Wait for the entrance to finish and verify the chosen variant actually ran.
    await expect.poll(() => page.locator(".tr").getAttribute("data-enter")).toBe("done");
    const states = await page.evaluate(() => (window as unknown as { enterStates: string[] }).enterStates);
    expect(states).toContain(variant);
    expect(states.at(-1)).toBe("done");
    await expect(page.getByRole("heading", { level: 1 })).toContainText("一个");
    await expect(page.locator(".hs > main .lede")).toContainText("每条线路实测");
    expect(errors).toEqual([]);
    expect(await violations()).toEqual([]);
  });
}

test("switching language mid-decode settles the entrance without reverting text", async ({ page }) => {
  await page.addInitScript(() => {
    Math.random = () => 0.9;
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await running(page);
  await page.getByRole("banner").getByRole("button", { name: "简体中文" }).click();
  await page.getByRole("option", { name: /English/ }).click();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("One key for every model");
  // Wait longer than decode duration (1560ms) to confirm text does not revert.
  await page.waitForTimeout(1800);
  await expect(page.locator(".hs > main").getByRole("link", { name: "Sign in and get an API key" })).toBeVisible();
});

for (const { label, width, height, lang } of [
  { label: "phone zh", width: 390, height: 844, lang: "zh" },
  { label: "phone en", width: 390, height: 844, lang: "en" },
  { label: "tablet zh", width: 820, height: 1180, lang: "zh" },
  { label: "tablet en", width: 820, height: 1180, lang: "en" },
]) {
  test(`narrow steps and trust frames size to their text height without clipping (${label})`, async ({ page }) => {
    await page.addInitScript((l) => localStorage.setItem("axiom.language", l), lang);
    await page.setViewportSize({ width, height });
    await open(page);

    for (const sec of [3, 4]) {
      await scrollTo(page, sec);
      await shown(page, sec);
      const metrics = await page.locator(`.hfm${sec} .frame`).evaluate((frame: HTMLElement) => {
        const sc = frame.querySelector<HTMLElement>(":scope > .sec") as HTMLElement;
        sc.style.position = "relative";
        sc.style.height = "auto";
        const needed = sc.offsetHeight;
        sc.style.position = "";
        sc.style.height = "";
        return {
          frameHeight: frame.getBoundingClientRect().height,
          expected: Math.ceil(needed + 96),
          secScrollHeight: sc.scrollHeight,
          secClientHeight: sc.clientHeight,
        };
      });
      expect(Math.abs(metrics.frameHeight - metrics.expected)).toBeLessThanOrEqual(2);
      // No vertical clipping inside the frame.
      expect(metrics.frameHeight).toBeGreaterThanOrEqual(metrics.secScrollHeight);
    }
  });
}
