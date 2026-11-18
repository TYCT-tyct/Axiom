import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test as base, expect, type Page, type TestInfo } from "@playwright/test";
import { build, preview } from "vite";
import { boardEngines, boardNames } from "../packages/boards/catalog.ts";
import { regions } from "../packages/boards/regions.ts";
import { readBoard } from "../packages/boards/source.ts";
import { collectErrors } from "./collect-errors.ts";
import { openBoard } from "./design/board.ts";
import { compare } from "./design/compare.ts";
import { fontsLoaded } from "./design/fonts.ts";
import { rulings } from "./design/rulings.ts";
import { canvasesReady, type Stage, stagePaused } from "./design/stage.ts";

const root = join(import.meta.dirname, "../packages/boards");
const design = process.env.AXIOM_DESIGN_DIR;
const referenceRoot = design
  ? existsSync(join(design, "project"))
    ? join(design, "project")
    : design
  : join(root, "inputs");
const test = base.extend<object, { boardsURL: string }>({
  boardsURL: [
    // biome-ignore lint/correctness/noEmptyPattern: Playwright requires a destructured fixture argument.
    async ({}, use) => {
      // The production build cache restores only site, console and admin. Build this isolated demo from this tree.
      const outDir = mkdtempSync(join(tmpdir(), "axiom-boards-demo-"));
      await build({ root, logLevel: "error", build: { outDir } });
      const server = await preview({ root, build: { outDir }, preview: { port: 0, strictPort: true } });
      const address = server.httpServer.address();
      if (!address || typeof address === "string") throw new Error("no demo server port");
      try {
        await use(`http://localhost:${address.port}`);
      } finally {
        await new Promise<void>((resolve, reject) =>
          server.httpServer.close((error) => (error ? reject(error) : resolve())),
        );
        rmSync(outDir, { recursive: true, force: true });
      }
    },
    // the demo build of the whole boards takes more than the default 30 s fixture time on a loaded runner (#819's run)
    { scope: "worker", timeout: 240_000 },
  ],
});
const originalCases = [
  { name: "Login", engines: ["ocean-orig", "login-ui"], selector: ".lg-form .lg-step", prop: "mode" },
  { name: "ClawdEgg", engines: ["ocean-orig", "clawd", "clawd-egg"], selector: ".eg .mr", prop: "mode" },
  { name: "AxApp", engines: [], selector: ".p-ov", prop: "theme" },
];
const cases = [
  ...originalCases.map((board) => ({ ...board, region: false, canvas: false })),
  ...boardNames
    .filter((name) => !originalCases.some((board) => board.name === name))
    .map((name) => ({
      name,
      engines: boardEngines(name, readBoard(join(root, "inputs", `${name}.dc.html`))),
      selector: originalCases.find((board) => name.startsWith(board.name))?.selector ?? ".board",
      prop: name.startsWith("Site") ? "system" : name.startsWith("Login") ? "mode" : "theme",
      region: false,
      canvas: /<canvas[\s>]/.test(readBoard(join(root, "inputs", `${name}.dc.html`))),
    })),
  ...["AxApp", "AxAppPhone"].map((name) => ({
    name,
    engines: [],
    selector: ".p-ov",
    prop: "theme",
    region: true,
    canvas: false,
  })),
];

async function extraInteractions(page: Page, name: string) {
  if (name.startsWith("Site")) {
    await page.locator(".ho header .lang").dispatchEvent("click");
    await page.clock.runFor(1000);
    await expect(page.locator(".lp")).toHaveAttribute("data-open", "");
    await page.locator(".lp-x").dispatchEvent("click");
    await page.locator(".ho header .theme").dispatchEvent("click");
    await page.locator(".ho [data-go]").first().dispatchEvent("click");
    await page.clock.runFor(5000);
    await expect(page.locator(".lg [data-act='continue']")).toBeVisible();
  } else if (name.startsWith("AxApp")) {
    await page.locator(".gbar .gsr,.gbar .gsi").first().dispatchEvent("click");
    await expect(page.locator(".kpn")).toBeVisible();
  } else {
    const selectors: Record<string, string> = { Models: ".tab", Faq: ".q", Steps: ".st", ConnectV6: ".tab" };
    const family = Object.keys(selectors).find((family) => name.startsWith(family));
    if (family)
      await page
        .locator(selectors[family] as string)
        .nth(1)
        .dispatchEvent("click");
  }
  await page.clock.runFor(1000);
}
async function regionMenus(page: Page, reference: Page, name: string, suffix: string, info: TestInfo) {
  for (const selector of [".gbal", ".gav", ".ghlp"]) {
    if (!(await page.locator(selector).count())) continue;
    await page.locator(selector).first().dispatchEvent("click");
    await reference.locator(selector).first().dispatchEvent("click");
    await page.clock.runFor(500);
    await reference.clock.runFor(500);
    await expect(page.locator(".pm.open").first()).toBeVisible();
    await expect(reference.locator(".pm.open").first()).toBeVisible();
    const menu = await compare(reference, page, [], `${name}-regions-${selector.slice(1)}-${suffix}`, info);
    expect.soft(menu.error, `see ${menu.diff ?? menu.page}`).toBeUndefined();
  }
}

