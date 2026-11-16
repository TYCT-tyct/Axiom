import { existsSync } from "node:fs";
import { join } from "node:path";
import { test as base, expect } from "@playwright/test";
import { build, preview } from "vite";
import { collectErrors } from "./collect-errors.ts";
import { openBoard } from "./design/board.ts";
import { compare } from "./design/compare.ts";
import { fontsLoaded } from "./design/fonts.ts";
import { type Stage, stage } from "./design/stage.ts";

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
      await build({ root, logLevel: "error" });
      const server = await preview({ root, preview: { port: 0, strictPort: true } });
      const address = server.httpServer.address();
      if (!address || typeof address === "string") throw new Error("no demo server port");
      try {
        await use(`http://localhost:${address.port}`);
      } finally {
        await new Promise<void>((resolve, reject) =>
          server.httpServer.close((error) => (error ? reject(error) : resolve())),
        );
      }
    },
    { scope: "worker" },
  ],
});
const cases = [
  { name: "Login", engines: ["ocean-orig", "login-ui"], selector: ".lg-form .lg-step", prop: "mode" },
  { name: "ClawdEgg", engines: ["ocean-orig", "clawd", "clawd-egg"], selector: ".eg .mr", prop: "mode" },
  { name: "AxApp", engines: [], selector: ".p-ov", prop: "theme" },
];
test.describe.configure({ timeout: 120_000 });
test.use({ timezoneId: "Asia/Shanghai" });

for (const board of cases)
  for (const width of [1440, 390])
    for (const colorScheme of ["light", "dark"] as const) {
      test(`${board.name} ${width} ${colorScheme}: artboard diff and CSP interactions`, async ({
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
        await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
        await stage(page, view);
        await page.clock.pauseAt(view.time);
        await page.goto(`${boardsURL}/${board.name}?theme=${colorScheme}`);
        await expect(page.locator(board.selector).first()).toBeAttached();
        await page.evaluate(() => document.fonts.ready.then(() => {}));
        const reference = await openBoard(
          browser,
          {
            file: `${board.name}.dc.html`,
            props: { [board.prop]: colorScheme === "dark" ? "深色" : "浅色" },
            engines: board.engines,
            actions: board.name === "AxApp" ? [["go", "ov"]] : [],
          },
          referenceRoot,
          view,
        );
        try {
          await expect(reference.page.locator(board.selector).first()).toBeAttached();
          await fontsLoaded(reference.page, reference.fonts);
          await page.clock.runFor(5000);
          await reference.page.clock.runFor(5000);
          const result = await compare(
            reference.page,
            page,
            [],
            `converted-${board.name}-${width}-${colorScheme}`,
            info,
          );
          await info.attach("artboard", { path: result.board, contentType: "image/png" });
          await info.attach("react", { path: result.page, contentType: "image/png" });
          expect.soft(result.error, `see ${result.diff ?? result.page}`).toBeUndefined();
          if (board.name === "Login") {
            await page.locator("#lg-id").fill("reader@example.com");
            await page.locator('[data-act="consent"]').check();
            await page.locator('[data-act="continue"]').click();
            await page.clock.runFor(2000);
            await expect(page.locator('[data-step="code"]')).toBeVisible();
            for (let i = 0; i < 6; i++) await page.locator(`[data-act="digit"][data-i="${i}"]`).fill(String(i + 1));
            await page.clock.runFor(3000);
            await expect(page.locator('[data-step="key"]')).toBeVisible();
          } else if (board.name === "ClawdEgg") {
            await page.locator('.mr[data-i="1"]').dispatchEvent("pointerover", { bubbles: true });
            await page.clock.runFor(12000);
            await expect(page.locator('.mr[data-i="1"]')).toHaveClass(/on/);
          } else {
            await page.locator(".app2").dispatchEvent("mouseover", { bubbles: true });
            await page.keyboard.press("Control+k");
            await expect(page.locator(".kpn")).toBeVisible();
            await page.clock.runFor(500);
            await expect(page.locator(".kin input")).toBeFocused();
            await page.keyboard.press("Escape");
            await page.clock.runFor(500);
            await expect(page.locator(".kpn")).toBeHidden();
          }
          expect(errors).toEqual([]);
        } finally {
          await reference.close();
        }
      });
    }
