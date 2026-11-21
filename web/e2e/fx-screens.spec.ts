import { expect, type Page, test } from "@playwright/test";
import { brandStyle } from "../packages/ui/src/brand.ts";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { delivered, open, pause, running, screenChunk, scrollTo, setHidden, shown } from "./fx-helpers.ts";
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
  // the heading's comma is the language's, which the page hands the region
  await expect(page.locator(".hfm1 h2")).toContainText("改一个地址，");
  await expect(page.locator(".hfm1 pre")).toContainText("https://api.example.com");
  await expect(page.locator(".hfm1")).not.toContainText("api.axiom.example");
  // a tool's tab changes the configuration
  await page.locator(".hfm1 .tab").nth(1).click();
  await expect(page.locator(".hfm1 pre")).toContainText("[model_providers.axiom]");
  // the screen built again for the other layout puts the figure in place without asking for it again
  await page.setViewportSize({ width: 390, height: 844 });
  await scrollTo(page, 1);
  await expect(page.locator(".hfm1 .fig svg")).toHaveCount(1);
  await page.setViewportSize({ width: 1440, height: 900 });
  await scrollTo(page, 1);
  await expect(page.locator(".hfm1 .fig svg")).toHaveCount(1);
  expect(asked.filter((name) => name === "machine")).toHaveLength(1);
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
  // the first call the story makes is the board's second: gpt-6.1-sol, OpenAI's
  await expect(page.locator(".hfm1 .rc-m")).toHaveText("gpt-6.1-sol");
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

test("a clipboard that refuses is not reported as copied, and the card says to select it by hand", async ({ page }) => {
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
  // said on the card too, where the file's name is, not only to a screen reader
  await expect(page.locator(".hfm1 .fl")).toHaveText(/复制失败/);
  await expect(page.locator(".hfm1 .fl")).toBeVisible();
});

// A copy's word belongs to the copy and the tool it copied: picking another tool before the clipboard answers, or after,
// leaves no word about it on the new tool. The clipboard answers when the case says, and the page's clock stands still,
// so a word that is not taken away would stay.
test("a copy's word belongs to the tool it copied", async ({ page }) => {
  await page.addInitScript(() => {
    const held: (() => void)[] = [];
    (window as unknown as { answerCopy: () => void }).answerCopy = () => held.shift()?.();
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: () => new Promise<void>((done) => held.push(done)) },
    });
  });
  const answer = () => page.evaluate(() => (window as unknown as { answerCopy: () => void }).answerCopy());
  const copy = page.locator(".hfm1 .cpy");
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  await page.clock.install();
  await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 2000);

  // the clipboard answers after another tool is picked
  await copy.click();
  await page.locator(".hfm1 .tab").nth(1).click();
  await answer();
  await page.waitForTimeout(300);
  await expect(copy).not.toHaveAttribute("data-state", "done");

  // the clipboard answers first, and then another tool is picked
  await copy.click();
  await answer();
  await expect(copy).toHaveAttribute("data-state", "done");
  await page.locator(".hfm1 .tab").nth(2).click();
  await expect(copy).not.toHaveAttribute("data-state", "done");
  // the button says what it copies for the tool shown: Cursor takes the base URL alone
  await expect(copy).toHaveAttribute("aria-label", "复制 Base URL");
  await expect(copy).toHaveAttribute("title", "复制 Base URL");

  // a second copy takes the first one's word away at once, before its own answer
  await copy.click();
  await answer();
  await expect(copy).toHaveAttribute("data-state", "done");
  await copy.click();
  await expect(copy).not.toHaveAttribute("data-state", "done");
  await answer();
  await expect(copy).toHaveAttribute("data-state", "done");

  // the page put out of view drops a copy still waiting on the clipboard
  await copy.click();
  await setHidden(page, true);
  await answer();
  await setHidden(page, false);
  await page.waitForTimeout(300);
  await expect(copy).not.toHaveAttribute("data-state", "done");
});