// What the demo's regions (Shell and Overview, demo/regions.tsx) leave out, hidden on the reference. Only theirs: another
// region of the same board, such as the admin's shell, roots at the same elements and leaves out other things.
function omitted(board: string) {
  return regions
    .filter((region) => region.board === board && ["Shell", "Overview"].includes(region.name))
    .flatMap((region) =>
      region.roots.flatMap((root) =>
        (region.omit ?? []).map((selector) => `${root} ${selector}{display:none!important}`),
      ),
    )
    .join("\n");
}

async function loginInteractions(page: Page) {
  await page.locator("#lg-id").fill("reader@example.com");
  await page.locator('[data-act="consent"]').check();
  await page.locator('[data-act="continue"]').click();
  await page.clock.runFor(2000);
  await expect(page.locator('[data-step="code"]')).toBeVisible();
  for (let i = 0; i < 6; i++) await page.locator(`[data-act="digit"][data-i="${i}"]`).fill(String(i + 1));
  await page.clock.runFor(3000);
  await expect(page.locator('[data-step="key"]')).toBeVisible();
}

async function boardInteractions(page: Page, name: string) {
  if (name.startsWith("Login")) await loginInteractions(page);
  else if (name === "ClawdEgg") {
    await page.locator('.mr[data-i="1"]').dispatchEvent("pointerover", { bubbles: true });
    await page.clock.runFor(12000);
    await expect(page.locator('.mr[data-i="1"]')).toHaveClass(/on/);
  } else if (name === "AxApp") {
    await page.locator(".app2").dispatchEvent("mouseover", { bubbles: true });
    await page.keyboard.press("Control+k");
    await expect(page.locator(".kpn")).toBeVisible();
    await page.clock.runFor(500);
    await expect(page.locator(".kin input")).toBeFocused();
    await page.keyboard.press("Escape");
    await page.clock.runFor(500);
    await expect(page.locator(".kpn")).toBeHidden();
  } else await extraInteractions(page, name);
}

test.use({ timezoneId: "Asia/Shanghai" });

for (const board of cases)
  for (const width of [1440, 390])
    for (const colorScheme of ["light", "dark"] as const) {
      test(`${board.name}${board.region ? " regions" : ""} ${width} ${colorScheme}: artboard diff and CSP interactions`, async ({
        browser,
        page,
        boardsURL,
      }, info) => {
        const view: Stage = {
          viewport: { width, height: 900 },
          colorScheme,
          timezoneId: "Asia/Shanghai",
          time: new Date("2026-10-24T04:00:00Z"),
        };
        const errors = collectErrors(page);
        await page.addInitScript(() => {
          document.addEventListener("securitypolicyviolation", (event) =>
            console.error(`CSP ${event.violatedDirective}: ${event.blockedURI}`),
          );
        });
        await page.setViewportSize(view.viewport);
        await page.emulateMedia({ colorScheme, reducedMotion: "no-preference" });
        await stagePaused(page, view);
        await page.goto(`${boardsURL}/${board.name}${board.region ? "/regions" : ""}?theme=${colorScheme}`);
        await expect(page.locator(board.selector).first()).toBeAttached();
        await page.evaluate(() => document.fonts.ready.then(() => {}));
        const reference = await openBoard(
          browser,
          {
            file: `${board.name}.dc.html`,
            props: { [board.prop]: colorScheme === "dark" ? "深色" : "浅色" },
            engines: board.engines,
            actions: board.name === "AxApp" || board.region ? [["go", "ov"]] : [],
          },
          referenceRoot,
          view,
        );
        try {
          await expect(reference.page.locator(board.selector).first()).toBeAttached();
          await fontsLoaded(reference.page, reference.fonts);
          await Promise.all([canvasesReady(page), canvasesReady(reference.page)]);
          await page.clock.runFor(5000);
          await reference.page.clock.runFor(5000);
          // A region leaves out what its manifest omits, so its reference is the board without those parts, as in
          // regions.test.ts. A style rather than removal, since the board re-renders on every interaction.
          if (board.region) await reference.page.addStyleTag({ content: omitted(board.name) });
          const scene = rulings.filter((ruling) => ruling.scene === `converted-${board.name}`);
          if (board.canvas) {
            expect(await page.locator("canvas").count()).toBe(await reference.page.locator("canvas").count());
            scene.push({
              scene: `converted-${board.name}`,
              basis: "animated design canvases; pinned engine and logic bytes checked by boards.test",
              board: { canvas: "canvas" },
              page: { canvas: "canvas" },
            });
          }
          const result = await compare(
            reference.page,
            page,
            scene,
            `converted-${board.name}${board.region ? "-regions" : ""}-${width}-${colorScheme}`,
            info,
          );
          await info.attach("artboard", { path: result.board, contentType: "image/png" });
          await info.attach("react", { path: result.page, contentType: "image/png" });
          expect.soft(result.error, `see ${result.diff ?? result.page}`).toBeUndefined();
          if (board.region) await regionMenus(page, reference.page, board.name, `${width}-${colorScheme}`, info);
          else await boardInteractions(page, board.name);
          expect(errors).toEqual([]);
        } finally {
          await reference.close();
        }
      });
    }
