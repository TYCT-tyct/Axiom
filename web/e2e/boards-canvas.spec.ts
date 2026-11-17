import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { compare } from "./design/compare.ts";
import { type Ruling, rulings } from "./design/rulings.ts";
import { stage, stagePaused } from "./design/stage.ts";

const view = {
  viewport: { width: 400, height: 300 },
  colorScheme: "light" as const,
  timezoneId: "Asia/Shanghai",
  time: new Date("2026-10-24T04:00:00Z"),
};

async function prepare(page: Page) {
  await stage(page, view);
  await page.goto("about:blank");
  await page.clock.pauseAt(view.time);
  await page.setViewportSize(view.viewport);
  await page.setContent('<canvas class="sea" width="400" height="300"></canvas>');
}

test("board clocks replay the same zero epoch and animation frames after navigation", async ({ browser }) => {
  const timelines: number[][] = [];
  for (let i = 0; i < 2; i++) {
    const context = await browser.newContext();
    const page = await context.newPage();
    await stagePaused(page, view);
    await page.goto("about:blank");
    const samples = await page.evaluateHandle(() => {
      const samples = [performance.now()];
      requestAnimationFrame((time) => samples.push(time, performance.now()));
      return samples;
    });
    await page.clock.runFor(16);
    timelines.push(await samples.jsonValue());
    await samples.dispose();
    await context.close();
  }
  expect(timelines).toEqual([
    [0, 16, 16],
    [0, 16, 16],
  ]);
});

test("canvas rulings preserve overlaid text, glass, geometry and the original bitmap", async ({ context }, info) => {
  const board = await context.newPage();
  const page = await context.newPage();
  const fixture = async (page: Page, colour: string) => {
    await prepare(page);
    await page.evaluate((colour) => {
      const canvas = document.querySelector("canvas") as HTMLCanvasElement;
      const ctx = canvas.getContext("2d", { alpha: false }) as CanvasRenderingContext2D;
      ctx.fillStyle = colour;
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      const text = document.createElement("p");
      text.textContent = "Text over the sea";
      Object.assign(text.style, {
        position: "absolute",
        top: "40px",
        left: "20px",
        font: "bold 24px sans-serif",
        background: "rgba(255,255,255,.35)",
        backdropFilter: "blur(4px)",
      });
      document.body.append(text);
      // A transparent animated layer must not become an opaque cover over otherwise visible DOM.
      const overlay = document.createElement("canvas");
      overlay.className = "cw";
      overlay.width = 400;
      overlay.height = 300;
      Object.assign(overlay.style, { position: "absolute", inset: "0", zIndex: "2" });
      document.body.append(overlay);
      const brush = overlay.getContext("2d") as CanvasRenderingContext2D;
      brush.fillStyle = colour;
      brush.fillRect(0, 0, 10, 10);
    }, colour);
  };
  await fixture(board, "blue");
  await fixture(page, "green");
  const scene = rulings.filter((ruling) => ruling.scene === "converted-ClawdEgg");
  const bitmap = () => page.evaluate(() => (document.querySelector("canvas") as HTMLCanvasElement).toDataURL());
  const original = await bitmap();
  const equal = await compare(board, page, scene, "canvas-layer-equal", info);
  expect(equal.error).toBeUndefined();
  expect(await bitmap()).toBe(original);
  await page.locator("p").evaluate((p) => {
    p.textContent = "Wrong text over the sea";
  });
  const changedText = await compare(board, page, scene, "canvas-layer-text", info);
  expect(changedText.differentPixels).toBeGreaterThan(0);
  await page.locator("p").evaluate((p) => {
    p.textContent = "Text over the sea";
  });
  await page.locator("canvas.sea").evaluate((canvas) => {
    canvas.style.width = "200px";
  });
  const changedSize = await compare(board, page, scene, "canvas-layer-size", info);
  expect(changedSize.differentPixels).toBeGreaterThan(0);
  for (const canvas of ["canvas.missing", "p"]) {
    const invalid: Ruling[] = [{ scene: "test", basis: "test", board: { canvas }, page: { canvas } }];
    await expect(compare(board, page, invalid, "canvas-layer-invalid", info)).rejects.toThrow("canvas ruling:");
  }
  await board.close();
  await page.close();
});

type AnimationEngine = {
  last: number;
  raf: number;
  frame: (time: number) => void;
  tick: (dt: number) => void;
  destroy: () => void;
};
type Engines = {
  AxiomOceanOrig: { Engine: new (canvas: HTMLCanvasElement, specs: object[], options: object) => AnimationEngine };
  ClawdScene: new (canvas: HTMLCanvasElement, options: object) => AnimationEngine;
};

async function engineFrames(page: Page, name: string, source: string) {
  await prepare(page);
  await page.addScriptTag({ content: source, type: "module" });
  return page.evaluate((name) => {
    const canvas = document.querySelector("canvas") as HTMLCanvasElement;
    const engines = window as unknown as Engines;
    const engine =
      name === "ocean-orig"
        ? new engines.AxiomOceanOrig.Engine(canvas, [{ kind: "classic", size: 0.8 }], { hold: true, whale: null })
        : new engines.ClawdScene(canvas, { helmet: true, bubbles: true, transparent: true });
    const initial = canvas.toDataURL();
    for (let i = 0; i < 300; i++) {
      if (name === "ocean-orig") engine.tick(1 / 60);
      else engine.frame(engine.last + 1000 / 60);
      cancelAnimationFrame(engine.raf);
    }
    const final = canvas.toDataURL();
    engine.destroy();
    return { initial, final };
  }, name);
}

for (const name of ["ocean-orig", "clawd"]) {
  test(`${name}: converted engine matches fixed-seed source frames`, async ({ context }, info) => {
    const root = join(import.meta.dirname, "../packages/boards");
    const board = await context.newPage();
    const page = await context.newPage();
    const expected = await engineFrames(board, name, readFileSync(join(root, "inputs", `${name}.js.txt`), "utf8"));
    const actual = await engineFrames(page, name, readFileSync(join(root, "src/engines", `${name}.js`), "utf8"));
    expect(actual.final === actual.initial, "engine must draw during the five simulated seconds").toBe(false);
    await info.attach("source-engine", {
      body: Buffer.from(expected.final.split(",")[1] as string, "base64"),
      contentType: "image/png",
    });
    await info.attach("converted-engine", {
      body: Buffer.from(actual.final.split(",")[1] as string, "base64"),
      contentType: "image/png",
    });
    expect(actual.final === expected.final, "fixed-seed canvas pixels must match exactly").toBe(true);
    await board.close();
    await page.close();
  });
}