// Each tool's card is a configuration as its target takes it: Codex's file keeps the key's terminal line a comment, so
// the card is a valid file, and its button copies the file; without an address there is no command at all.
test("each tool's card holds what its target takes, and says what its button copies", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 1);
  await shown(page, 1);
  const copy = page.locator(".hfm1 .cpy");
  const expected = ["复制终端命令", "复制配置文件", "复制 Base URL", "复制代码", "复制 .env"];
  for (const [i, label] of expected.entries()) {
    await page.locator(".hfm1 .tab").nth(i).click();
    await expect(copy).toHaveAttribute("aria-label", label);
  }
  await page.locator(".hfm1 .tab").nth(1).click();
  // the picked tool's lines are decoded in: read them once nothing is left tumbling or still to come
  const unwritten = () =>
    page.locator(".hfm1 pre .g, .hfm1 pre .z").evaluateAll((els) => els.map((el) => el.textContent).join(""));
  await expect.poll(unwritten).toBe("");
  const lines = await page.locator(".hfm1 pre .ln").allTextContents();
  // every line of the Codex card that is not TOML is a comment
  expect(lines.filter((line) => /export/.test(line))).toEqual([expect.stringMatching(/#\s*export AXIOM_API_KEY=/)]);
});

test("without the site's API address the card says so and the copy button is disabled", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockConsole(page, "usual", {
    site: { name: "Axiom", currency: "USD", key_prefix: "sk-ax-v1-", registration: "invite" },
  });
  await page.goto(apps[0].url);
  await pageHeading(page);
  await expect(page.locator(".hfm1 pre")).toContainText("本站 API 地址暂不可用");
  // no command with a hole where the address would be, on the card or in the questions' picture
  await expect(page.locator(".hfm1 pre")).not.toContainText(/BASE_URL|base_url|export/);
  await scrollTo(page, 5);
  await shown(page, 5);
  await expect(page.locator(".hfm5 .w2")).not.toContainText("export");
  await expect(page.locator(".hfm5 .w2")).toContainText("本站 API 地址暂不可用");
  await expect(page.locator(".hfm1 .cpy")).toBeDisabled();
  await expect(page.locator(".hfm1 .cpy")).toHaveAttribute("aria-label", /无法复制/);
});

// Pointing at the address line of the configuration lights the machine's one input, the plug: the script marks it .hl
// at run time, and the rule that draws it lit is the board's, kept for the inserted figure. (The key's line marks the key
// the same way; in the lock the key is drawn lit already.)
test("pointing at the address line lights the machine's plug", async ({ page }) => {
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
  for (const sec of delivered) {
    await scrollTo(page, sec);
    await shown(page, sec);
  }
  // the words that name the service name the site
  await expect(page.locator(".hfm1 .intro")).toContainText("Acme Relay");
  await expect(page.locator(".hfm5 .ans").first()).toContainText("Acme Relay");
  // the whole brand is the site's: the board's own "Axiom" beside it would be read and shown as a second name
  await expect(page.locator(".hfm6 .ft .brand")).toHaveAccessibleName("Acme Relay");
  await expect(page.locator(".hfm6 .ft .brand")).toHaveText("Acme Relay");
  await expect(page.locator(".hfm6 .ft")).toContainText("MiSans");
  expect(asked).toBe(1);
});

// The site's brand colour is put on the page once and the screens take it: the accent the questions write the address
// in (--accentT) and the lamp of the connect machine (--accentSolid in light, --accentT in dark), as brandStyle makes
// them for the scheme.
const purple = "#7c3aed";
const brandPair = (name: string) =>
  (brandStyle(purple)?.[name] ?? "").match(/^light-dark\((.*), (.*)\)$/)?.slice(1) ?? [];
const rgbOf = (hex = "") => `rgb(${[1, 3, 5].map((i) => Number.parseInt(hex.slice(i, i + 2), 16)).join(", ")})`;
for (const [scheme, at] of [
  ["light", 0],
  ["dark", 1],
] as const) {
  test(`the site's brand colour reaches the screens below, ${scheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.setViewportSize({ width: 1440, height: 900 });
    await mockConsole(page, "usual", {
      site: {
        name: "Axiom",
        currency: "USD",
        key_prefix: "sk-ax-v1-",
        api_base_url: "https://api.example.com",
        registration: "invite",
        brand_color: purple,
      },
    });
    await page.goto(apps[0].url);
    await pageHeading(page);
    await scrollTo(page, 1);
    await shown(page, 1);
    const lamp = scheme === "light" ? brandPair("--accentSolid")[at] : brandPair("--accentT")[at];
    await expect(page.locator(".hfm1 .fig .lamp.on .iso-fill").first()).toHaveCSS("fill", rgbOf(lamp));
    await scrollTo(page, 5);
    await shown(page, 5);
    await expect(page.locator(".hfm5 .ac").first()).toHaveCSS("color", rgbOf(brandPair("--accentT")[at]));
  });
}

// The questions (§8.2's Faq row): the first answer's picture sends its request to one vendor after another on a timer of
// the screen's own, which runs on the shared loop: it stops with the tab hidden.
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

// The trust screen's log and the failed request's picture are rows of one line: the design's stylesheet also holds the
// sign-in page's .lg, a page a view tall, which they must not take.
for (const [name, viewport] of [
  ["wide", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
] as const)
  test(`the screens' log rows are one line tall, ${name}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await open(page);
    for (const sec of [4, 5]) {
      await scrollTo(page, sec);
      await shown(page, sec);
    }
    // the failed request's answer, open
    if (name === "wide") await page.locator(".hfm5 .qs .q").nth(4).hover();
    else await page.locator(".hfm5 .fq .q").nth(4).click();
    const rows = page.locator(".hfm4 .lg, .hfm5 .lg");
    await expect(rows).toHaveCount(7);
    const heights = await rows.evaluateAll((els) => els.map((el) => el.getBoundingClientRect().height));
    for (const height of heights) expect(height).toBeLessThan(32);
  });

