import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { loop, open, pause, place, running, screenChunk, scrollTo, sections, setHidden, shown } from "./fx-helpers.ts";
import { pageHeading } from "./page-heading.ts";

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

// The machine's story runs on the shared loop, from when its screen is seen: the display names each call's model, and
// while the tab is hidden the story stands where it is.
const display = (page: Page) => page.locator('.hfm1 [data-disp="m"]').textContent();

test("the machine's story runs while it is seen, stands while the tab is hidden, and goes on when it is back", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  await expect(page.locator(".hfm1 .fig svg")).toHaveCount(1);
  await expect.poll(() => display(page), { timeout: 30_000 }).not.toBe("AXIOM");
  await setHidden(page, true);
  await pause(page);
  const held = await display(page);
  // longer than a call takes
  await page.waitForTimeout(8000);
  expect(await display(page)).toBe(held);
  await setHidden(page, false);
  await expect.poll(() => display(page), { timeout: 30_000 }).not.toBe(held);
});

test("after a call the card names the model, and the copied check shows once the clipboard has taken the text", async ({
  context,
  page,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  await expect
    .poll(() => page.locator(".hfm1 .rcard").evaluate((el) => getComputedStyle(el).opacity), { timeout: 30_000 })
    .toBe("1");
  await expect(page.locator(".hfm1 .rc-m")).not.toHaveText("");
  // the Codex tab copies the file alone: the key is set in the terminal, and a TOML parser would stop at the export
  await page.locator(".hfm1 .tab").nth(1).click();
  await page.locator(".hfm1 .cpy").click();
  await expect(page.locator(".hfm1 .cpy")).toHaveAttribute("data-state", "done");
  await expect.poll(() => page.locator(".hfm1 .cpy .ok").evaluate((el) => getComputedStyle(el).opacity)).not.toBe("0");
  const copied = await page.evaluate(() => navigator.clipboard.readText());
  expect(copied).toContain('base_url = "https://api.example.com/v1"');
  expect(copied).toContain('model = "<model>"');
  expect(copied).not.toContain("export");
});

test("a clipboard that refuses is not reported as copied, and without an API address there is nothing to copy", async ({
  page,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: () => Promise.reject(new DOMException("denied", "NotAllowedError")) },
    });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  await expect(page.locator(".hfm1 .fig svg")).toHaveCount(1);
  await page.locator(".hfm1 .cpy").click();
  await expect(page.locator(".hfm1 .cpy")).toHaveAttribute("aria-label", /复制失败/);
  await expect(page.locator(".hfm1 .cpy")).not.toHaveAttribute("data-state", "done");
});

test("without the site's API address the card says so and the copy button is disabled", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockConsole(page, "usual", {
    site: { name: "Axiom", currency: "USD", key_prefix: "sk-ax-v1-", registration: "invite" },
  });
  await page.goto(apps[0].url);
  await pageHeading(page);
  await expect(page.locator(".hfm1 pre")).toContainText("本站 API 地址暂不可用");
  await expect(page.locator(".hfm1 .cpy")).toBeDisabled();
  await expect(page.locator(".hfm1 .cpy")).toHaveAttribute("aria-label", /无法复制/);
});

// Pointing at the address line of the configuration lights the machine's one input, the plug: the script marks it .hl
// at run time, and the rule that draws it lit is the board's, kept for the inserted figure. (The key's line marks the key
// the same way; in the lock the key is drawn lit already.)
test("pointing at the address line lights the machine's plug", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  await expect(page.locator(".hfm1 .fig .lamp.on")).toHaveCount(1);
  // Cursor's lines: the address, then the key
  await page.locator(".hfm1 .tab").nth(2).click();
  const edge = page.locator('.hfm1 .fig [data-m="plug"] .iso-edge').first();
  const stroke = () => edge.evaluate((el) => getComputedStyle(el).stroke);
  const unlit = await stroke();
  await page.locator(".hfm1 pre .ln").nth(0).hover();
  await expect(page.locator('.hfm1 .fig [data-m="plug"]')).toHaveClass(/\bhl\b/);
  await expect.poll(stroke).not.toBe(unlit);
});

test("at 820 the page is laid out for a tablet as the board's script lays it out", async ({ page }) => {
  await page.setViewportSize({ width: 820, height: 1180 });
  await open(page);
  await expect(page.locator(".ho")).toHaveAttribute("data-tab", "");
  // the connect screen drops its cable, which the board's tablet rule hides
  await expect.poll(() => page.locator(".hfm1 .wire").evaluate((el) => getComputedStyle(el).display)).toBe("none");
  await page.setViewportSize({ width: 600, height: 900 });
  await expect(page.locator(".ho")).not.toHaveAttribute("data-tab");
});

test("the page asks for the site once, and the footer carries the site's own name", async ({ page }) => {
  let asked = 0;
  page.on("request", (request) => {
    if (/\/api\/console\/v1\/site(\?|$)/.test(request.url())) asked++;
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockConsole(page, "usual", {
    site: {
      name: "Acme Relay",
      currency: "USD",
      key_prefix: "sk-ax-v1-",
      registration: "invite",
      api_base_url: "https://api.acme.example",
    },
  });
  await page.goto(apps[0].url);
  await pageHeading(page);
  for (const sec of await sections(page)) {
    await scrollTo(page, sec);
    await shown(page, sec);
  }
  await expect(page.locator(".hfm6 .ft .brand")).toContainText("Acme Relay");
  await expect(page.locator(".hfm6 .ft")).toContainText("MiSans");
  expect(asked).toBe(1);
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
  // not reachable by the keyboard either: pressing Enter on it would start the tour
  await page.locator(".tour").focus();
  await expect(page.locator(".tour")).not.toBeFocused();
  await expect(page.locator(".tour")).toHaveAttribute("inert", "");
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
