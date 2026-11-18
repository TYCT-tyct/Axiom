import { expect, type Page, test } from "@playwright/test";
import { loop, open, pause, place, running, screenChunk, scrollTo, sections, setHidden, shown } from "./fx-helpers.ts";

// The connect screen (§8.2's connect row): the machine is one SVG, fetched once the screen's code has come, which is when
// its place nears the view; the card shows the site's address in the tool's configuration.
const machine = /\/assets\/connect-figure-[\w-]+\.svg$/;

test("the machine is fetched after the screen's code, once, and the card carries the site's address", async ({
  page,
}) => {
  const asked: string[] = [];
  page.on("request", (request) => {
    const name = screenChunk.exec(request.url())?.[1] ?? (machine.test(request.url()) ? "machine" : undefined);
    if (name) asked.push(name);
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await expect.poll(() => asked).toContain("machine");
  expect(asked.indexOf("connect")).toBeGreaterThanOrEqual(0);
  expect(asked.indexOf("connect")).toBeLessThan(asked.indexOf("machine"));
  expect(asked.filter((name) => name === "machine")).toHaveLength(1);
  await expect(page.locator(".hfm1 .fig svg")).toHaveCount(1);
  await expect(page.locator(".hfm1 pre")).toContainText("https://api.example.com");
  await expect(page.locator(".hfm1")).not.toContainText("api.axiom.example");
  // a tool's tab changes the configuration
  await page.locator(".hfm1 .tab").nth(1).click();
  await expect(page.locator(".hfm1 pre")).toContainText("[model_providers.axiom]");
});

test("the machine's animations rest while the tab is hidden", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await expect(page.locator(".hfm1 .fig svg")).toHaveCount(1);
  await running(page);
  await setHidden(page, true);
  await pause(page);
  const states = () =>
    page.locator(".hfm1 .fig").evaluate((el) => el.getAnimations({ subtree: true }).map((a) => a.playState));
  await expect.poll(async () => (await states()).every((state) => state !== "running")).toBe(true);
  await setHidden(page, false);
});

// The tour and the menu's list of sections (the last screen's card): the tour glides through the screens, the list goes
// to one, and under reduced motion there is no tour to offer.
test("the tour glides down the page, and the menu's list of sections goes to one", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await page.locator(".tour").click();
  await expect.poll(() => page.locator(".hs").evaluate((el) => el.scrollTop)).toBeGreaterThan(100);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator(".hs").evaluate((el) => el.scrollTo({ top: 0 }));
  await page.locator(".bar .menu").click();
  await page.locator(".sml[data-sec='5']").click();
  await expect
    .poll(() => place(page, 5).evaluate((el) => Math.abs(el.getBoundingClientRect().top)), { timeout: 15_000 })
    .toBeLessThan(40);
});

test("under reduced motion the tour is not offered", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await open(page);
  await expect(page.locator(".tour")).toHaveClass(/\bgone\b/);
});

// The questions (§8.2's Faq row): the first answer's picture sends its request to one vendor after another on a timer of
// the screen's own, which runs on the shared loop: it stops with the tab hidden, and under reduced motion it never starts.
const vendor = (page: Page) => page.locator(".hfm5 .fan text.ft.on").textContent();

test("the first answer's picture moves on while it is seen, and stops while the tab is hidden", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 5);
  await shown(page, 5);
  const first = await vendor(page);
  await expect.poll(() => vendor(page)).not.toBe(first);
  await setHidden(page, true);
  await pause(page);
  const held = await vendor(page);
  // two turns of its timer, were it running
  await page.waitForTimeout(3200);
  expect(await vendor(page)).toBe(held);
  await setHidden(page, false);
  await expect.poll(() => vendor(page)).not.toBe(held);
});

test("a question is chosen by pointing at it, the cable follows it, and under reduced motion nothing moves on its own", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 5);
  await shown(page, 5);
  const first = await vendor(page);
  await page.locator(".hfm5 .qs .q").nth(2).hover();
  await expect(page.locator(".hfm5 .qs .q.on")).toContainText("03");
  await expect(page.locator(".hfm5 .patch .ring")).toHaveAttribute("cy", String(2 * 48 + 24));
  await page.locator(".hfm5 .qs .q").first().hover();
  await expect.poll(() => vendor(page)).toBe(first);
  await pause(page);
  expect(await loop(page)).toMatchObject({ frames: 0, owners: 0 });
});