for (const language of ["zh", "en"] as const)
  for (const width of [1440, 1280])
    test(`every answer sits in the middle of the room above its picture, which is whole in the card, ${language} at ${width}`, async ({
      page,
    }) => {
      await page.addInitScript((lang) => localStorage.setItem("axiom.language", lang), language);
      await page.setViewportSize({ width, height: 900 });
      await open(page);
      await scrollTo(page, 5);
      await shown(page, 5);
      const questions = page.locator(".hfm5 .qs .q");
      for (let i = 0; i < 8; i++) {
        await questions.nth(i).hover();
        // measured once the answer has risen into place
        await page
          .locator(".hfm5 .pane.on .ans")
          .evaluate((answer) => Promise.all(answer.getAnimations().map((animation) => animation.finished)));
        const box = await page.locator(".hfm5 .pane.on").evaluate((pane) => {
          const answer = pane.querySelector(".ans") ?? pane;
          // the answer's lines, not its box, which spans the room
          const lines = document.createRange();
          lines.selectNodeContents(answer);
          const { top, bottom } = lines.getBoundingClientRect();
          const { paddingLeft, paddingRight } = getComputedStyle(answer);
          return {
            text: { top, bottom },
            sides: [paddingLeft, paddingRight],
            stage: (pane.querySelector(".stage") ?? pane).getBoundingClientRect(),
            card: (pane.closest(".card") ?? pane).getBoundingClientRect(),
          };
        });
        const [above, below] = [box.text.top - box.card.top, box.stage.top - box.text.bottom];
        expect(below, `answer ${i + 1}`).toBeGreaterThanOrEqual(16);
        expect(Math.abs(above - below), `answer ${i + 1}: ${above} above, ${below} below`).toBeLessThanOrEqual(1);
        expect(box.sides[0], `answer ${i + 1}`).toBe(box.sides[1]);
        expect(box.stage.bottom, `answer ${i + 1}`).toBeLessThanOrEqual(box.card.bottom + 0.5);
      }
      // the Chinese answers are written to the board's three lines, so the card keeps the board's height
      if (language === "zh" && width === 1440)
        expect(await page.locator(".hfm5 .card").evaluate((card) => card.getBoundingClientRect().height)).toBe(433);
    });

test("a question is chosen by pointing at it, and the cable follows it", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 5);
  await shown(page, 5);
  await page.locator(".hfm5 .qs .q").nth(2).hover();
  await expect(page.locator(".hfm5 .qs .q.on")).toContainText("03");
  await expect(page.locator(".hfm5 .patch .ring")).toHaveAttribute("cy", String(2 * 48 + 24));
});

test("on a phone the questions are an accordion: a second press closes the open one", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await open(page);
  await scrollTo(page, 5);
  await shown(page, 5);
  // a closed answer is clipped to nothing on screen, and out of what is read by the same state
  const answer = (i: number) => page.locator(".hfm5 .fq").nth(i).locator(".fqa");
  await page.locator(".hfm5 .fq .q").nth(1).click();
  await expect(page.locator(".hfm5 .fq.on")).toHaveCount(1);
  await expect(page.locator(".hfm5 .fq.on .q")).toContainText("02");
  await expect(answer(1)).toHaveAttribute("aria-hidden", "false");
  await expect(answer(0)).toHaveAttribute("aria-hidden", "true");
  // the open answer is there to read: its text shows in full height
  const height = (i: number) => answer(i).evaluate((el) => el.getBoundingClientRect().height);
  await expect.poll(() => height(1)).toBeGreaterThan(40);
  await expect(answer(1).locator(".ans")).toBeVisible();
  await page.locator(".hfm5 .fq .q").nth(1).click();
  await expect(page.locator(".hfm5 .fq.on")).toHaveCount(0);
  await expect(answer(1)).toHaveAttribute("aria-hidden", "true");
  await expect.poll(() => height(1)).toBeLessThan(1);
});

