import { createHash } from "node:crypto";
import { type Browser, expect, type Page, test } from "@playwright/test";
import type { AdminState } from "./admin-data.ts";
import { adminSuite } from "./admin-scenes.ts";
import type { MockState } from "./console-data.ts";
import { consoleSuite } from "./console-scenes.ts";
import type { Scene, Suite } from "./scene.ts";
import { zhText } from "./words.ts";

// The site's logo is a 20x20 box with object-contain (docs/frontend.md「主题与自由度」): a logo of any size, one that
// arrives late or one that fails must not move anything around it. The vitals test measures the whole page's CLS, where
// a logo's few pixels hide under the content that arrives after the load; this test watches the layout-shift entries
// whose sources are inside the logo slot, and requires every one of them to be absent at a desk and at 360px, on the
// sign-in page and in the shell of both apps. It also holds the mark itself to its 20x20 box.

type Logo = {
  name: string;
  width: number;
  height: number;
  // A response held back by this many milliseconds: the image box is painted before its bytes arrive.
  delay?: number;
  // The URL answers 404, so the component falls back to the platform mark.
  fail?: boolean;
};

const logos: Logo[] = [
  { name: "tiny", width: 1, height: 1 },
  { name: "huge", width: 1024, height: 1024 },
  { name: "very wide", width: 1024, height: 1 },
  { name: "very tall", width: 1, height: 1024 },
  { name: "slow", width: 64, height: 64, delay: 800 },
  { name: "failing", width: 64, height: 64, fail: true },
];

const widths = [1280, 360] as const;

const apps = [
  { name: "console", suite: consoleSuite, shell: "overview" },
  { name: "admin", suite: adminSuite, shell: "home" },
] as const;
type App = (typeof apps)[number];

function sceneOf(suite: Suite, name: string): Scene {
  const scene = suite.scenes.find((candidate) => candidate.name === name);
  if (!scene) {
    throw new Error(`no scene ${name}`);
  }
  return scene;
}

// The public address carries the content hash, as the server's does; the host serves the same bytes at the console and
// the admin paths.
function logoPath(app: App, logo: Logo) {
  const hash = createHash("sha256").update(`${logo.name} ${logo.width}x${logo.height}`).digest("hex");
  return app.name === "console" ? `/api/console/v1/site/logo/${hash}` : `/api/admin/v1/site/logo/${hash}`;
}

function logoBody({ width, height }: Logo) {
  const side = `width="${width}" height="${height}" viewBox="0 0 ${width} ${height}"`;
  return `<svg xmlns="http://www.w3.org/2000/svg" ${side}><rect width="${width}" height="${height}" fill="#5b8def"/></svg>`;
}

// The mocked API answers inside the browser, so the logo route is registered after suite.prepare's: Playwright tries
// the routes of a page in the reverse order, and the logo path sits under the API's own prefix.
async function serveLogo(page: Page, path: string, logo: Logo) {
  await page.route(new RegExp(`${path}$`), async (route) => {
    if (logo.delay) {
      await new Promise((done) => setTimeout(done, logo.delay));
    }
    if (logo.fail) {
      await route.fulfill({ status: 404, body: "not found", contentType: "text/plain" });
      return;
    }
    await route.fulfill({
      status: 200,
      body: logoBody(logo),
      contentType: "image/svg+xml",
      headers: { "Cache-Control": "no-store" },
    });
  });
}

// The observer is installed before the page's own scripts, so it sees the logo replace the fallback and any later shift.
async function observeShifts(page: Page) {
  await page.addInitScript(() => {
    const shifts: PerformanceEntry[] = [];
    (window as unknown as { __logoShifts: PerformanceEntry[] }).__logoShifts = shifts;
    new PerformanceObserver((list) => {
      shifts.push(...list.getEntries());
    }).observe({ type: "layout-shift", buffered: true });
  });
}

// One Logo is visible at a time: in the admin's sidebar at a desk, in its top bar below 1024px, on a sign-in page in its
// header, and in the console's bar, where the board's mark holds it. The hidden copy in the drawer and the other
// breakpoint's copy have no layout box.
const slotSelector = "header > span:has(> img), header > span:has(> svg), nav span:has(> img), nav span:has(> svg)";
// The console's mark, which shows the site's initial when there is no logo, as the board draws Axiom's A.
const boardMark = "header a.glogo > span";
const logoSlot = (page: Page) => page.locator(`${slotSelector}, ${boardMark}`).filter({ visible: true });

async function waitForLogo(page: Page, path: string, failing: boolean) {
  const slot = logoSlot(page);
  await expect(slot, "one visible logo slot").toHaveCount(1);
  if (failing) {
    await expect(slot.locator("img")).toHaveCount(0);
    await expect(slot.locator("svg").or(slot.getByText(/^\S$/))).toHaveCount(1);
    return;
  }
  const image = slot.locator("img");
  await expect(image).toHaveAttribute("src", path);
  await expect
    .poll(() =>
      image.evaluate((node) => {
        const img = node as HTMLImageElement;
        return img.complete && img.naturalWidth > 0;
      }),
    )
    .toBe(true);
}

