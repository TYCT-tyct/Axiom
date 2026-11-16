// @vitest-environment jsdom
import type { JSHandle, Page } from "@playwright/test";
import { afterEach, expect, it, vi } from "vitest";
import { blankCanvases } from "../e2e/design/canvas.ts";

const page = {
  evaluateHandle: async (fn: (selectors: string[]) => () => void, selectors: string[]) => {
    const restore = fn(selectors);
    return { evaluate: async (fn: (restore: () => void) => void) => fn(restore) };
  },
} as unknown as Page;

afterEach(() => {
  vi.restoreAllMocks();
  document.body.replaceChildren();
});

it.each([true, false])(
  "preserves the canvas alpha mode (%s) and restores pixels without touching DOM or drawing state",
  async (alpha) => {
    document.body.innerHTML = '<canvas class="sea"></canvas><p>Text over the sea</p>';
    const canvas = document.querySelector("canvas") as HTMLCanvasElement;
    const original = new Uint8ClampedArray([10, 20, 30, 255, 40, 50, 60, 128]);
    let pixels = original;
    const context = {
      getContextAttributes: () => ({ alpha }),
      getImageData: () => ({ width: 2, height: 1, data: pixels }),
      createImageData: (width: number, height: number) => ({
        width,
        height,
        data: new Uint8ClampedArray(width * height * 4),
      }),
      putImageData: (image: ImageData) => {
        pixels = image.data;
      },
      globalAlpha: 0.25,
      globalCompositeOperation: "multiply",
    };
    vi.spyOn(canvas, "getContext").mockReturnValue(context as unknown as CanvasRenderingContext2D);
    const markup = document.body.innerHTML;
    const saved: JSHandle<() => void> = await blankCanvases(page, ["canvas.sea"]);
    expect([...pixels]).toEqual([0, 0, 0, alpha ? 0 : 255, 0, 0, 0, alpha ? 0 : 255]);
    expect(document.body.innerHTML).toBe(markup);
    expect(context.globalAlpha).toBe(0.25);
    expect(context.globalCompositeOperation).toBe("multiply");
    await saved.evaluate((restore) => restore());
    expect(pixels).toBe(original);
  },
);

it.each(["canvas.missing", "p"])("rejects an invalid canvas registration: %s", async (selector) => {
  document.body.innerHTML = "<p>Must remain compared</p>";
  await expect(blankCanvases(page, [selector])).rejects.toThrow("canvas ruling:");
});