// The sea bed (§8.2's Bed row) is the page's footer: the credit for the Chinese font stays, the board's placeholder ICP
// numbers and example address are left out, and its language and appearance switches are the header's.
test("the footer keeps the font's credit, shows no placeholder record numbers, and its switches change the page", async ({
  page,
}) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  const last = delivered.at(-1) as number;
  await scrollTo(page, last);
  await shown(page, last);
  const footer = page.locator(".hfm6 .ft");
  await expect(footer).toContainText("MiSans");
  for (const sec of [".hfm5", ".hfm6"])
    await expect(page.locator(sec)).not.toContainText(/ICP|公网安备|api\.axiom\.example/);
  await expect(footer.locator("[data-pick=zh]")).toHaveAttribute("aria-pressed", "true");

  await footer.locator("[data-pick=en]").click();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
  await expect(footer.locator("[data-pick=en]")).toHaveAttribute("aria-pressed", "true");
  await expect(footer).toContainText("One key for every model it may use.");
  await footer.locator("[data-pick=dark]").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(footer.locator("[data-pick=dark]")).toHaveAttribute("aria-pressed", "true");
  // pressing the one already chosen changes nothing
  await footer.locator("[data-pick=dark]").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
});

// The whale (§8.2's whale row): its code is not asked for until the egg is called, by typing deepseek anywhere or by
// pressing the word in the first screen's strip.
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

// The whale's first pixels come after about 1.3 s of its own time, and its step is capped at 0.05 s, so a machine
// drawing under 20 frames a second stretches that past any wall-clock wait. The page's clock drives it instead, as
// fx-environments.spec.ts does: the case no longer depends on how busy the machine is.
async function whaleShows(page: Page) {
  for (let seen = 0; !(await painted(page).catch(() => false)); seen += 100) {
    expect(seen, "the whale drew within 5 s of the page's time").toBeLessThan(5000);
    await page.clock.runFor(100);
    await page.waitForTimeout(20);
  }
}

test("the whale's code comes when deepseek is typed, not before, and the whale swims on the sea", async ({ page }) => {
  const asked = watchWhale(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await running(page);
  await scrollTo(page, delivered.at(-1) as number);
  await page.clock.install();
  await page.keyboard.type("deeps");
  expect(asked).toEqual([]);
  await page.keyboard.type("eek");
  await expect.poll(() => asked).toHaveLength(1);
  await whaleShows(page);
});

test("pressing deepseek in the first screen's strip calls the whale", async ({ page }) => {
  const asked = watchWhale(page);
  await open(page);
  await running(page);
  expect(asked).toEqual([]);
  await page.clock.install();
  await page.locator(".hs > .main .fam [data-egg=deepseek]").click();
  await expect.poll(() => asked).toHaveLength(1);
  await whaleShows(page);
});

// The sea bed's dune is drawn, from the site's own name: its wordmark mask is made by drawing the name on an offscreen
// canvas (never in the document), so the test records what that canvas is asked to draw.
const watchWordmark = (page: Page) =>
  page.addInitScript(() => {
    const drawn: string[] = [];
    (window as unknown as { offscreenText: string[] }).offscreenText = drawn;
    const fillText = CanvasRenderingContext2D.prototype.fillText;
    CanvasRenderingContext2D.prototype.fillText = function (this: CanvasRenderingContext2D, text: string, ...rest) {
      if (!this.canvas.isConnected) drawn.push(text);
      return fillText.call(this, text, ...rest);
    } as typeof fillText;
  });
const wordmarks = (page: Page) => page.evaluate(() => (window as unknown as { offscreenText: string[] }).offscreenText);
const inked = (page: Page) =>
  page.locator(".hfm6 canvas.art").evaluate((canvas: HTMLCanvasElement) => {
    const { data } = (canvas.getContext("2d") as CanvasRenderingContext2D).getImageData(
      0,
      0,
      canvas.width,
      canvas.height,
    );
    return data.filter((value, i) => i % 4 === 3 && value > 0).length;
  });
for (const [name, site] of [
  ["Axiom", undefined],
  [
    "Acme Relay",
    {
      name: "Acme Relay",
      currency: "USD",
      key_prefix: "sk-ax-v1-",
      registration: "invite",
      api_base_url: "https://api.example.com",
    },
  ],
] as const)
  test(`the sea bed draws its dune, with the site's name for the wordmark (${name})`, async ({ page }) => {
    await watchWordmark(page);
    await page.setViewportSize({ width: 1440, height: 900 });
    await mockConsole(page, "usual", site && { site });
    await page.goto(apps[0].url);
    await pageHeading(page);
    await scrollTo(page, 6);
    await shown(page, 6);
    await expect.poll(() => inked(page)).toBeGreaterThan(1000);
    // The dune may be drawn once under the default name before the site's answer arrives; the one that stays is the site's.
    await expect.poll(async () => (await wordmarks(page)).at(-1)).toBe(name);
    if (name === "Axiom") expect(await wordmarks(page)).not.toContain("Acme Relay");
  });