test("on a phone the questions are an accordion: a second press closes the open one", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await open(page);
  await scrollTo(page, 5);
  await shown(page, 5);
  await page.locator(".hfm5 .fq .q").nth(1).click();
  await expect(page.locator(".hfm5 .fq.on")).toHaveCount(1);
  await expect(page.locator(".hfm5 .fq.on .q")).toContainText("02");
  await page.locator(".hfm5 .fq .q").nth(1).click();
  await expect(page.locator(".hfm5 .fq.on")).toHaveCount(0);
});

// The sea bed (§8.2's Bed row) is the page's footer: the credit for the Chinese font stays, what the site has no page for
// is left out, and its language and appearance switches are the header's.
test("the footer keeps the font's credit, names no page the site lacks, and its switches change the page", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce", colorScheme: "light" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  const last = (await sections(page)).at(-1) as number;
  await scrollTo(page, last);
  await shown(page, last);
  const footer = page.locator(".hfm6 .ft");
  await expect(footer).toContainText("MiSans");
  for (const sec of [".hfm5", ".hfm6"])
    await expect(page.locator(sec)).not.toContainText(/ICP|公网安备|api\.axiom\.example|接入文档|查看文档|工单/);
  await expect(footer.locator("[data-pick=zh]")).toHaveAttribute("aria-pressed", "true");

  await footer.locator("[data-pick=en]").click();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
  await expect(footer.locator("[data-pick=en]")).toHaveAttribute("aria-pressed", "true");
  await expect(footer).toContainText("One key for every model.");
  await footer.locator("[data-pick=dark]").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(footer.locator("[data-pick=dark]")).toHaveAttribute("aria-pressed", "true");
  // pressing the one already chosen changes nothing
  await footer.locator("[data-pick=dark]").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
});

// The whale (§8.2's whale row): its code is not asked for until the egg is called, by typing deepseek anywhere or by
// pressing the word in the first screen's strip, and under reduced motion it is never asked for.
const whale = /\/assets\/whale-[\w-]+\.js$/;
const watchWhale = (page: Page) => {
  const asked: string[] = [];
  page.on("request", (request) => {
    if (whale.test(request.url())) asked.push(request.url());
  });
  return asked;
};
// the whale's canvas has something on it: the shadow, or the whale
const painted = (page: Page) =>
  page.locator(".tr > canvas.wh").evaluate((canvas: HTMLCanvasElement) => {
    const { data } = (canvas.getContext("2d") as CanvasRenderingContext2D).getImageData(
      0,
      0,
      canvas.width,
      canvas.height,
    );
    return data.some((value, i) => i % 4 === 3 && value > 0);
  });

test("the whale's code comes when deepseek is typed, not before, and the whale swims on the sea", async ({ page }) => {
  const asked = watchWhale(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await running(page);
  await scrollTo(page, (await sections(page)).at(-1) as number);
  await page.keyboard.type("deeps");
  expect(asked).toEqual([]);
  await page.keyboard.type("eek");
  await expect.poll(() => asked).toHaveLength(1);
  await expect.poll(() => painted(page)).toBe(true);
});

test("pressing deepseek in the first screen's strip calls the whale", async ({ page }) => {
  const asked = watchWhale(page);
  await open(page);
  await running(page);
  expect(asked).toEqual([]);
  await page.locator(".hs > .main .fam [data-egg=deepseek]").click();
  await expect.poll(() => asked).toHaveLength(1);
  await expect.poll(() => painted(page)).toBe(true);
});

test("under reduced motion the whale is never called", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const asked = watchWhale(page);
  await open(page);
  await page.keyboard.type("deepseek");
  await page.locator(".hs > .main .fam [data-egg=deepseek]").click();
  await pause(page);
  expect(asked).toEqual([]);
  expect(await painted(page)).toBe(false);
});