const frames = (page: Page) =>
  page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))));

// The shifts whose source is the logo slot or a node inside it: the name beside the mark moves whenever the mark's box
// grows or shrinks, so a logo that took its own size to the page is caught. Entries from an input are not CLS and are
// left out; a shrunken name or a repaint adds none. A page-wide reflow that crosses the slot in passing is not the
// logo's doing and is not counted.
async function shiftsAroundLogo(page: Page) {
  return page.evaluate(() => {
    type Source = { node: Node | null; previousRect: DOMRectReadOnly; currentRect: DOMRectReadOnly };
    type Entry = PerformanceEntry & { value: number; hadRecentInput: boolean; sources?: Source[] };
    const entries = (window as unknown as { __logoShifts?: Entry[] }).__logoShifts ?? [];
    const slots = [...document.querySelectorAll<HTMLElement>("header > span, nav span, header a.glogo > span")].filter(
      (span) => {
        if (span.getClientRects().length === 0) {
          return false;
        }
        const mark = span.matches("a.glogo > span");
        return mark || [...span.children].some((child) => ["img", "svg"].includes(child.tagName.toLowerCase()));
      },
    );
    const touches = (source: Source) => {
      const node = source.node;
      return node !== null && slots.some((slot) => slot === node || slot.contains(node));
    };
    const rectOf = (rect: DOMRectReadOnly) => ({ x: rect.x, y: rect.y, width: rect.width, height: rect.height });
    return {
      slots: slots.length,
      shifts: entries
        .filter((entry) => !entry.hadRecentInput && entry.value > 0)
        .flatMap((entry) => {
          const sources = (entry.sources ?? []).filter(touches);
          return sources.length === 0
            ? []
            : [
                {
                  value: entry.value,
                  startTime: entry.startTime,
                  sources: sources.map((source) => ({
                    node: source.node instanceof Element ? source.node.tagName.toLowerCase() : source.node?.nodeName,
                    previous: rectOf(source.previousRect),
                    current: rectOf(source.currentRect),
                  })),
                },
              ];
        }),
    };
  });
}

async function checkSurface(browser: Browser, app: App, surface: string, logo: Logo, width: number) {
  const context = await browser.newContext({ viewport: { width, height: 800 }, locale: "zh-CN" });
  const page = await context.newPage();
  await observeShifts(page);
  const scene = sceneOf(app.suite, surface);
  const path = logoPath(app, logo);
  if (app.name === "console") {
    const state = (await app.suite.prepare(page, scene)) as MockState;
    state.site = { ...state.site, logo_url: path };
  } else {
    const state = (await app.suite.prepare(page, scene)) as AdminState;
    // The admin shows the platform's name until the public brand arrives; a site with that same name makes the logo the
    // only thing that changes, so a name update cannot be mistaken for a logo shift.
    state.brandName = zhText("brandName");
    state.logoUrl = path;
  }
  await serveLogo(page, path, logo);
  const failed = logo.fail
    ? page.waitForResponse((response) => response.url().endsWith(path) && response.status() === 404)
    : undefined;
  await scene.open(page);
  if (failed) {
    await failed;
  }
  await waitForLogo(page, path, Boolean(logo.fail));
  await page.waitForLoadState("networkidle");
  await frames(page);

  // The console's initial in place of a failed logo is the board's own 24px mark, not a 20x20 box.
  const lettered = app.name === "console" && surface !== "sign-in" && logo.fail;
  const slot = logoSlot(page);
  const mark = await (lettered ? slot : slot.locator("img, svg")).first().boundingBox();
  expect(mark, "the logo mark").not.toBeNull();
  const side = lettered ? 24 : 20;
  expect(mark?.width, "the logo mark's width").toBeCloseTo(side, 1);
  expect(mark?.height, "the logo mark's height").toBeCloseTo(side, 1);
  const outcome = await shiftsAroundLogo(page);
  expect(outcome.slots, "one visible logo slot").toBe(1);
  expect(outcome.shifts, `${app.name} ${surface} with a ${logo.name} logo at ${width}px`).toEqual([]);
  await context.close();
}

test.describe.configure({ mode: "parallel", timeout: 60_000 });

for (const app of apps) {
  for (const width of widths) {
    for (const logo of logos) {
      test(`${app.name} shows a ${logo.name} logo without a layout shift at ${width}px`, async ({ browser }) => {
        for (const surface of ["sign-in", app.shell]) {
          await checkSurface(browser, app, surface, logo, width);
        }
      });
    }
  }
}
